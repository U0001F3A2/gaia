#!/bin/bash
# Self-contained local benchmark: baseline vs sequential vs timestamp nonce.
#
# Does everything: init chain + accounts, start node, run 3 scenarios, compare, cleanup.
# No Docker required. Run from repo root (where gaiad binary lives).
#
# Usage:
#   ./tests/e2e/nonce/benchmark/run_local_benchmark.sh [--accounts N] [--concurrency N]
#
# Scenarios:
#   1. baseline:    N accounts, 1 tx each, full concurrency (raw node TPS)
#   2. sequential:  1 account, N txs, serial send (nonce manager pattern)
#   3. timestamp:   1 account, N txs, full concurrency (timestamp nonce)
#
# Defaults: 1000 accounts/txs, concurrency 50

set -euo pipefail

# ---- Parse args ----
NUM_ACCOUNTS=1000
CONCURRENCY=50
while [[ $# -gt 0 ]]; do
  case "$1" in
    --accounts)    NUM_ACCOUNTS="$2"; shift 2 ;;
    --concurrency) CONCURRENCY="$2"; shift 2 ;;
    *)             echo "Unknown arg: $1"; exit 1 ;;
  esac
done

TOTAL="$NUM_ACCOUNTS"  # same tx count across all scenarios for fair comparison

# ---- Paths ----
GAIAD="./gaiad"
SPAMMER="./spammer"
HOME_DIR="/tmp/nonce-bench"
CHAIN_ID="nonce-test"
RESULTS_DIR="/tmp/benchmark-results"
RPC_URL="http://localhost:26657"

if [ ! -x "$GAIAD" ]; then
  echo "ERROR: $GAIAD not found. Build with: go build -o ./gaiad ./cmd/gaiad"
  exit 1
fi

# Build spammer if stale or missing
SPAMMER_SRC="./tests/e2e/nonce/benchmark/spammer.go"
if [ ! -x "$SPAMMER" ] || [ "$SPAMMER_SRC" -nt "$SPAMMER" ]; then
  echo "Building spammer..."
  CGO_ENABLED=0 go build -o "$SPAMMER" "$SPAMMER_SRC"
fi

cleanup() {
  echo ""
  echo "=== Cleanup ==="
  kill "$GAIAD_PID" 2>/dev/null && wait "$GAIAD_PID" 2>/dev/null
  echo "  gaiad stopped"
  echo "  chain data: $HOME_DIR"
  echo "  results:    $RESULTS_DIR"
}
trap cleanup EXIT
GAIAD_PID=""

# ---- Helpers ----
wait_blocks() {
  local n="${1:-2}"
  local start_height
  start_height=$(curl -sf "$RPC_URL/status" | jq -r '.result.sync_info.latest_block_height')
  local target=$((start_height + n))
  while true; do
    cur=$(curl -sf "$RPC_URL/status" | jq -r '.result.sync_info.latest_block_height')
    if [ "$cur" -ge "$target" ] 2>/dev/null; then break; fi
    sleep 1
  done
}

# ---- Phase 0: Init chain + accounts ----
echo "=== Init chain ==="
rm -rf "$HOME_DIR"
mkdir -p "$RESULTS_DIR"

$GAIAD init bench-node --chain-id "$CHAIN_ID" --home "$HOME_DIR" > /dev/null 2>&1

# Create validator (used for sequential/timestamp tests)
$GAIAD keys add validator --keyring-backend test --home "$HOME_DIR" > /dev/null 2>&1
VALIDATOR=$($GAIAD keys show validator -a --keyring-backend test --home "$HOME_DIR")
$GAIAD genesis add-genesis-account "$VALIDATOR" 100000000000stake --home "$HOME_DIR"
$GAIAD genesis gentx validator 100000000stake \
  --keyring-backend test --chain-id "$CHAIN_ID" --home "$HOME_DIR" > /dev/null 2>&1
$GAIAD genesis collect-gentxs --home "$HOME_DIR" > /dev/null 2>&1

# Create benchmark accounts for baseline mode (parallelized for speed)
echo "  creating $NUM_ACCOUNTS benchmark accounts..."
ADDR_FILE="$HOME_DIR/bench_addrs.txt"
> "$ADDR_FILE"

# Phase 1: Generate keys in parallel (32 at a time)
KEYGEN_BATCH=32
for i in $(seq 0 $((NUM_ACCOUNTS - 1))); do
  $GAIAD keys add "bench$i" --keyring-backend test --home "$HOME_DIR" > /dev/null 2>&1 &
  if (( (i + 1) % KEYGEN_BATCH == 0 )); then wait; fi
done
wait
echo "  keys generated, collecting addresses..."

# Phase 2: Collect all addresses in one call (much faster than N individual lookups)
$GAIAD keys list --keyring-backend test --home "$HOME_DIR" --output json 2>/dev/null \
  | jq -r '.[] | select(.name | startswith("bench")) | .address' > "$ADDR_FILE"

# Phase 3: Inject all accounts into genesis in one jq pass (much faster than N gaiad calls)
GENESIS="$HOME_DIR/config/genesis.json"
jq --arg amt "10000000" --rawfile addrs "$ADDR_FILE" '
  ($addrs | split("\n") | map(select(length > 0))) as $addr_list |
  .app_state.bank.balances += [
    $addr_list[] | {"address": ., "coins": [{"denom": "stake", "amount": $amt}]}
  ] |
  .app_state.auth.accounts += [
    $addr_list[] | {"@type": "/cosmos.auth.v1beta1.BaseAccount", "address": ., "pub_key": null, "account_number": "0", "sequence": "0"}
  ] |
  .app_state.bank.supply = [
    {"denom": "stake", "amount": (
      (.app_state.bank.balances | map(.coins[] | select(.denom == "stake") | .amount | tonumber) | add) | tostring
    )}
  ]
' "$GENESIS" > "$GENESIS.tmp" && mv "$GENESIS.tmp" "$GENESIS"
echo "  $NUM_ACCOUNTS accounts created and funded"

# Config: fast blocks, large mempool, gRPC on all interfaces, zero gas, feemarket off
sed -i 's/timeout_commit = "5s"/timeout_commit = "1s"/g' "$HOME_DIR/config/config.toml"
sed -i 's/timeout_propose = "3s"/timeout_propose = "1s"/g' "$HOME_DIR/config/config.toml"
sed -i 's/index_all_keys = false/index_all_keys = true/g' "$HOME_DIR/config/config.toml"
sed -i 's/size = 5000/size = 50000/g' "$HOME_DIR/config/config.toml"
sed -i 's#address = "localhost:9090"#address = "0.0.0.0:9090"#g' "$HOME_DIR/config/app.toml"
sed -i 's/minimum-gas-prices = ""/minimum-gas-prices = "0stake"/g' "$HOME_DIR/config/app.toml"
sed -i 's/enable = false/enable = true/g' "$HOME_DIR/config/app.toml"

# Neuter feemarket: keep enabled (required) but set base fee to minimum and max utilization very high
# so the dynamic fee never spikes even with full blocks
jq '.app_state.feemarket.params.min_base_gas_price = "0.001000000000000000" |
    .app_state.feemarket.params.max_block_utilization = "300000000000" |
    .app_state.feemarket.state.base_gas_price = "0.001000000000000000"' \
  "$HOME_DIR/config/genesis.json" > "$HOME_DIR/config/genesis.json.tmp" && \
  mv "$HOME_DIR/config/genesis.json.tmp" "$HOME_DIR/config/genesis.json"

echo "  validator: $VALIDATOR"
echo "  config: 1s blocks, mempool=50k, gRPC 0.0.0.0:9090, zero gas, feemarket=off"

# ---- Phase 1: Start node ----
echo ""
echo "=== Starting gaiad ==="
$GAIAD start --home "$HOME_DIR" > "$HOME_DIR/gaiad.log" 2>&1 &
GAIAD_PID=$!

for i in $(seq 1 30); do
  HEIGHT=$(curl -sf "$RPC_URL/status" 2>/dev/null | jq -r '.result.sync_info.latest_block_height' 2>/dev/null || echo 0)
  if [ "$HEIGHT" -gt 1 ] 2>/dev/null; then
    echo "  node ready at block $HEIGHT (PID $GAIAD_PID)"
    break
  fi
  if [ "$i" = 30 ]; then echo "ERROR: node did not start"; exit 1; fi
  sleep 1
done

# ---- Phase 2: Run scenarios ----

# Scenario 1: Baseline (multi-account, no nonce contention)
echo ""
echo "=== Scenario: baseline ($NUM_ACCOUNTS accounts x 1 tx, concurrency=$NUM_ACCOUNTS) ==="
echo "  Each account sends 1 tx. No nonce contention. Measures raw node throughput."
$SPAMMER \
  --mode baseline \
  --num-accounts "$NUM_ACCOUNTS" \
  --key-prefix bench \
  --concurrency "$NUM_ACCOUNTS" \
  --chain-id "$CHAIN_ID" \
  --grpc localhost:9090 \
  --key-home "$HOME_DIR" \
  --key-name validator \
  --recipient "$VALIDATOR" \
  --wait-blocks 30 \
  --emit-txs \
  > "$RESULTS_DIR/baseline.json" 2>"$RESULTS_DIR/baseline.log"
cat "$RESULTS_DIR/baseline.log"
echo ""
wait_blocks 3

# Scenario 2: Sequential-sync (1 account, serial nonce manager, SYNC broadcast)
echo ""
echo "=== Scenario: seq-sync ($TOTAL txs, 1 account, concurrency=1, SYNC) ==="
echo "  Nonce manager: sign seq=N, broadcast SYNC (wait for CheckTx), increment, repeat."
$SPAMMER \
  --mode sequential \
  --total "$TOTAL" \
  --concurrency 1 \
  --broadcast-mode sync \
  --chain-id "$CHAIN_ID" \
  --grpc localhost:9090 \
  --key-home "$HOME_DIR" \
  --key-name validator \
  --recipient "$VALIDATOR" \
  --wait-blocks 30 \
  --emit-txs \
  > "$RESULTS_DIR/seq-sync.json" 2>"$RESULTS_DIR/seq-sync.log"
cat "$RESULTS_DIR/seq-sync.log"
echo ""
wait_blocks 3

# Scenario 3: Sequential-async (1 account, serial nonce manager, ASYNC broadcast)
echo ""
echo "=== Scenario: seq-async ($TOTAL txs, 1 account, concurrency=1, ASYNC) ==="
echo "  Nonce manager: sign seq=N, broadcast ASYNC (no CheckTx wait), increment, repeat."
$SPAMMER \
  --mode sequential \
  --total "$TOTAL" \
  --concurrency 1 \
  --broadcast-mode async \
  --chain-id "$CHAIN_ID" \
  --grpc localhost:9090 \
  --key-home "$HOME_DIR" \
  --key-name validator \
  --recipient "$VALIDATOR" \
  --wait-blocks 30 \
  --emit-txs \
  > "$RESULTS_DIR/seq-async.json" 2>"$RESULTS_DIR/seq-async.log"
cat "$RESULTS_DIR/seq-async.log"
echo ""
wait_blocks 3

# Scenario 4: Timestamp (1 account, parallel)
echo ""
echo "=== Scenario: timestamp ($TOTAL txs, 1 account, concurrency=$CONCURRENCY) ==="
echo "  Each tx gets unique timestamp nonce. Full parallel broadcast."
$SPAMMER \
  --mode timestamp \
  --total "$TOTAL" \
  --concurrency "$CONCURRENCY" \
  --chain-id "$CHAIN_ID" \
  --grpc localhost:9090 \
  --key-home "$HOME_DIR" \
  --key-name validator \
  --recipient "$VALIDATOR" \
  --wait-blocks 30 \
  --emit-txs \
  > "$RESULTS_DIR/timestamp.json" 2>"$RESULTS_DIR/timestamp.log"
cat "$RESULTS_DIR/timestamp.log"
echo ""

# ---- Phase 3: Comparison table ----
echo "============================================"
echo " Nonce Benchmark Comparison"
echo " $TOTAL txs per scenario"
echo "============================================"
echo ""

printf "%-22s %14s %14s %14s %14s\n" "" "Baseline" "Seq-Sync" "Seq-Async" "Timestamp"
echo "------------------------------------------------------------------------------------"

for metric in \
  "submitted:.summary.total_submitted" \
  "accepted:.summary.total_accepted" \
  "confirmed:.summary.total_confirmed" \
  "failed:.summary.total_failed" \
  "accept_rate:.summary.total_accepted / .summary.total_submitted * 100" \
  "broadcast_tps:.summary.broadcast_tps" \
  "confirmed_tps:.summary.confirmed_tps" \
  "bcast_p50_ms:.summary.broadcast_latency.p50_ms" \
  "bcast_p95_ms:.summary.broadcast_latency.p95_ms" \
  "incl_p50_ms:.summary.inclusion_latency.p50_ms" \
  "incl_p95_ms:.summary.inclusion_latency.p95_ms" \
  "broadcast_dur_ms:.summary.duration_ms" \
  "total_dur_ms:.summary.total_duration_ms" \
  "wrong_sequence:.failures.wrong_sequence" \
  "duplicate_nonce:.failures.duplicate_nonce" \
  "expired_nonce:.failures.expired_nonce" \
  "mempool_full:.failures.mempool_full" \
  "other:.failures.other"; do
  label="${metric%%:*}"
  path="${metric#*:}"
  fmt_val() { echo "$1" | awk '{if($1+0==$1 && $1~/\./){printf "%.1f",$1}else{print $1}}'; }
  base_val=$(fmt_val "$(jq -r "$path // 0" "$RESULTS_DIR/baseline.json")")
  seqs_val=$(fmt_val "$(jq -r "$path // 0" "$RESULTS_DIR/seq-sync.json")")
  seqa_val=$(fmt_val "$(jq -r "$path // 0" "$RESULTS_DIR/seq-async.json")")
  ts_val=$(fmt_val "$(jq -r "$path // 0" "$RESULTS_DIR/timestamp.json")")
  if [ "$label" = "accept_rate" ]; then
    base_val="${base_val}%"; seqs_val="${seqs_val}%"; seqa_val="${seqa_val}%"; ts_val="${ts_val}%"
  fi
  printf "%-22s %14s %14s %14s %14s\n" "$label" "$base_val" "$seqs_val" "$seqa_val" "$ts_val"
done

echo ""

# Show error breakdown for any scenario with failures
for scenario in baseline seq-sync seq-async timestamp; do
  FAIL=$(jq -r '.summary.total_failed' "$RESULTS_DIR/$scenario.json")
  if [ "$FAIL" -gt 0 ] 2>/dev/null; then
    echo "Error samples ($scenario, $FAIL failures):"
    jq -r '[.txs[]? | select(.error != "")] | .[0:3][] | "  seq=\(.sequence) code=\(.broadcast_code) err=\(.error[0:120])"' \
      "$RESULTS_DIR/$scenario.json" 2>/dev/null || echo "  (no per-tx data)"
    echo ""
  fi
done

# Block distribution
for scenario in baseline seq-sync seq-async timestamp; do
  echo "Block distribution ($scenario):"
  NBLOCKS=$(jq '.summary.block_stats | length' "$RESULTS_DIR/$scenario.json")
  if [ "$NBLOCKS" -eq 0 ] 2>/dev/null; then
    echo "  (no confirmed txs)"
  elif [ "$NBLOCKS" -gt 10 ]; then
    jq -r '.summary.block_stats[:3][] | "  height=\(.height) txs=\(.tx_count)"' "$RESULTS_DIR/$scenario.json"
    echo "  ... ($NBLOCKS blocks total)"
    jq -r '.summary.block_stats[-3:][] | "  height=\(.height) txs=\(.tx_count)"' "$RESULTS_DIR/$scenario.json"
  else
    jq -r '.summary.block_stats[] | "  height=\(.height) txs=\(.tx_count)"' "$RESULTS_DIR/$scenario.json"
  fi
  echo ""
done

echo "Raw JSON: $RESULTS_DIR/{baseline,seq-sync,seq-async,timestamp}.json"
echo "=== Done ==="
