#!/bin/bash
# Shared chain initialization for e2e and benchmark modes.
# Usage: init_chain.sh [node_index]
#   node_index: optional, for multi-node benchmark setup (0-3)
#   If omitted, initializes a single-node chain.

set -euo pipefail

NODE_INDEX="${1:-}"
CHAIN_ID="nonce-test"
HOME_DIR="${GAIA_HOME:-/root/.gaia}"
GAIAD="gaiad"
MONIKER="nonce-node${NODE_INDEX:-0}"
USER_COINS="100000000000stake"
VALIDATOR_STAKE="100000000stake"

echo "[init_chain] chain_id=$CHAIN_ID home=$HOME_DIR node_index=${NODE_INDEX:-single}"

# Clean slate (rm contents, not the dir itself -- it may be a volume mount point)
rm -rf "${HOME_DIR:?}"/*

$GAIAD init "$MONIKER" --chain-id "$CHAIN_ID" --home "$HOME_DIR"

# Create keys
$GAIAD keys add validator --keyring-backend test --home "$HOME_DIR"
$GAIAD keys add user --keyring-backend test --home "$HOME_DIR"

# Fund both accounts
VALIDATOR_ADDR=$($GAIAD keys show validator -a --keyring-backend test --home "$HOME_DIR")
USER_ADDR=$($GAIAD keys show user -a --keyring-backend test --home "$HOME_DIR")

$GAIAD genesis add-genesis-account "$VALIDATOR_ADDR" "$USER_COINS" --home "$HOME_DIR"
$GAIAD genesis add-genesis-account "$USER_ADDR" "$USER_COINS" --home "$HOME_DIR"

# Gentx
$GAIAD genesis gentx validator "$VALIDATOR_STAKE" \
  --keyring-backend test \
  --chain-id "$CHAIN_ID" \
  --home "$HOME_DIR"

$GAIAD genesis collect-gentxs --home "$HOME_DIR"

# Genesis tweaks: fast blocks, shorter voting period, short nonce past window (10s)
jq '.app_state.gov.params.voting_period = "20s" |
    .app_state.gov.params.expedited_voting_period = "10s" |
    .app_state.staking.params.unbonding_time = "86400s" |
    .app_state.nonce.params.past_window_us = "10000000" |
    .app_state.nonce.params.future_window_us = "300000000" |
    .app_state.nonce.params.timestamp_nonce_cutoff = "1099511627776"' \
    "$HOME_DIR/config/genesis.json" > "$HOME_DIR/config/genesis_tmp.json"
mv "$HOME_DIR/config/genesis_tmp.json" "$HOME_DIR/config/genesis.json"

# Config: fast block times
sed -i 's/timeout_commit = "5s"/timeout_commit = "1s"/g' "$HOME_DIR/config/config.toml"
sed -i 's/timeout_propose = "3s"/timeout_propose = "1s"/g' "$HOME_DIR/config/config.toml"
sed -i 's/index_all_keys = false/index_all_keys = true/g' "$HOME_DIR/config/config.toml"

# Listen on all interfaces (needed inside Docker)
sed -i 's#laddr = "tcp://127.0.0.1:26657"#laddr = "tcp://0.0.0.0:26657"#g' "$HOME_DIR/config/config.toml"
sed -i 's#laddr = "tcp://127.0.0.1:26656"#laddr = "tcp://0.0.0.0:26656"#g' "$HOME_DIR/config/config.toml"

# App config: zero gas prices, enable API + gRPC
sed -i 's/minimum-gas-prices = ""/minimum-gas-prices = "0stake"/g' "$HOME_DIR/config/app.toml"
sed -i 's/enable = false/enable = true/g' "$HOME_DIR/config/app.toml"
# gRPC on 0.0.0.0
sed -i 's#address = "localhost:9090"#address = "0.0.0.0:9090"#g' "$HOME_DIR/config/app.toml"
# REST API on 0.0.0.0
sed -i 's#address = "tcp://localhost:1317"#address = "tcp://0.0.0.0:1317"#g' "$HOME_DIR/config/app.toml"

echo "[init_chain] done. validator=$VALIDATOR_ADDR user=$USER_ADDR"
