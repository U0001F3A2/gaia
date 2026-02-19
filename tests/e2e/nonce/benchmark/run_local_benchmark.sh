#!/bin/bash
# Self-contained local benchmark: baseline vs sequential vs timestamp nonce.
#
# Does everything: init chain + accounts, start node(s), run 4 scenarios, compare, cleanup.
# No Docker required. Run from repo root (where gaiad binary lives).
#
# Usage:
#   ./tests/e2e/nonce/benchmark/run_local_benchmark.sh [--accounts N] [--concurrency N] [--nodes N]
#
# Scenarios:
#   1. baseline:    N accounts, 1 tx each, full concurrency (raw node TPS)
#   2. seq-sync:    1 account, N txs, serial send, SYNC broadcast
#   3. timestamp:   1 account, N txs, full concurrency (timestamp nonce)
#
# Defaults: 1000 accounts/txs, concurrency 50, 1 node
#
# Multi-node mode (--nodes 4):
#   Starts N local validators with unique ports (offset 100 per node).
#   Port allocation: P2P 26656+i*100, RPC 26657+i*100, gRPC 9090+i*100, API 1317+i*100
#   Txs are distributed round-robin across all gRPC endpoints.

set -euo pipefail

# ---- Parse args ----
NUM_ACCOUNTS=1000
CONCURRENCY=10
NODES=1
while [[ $# -gt 0 ]]; do
  case "$1" in
    --accounts)    NUM_ACCOUNTS="$2"; shift 2 ;;
    --concurrency) CONCURRENCY="$2"; shift 2 ;;
    --nodes)       NODES="$2"; shift 2 ;;
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

GAIAD_PIDS=()
cleanup() {
  echo ""
  echo "=== Cleanup ==="
  for pid in "${GAIAD_PIDS[@]}"; do
    kill "$pid" 2>/dev/null && wait "$pid" 2>/dev/null
  done
  echo "  gaiad stopped (${#GAIAD_PIDS[@]} process(es))"
  echo "  chain data: $HOME_DIR"
  echo "  results:    $RESULTS_DIR"
}
trap cleanup EXIT

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
echo "=== Init chain ($NODES node(s)) ==="
rm -rf "$HOME_DIR"
mkdir -p "$RESULTS_DIR"

if [ "$NODES" -eq 1 ]; then
  # ---- Single-node init (unchanged) ----
  $GAIAD init bench-node --chain-id "$CHAIN_ID" --home "$HOME_DIR" > /dev/null 2>&1

  $GAIAD keys add validator --keyring-backend test --home "$HOME_DIR" > /dev/null 2>&1
  VALIDATOR=$($GAIAD keys show validator -a --keyring-backend test --home "$HOME_DIR")
  $GAIAD genesis add-genesis-account "$VALIDATOR" 100000000000stake --home "$HOME_DIR"
  $GAIAD genesis gentx validator 100000000stake \
    --keyring-backend test --chain-id "$CHAIN_ID" --home "$HOME_DIR" > /dev/null 2>&1
  $GAIAD genesis collect-gentxs --home "$HOME_DIR" > /dev/null 2>&1

  KEY_HOME="$HOME_DIR"
  KEY_NAME="validator"
  GRPC_ENDPOINTS="localhost:9090"

else
  # ---- Multi-node init ----
  $GAIAD testnet init-files \
    --v "$NODES" \
    --output-dir "$HOME_DIR" \
    --chain-id "$CHAIN_ID" \
    --keyring-backend test \
    --minimum-gas-prices "0stake" > /dev/null 2>&1

  # Patch ports and persistent_peers for localhost
  for i in $(seq 0 $((NODES - 1))); do
    NODE_HOME="$HOME_DIR/node${i}/gaiad"
    CONFIG="$NODE_HOME/config/config.toml"
    APP="$NODE_HOME/config/app.toml"

    P2P=$((26656 + i * 100))
    RPC=$((26657 + i * 100))
    GRPC=$((9090 + i * 100))
    API=$((1317 + i * 100))
    PPROF=$((6060 + i * 100))
    PROM=$((26660 + i * 100))

    # Rewrite peer IPs: 192.168.0.(j+1):26656 -> localhost:(26656+j*100)
    for j in $(seq 0 $((NODES - 1))); do
      sed -i "s|192.168.0.$((j + 1)):26656|localhost:$((26656 + j * 100))|g" "$CONFIG"
    done

    # This node's listen addresses
    sed -i "s|tcp://0.0.0.0:26656|tcp://0.0.0.0:${P2P}|" "$CONFIG"
    sed -i "s|tcp://0.0.0.0:26657|tcp://0.0.0.0:${RPC}|" "$CONFIG"
    sed -i "s|localhost:6060|localhost:${PPROF}|" "$CONFIG"
    sed -i "s|:26660|:${PROM}|" "$CONFIG"
    sed -i "s|localhost:9090|0.0.0.0:${GRPC}|" "$APP"
    sed -i "s|tcp://localhost:1317|tcp://0.0.0.0:${API}|" "$APP"
  done

  KEY_HOME="$HOME_DIR/node0/gaiad"
  KEY_NAME="node0"
  VALIDATOR=$($GAIAD keys show node0 -a --keyring-backend test --home "$KEY_HOME")

  # Build GRPC endpoint string
  GRPC_ENDPOINTS=""
  for i in $(seq 0 $((NODES - 1))); do
    [ -n "$GRPC_ENDPOINTS" ] && GRPC_ENDPOINTS="${GRPC_ENDPOINTS},"
    GRPC_ENDPOINTS="${GRPC_ENDPOINTS}localhost:$((9090 + i * 100))"
  done
fi

# Create benchmark accounts on KEY_HOME's keyring
echo "  creating $NUM_ACCOUNTS benchmark accounts..."
ADDR_FILE="$KEY_HOME/bench_addrs.txt"
> "$ADDR_FILE"

KEYGEN_BATCH=32
for i in $(seq 0 $((NUM_ACCOUNTS - 1))); do
  $GAIAD keys add "bench$i" --keyring-backend test --home "$KEY_HOME" > /dev/null 2>&1 &
  if (( (i + 1) % KEYGEN_BATCH == 0 )); then wait; fi
done
wait
echo "  keys generated, collecting addresses..."

$GAIAD keys list --keyring-backend test --home "$KEY_HOME" --output json 2>/dev/null \
  | jq -r '.[] | select(.name | startswith("bench")) | .address' > "$ADDR_FILE"

# Inject bench accounts into genesis, bump validator balance for fees, neuter feemarket
GENESIS="$KEY_HOME/config/genesis.json"
jq --arg amt "10000000" --arg valaddr "$VALIDATOR" --rawfile addrs "$ADDR_FILE" '
  ($addrs | split("\n") | map(select(length > 0))) as $addr_list |
  .app_state.bank.balances += [
    $addr_list[] | {"address": ., "coins": [{"denom": "stake", "amount": $amt}]}
  ] |
  .app_state.auth.accounts += [
    $addr_list[] | {"@type": "/cosmos.auth.v1beta1.BaseAccount", "address": ., "pub_key": null, "account_number": "0", "sequence": "0"}
  ] |
  # Bump validator balance to 100B stake (enough for 5000 txs * 200k fee each)
  .app_state.bank.balances |= map(
    if .address == $valaddr then
      .coins |= map(if .denom == "stake" then .amount = "100000000000" else . end)
    else . end
  ) |
  # Recalculate total supply (per-denom to preserve testtoken etc.)
  .app_state.bank.supply = (
    [.app_state.bank.balances[].coins[]] | group_by(.denom) |
    map({"denom": .[0].denom, "amount": ([.[].amount | tonumber] | add | tostring)})
  ) |
  # Neuter feemarket
  .app_state.feemarket.params.min_base_gas_price = "0.001000000000000000" |
  .app_state.feemarket.params.max_block_utilization = "300000000000" |
  .app_state.feemarket.state.base_gas_price = "0.001000000000000000"
' "$GENESIS" > "$GENESIS.tmp" && mv "$GENESIS.tmp" "$GENESIS"
echo "  $NUM_ACCOUNTS accounts created and funded"

# Apply node config: fast blocks, large mempool, tx indexing, enable gRPC/API
apply_config() {
  local home="$1"
  sed -i 's/timeout_commit = "5s"/timeout_commit = "1s"/g' "$home/config/config.toml"
  sed -i 's/timeout_propose = "3s"/timeout_propose = "1s"/g' "$home/config/config.toml"
  sed -i 's/index_all_keys = false/index_all_keys = true/g' "$home/config/config.toml"
  sed -i 's/size = 5000/size = 50000/g' "$home/config/config.toml"
  # Allow localhost peers (all nodes share 127.0.0.1)
  sed -i 's/addr_book_strict = true/addr_book_strict = false/g' "$home/config/config.toml"
  sed -i 's/allow_duplicate_ip = false/allow_duplicate_ip = true/g' "$home/config/config.toml"
  sed -i 's/enable = false/enable = true/g' "$home/config/app.toml"
}

if [ "$NODES" -eq 1 ]; then
  apply_config "$HOME_DIR"
  sed -i 's#address = "localhost:9090"#address = "0.0.0.0:9090"#g' "$HOME_DIR/config/app.toml"
  sed -i 's/minimum-gas-prices = ""/minimum-gas-prices = "0stake"/g' "$HOME_DIR/config/app.toml"
else
  # Copy modified genesis to all nodes, apply config
  for i in $(seq 0 $((NODES - 1))); do
    NODE_HOME="$HOME_DIR/node${i}/gaiad"
    [ "$i" -gt 0 ] && cp "$GENESIS" "$NODE_HOME/config/genesis.json"
    apply_config "$NODE_HOME"
  done
fi

echo "  validator: $VALIDATOR"
echo "  config: 1s blocks, mempool=50k, gRPC=$GRPC_ENDPOINTS, zero gas, feemarket=off"

# ---- Phase 1: Start node(s) ----
echo ""
echo "=== Starting gaiad ($NODES node(s)) ==="

if [ "$NODES" -eq 1 ]; then
  $GAIAD start --home "$HOME_DIR" > "$HOME_DIR/gaiad.log" 2>&1 &
  GAIAD_PIDS+=($!)
else
  for i in $(seq 0 $((NODES - 1))); do
    NODE_HOME="$HOME_DIR/node${i}/gaiad"
    $GAIAD start --home "$NODE_HOME" > "$NODE_HOME/gaiad.log" 2>&1 &
    GAIAD_PIDS+=($!)
  done
fi

# Wait for node0 to produce blocks
for i in $(seq 1 30); do
  HEIGHT=$(curl -sf "$RPC_URL/status" 2>/dev/null | jq -r '.result.sync_info.latest_block_height' 2>/dev/null || echo 0)
  if [ "$HEIGHT" -gt 1 ] 2>/dev/null; then
    echo "  node0 ready at block $HEIGHT (PIDs: ${GAIAD_PIDS[*]})"
    break
  fi
  if [ "$i" = 30 ]; then echo "ERROR: node did not start"; exit 1; fi
  sleep 1
done

# Multi-node: verify all nodes are synced
if [ "$NODES" -gt 1 ]; then
  for i in $(seq 1 $((NODES - 1))); do
    RPC_I="http://localhost:$((26657 + i * 100))"
    for attempt in $(seq 1 30); do
      H=$(curl -sf "$RPC_I/status" 2>/dev/null | jq -r '.result.sync_info.latest_block_height' 2>/dev/null || echo 0)
      if [ "$H" -gt 1 ] 2>/dev/null; then
        echo "  node$i ready at block $H"
        break
      fi
      if [ "$attempt" = 30 ]; then echo "ERROR: node$i did not start"; exit 1; fi
      sleep 1
    done
  done
fi

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
  --grpc "$GRPC_ENDPOINTS" \
  --key-home "$KEY_HOME" \
  --key-name "$KEY_NAME" \
  --recipient "$VALIDATOR" \
  --wait-blocks 30 \
  --emit-txs \
  > "$RESULTS_DIR/baseline.json" 2>"$RESULTS_DIR/baseline.log"
cat "$RESULTS_DIR/baseline.log"
echo ""
wait_blocks 3

# Scenario 2: Sequential-sync (1 account, serial nonce manager, SYNC broadcast)
echo ""
echo "=== Scenario: seq-sync ($TOTAL txs, 1 account, concurrency=1, SYNC, retry=10) ==="
echo "  Nonce manager with retry: on wrong_sequence, backoff and retry on next endpoint."
$SPAMMER \
  --mode sequential \
  --total "$TOTAL" \
  --concurrency 1 \
  --broadcast-mode sync \
  --retry-seq 10 \
  --chain-id "$CHAIN_ID" \
  --grpc "$GRPC_ENDPOINTS" \
  --key-home "$KEY_HOME" \
  --key-name "$KEY_NAME" \
  --recipient "$VALIDATOR" \
  --wait-blocks 60 \
  --emit-txs \
  > "$RESULTS_DIR/seq-sync.json" 2>"$RESULTS_DIR/seq-sync.log"
cat "$RESULTS_DIR/seq-sync.log"
echo ""
wait_blocks 3

# Scenario 3: Timestamp (1 account, parallel)
echo ""
echo "=== Scenario: timestamp ($TOTAL txs, 1 account, concurrency=$CONCURRENCY) ==="
echo "  Each tx gets unique timestamp nonce. Full parallel broadcast."
$SPAMMER \
  --mode timestamp \
  --total "$TOTAL" \
  --concurrency "$CONCURRENCY" \
  --chain-id "$CHAIN_ID" \
  --grpc "$GRPC_ENDPOINTS" \
  --key-home "$KEY_HOME" \
  --key-name "$KEY_NAME" \
  --recipient "$VALIDATOR" \
  --wait-blocks 30 \
  --emit-txs \
  > "$RESULTS_DIR/timestamp.json" 2>"$RESULTS_DIR/timestamp.log"
cat "$RESULTS_DIR/timestamp.log"
echo ""

# ---- Phase 3: Comparison table ----
echo "============================================"
echo " Nonce Benchmark Comparison"
echo " $TOTAL txs per scenario, $NODES node(s)"
echo "============================================"
echo ""

SCENARIOS="baseline seq-sync timestamp"
HEADERS="Baseline Seq-Sync Timestamp"

printf "%-22s" ""
for h in $HEADERS; do printf " %12s" "$h"; done
echo ""
echo "---------------------------------------------------------------------------------------------------------------------"

for metric in \
  "submitted:.summary.total_submitted" \
  "accepted:.summary.total_accepted" \
  "confirmed:.summary.total_confirmed" \
  "failed:.summary.total_failed" \
  "retries:.summary.total_retries" \
  "broadcast_tps:.summary.broadcast_tps" \
  "confirmed_tps:.summary.confirmed_tps" \
  "bcast_p50_ms:.summary.broadcast_latency.p50_ms" \
  "bcast_p95_ms:.summary.broadcast_latency.p95_ms" \
  "incl_p50_ms:.summary.inclusion_latency.p50_ms" \
  "incl_p95_ms:.summary.inclusion_latency.p95_ms" \
  "broadcast_dur_ms:.summary.duration_ms" \
  "wrong_sequence:.failures.wrong_sequence" \
  "duplicate_nonce:.failures.duplicate_nonce" \
  "expired_nonce:.failures.expired_nonce"; do
  label="${metric%%:*}"
  path="${metric#*:}"
  fmt_val() { echo "$1" | awk '{if($1+0==$1 && $1~/\./){printf "%.1f",$1}else{print $1}}'; }
  printf "%-22s" "$label"
  for s in $SCENARIOS; do
    val=$(fmt_val "$(jq -r "$path // 0" "$RESULTS_DIR/$s.json")")
    printf " %12s" "$val"
  done
  echo ""
done

echo ""

# Show error breakdown for any scenario with failures
for scenario in $SCENARIOS; do
  FAIL=$(jq -r '.summary.total_failed' "$RESULTS_DIR/$scenario.json")
  if [ "$FAIL" -gt 0 ] 2>/dev/null; then
    echo "Error samples ($scenario, $FAIL failures):"
    jq -r '[.txs[]? | select(.error != "")] | .[0:3][] | "  seq=\(.sequence) code=\(.broadcast_code) err=\(.error[0:120])"' \
      "$RESULTS_DIR/$scenario.json" 2>/dev/null || echo "  (no per-tx data)"
    echo ""
  fi
done

# Block distribution
for scenario in $SCENARIOS; do
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

echo "Raw JSON: $RESULTS_DIR/{baseline,seq-sync,timestamp}.json"
echo "=== Done ==="
