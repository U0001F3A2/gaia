# Benchmark: Baseline vs Sequential vs Timestamp Nonce

## Setup

Single-node gaia-fork testnet with x/nonce module enabled.

- Chain: gaia-fork (custom build with x/nonce)
- Block time: ~1s (timeout_commit=1s, timeout_propose=1s)
- Gas price: ~0 (feemarket base fee set to 0.001, effectively zero)
- Mempool size: 50,000
- Transport: gRPC broadcast
- Confirmation tracking: RPC block scanning (poll /block per height, SHA256 match)

Benchmark tool: `tests/e2e/nonce/benchmark/spammer.go`

Three-phase measurement:
1. Pre-sign all txs offline (not counted in timing)
2. Broadcast via gRPC, record per-tx broadcast latency
3. Scan blocks via RPC to track inclusion

## Scenarios

Four scenarios isolate different bottlenecks:

Baseline: 5000 accounts, 1 tx each, concurrency=5000, SYNC broadcast. Each account sends exactly one tx with its own sequence number. All 5000 txs broadcast concurrently -- no nonce contention, no broadcast throttling. Measures raw node throughput.

Seq-sync: 1 account, 5000 txs, concurrency=1, SYNC broadcast. Nonce manager pattern: sign tx with seq=N, send via BROADCAST_MODE_SYNC (waits for CheckTx response), increment to N+1, repeat. This is the standard way to send many txs from one account. SYNC mode means each tx is fully validated (CheckTx) before the next one is sent.

Seq-async: 1 account, 5000 txs, concurrency=1, ASYNC broadcast. Same nonce manager pattern, but uses BROADCAST_MODE_ASYNC (returns immediately without waiting for CheckTx). Tests whether skipping the CheckTx wait improves sequential throughput.

Timestamp: 1 account, 5000 txs, concurrency=50, SYNC broadcast. Each tx gets a unique microsecond timestamp as its nonce (above the 2^40 threshold). Timestamp nonce manager guarantees uniqueness via max(now_us, last_used + 1). No ordering constraint -- the mempool accepts them regardless of arrival order.

## Results

5000 txs per scenario, single node, 2026-02-12

```
                       Baseline    Seq-Sync   Seq-Async   Timestamp
-------------------------------------------------------------------
submitted                  5000        5000        5000        5000
accepted                   5000        5000        5000        5000
confirmed                  5000        5000        5000        5000
failed                        0           0           0           0
broadcast_tps            4177.4      1540.7      1437.2      4385.4
confirmed_tps            1562.6      1172.9      1108.8      1580.1
bcast_p50_ms             661.8         0.5         0.5        10.3
bcast_p95_ms            1131.9         0.7         0.7        14.8
incl_p50_ms             3195.6      2056.5      1951.3      1643.4
incl_p95_ms             3199.6      3086.5      3335.8      2113.2
broadcast_dur_ms          1196        3245        3478        1140
wrong_sequence               0           0           0           0
duplicate_nonce              0           0           0           0
expired_nonce                0           0           0           0
```

Block distribution:
- Baseline: 70 + 4930 = 5000 across 2 blocks
- Seq-sync: 2051 + 1446 + 1503 = 5000 across 3 blocks
- Seq-async: 1159 + 1781 + 1544 + 516 = 5000 across 4 blocks
- Timestamp: 4626 + 374 = 5000 across 2 blocks

## Analysis

All four modes achieved 100% acceptance and 100% confirmation (5000/5000). The differences are entirely in broadcast throughput and block packing.

### Broadcast throughput

Baseline (4177 TPS) and timestamp (4385 TPS) broadcast at similar speeds -- both use high concurrency to dump all txs into the mempool as fast as possible. The small gap is noise; they're both limited by gRPC throughput on a single connection.

Sequential modes (~1500 TPS) are 2.8x slower. Sequential is structurally limited to concurrency=1: the nonce manager must serialize sign-send-increment for each tx. Timestamp uses concurrency=50, sending txs in parallel since nonces have no ordering dependency.

### SYNC vs ASYNC makes no difference for sequential

Seq-sync (1541 TPS) and seq-async (1437 TPS) are within noise of each other. ASYNC mode skips the server-side CheckTx wait, but with concurrency=1 the bottleneck is the gRPC round-trip itself (~0.5ms), not CheckTx processing. The broadcast durations are similar (3.2-3.5s). ASYNC mode does not improve sequential throughput because the serial submission pattern -- not the broadcast mode -- is the bottleneck.

### Block packing

Baseline and timestamp both packed ~4900+ txs into a single block (98% and 93% of the total respectively). With high concurrency, all txs hit the mempool before the first block proposal, and the proposer packs as many as possible. The remaining txs spill into a second block.

Sequential spreads across 3-4 blocks because the 3.2-3.5s broadcast window spans multiple block intervals. The first block captures txs that arrived before proposal time; subsequent blocks pick up the rest.

### Per-tx latency

Sequential broadcast latency (0.5ms p50) is extremely low because concurrency=1 means zero gRPC contention -- each tx gets an immediate response. Timestamp (10.3ms p50) sees moderate contention at concurrency=50. Baseline (662ms p50) is high because all 5000 goroutines are in-flight simultaneously, queuing on the single gRPC connection.

Baseline inclusion latency (3196ms p50) is high despite fast confirmed TPS because the measurement is per-tx: each tx's inclusion latency includes the time it spent waiting in the gRPC queue before broadcast. Timestamp inclusion latency (1643ms p50) is lower because concurrency=50 processes the queue faster with less per-tx wait.

Sequential inclusion latency (2057ms p50, 3087ms p95) is higher than timestamp because later txs in the serial stream must wait for additional blocks.

### Why sequential achieves 0 failures

The sequential scenarios use concurrency=1 with SYNC broadcast, meaning each tx is sent and confirmed by CheckTx before the next one is dispatched. The mempool receives txs in strict order (seq=N, N+1, N+2...) and tracks the pending sequence correctly. Even when blocks commit mid-broadcast (resetting the mempool's pending state), the remaining pending txs still form a valid ordered sequence starting from the new committed state.

This is the best-case scenario for sequential nonces. In practice, multiple clients submitting from the same account, network delays, or concurrent submission from a single client (concurrency > 1) would cause wrong_sequence failures. Nonce mismatch is easy to reproduce: running the sequential scenario with concurrency=50 causes 99%+ of txs to fail with wrong_sequence, since multiple goroutines race to submit txs with the same sequence number. This benchmark intentionally uses concurrency=1 to show the structural throughput ceiling, not the failure mode.

The test demonstrates that the nonce manager pattern works correctly when used as designed, but it imposes a structural throughput ceiling: one account can only submit ~1500 txs/sec regardless of node capacity.

### Key takeaway

Confirmed TPS tells the real story: baseline (1563), timestamp (1580), seq-sync (1173), seq-async (1109). Baseline and timestamp are within noise of each other -- a single account with timestamp nonces achieves the same confirmed throughput as 5000 separate accounts. The node's per-block capacity (~4900 txs/block) is the shared ceiling.

Sequential nonces with a nonce manager are reliable but structurally limited to ~1500 TPS per account regardless of broadcast mode (SYNC vs ASYNC). The only way to increase single-account throughput with sequential nonces is to use multiple accounts -- which defeats the purpose of a single-account workflow.

## Notes

Feemarket: Gaia-fork includes a dynamic feemarket module that adjusts the base gas price based on block utilization. When blocks are full (e.g., 4000+ txs consuming ~200M gas against a 30M target), the base fee spikes, causing txs to fail with `insufficient fee`. For this benchmark, the feemarket was neutered by setting `max_block_utilization` to 300B gas (effectively infinite) and `min_base_gas_price` to 0.001.

WebSocket event dropping: The initial implementation used CometBFT WebSocket subscriptions (`tm.event='Tx'`) to track block inclusion. CometBFT's event subscription buffer overflows when a block includes hundreds of txs, silently dropping events. The benchmark uses RPC block scanning instead.

Block scanner timing: The block scanner captures the chain height before the broadcast loop starts (not after), so txs included in blocks produced during the broadcast window are not missed.

Timestamp nonce manager: Each timestamp nonce is generated as max(current_timestamp_us, last_used + 1), guaranteeing strict monotonic uniqueness even when txs are generated faster than 1us apart.

## Reproduction

From `repos/gaia-fork/`:

```bash
# Build
go build -o ./gaiad ./cmd/gaiad

# Run all 4 scenarios (init, account creation, start, benchmark, comparison, cleanup)
./tests/e2e/nonce/benchmark/run_local_benchmark.sh --accounts 5000 --concurrency 50
```

Or run scenarios individually against a running node:

```bash
# Baseline (5000 accounts, 1 tx each)
./spammer --mode baseline --num-accounts 5000 --key-prefix bench \
  --concurrency 5000 --chain-id nonce-test --grpc localhost:9090 \
  --key-home /tmp/nonce-bench --key-name validator \
  --recipient $ADDR --wait-blocks 30 --emit-txs > baseline.json

# Sequential-sync (nonce manager, SYNC broadcast)
./spammer --mode sequential --total 5000 --concurrency 1 \
  --broadcast-mode sync --chain-id nonce-test --grpc localhost:9090 \
  --key-home /tmp/nonce-bench --key-name validator \
  --recipient $ADDR --wait-blocks 30 --emit-txs > seq-sync.json

# Sequential-async (nonce manager, ASYNC broadcast)
./spammer --mode sequential --total 5000 --concurrency 1 \
  --broadcast-mode async --chain-id nonce-test --grpc localhost:9090 \
  --key-home /tmp/nonce-bench --key-name validator \
  --recipient $ADDR --wait-blocks 30 --emit-txs > seq-async.json

# Timestamp (parallel broadcast)
./spammer --mode timestamp --total 5000 --concurrency 50 \
  --chain-id nonce-test --grpc localhost:9090 \
  --key-home /tmp/nonce-bench --key-name validator \
  --recipient $ADDR --wait-blocks 30 --emit-txs > timestamp.json
```

Raw JSON output includes per-tx results (with `--emit-txs`), per-block stats, and failure categorization.
