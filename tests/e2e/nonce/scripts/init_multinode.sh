#!/bin/bash
# Initialize a 4-validator testnet using gaiad testnet init-files.
# Outputs to /nodes/node{0..3}/gaiad/ which are volume-mounted by compose.

set -euo pipefail

CHAIN_ID="nonce-test"
NUM_VALIDATORS=4
OUTPUT_DIR="/nodes"
GAIAD="gaiad"
USER_COINS="100000000000stake"

echo "[init_multinode] Generating $NUM_VALIDATORS-validator testnet..."

# Generate all validator home dirs in one shot
$GAIAD testnet init-files \
  --v "$NUM_VALIDATORS" \
  --chain-id "$CHAIN_ID" \
  --output-dir "$OUTPUT_DIR" \
  --keyring-backend test \
  --minimum-gas-prices "0stake"

# Add a funded "user" account to node0's keyring and genesis for the spammer
NODE0_HOME="$OUTPUT_DIR/node0/gaiad"
$GAIAD keys add user --keyring-backend test --home "$NODE0_HOME"
USER_ADDR=$($GAIAD keys show user -a --keyring-backend test --home "$NODE0_HOME")

# Fund user in genesis (modify genesis on node0, then copy to others)
$GAIAD genesis add-genesis-account "$USER_ADDR" "$USER_COINS" --home "$NODE0_HOME"

# Replace persistent_peers IP addresses with Docker service names.
# gaiad testnet init-files uses 192.168.x.x IPs which don't work in Docker compose.
for i in $(seq 0 $((NUM_VALIDATORS - 1))); do
  NODE_HOME="$OUTPUT_DIR/node$i/gaiad"
  CONFIG="$NODE_HOME/config/config.toml"

  # Get this node's ID from node_key.json
  NODE_ID=$($GAIAD comet show-node-id --home "$NODE_HOME")

  # Build peer list excluding self
  PEERS=""
  for j in $(seq 0 $((NUM_VALIDATORS - 1))); do
    if [ "$j" != "$i" ]; then
      PEER_HOME="$OUTPUT_DIR/node$j/gaiad"
      PEER_ID=$($GAIAD comet show-node-id --home "$PEER_HOME")
      if [ -n "$PEERS" ]; then
        PEERS="$PEERS,"
      fi
      PEERS="${PEERS}${PEER_ID}@node${j}:26656"
    fi
  done

  # Replace persistent_peers
  sed -i "s|persistent_peers = \".*\"|persistent_peers = \"$PEERS\"|g" "$CONFIG"

  # Fast block times
  sed -i 's/timeout_commit = "5s"/timeout_commit = "1s"/g' "$CONFIG"
  sed -i 's/timeout_propose = "3s"/timeout_propose = "1s"/g' "$CONFIG"
  sed -i 's/index_all_keys = false/index_all_keys = true/g' "$CONFIG"

  # Listen on all interfaces (Docker networking)
  sed -i 's#laddr = "tcp://127.0.0.1:26657"#laddr = "tcp://0.0.0.0:26657"#g' "$CONFIG"
  sed -i 's#laddr = "tcp://127.0.0.1:26656"#laddr = "tcp://0.0.0.0:26656"#g' "$CONFIG"

  # App config
  APP_CONFIG="$NODE_HOME/config/app.toml"
  sed -i 's/enable = false/enable = true/g' "$APP_CONFIG"
  sed -i 's#address = "localhost:9090"#address = "0.0.0.0:9090"#g' "$APP_CONFIG"
  sed -i 's#address = "tcp://localhost:1317"#address = "tcp://0.0.0.0:1317"#g' "$APP_CONFIG"

  # Copy updated genesis from node0 to all other nodes
  if [ "$i" != "0" ]; then
    cp "$NODE0_HOME/config/genesis.json" "$NODE_HOME/config/genesis.json"
  fi

  echo "[init_multinode] node$i: id=$NODE_ID peers=$PEERS"
done

# Copy user keyring from node0 to spammer volume (node0-data is shared)
echo "[init_multinode] user=$USER_ADDR"
echo "[init_multinode] Done. $NUM_VALIDATORS validators initialized."
