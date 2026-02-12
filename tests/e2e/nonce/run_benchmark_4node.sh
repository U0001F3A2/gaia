#!/bin/bash
# Convenience wrapper: build Docker images and run 4-node benchmark.
# Usage: bash tests/e2e/nonce/run_benchmark_4node.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "=== Building Docker images and running 4-node benchmark ==="
echo "This will take several minutes on first build."
echo ""

cd "$SCRIPT_DIR"

# Build and run benchmark
docker compose -f docker-compose.benchmark-4node.yml down -v 2>/dev/null || true
docker compose -f docker-compose.benchmark-4node.yml up --build --abort-on-container-exit --exit-code-from spammer

# Cleanup
docker compose -f docker-compose.benchmark-4node.yml down -v
