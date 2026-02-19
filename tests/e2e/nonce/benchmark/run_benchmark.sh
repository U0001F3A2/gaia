#!/bin/bash
# Benchmark orchestrator: runs multiple spammer scenarios and produces a summary.
# Runs inside the Docker spammer container.

set -euo pipefail

HOME_DIR="${GAIA_HOME:-/root/.gaia}"
GRPC="${GRPC_ENDPOINTS:-node:9090}"
# Derive WS host from first gRPC endpoint
FIRST_GRPC="${GRPC%%,*}"
WS_HOST="${FIRST_GRPC%%:*}"
WS_URL="ws://${WS_HOST}:26657"
RPC_URL="http://${WS_HOST}:26657"

CHAIN_ID="nonce-test"
KEY_NAME="validator"
RESULTS_DIR="/tmp/benchmark-results"

mkdir -p "$RESULTS_DIR"

VALIDATOR_ADDR=$(gaiad keys show "$KEY_NAME" -a --keyring-backend test --home "$HOME_DIR")
echo "Benchmark account: $VALIDATOR_ADDR"
echo "gRPC: $GRPC, WebSocket: $WS_URL"
echo ""

# Wait for N new blocks (polls /status). More reliable than sleep.
wait_blocks() {
  local n="${1:-2}"
  local start_height
  start_height=$(curl -sf "${RPC_URL}/status" | jq -r '.result.sync_info.latest_block_height')
  local target=$((start_height + n))
  echo "  waiting for block $target (current: $start_height)..."
  while true; do
    local cur
    cur=$(curl -sf "${RPC_URL}/status" | jq -r '.result.sync_info.latest_block_height')
    if [ "$cur" -ge "$target" ] 2>/dev/null; then
      break
    fi
    sleep 1
  done
}

run_scenario() {
  local NAME="$1"
  local MODE="$2"
  local TOTAL="$3"
  local CONCURRENCY="$4"
  local EXTRA_FLAGS="${5:-}"

  echo "=== Scenario: $NAME ==="
  echo "  mode=$MODE total=$TOTAL concurrency=$CONCURRENCY"

  spammer \
    --mode "$MODE" \
    --total "$TOTAL" \
    --concurrency "$CONCURRENCY" \
    --chain-id "$CHAIN_ID" \
    --grpc "$GRPC" \
    --ws "$WS_URL" \
    --key-home "$HOME_DIR" \
    --key-name "$KEY_NAME" \
    --recipient "$VALIDATOR_ADDR" \
    --wait-blocks 15 \
    $EXTRA_FLAGS \
    > "$RESULTS_DIR/$NAME.json" 2>"$RESULTS_DIR/$NAME.log"

  # Print spammer log (stderr) and summary
  cat "$RESULTS_DIR/$NAME.log" >&2
  echo "  Result:"
  jq '{accepted: .summary.total_accepted, confirmed: .summary.total_confirmed, failed: .summary.total_failed, broadcast_tps: .summary.broadcast_tps, confirmed_tps: .summary.confirmed_tps, broadcast_p50: .summary.broadcast_latency.p50_ms, inclusion_p50: .summary.inclusion_latency.p50_ms}' "$RESULTS_DIR/$NAME.json"
  echo ""

  # Wait for 2 new blocks between scenarios so state settles
  wait_blocks 2
}

echo "========================================"
echo " x/nonce Benchmark Suite"
echo "========================================"
echo ""

# Scenario 1: Sequential baseline (serial, no parallelism possible)
run_scenario "01_sequential_baseline" "sequential" 50 1

# Scenario 2: Timestamp single-account, moderate parallelism
run_scenario "02_timestamp_parallel" "timestamp" 200 50

# Scenario 3: Mixed sequential + timestamp interleave
run_scenario "03_mixed" "mixed" 100 10

# Scenario 4: Timestamp burst (high concurrency)
run_scenario "04_timestamp_burst" "timestamp" 500 200

# ---- Summary Table ----
echo "========================================"
echo " Benchmark Summary"
echo "========================================"
printf "%-25s %6s %6s %6s %9s %9s %9s %9s\n" \
  "Scenario" "Acpt" "Conf" "Fail" "Bcast/s" "Conf/s" "BcP50ms" "IncP50ms"
echo "---------------------------------------------------------------------------------------------------"

for f in "$RESULTS_DIR"/*.json; do
  NAME=$(basename "$f" .json)
  ACPT=$(jq -r '.summary.total_accepted' "$f")
  CONF=$(jq -r '.summary.total_confirmed' "$f")
  FAIL=$(jq -r '.summary.total_failed' "$f")
  BTPS=$(jq -r '.summary.broadcast_tps | . * 10 | floor / 10' "$f")
  CTPS=$(jq -r '.summary.confirmed_tps | . * 10 | floor / 10' "$f")
  BP50=$(jq -r '.summary.broadcast_latency.p50_ms | . * 10 | floor / 10' "$f")
  IP50=$(jq -r '.summary.inclusion_latency.p50_ms | . * 10 | floor / 10' "$f")
  printf "%-25s %6s %6s %6s %9s %9s %9s %9s\n" \
    "$NAME" "$ACPT" "$CONF" "$FAIL" "$BTPS" "$CTPS" "$BP50" "$IP50"
done

echo ""

# Show per-block distribution for the burst scenario
if [ -f "$RESULTS_DIR/04_timestamp_burst.json" ]; then
  echo "Per-block tx distribution (burst scenario):"
  jq -r '.summary.block_stats[] | "  height=\(.height) txs=\(.tx_count)"' \
    "$RESULTS_DIR/04_timestamp_burst.json" 2>/dev/null || echo "  (no block stats available)"
  echo ""
fi

echo "Full results in $RESULTS_DIR/"
echo "Done."
