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
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
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
	Mode          string   `json:"mode"`
	TotalTxs      int      `json:"total_txs"`
	Concurrency   int      `json:"concurrency"`
	ChainID       string   `json:"chain_id"`
	GRPCEndpoints []string `json:"grpc_endpoints"`
	WSAddr        string   `json:"ws_addr"`
	AcctNumber    uint64   `json:"acct_number"`
	StartSeq      uint64   `json:"start_seq"`
	Denom         string   `json:"denom"`
	Amount        int64    `json:"amount"`
	Recipient     string   `json:"recipient"`
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
	Height  int64 `json:"height"`
	TxCount int   `json:"tx_count"`
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
	TotalDurationMs   int64        `json:"total_duration_ms"`
	StartedAt         string       `json:"started_at"`
	BlockStats        []BlockStat  `json:"block_stats"`
	TotalRetries      int64        `json:"total_retries"`
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
// CometBFT v0.38 Tx event structure (nested, not flat):
//
//	{
//	  "result": {
//	    "data": {
//	      "value": {
//	        "TxResult": {
//	          "height": "123",
//	          "tx": "<base64-encoded-tx-bytes>"
//	        }
//	      }
//	    }
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
		log("ws: json unmarshal error: %v (msg prefix: %.100s)", err, string(msg))
		return
	}

	txB64 := envelope.Result.Data.Value.TxResult.Tx
	if txB64 == "" {
		// Not a Tx event (e.g. NewBlock subscription or subscribe ack)
		return
	}

	height, _ := envelope.Result.Data.Value.TxResult.Height.Int64()

	txBz, err := base64.StdEncoding.DecodeString(txB64)
	if err != nil {
		log("ws: base64 decode error: %v", err)
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
		idx := int(math.Ceil(float64(len(nanos))*p)) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(nanos) {
			idx = len(nanos) - 1
		}
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
// Block scanner: queries blocks via RPC to find confirmed tx hashes.
// More reliable than WebSocket which drops events under load.
// ---------------------------------------------------------------------------

// scanBlocks queries blocks from startHeight to the current height via the
// CometBFT RPC endpoint and records which of the pending tx hashes appear.
func (t *txTracker) scanBlocks(rpcURL string, startHeight int64) {
	// Get current height
	currentHeight := getBlockHeight(rpcURL)
	if currentHeight <= startHeight {
		return
	}

	now := time.Now()
	matched := 0

	for h := startHeight; h <= currentHeight; h++ {
		hashes := getBlockTxHashes(rpcURL, h)
		for _, hash := range hashes {
			t.mu.Lock()
			if _, tracked := t.pending[hash]; tracked {
				if _, already := t.included[hash]; !already {
					t.included[hash] = txConfirm{height: h, timestamp: now}
					matched++
				}
			}
			t.mu.Unlock()
		}
	}

	log("Block scan: %d blocks (%d->%d), found %d txs",
		currentHeight-startHeight+1, startHeight, currentHeight, matched)
}

// rpcGet fetches a CometBFT RPC endpoint and unmarshals the JSON response.
func rpcGet(url string, dest any) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dest)
}

func getBlockHeight(rpcURL string) int64 {
	var status struct {
		Result struct {
			SyncInfo struct {
				LatestBlockHeight string `json:"latest_block_height"`
			} `json:"sync_info"`
		} `json:"result"`
	}
	if err := rpcGet(rpcURL+"/status", &status); err != nil {
		return 0
	}
	h, _ := strconv.ParseInt(status.Result.SyncInfo.LatestBlockHeight, 10, 64)
	return h
}

func getBlockTxHashes(rpcURL string, height int64) []string {
	var block struct {
		Result struct {
			Block struct {
				Data struct {
					Txs []string `json:"txs"` // base64-encoded tx bytes
				} `json:"data"`
			} `json:"block"`
		} `json:"result"`
	}
	if err := rpcGet(fmt.Sprintf("%s/block?height=%d", rpcURL, height), &block); err != nil {
		return nil
	}

	var hashes []string
	for _, txB64 := range block.Result.Block.Data.Txs {
		txBz, err := base64.StdEncoding.DecodeString(txB64)
		if err != nil {
			continue
		}
		h := sha256.Sum256(txBz)
		hashes = append(hashes, strings.ToUpper(hex.EncodeToString(h[:])))
	}
	return hashes
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

// acctInfo holds per-account state for tx signing.
type acctInfo struct {
	name    string
	addr    sdk.AccAddress
	number  uint64
	nextSeq uint64
}

func main() {
	var (
		mode        = flag.String("mode", "timestamp", "tx mode: sequential, timestamp, baseline")
		totalTxs    = flag.Int("total", 100, "total transactions to send")
		concurrency = flag.Int("concurrency", 10, "concurrent broadcast goroutines")
		chainID     = flag.String("chain-id", "nonce-test", "chain ID")
		grpcAddrs   = flag.String("grpc", "localhost:9090", "gRPC endpoint(s), comma-separated for multi-node")
		wsAddr      = flag.String("ws", "", "WebSocket endpoint (default: derived from first grpc host)")
		acctNumber  = flag.Uint64("account-number", 0, "account number")
		startSeq    = flag.Uint64("start-seq", 0, "starting sequence for sequential mode")
		denom       = flag.String("denom", "stake", "coin denom")
		amount      = flag.Int64("amount", 1, "send amount per tx")
		recipient   = flag.String("recipient", "", "recipient address (default: self-send)")
		keyHome     = flag.String("key-home", "/root/.gaia", "keyring home directory")
		keyName     = flag.String("key-name", "validator", "keyring key name")
		keyPrefix   = flag.String("key-prefix", "bench", "key name prefix for baseline (multi-account) mode")
		numAccounts = flag.Int("num-accounts", 0, "number of accounts for baseline mode")
		emitTxs       = flag.Bool("emit-txs", false, "include per-tx results in output")
		waitBlocks    = flag.Int("wait-blocks", 10, "max blocks to wait for inclusion after broadcast")
		broadcastMode = flag.String("broadcast-mode", "sync", "broadcast mode: sync (wait for CheckTx) or async (fire-and-forget)")
		retrySeq      = flag.Int("retry-seq", 0, "max retries per tx on wrong_sequence (SYNC) or max re-broadcast rounds (ASYNC)")
	)
	flag.Parse()

	// Parse comma-separated gRPC endpoints
	endpoints := strings.Split(*grpcAddrs, ",")
	for i := range endpoints {
		endpoints[i] = strings.TrimSpace(endpoints[i])
	}

	// Derive WS address from first gRPC host if not specified
	if *wsAddr == "" {
		host := strings.Split(endpoints[0], ":")[0]
		*wsAddr = "ws://" + host + ":26657"
	}

	cfg := Config{
		Mode:          *mode,
		TotalTxs:      *totalTxs,
		Concurrency:   *concurrency,
		ChainID:       *chainID,
		GRPCEndpoints: endpoints,
		WSAddr:        *wsAddr,
		AcctNumber:    *acctNumber,
		StartSeq:      *startSeq,
		Denom:         *denom,
		Amount:        *amount,
		Recipient:     *recipient,
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

	// ---- gRPC connection pool ----
	var grpcConns []*grpc.ClientConn
	for _, ep := range cfg.GRPCEndpoints {
		conn, err := grpc.NewClient(ep, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log("gRPC connect error (%s): %v", ep, err)
			os.Exit(1)
		}
		defer conn.Close()
		grpcConns = append(grpcConns, conn)
	}
	log("gRPC pool: %d endpoint(s)", len(grpcConns))

	// ---- Load accounts ----
	// Baseline mode: load N accounts by prefix. Other modes: load single key.
	var accounts []acctInfo
	authClient := authtypes.NewQueryClient(grpcConns[0])

	queryAccount := func(address string) (uint64, uint64, error) {
		resp, err := authClient.Account(context.Background(), &authtypes.QueryAccountRequest{
			Address: address,
		})
		if err != nil {
			return 0, 0, err
		}
		var acct authtypes.BaseAccount
		if err := cdc.Unmarshal(resp.Account.Value, &acct); err != nil {
			return 0, 0, err
		}
		return acct.AccountNumber, acct.Sequence, nil
	}

	if cfg.Mode == "baseline" && *numAccounts > 0 {
		log("Loading %d accounts (prefix=%s)...", *numAccounts, *keyPrefix)
		accounts = make([]acctInfo, *numAccounts)
		var mu sync.Mutex
		var loadWg sync.WaitGroup
		loadSem := make(chan struct{}, 20) // parallel account queries
		var loadErr error
		for i := 0; i < *numAccounts; i++ {
			loadWg.Add(1)
			loadSem <- struct{}{}
			go func(idx int) {
				defer loadWg.Done()
				defer func() { <-loadSem }()
				name := fmt.Sprintf("%s%d", *keyPrefix, idx)
				rec, err := kr.Key(name)
				if err != nil {
					mu.Lock()
					loadErr = fmt.Errorf("key %q: %w", name, err)
					mu.Unlock()
					return
				}
				addr, err := rec.GetAddress()
				if err != nil {
					mu.Lock()
					loadErr = fmt.Errorf("addr %q: %w", name, err)
					mu.Unlock()
					return
				}
				num, seq, err := queryAccount(addr.String())
				if err != nil {
					mu.Lock()
					loadErr = fmt.Errorf("query %q: %w", name, err)
					mu.Unlock()
					return
				}
				accounts[idx] = acctInfo{name: name, addr: addr, number: num, nextSeq: seq}
			}(i)
		}
		loadWg.Wait()
		if loadErr != nil {
			log("account load error: %v", loadErr)
			os.Exit(1)
		}
		cfg.TotalTxs = *numAccounts // one tx per account for baseline
		log("Loaded %d accounts", len(accounts))
	} else {
		// Single-account mode (sequential, timestamp)
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
		if cfg.AcctNumber == 0 {
			num, seq, err := queryAccount(addr.String())
			if err != nil {
				log("auto-query account error: %v", err)
				os.Exit(1)
			}
			cfg.AcctNumber = num
			if cfg.StartSeq == 0 {
				cfg.StartSeq = seq
			}
			log("Auto-queried account: number=%d sequence=%d", cfg.AcctNumber, cfg.StartSeq)
		}
		accounts = []acctInfo{{name: *keyName, addr: addr, number: cfg.AcctNumber, nextSeq: cfg.StartSeq}}
	}

	// Recipient defaults to first account's address
	if cfg.Recipient == "" {
		cfg.Recipient = accounts[0].addr.String()
	}
	recip, err := sdk.AccAddressFromBech32(cfg.Recipient)
	if err != nil {
		log("invalid recipient: %v", err)
		os.Exit(1)
	}

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
	startTime := time.Now()
	log("Pre-signing %d txs (mode=%s)...", cfg.TotalTxs, cfg.Mode)

	type signedTx struct {
		bytes    []byte
		sequence uint64
		hash     string
	}
	txs := make([]signedTx, cfg.TotalTxs)
	var lastTimestampNonce uint64 // timestamp nonce manager: tracks max used nonce

	for i := 0; i < cfg.TotalTxs; i++ {
		// Default: single-account sender (sequential, timestamp)
		senderAddr := accounts[0].addr
		senderName := accounts[0].name
		acctNum := accounts[0].number
		var seq uint64

		switch cfg.Mode {
		case "sequential":
			seq = cfg.StartSeq + uint64(i)
		case "timestamp":
			// Timestamp nonce manager: use max(now_us, last+1) to guarantee uniqueness
			now := uint64(time.Now().UnixMicro())
			if now <= lastTimestampNonce {
				now = lastTimestampNonce + 1
			}
			lastTimestampNonce = now
			seq = now
		case "baseline":
			acct := &accounts[i%len(accounts)]
			senderAddr = acct.addr
			senderName = acct.name
			acctNum = acct.number
			seq = acct.nextSeq
			acct.nextSeq++
		default:
			log("unknown mode: %s", cfg.Mode)
			os.Exit(1)
		}

		msg := banktypes.NewMsgSend(senderAddr, recip,
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
			WithAccountNumber(acctNum).
			WithSequence(seq).
			WithKeybase(kr).
			WithTxConfig(txCfg).
			WithSignMode(signing.SignMode_SIGN_MODE_DIRECT)

		if err := clienttx.Sign(context.Background(), factory, senderName, txBuilder, true); err != nil {
			log("sign error (tx %d, seq %d): %v", i, seq, err)
			os.Exit(1)
		}

		txBz, err := txCfg.TxEncoder()(txBuilder.GetTx())
		if err != nil {
			log("encode error: %v", err)
			os.Exit(1)
		}

		h := sha256.Sum256(txBz)
		hash := strings.ToUpper(hex.EncodeToString(h[:]))

		txs[i] = signedTx{bytes: txBz, sequence: seq, hash: hash}
	}
	log("Pre-signing complete.")

	// ---- Phase 2: Broadcast ----
	var txBroadcastMode txtypes.BroadcastMode
	switch *broadcastMode {
	case "async":
		txBroadcastMode = txtypes.BroadcastMode_BROADCAST_MODE_ASYNC
	default:
		txBroadcastMode = txtypes.BroadcastMode_BROADCAST_MODE_SYNC
	}
	log("Broadcasting %d txs, concurrency=%d, mode=%s...", cfg.TotalTxs, cfg.Concurrency, *broadcastMode)

	// Capture height BEFORE broadcasting starts so the block scanner
	// doesn't miss txs included in blocks produced during the broadcast window.
	rpcURL := strings.Replace(strings.Replace(cfg.WSAddr, "ws://", "http://", 1), "wss://", "https://", 1)
	preBroadcastHeight := getBlockHeight(rpcURL)

	results := make([]TxResult, cfg.TotalTxs)
	var wg sync.WaitGroup
	sem := make(chan struct{}, cfg.Concurrency)
	var submitted atomic.Int64
	var totalRetries atomic.Int64

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

			// SYNC retry: on wrong_sequence (code 32), backoff and retry on next endpoint.
			// Gossip needs time to propagate the previous tx to other nodes.
			maxAttempts := 1
			if *broadcastMode == "sync" && *retrySeq > 0 {
				maxAttempts = 1 + *retrySeq
			}

			var resp *txtypes.BroadcastTxResponse
			var err error
			for attempt := 0; attempt < maxAttempts; attempt++ {
				txClient := txtypes.NewServiceClient(grpcConns[(idx+attempt)%len(grpcConns)])
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				resp, err = txClient.BroadcastTx(ctx, &txtypes.BroadcastTxRequest{
					TxBytes: tx.bytes,
					Mode:    txBroadcastMode,
				})
				cancel()

				if err == nil && resp != nil && resp.TxResponse != nil &&
					resp.TxResponse.Code == 32 && attempt < maxAttempts-1 {
					totalRetries.Add(1)
					backoff := time.Duration(math.Min(
						float64(100)*math.Pow(2, float64(attempt)), 2000,
					)) * time.Millisecond
					time.Sleep(backoff)
					continue
				}
				break
			}

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
		pollInterval := 1 * time.Second
		asyncRetryRound := 0

		for time.Now().Before(deadline) {
			// Scan blocks via RPC (reliable, no event dropping)
			tracker.scanBlocks(rpcURL, preBroadcastHeight)

			if tracker.allConfirmed(accepted) {
				log("All %d txs confirmed!", accepted)
				break
			}

			// ASYNC retry: re-broadcast unconfirmed txs each poll cycle.
			// ASYNC masks CheckTx failures (returns code 0 always), so we detect
			// unconfirmed txs via block scanning and re-broadcast them.
			if *broadcastMode == "async" && *retrySeq > 0 && asyncRetryRound < *retrySeq {
				var unconfirmed []int
				for i, r := range results {
					if r.BroadcastCode == 0 && r.Error == "" {
						if _, ok := tracker.getConfirmation(r.TxHash); !ok {
							unconfirmed = append(unconfirmed, i)
						}
					}
				}
				if len(unconfirmed) > 0 {
					asyncRetryRound++
					log("Async retry round %d: re-broadcasting %d unconfirmed txs...", asyncRetryRound, len(unconfirmed))
					for _, ui := range unconfirmed {
						tx := txs[ui]
						txClient := txtypes.NewServiceClient(grpcConns[(ui+asyncRetryRound)%len(grpcConns)])
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						txClient.BroadcastTx(ctx, &txtypes.BroadcastTxRequest{TxBytes: tx.bytes, Mode: txBroadcastMode})
						cancel()
						totalRetries.Add(1)
					}
				}
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
	var lastConfTime time.Time // latest confirmation timestamp across all txs

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
				if conf.timestamp.After(lastConfTime) {
					lastConfTime = conf.timestamp
				}
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

	for _, h := range heights {
		blockStats = append(blockStats, BlockStat{Height: h, TxCount: blockTxCounts[h]})
	}

	// TPS calculations
	broadcastTPS := float64(0)
	if broadcastElapsed.Seconds() > 0 {
		broadcastTPS = float64(accepted) / broadcastElapsed.Seconds()
	}

	confirmedTPS := float64(0)
	if confirmed > 0 && !lastConfTime.IsZero() {
		// Wall-clock span from first broadcast to last confirmation
		totalTime := lastConfTime.Sub(broadcastStart)
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
			TotalDurationMs:  time.Since(startTime).Milliseconds(),
			StartedAt:        startTime.UTC().Format(time.RFC3339),
			BlockStats:       blockStats,
			TotalRetries:     totalRetries.Load(),
		},
		Failures: failures,
	}

	if *emitTxs {
		result.Txs = results
	}

	out, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(out))

	log("Done. accepted=%d confirmed=%d failed=%d retries=%d broadcast_tps=%.1f confirmed_tps=%.1f",
		accepted, confirmed, failed, totalRetries.Load(), broadcastTPS, confirmedTPS)
}
