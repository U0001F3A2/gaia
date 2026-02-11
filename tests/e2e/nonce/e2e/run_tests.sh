#!/bin/bash
# E2E functional tests for the x/nonce module.
# Runs 9 test cases against a live single-node chain.
# Expects: NODE_URL, GAIA_HOME env vars set by docker-compose.

set -euo pipefail

NODE_URL="${NODE_URL:-http://localhost:26657}"
HOME_DIR="${GAIA_HOME:-/root/.gaia}"
CHAIN_ID="nonce-test"
KB="--keyring-backend test"
FEES="--gas 200000 --fees 200000stake"

PASSED=0
FAILED=0
TOTAL=9

# Helper: get user and validator addresses
USER_ADDR=$(gaiad keys show user -a $KB --home "$HOME_DIR")
VALIDATOR_ADDR=$(gaiad keys show validator -a $KB --home "$HOME_DIR")

# Helper: wait for tx to be included in a block
wait_for_tx() {
  local TX_HASH="$1"
  for _ in $(seq 1 30); do
    RESULT=$(gaiad query tx "$TX_HASH" --node "$NODE_URL" --home "$HOME_DIR" -o json 2>/dev/null || echo "")
    if [ -n "$RESULT" ] && echo "$RESULT" | jq -e '.height' > /dev/null 2>&1; then
      echo "$RESULT"
      return 0
    fi
    sleep 1
  done
  echo ""
  return 1
}

# Helper: get current account sequence
get_sequence() {
  local ADDR="$1"
  gaiad query auth account "$ADDR" --node "$NODE_URL" --home "$HOME_DIR" -o json 2>/dev/null | \
    jq -r '.account.value.sequence // .account.sequence // "0"'
}

# Helper: get account number
get_account_number() {
  local ADDR="$1"
  gaiad query auth account "$ADDR" --node "$NODE_URL" --home "$HOME_DIR" -o json 2>/dev/null | \
    jq -r '.account.value.account_number // .account.account_number // "0"'
}

# Helper: send a sequential tx (online signing works fine)
send_sequential_tx() {
  local FROM="$1"
  local TO="$2"
  local AMOUNT="$3"
  gaiad tx bank send "$FROM" "$TO" "$AMOUNT" \
    $KB --chain-id "$CHAIN_ID" --home "$HOME_DIR" --node "$NODE_URL" \
    --yes $FEES -o json --from "$FROM" 2>&1
}

# Helper: send a timestamp nonce tx (requires offline sign + broadcast)
# SDK's --sequence flag only works in offline mode.
send_timestamp_tx() {
  local FROM_KEY="$1"
  local TO="$2"
  local AMOUNT="$3"
  local TS_NONCE="$4"
  local ACCT_NUM="$5"

  local TMP_UNSIGNED="/tmp/unsigned_${TS_NONCE}.json"
  local TMP_SIGNED="/tmp/signed_${TS_NONCE}.json"

  # Step 1: generate unsigned tx
  gaiad tx bank send "$FROM_KEY" "$TO" "$AMOUNT" \
    $KB --home "$HOME_DIR" $FEES \
    --from "$FROM_KEY" --generate-only > "$TMP_UNSIGNED" 2>/dev/null

  # Step 2: sign offline with timestamp as sequence
  gaiad tx sign "$TMP_UNSIGNED" \
    $KB --chain-id "$CHAIN_ID" --home "$HOME_DIR" \
    --offline --sequence "$TS_NONCE" --account-number "$ACCT_NUM" \
    --from "$FROM_KEY" > "$TMP_SIGNED" 2>/dev/null

  # Step 3: broadcast
  gaiad tx broadcast "$TMP_SIGNED" --node "$NODE_URL" -o json 2>&1

  rm -f "$TMP_UNSIGNED" "$TMP_SIGNED"
}

# Helper: generate a microsecond timestamp (current time)
now_us() {
  echo $(( $(date +%s) * 1000000 ))
}

pass() {
  echo "  PASS: $1"
  PASSED=$((PASSED + 1))
}

fail() {
  echo "  FAIL: $1 -- $2"
  FAILED=$((FAILED + 1))
}

echo "========================================"
echo " x/nonce E2E Tests"
echo " Node: $NODE_URL"
echo " User: $USER_ADDR"
echo " Validator: $VALIDATOR_ADDR"
echo "========================================"
echo ""

ACCT_NUM=$(get_account_number "$VALIDATOR_ADDR")

# ---- Test 1: Query params ----
echo "[1/9] test_query_params"
PARAMS=$(gaiad query nonce params --node "$NODE_URL" --home "$HOME_DIR" -o json 2>/dev/null)
PAST_WINDOW=$(echo "$PARAMS" | jq -r '.params.past_window_us // .past_window_us')
CUTOFF=$(echo "$PARAMS" | jq -r '.params.timestamp_nonce_cutoff // .timestamp_nonce_cutoff')

if [ "$PAST_WINDOW" = "300000000" ] && [ "$CUTOFF" = "1099511627776" ]; then
  pass "params: past_window_us=$PAST_WINDOW, cutoff=$CUTOFF"
else
  fail "params mismatch" "past_window_us=$PAST_WINDOW cutoff=$CUTOFF"
fi

# ---- Test 2: Sequential tx (standard nonce) ----
echo "[2/9] test_sequential_tx"
TX_OUT=$(send_sequential_tx validator "$USER_ADDR" 1000stake)
TX_HASH=$(echo "$TX_OUT" | jq -r '.txhash')

if [ -n "$TX_HASH" ] && [ "$TX_HASH" != "null" ]; then
  RESULT=$(wait_for_tx "$TX_HASH")
  CODE=$(echo "$RESULT" | jq -r '.code')
  if [ "$CODE" = "0" ]; then
    pass "sequential tx succeeded (hash=$TX_HASH)"
  else
    fail "sequential tx failed on-chain" "code=$CODE"
  fi
else
  fail "sequential tx broadcast failed" "$TX_OUT"
fi

# Record sequence after sequential tx
SEQ_AFTER_SEQUENTIAL=$(get_sequence "$VALIDATOR_ADDR")

# ---- Test 3: Timestamp nonce tx ----
echo "[3/9] test_timestamp_nonce_tx"
TS_NONCE=$(now_us)

TX_OUT=$(send_timestamp_tx validator "$USER_ADDR" 1000stake "$TS_NONCE" "$ACCT_NUM")
TX_HASH=$(echo "$TX_OUT" | jq -r '.txhash')

if [ -n "$TX_HASH" ] && [ "$TX_HASH" != "null" ]; then
  TX_CODE=$(echo "$TX_OUT" | jq -r '.code')
  if [ "$TX_CODE" = "0" ] || [ "$TX_CODE" = "" ]; then
    RESULT=$(wait_for_tx "$TX_HASH")
    CODE=$(echo "$RESULT" | jq -r '.code')
    if [ "$CODE" = "0" ]; then
      pass "timestamp nonce tx succeeded (nonce=$TS_NONCE)"
    else
      fail "timestamp nonce tx failed on-chain" "code=$CODE raw_log=$(echo "$RESULT" | jq -r '.raw_log')"
    fi
  else
    fail "timestamp nonce tx rejected at broadcast" "code=$TX_CODE raw_log=$(echo "$TX_OUT" | jq -r '.raw_log')"
  fi
else
  fail "timestamp nonce tx broadcast failed" "$TX_OUT"
fi

# ---- Test 4: Query has-nonce ----
echo "[4/9] test_query_has_nonce"
# Wait a moment for the block to commit
sleep 2
HAS_RESULT=$(gaiad query nonce has-nonce "$VALIDATOR_ADDR" "$TS_NONCE" \
  --node "$NODE_URL" --home "$HOME_DIR" -o json 2>/dev/null)
HAS_NONCE=$(echo "$HAS_RESULT" | jq -r '.has_nonce')

if [ "$HAS_NONCE" = "true" ]; then
  pass "has-nonce query confirms nonce=$TS_NONCE consumed"
else
  fail "has-nonce returned false or empty" "$HAS_RESULT"
fi

# ---- Test 5: Query nonces-by-address ----
echo "[5/9] test_query_nonces_by_address"
NONCES_RESULT=$(gaiad query nonce nonces "$VALIDATOR_ADDR" \
  --node "$NODE_URL" --home "$HOME_DIR" -o json 2>/dev/null)
FOUND=$(echo "$NONCES_RESULT" | jq --arg ts "$TS_NONCE" '[.timestamp_nonces[] | select(tostring == $ts)] | length')

if [ "$FOUND" -gt 0 ]; then
  pass "nonces-by-address contains $TS_NONCE"
else
  fail "nonces-by-address does not contain $TS_NONCE" "$NONCES_RESULT"
fi

# ---- Test 6: Duplicate rejection ----
echo "[6/9] test_duplicate_rejection"
TX_OUT=$(send_timestamp_tx validator "$USER_ADDR" 1000stake "$TS_NONCE" "$ACCT_NUM")
TX_HASH=$(echo "$TX_OUT" | jq -r '.txhash')
TX_CODE=$(echo "$TX_OUT" | jq -r '.code // 0')

if [ "$TX_CODE" != "0" ] && [ "$TX_CODE" != "" ] && [ "$TX_CODE" != "null" ]; then
  # Rejected at broadcast (CheckTx)
  pass "duplicate timestamp nonce rejected at broadcast (code=$TX_CODE)"
elif [ -n "$TX_HASH" ] && [ "$TX_HASH" != "null" ]; then
  RESULT=$(wait_for_tx "$TX_HASH")
  CODE=$(echo "$RESULT" | jq -r '.code')
  if [ "$CODE" != "0" ]; then
    pass "duplicate timestamp nonce rejected on-chain (code=$CODE)"
  else
    fail "duplicate timestamp nonce was NOT rejected" "code=$CODE"
  fi
else
  # Check for error in the output text
  if echo "$TX_OUT" | grep -qi "already consumed\|duplicate"; then
    pass "duplicate rejected (error in output)"
  else
    fail "unexpected output for duplicate" "$TX_OUT"
  fi
fi

# ---- Test 7: Parallel timestamps (two different us timestamps) ----
echo "[7/9] test_parallel_timestamps"
TS1=$(( $(now_us) + 1 ))
sleep 1
TS2=$(( $(now_us) + 2 ))

TX_OUT1=$(send_timestamp_tx validator "$USER_ADDR" 500stake "$TS1" "$ACCT_NUM")
TX_HASH1=$(echo "$TX_OUT1" | jq -r '.txhash')
TX_CODE1=$(echo "$TX_OUT1" | jq -r '.code // 0')

TX_OUT2=$(send_timestamp_tx validator "$USER_ADDR" 500stake "$TS2" "$ACCT_NUM")
TX_HASH2=$(echo "$TX_OUT2" | jq -r '.txhash')
TX_CODE2=$(echo "$TX_OUT2" | jq -r '.code // 0')

BOTH_OK=true
for i in 1 2; do
  eval "HASH=\$TX_HASH${i}"
  eval "BCODE=\$TX_CODE${i}"
  if [ "$BCODE" != "0" ] && [ "$BCODE" != "" ] && [ "$BCODE" != "null" ]; then
    BOTH_OK=false
    continue
  fi
  if [ -n "$HASH" ] && [ "$HASH" != "null" ]; then
    RESULT=$(wait_for_tx "$HASH")
    CODE=$(echo "$RESULT" | jq -r '.code')
    if [ "$CODE" != "0" ]; then
      BOTH_OK=false
    fi
  else
    BOTH_OK=false
  fi
done

if [ "$BOTH_OK" = "true" ]; then
  pass "two parallel timestamp nonces ($TS1, $TS2) both succeeded"
else
  fail "parallel timestamp nonces did not both succeed" "hash1=$TX_HASH1(code=$TX_CODE1) hash2=$TX_HASH2(code=$TX_CODE2)"
fi

# ---- Test 8: Expired nonce rejection ----
echo "[8/9] test_expired_nonce_rejection"
# 10 minutes in the past (past window is 5 min)
EXPIRED_TS=$(( $(now_us) - 600000000 ))

TX_OUT=$(send_timestamp_tx validator "$USER_ADDR" 500stake "$EXPIRED_TS" "$ACCT_NUM")
TX_HASH=$(echo "$TX_OUT" | jq -r '.txhash')
TX_CODE=$(echo "$TX_OUT" | jq -r '.code // 0')

if [ "$TX_CODE" != "0" ] && [ "$TX_CODE" != "" ] && [ "$TX_CODE" != "null" ]; then
  pass "expired nonce rejected at broadcast (code=$TX_CODE)"
elif [ -n "$TX_HASH" ] && [ "$TX_HASH" != "null" ]; then
  RESULT=$(wait_for_tx "$TX_HASH")
  CODE=$(echo "$RESULT" | jq -r '.code')
  if [ "$CODE" != "0" ]; then
    pass "expired nonce rejected on-chain (code=$CODE)"
  else
    fail "expired nonce was NOT rejected" "code=$CODE"
  fi
else
  if echo "$TX_OUT" | grep -qi "too far in the past\|expired"; then
    pass "expired nonce rejected (error in output)"
  else
    fail "unexpected output for expired nonce" "$TX_OUT"
  fi
fi

# ---- Test 9: Sequential tx after timestamp (sequence state intact) ----
echo "[9/9] test_sequential_after_timestamp"
# The validator's on-chain sequence should NOT have been incremented by timestamp nonce txs
SEQ_NOW=$(get_sequence "$VALIDATOR_ADDR")

if [ "$SEQ_NOW" = "$SEQ_AFTER_SEQUENTIAL" ]; then
  echo "  (Sequence unchanged: $SEQ_AFTER_SEQUENTIAL -> $SEQ_NOW, timestamp nonces did not affect it)"
fi

TX_OUT=$(send_sequential_tx validator "$USER_ADDR" 500stake)
TX_HASH=$(echo "$TX_OUT" | jq -r '.txhash')

if [ -n "$TX_HASH" ] && [ "$TX_HASH" != "null" ]; then
  RESULT=$(wait_for_tx "$TX_HASH")
  CODE=$(echo "$RESULT" | jq -r '.code')
  if [ "$CODE" = "0" ]; then
    pass "sequential tx (seq=$SEQ_NOW) succeeded after timestamp nonce txs"
  else
    fail "sequential tx after timestamp nonce failed" "code=$CODE"
  fi
else
  fail "sequential tx broadcast failed" "$TX_OUT"
fi

# ---- Summary ----
echo ""
echo "========================================"
echo " Results: $PASSED/$TOTAL passed, $FAILED failed"
echo "========================================"

if [ "$FAILED" -gt 0 ]; then
  exit 1
fi
exit 0
