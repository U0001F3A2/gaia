#!/bin/bash
# Poll until a CometBFT node is reachable, then exec the remaining args.
# Usage: wait_for_node.sh <host:port> [command...]

set -euo pipefail

TARGET="${1:?usage: wait_for_node.sh <host:port> [command...]}"
shift

echo "[wait_for_node] Waiting for $TARGET to become healthy..."

MAX_RETRIES=60
RETRY_INTERVAL=2

for i in $(seq 1 "$MAX_RETRIES"); do
  if curl -sf "http://$TARGET/status" > /dev/null 2>&1; then
    echo "[wait_for_node] Node $TARGET is healthy (attempt $i)"
    # Give it one more second for the first block
    sleep 2
    break
  fi
  if [ "$i" = "$MAX_RETRIES" ]; then
    echo "[wait_for_node] ERROR: $TARGET did not become healthy after $MAX_RETRIES attempts"
    exit 1
  fi
  sleep "$RETRY_INTERVAL"
done

if [ $# -gt 0 ]; then
  echo "[wait_for_node] Executing: $*"
  exec "$@"
fi
