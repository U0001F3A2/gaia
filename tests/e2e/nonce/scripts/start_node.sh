#!/bin/bash
# Initialize chain (if not already done) and start gaiad.
# Usage: start_node.sh [node_index]

set -euo pipefail

NODE_INDEX="${1:-}"
HOME_DIR="${GAIA_HOME:-/root/.gaia}"

# Initialize if genesis doesn't exist yet
if [ ! -f "$HOME_DIR/config/genesis.json" ]; then
  echo "[start_node] Initializing chain..."
  /scripts/init_chain.sh "$NODE_INDEX"
fi

echo "[start_node] Starting gaiad..."
exec gaiad start --home "$HOME_DIR"
