// Benchmark tx spammer for x/nonce module.
//
// Three-phase architecture:
//   1. Pre-sign: build and sign all txs offline using SDK keyring
//   2. Subscribe: connect WebSocket to CometBFT, subscribe to Tx events
//   3. Broadcast: fire txs concurrently via gRPC, track inclusion via WS
//
// Measures two distinct latencies per tx:
//   - Broadcast latency: time from gRPC send to SYNC response (mempool accept)
//   - Inclusion latency: time from broadcast to block confirmation (via WS event)
//
// Outputs JSON with per-tx results, percentile latencies, per-block stats,
// and failure categorization.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type Config struct {
	Mode        string `json:"mode"`
	TotalTxs    int    `json:"total_txs"`
	Concurrency int    `json:"concurrency"`
	ChainID     string `json:"chain_id"`
	GRPCAddr    string `json:"grpc_addr"`
	WSAddr      string `json:"ws_addr"`
	AcctNumber  uint64 `json:"acct_number"`
	StartSeq    uint64 `json:"start_seq"`
	Denom       string `json:"denom"`
	Amount      int64  `json:"amount"`
	Recipient   string `json:"recipient"`
}

type TxResult struct {
	Index            int    `json:"index"`
	Sequence         uint64 `json:"sequence"`
	TxHash           string `json:"tx_hash"`
	BroadcastCode    uint32 `json:"broadcast_code"`
	BroadcastLatNs   int64  `json:"broadcast_lat_ns"`
	InclusionLatNs   int64  `json:"inclusion_lat_ns,omitempty"`
	InclusionHeight  int64  `json:"inclusion_height,omitempty"`
	Error            string `json:"error,omitempty"`
}

type LatencyStats struct {
	P50Ms float64 `json:"p50_ms"`
	P95Ms float64 `json:"p95_ms"`
	P99Ms float64 `json:"p99_ms"`
	MaxMs float64 `json:"max_ms"`
}

type BlockStat struct {
	Height    int64   `json:"height"`
	TxCount   int     `json:"tx_count"`
	BlockTime float64 `json:"block_time_ms"`
}

type Summary struct {
	TotalSubmitted    int          `json:"total_submitted"`
	TotalAccepted     int          `json:"total_accepted"`
	TotalConfirmed    int          `json:"total_confirmed"`
	TotalFailed       int          `json:"total_failed"`
	BroadcastTPS      float64      `json:"broadcast_tps"`
	ConfirmedTPS      float64      `json:"confirmed_tps"`
	BroadcastLatency  LatencyStats `json:"broadcast_latency"`
	InclusionLatency  LatencyStats `json:"inclusion_latency"`
	DurationMs        int64        `json:"duration_ms"`
	BlockStats        []BlockStat  `json:"block_stats"`
}

type FailureCounts struct {
	DuplicateNonce int `json:"duplicate_nonce"`
	ExpiredNonce   int `json:"expired_nonce"`
	WrongSequence  int `json:"wrong_sequence"`
	Timeout        int `json:"timeout"`
	MempoolFull    int `json:"mempool_full"`
	Other          int `json:"other"`
}

type BenchmarkResult struct {
	Config   Config        `json:"config"`
	Summary  Summary       `json:"summary"`
	Failures FailureCounts `json:"failures"`
	Txs      []TxResult    `json:"txs,omitempty"`
}

// ---------------------------------------------------------------------------
// WebSocket tx tracker
// ---------------------------------------------------------------------------

// txTracker subscribes to CometBFT Tx events via WebSocket and records
// the block height + timestamp when each tx hash is included.
type txTracker struct {
	mu       sync.Mutex
	pending  map[string]time.Time  // tx_hash -> broadcast time
	included map[string]txConfirm  // tx_hash -> confirmation info
	done     chan struct{}
}

type txConfirm struct {
	height    int64
	timestamp time.Time // wall-clock time when WS event arrived
}

func newTxTracker() *txTracker {
	return &txTracker{
		pending:  make(map[string]time.Time),
		included: make(map[string]txConfirm),
		done:     make(chan struct{}),
	}
}

func (t *txTracker) trackBroadcast(hash string, broadcastTime time.Time) {
	t.mu.Lock()
	t.pending[strings.ToUpper(hash)] = broadcastTime
	t.mu.Unlock()
}

func (t *txTracker) getConfirmation(hash string) (txConfirm, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.included[strings.ToUpper(hash)]
	return c, ok
}

func (t *txTracker) allConfirmed(expected int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.included) >= expected
}

// subscribe connects to CometBFT WebSocket and listens for Tx events.
// It parses the tx hash and block height from each event.
func (t *txTracker) subscribe(ctx context.Context, wsURL string) error {
	u, err := url.Parse(wsURL)
	if err != nil {
		return fmt.Errorf("parse ws url: %w", err)
	}
	u.Path = "/websocket"

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("ws connect: %w", err)
	}

	// Subscribe to Tx events
	subMsg := `{"jsonrpc":"2.0","method":"subscribe","params":{"query":"tm.event='Tx'"},"id":1}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(subMsg)); err != nil {
		conn.Close()
		return fmt.Errorf("ws subscribe: %w", err)
	}

	// Also subscribe to NewBlock for per-block timing
	blockSubMsg := `{"jsonrpc":"2.0","method":"subscribe","params":{"query":"tm.event='NewBlock'"},"id":2}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(blockSubMsg)); err != nil {
		conn.Close()
		return fmt.Errorf("ws subscribe newblock: %w", err)
	}

	go func() {
		defer conn.Close()
		defer close(t.done)

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			conn.SetReadDeadline(time.Now().Add(30 * time.Second))
			_, msg, err := conn.ReadMessage()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log("ws read error: %v", err)
				return
			}

			now := time.Now()
			t.processWSMessage(msg, now)
		}
	}()

	return nil
}

// processWSMessage extracts tx hash and height from CometBFT event JSON.
// CometBFT Tx event structure (v0.38):
//
//	{
//	  "result": {
//	    "events": { "tx.hash": ["AABB..."], "tx.height": ["123"] },
//	    ...
//	  }
//	}
func (t *txTracker) processWSMessage(msg []byte, now time.Time) {
	// CometBFT v0.38 uses string-encoded int64 fields, so we parse with
	// json.Number / string types to avoid silent unmarshal failures.
	var envelope struct {
		Result struct {
			Data struct {
				Value struct {
					TxResult struct {
						Height json.Number `json:"height"`
						Tx     string      `json:"tx"`
					} `json:"TxResult"`
				} `json:"value"`
			} `json:"data"`
		} `json:"result"`
	}

	if err := json.Unmarshal(msg, &envelope); err != nil {
		return
	}

	txB64 := envelope.Result.Data.Value.TxResult.Tx
	if txB64 == "" {
		return
	}

	height, _ := envelope.Result.Data.Value.TxResult.Height.Int64()

	txBz, err := base64.StdEncoding.DecodeString(txB64)
	if err != nil {
		return
	}

	h := sha256.Sum256(txBz)
	hash := strings.ToUpper(hex.EncodeToString(h[:]))

	t.mu.Lock()
	if _, tracked := t.pending[hash]; tracked {
		t.included[hash] = txConfirm{height: height, timestamp: now}
	}
	t.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func log(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[spammer] "+format+"\n", args...)
}

func computeLatencyStats(nanos []int64) LatencyStats {
	if len(nanos) == 0 {
		return LatencyStats{}
	}
	sort.Slice(nanos, func(i, j int) bool { return nanos[i] < nanos[j] })
	pct := func(p float64) float64 {
		idx := int(float64(len(nanos)-1) * p)
		return float64(nanos[idx]) / 1e6 // ns -> ms
	}
	return LatencyStats{
		P50Ms: pct(0.50),
		P95Ms: pct(0.95),
		P99Ms: pct(0.99),
		MaxMs: float64(nanos[len(nanos)-1]) / 1e6,
	}
}

func categorizeError(errStr string) string {
	switch {
	case strings.Contains(errStr, "already consumed") || strings.Contains(errStr, "duplicate"):
		return "duplicate"
	case strings.Contains(errStr, "too far in the past") || strings.Contains(errStr, "expired"):
		return "expired"
	case strings.Contains(errStr, "sequence mismatch") || strings.Contains(errStr, "wrong sequence"):
		return "sequence"
	case strings.Contains(errStr, "mempool is full"):
		return "mempool_full"
	case errStr == "timeout" || strings.Contains(errStr, "DeadlineExceeded"):
		return "timeout"
	default:
		return "other"
	}
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	var (
		mode        = flag.String("mode", "timestamp", "tx mode: sequential, timestamp, mixed")
		totalTxs    = flag.Int("total", 100, "total transactions to send")
		concurrency = flag.Int("concurrency", 10, "concurrent broadcast goroutines")
		chainID     = flag.String("chain-id", "nonce-test", "chain ID")
		grpcAddr    = flag.String("grpc", "localhost:9090", "gRPC endpoint")
		wsAddr      = flag.String("ws", "", "WebSocket endpoint (default: derived from grpc host)")
		acctNumber  = flag.Uint64("account-number", 0, "account number")
		startSeq    = flag.Uint64("start-seq", 0, "starting sequence for sequential mode")
		denom       = flag.String("denom", "stake", "coin denom")
		amount      = flag.Int64("amount", 1, "send amount per tx")
		recipient   = flag.String("recipient", "", "recipient address (default: self-send)")
		keyHome     = flag.String("key-home", "/root/.gaia", "keyring home directory")
		keyName     = flag.String("key-name", "validator", "keyring key name")
		emitTxs     = flag.Bool("emit-txs", false, "include per-tx results in output")
		waitBlocks  = flag.Int("wait-blocks", 10, "max blocks to wait for inclusion after broadcast")
	)
	flag.Parse()

	// Derive WS address from gRPC host if not specified
	if *wsAddr == "" {
		host := strings.Split(*grpcAddr, ":")[0]
		*wsAddr = "ws://" + host + ":26657"
	}

	cfg := Config{
		Mode:        *mode,
		TotalTxs:    *totalTxs,
		Concurrency: *concurrency,
		ChainID:     *chainID,
		GRPCAddr:    *grpcAddr,
		WSAddr:      *wsAddr,
		AcctNumber:  *acctNumber,
		StartSeq:    *startSeq,
		Denom:       *denom,
		Amount:      *amount,
		Recipient:   *recipient,
	}

	// ---- Codec + tx config ----
	ir := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(ir)
	authtypes.RegisterInterfaces(ir)
	banktypes.RegisterInterfaces(ir)
	cdc := codec.NewProtoCodec(ir)
	txCfg := authtx.NewTxConfig(cdc, authtx.DefaultSignModes)

	// ---- Keyring ----
	kr, err := keyring.New("gaia", keyring.BackendTest, *keyHome, nil, cdc)
	if err != nil {
		log("keyring error: %v", err)
		os.Exit(1)
	}

	rec, err := kr.Key(*keyName)
	if err != nil {
		log("key %q not found: %v", *keyName, err)
		os.Exit(1)
	}

	addr, err := rec.GetAddress()
	if err != nil {
		log("address error: %v", err)
		os.Exit(1)
	}

	if cfg.Recipient == "" {
		cfg.Recipient = addr.String()
	}
	recip, err := sdk.AccAddressFromBech32(cfg.Recipient)
	if err != nil {
		log("invalid recipient: %v", err)
		os.Exit(1)
	}

	// ---- gRPC ----
	conn, err := grpc.NewClient(cfg.GRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log("gRPC connect error: %v", err)
		os.Exit(1)
	}
	defer conn.Close()
	txClient := txtypes.NewServiceClient(conn)

	// ---- WebSocket tracker ----
	wsCtx, wsCancel := context.WithCancel(context.Background())
	defer wsCancel()

	tracker := newTxTracker()
	if err := tracker.subscribe(wsCtx, cfg.WSAddr); err != nil {
		log("WARNING: WebSocket subscription failed: %v", err)
		log("Continuing without block inclusion tracking.")
	} else {
		log("WebSocket subscribed to Tx events at %s", cfg.WSAddr)
	}

	// Give WS a moment to establish subscriptions
	time.Sleep(500 * time.Millisecond)

	// ---- Phase 1: Pre-sign ----
	log("Pre-signing %d txs (mode=%s)...", cfg.TotalTxs, cfg.Mode)

	type signedTx struct {
		bytes    []byte
		sequence uint64
		hash     string
	}
	txs := make([]signedTx, cfg.TotalTxs)

	for i := 0; i < cfg.TotalTxs; i++ {
		var seq uint64
		switch cfg.Mode {
		case "sequential":
			seq = cfg.StartSeq + uint64(i)
		case "timestamp":
			seq = uint64(time.Now().UnixMicro()) + uint64(i)
		case "mixed":
			if i%2 == 0 {
				seq = cfg.StartSeq + uint64(i/2)
			} else {
				seq = uint64(time.Now().UnixMicro()) + uint64(i)
			}
		default:
			log("unknown mode: %s", cfg.Mode)
			os.Exit(1)
		}

		msg := banktypes.NewMsgSend(addr, recip,
			sdk.NewCoins(sdk.NewInt64Coin(cfg.Denom, cfg.Amount)))

		txBuilder := txCfg.NewTxBuilder()
		if err := txBuilder.SetMsgs(msg); err != nil {
			log("SetMsgs error: %v", err)
			os.Exit(1)
		}
		txBuilder.SetGasLimit(200000)
		txBuilder.SetFeeAmount(sdk.NewCoins(sdk.NewInt64Coin(cfg.Denom, 200000)))

		factory := clienttx.Factory{}.
			WithChainID(cfg.ChainID).
			WithAccountNumber(cfg.AcctNumber).
			WithSequence(seq).
			WithKeybase(kr).
			WithTxConfig(txCfg).
			WithSignMode(signing.SignMode_SIGN_MODE_DIRECT)

		if err := clienttx.Sign(context.Background(), factory, *keyName, txBuilder, true); err != nil {
			log("sign error (tx %d, seq %d): %v", i, seq, err)
			os.Exit(1)
		}

		txBz, err := txCfg.TxEncoder()(txBuilder.GetTx())
		if err != nil {
			log("encode error: %v", err)
			os.Exit(1)
		}

		// Compute tx hash (SHA256 of encoded bytes, uppercase hex)
		h := sha256.Sum256(txBz)
		hash := strings.ToUpper(hex.EncodeToString(h[:]))

		txs[i] = signedTx{bytes: txBz, sequence: seq, hash: hash}
	}
	log("Pre-signing complete.")

	// ---- Phase 2: Broadcast ----
	log("Broadcasting %d txs, concurrency=%d...", cfg.TotalTxs, cfg.Concurrency)

	results := make([]TxResult, cfg.TotalTxs)
	var wg sync.WaitGroup
	sem := make(chan struct{}, cfg.Concurrency)
	var submitted atomic.Int64

	broadcastStart := time.Now()

	for i := 0; i < cfg.TotalTxs; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()

			tx := txs[idx]
			broadcastTime := time.Now()

			// Register with tracker before broadcast
			tracker.trackBroadcast(tx.hash, broadcastTime)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			resp, err := txClient.BroadcastTx(ctx, &txtypes.BroadcastTxRequest{
				TxBytes: tx.bytes,
				Mode:    txtypes.BroadcastMode_BROADCAST_MODE_SYNC,
			})

			broadcastLat := time.Since(broadcastTime)

			res := TxResult{
				Index:          idx,
				Sequence:       tx.sequence,
				TxHash:         tx.hash,
				BroadcastLatNs: broadcastLat.Nanoseconds(),
			}

			if err != nil {
				res.Error = err.Error()
				if strings.Contains(err.Error(), "DeadlineExceeded") {
					res.Error = "timeout"
				}
			} else if resp.TxResponse != nil {
				res.BroadcastCode = resp.TxResponse.Code
				if resp.TxResponse.TxHash != "" {
					res.TxHash = strings.ToUpper(resp.TxResponse.TxHash)
					// Re-register with the server-returned hash (should match)
					tracker.trackBroadcast(res.TxHash, broadcastTime)
				}
				if resp.TxResponse.Code != 0 {
					res.Error = resp.TxResponse.RawLog
				}
			}

			results[idx] = res
			count := submitted.Add(1)
			if count%100 == 0 || count == int64(cfg.TotalTxs) {
				log("  broadcast %d/%d", count, cfg.TotalTxs)
			}
		}(i)
	}

	wg.Wait()
	broadcastElapsed := time.Since(broadcastStart)
	log("All %d txs broadcast in %s", cfg.TotalTxs, broadcastElapsed.Round(time.Millisecond))

	// ---- Phase 3: Wait for block inclusion ----
	accepted := 0
	for _, r := range results {
		if r.BroadcastCode == 0 && r.Error == "" {
			accepted++
		}
	}

	if accepted > 0 {
		log("Waiting for block inclusion of %d accepted txs (up to %d blocks)...", accepted, *waitBlocks)

		deadline := time.Now().Add(time.Duration(*waitBlocks) * 2 * time.Second)
		pollInterval := 200 * time.Millisecond

		for time.Now().Before(deadline) {
			if tracker.allConfirmed(accepted) {
				log("All %d txs confirmed!", accepted)
				break
			}
			time.Sleep(pollInterval)
		}
	}

	// Cancel WS subscription
	wsCancel()

	// ---- Phase 4: Compute results ----
	var (
		broadcastLats []int64
		inclusionLats []int64
		confirmed     int
		failed        int
		failures      FailureCounts
	)

	// Per-block aggregation: height -> count of our txs
	blockTxCounts := make(map[int64]int)

	for i := range results {
		r := &results[i]

		if r.BroadcastCode == 0 && r.Error == "" {
			broadcastLats = append(broadcastLats, r.BroadcastLatNs)

			// Check inclusion via WebSocket tracker
			if conf, ok := tracker.getConfirmation(r.TxHash); ok {
				r.InclusionHeight = conf.height

				// Inclusion latency = WS event arrival time - original broadcast time
				tracker.mu.Lock()
				broadcastTime := tracker.pending[r.TxHash]
				tracker.mu.Unlock()
				r.InclusionLatNs = conf.timestamp.Sub(broadcastTime).Nanoseconds()
				inclusionLats = append(inclusionLats, r.InclusionLatNs)
				blockTxCounts[conf.height]++
				confirmed++
			}
		} else {
			failed++
			switch categorizeError(r.Error) {
			case "duplicate":
				failures.DuplicateNonce++
			case "expired":
				failures.ExpiredNonce++
			case "sequence":
				failures.WrongSequence++
			case "mempool_full":
				failures.MempoolFull++
			case "timeout":
				failures.Timeout++
			default:
				failures.Other++
			}
		}
	}

	// Build per-block stats (sorted by height)
	var blockStats []BlockStat
	var heights []int64
	for h := range blockTxCounts {
		heights = append(heights, h)
	}
	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })

	for i, h := range heights {
		bs := BlockStat{Height: h, TxCount: blockTxCounts[h]}
		if i > 0 {
			// Approximate inter-block time from heights (1 height = ~1s with our config)
			bs.BlockTime = float64(h-heights[i-1]) * 1000 // rough ms
		}
		blockStats = append(blockStats, bs)
	}

	// TPS calculations
	broadcastTPS := float64(0)
	if broadcastElapsed.Seconds() > 0 {
		broadcastTPS = float64(accepted) / broadcastElapsed.Seconds()
	}

	confirmedTPS := float64(0)
	if len(inclusionLats) > 0 && broadcastElapsed.Seconds() > 0 {
		// Use total wall-clock time from first broadcast to last confirmation
		maxIncLat := int64(0)
		for _, l := range inclusionLats {
			if l > maxIncLat {
				maxIncLat = l
			}
		}
		totalTime := broadcastElapsed + time.Duration(maxIncLat-broadcastElapsed.Nanoseconds())
		if totalTime.Seconds() > 0 {
			confirmedTPS = float64(confirmed) / totalTime.Seconds()
		}
	}

	result := BenchmarkResult{
		Config: cfg,
		Summary: Summary{
			TotalSubmitted:   cfg.TotalTxs,
			TotalAccepted:    accepted,
			TotalConfirmed:   confirmed,
			TotalFailed:      failed,
			BroadcastTPS:     broadcastTPS,
			ConfirmedTPS:     confirmedTPS,
			BroadcastLatency: computeLatencyStats(broadcastLats),
			InclusionLatency: computeLatencyStats(inclusionLats),
			DurationMs:       broadcastElapsed.Milliseconds(),
			BlockStats:       blockStats,
		},
		Failures: failures,
	}

	if *emitTxs {
		result.Txs = results
	}

	out, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(out))

	log("Done. accepted=%d confirmed=%d failed=%d broadcast_tps=%.1f confirmed_tps=%.1f",
		accepted, confirmed, failed, broadcastTPS, confirmedTPS)
}
