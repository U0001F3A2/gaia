#!/bin/bash
# Convenience wrapper: build Docker image and run E2E tests on a 1-node chain.
# Usage: bash tests/e2e/nonce/run_e2e.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "=== Building Docker image and running E2E tests ==="
echo "This will take a few minutes on first build (gaiad compilation)."
echo ""

cd "$SCRIPT_DIR"

# Build and run
docker compose down -v 2>/dev/null || true
docker compose up --build --abort-on-container-exit --exit-code-from test-runner

# Cleanup
docker compose down -v
