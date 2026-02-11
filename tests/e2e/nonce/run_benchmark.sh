#!/bin/bash
# Convenience wrapper: build Docker images and run benchmarks on a single-node chain.
# Usage: bash tests/e2e/nonce/run_benchmark.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "=== Building Docker images and running benchmarks ==="
echo "This will take several minutes on first build."
echo ""

cd "$SCRIPT_DIR"

# Build and run benchmark
docker compose -f docker-compose.benchmark.yml down -v 2>/dev/null || true
docker compose -f docker-compose.benchmark.yml up --build --abort-on-container-exit --exit-code-from spammer

# Cleanup
docker compose -f docker-compose.benchmark.yml down -v
