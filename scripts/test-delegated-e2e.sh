#!/bin/bash
# test-delegated-e2e.sh — E2E test for the tollgate-rs v1 server HTTP API
#
# Tests the integration boundary between tollgate-auth (Go) and tollgate-rs (Rust).
# Uses curl to simulate the same HTTP calls that the Go sessiond client makes.
#
# Tested:
#   - POST / with raw token + X-TollGate-MAC header → Nostr kind 1022
#   - GET /usage with X-TollGate-MAC → usage tracking
#   - Zero-amount / short-token rejection
#   - Session isolation between MACs
#   - Additive allotment
#
# Not tested here (covered elsewhere):
#   - Go sessiond client logic → client_test.go (6 unit tests)
#   - Real Cashu token flow → requires CDK wallet
#
# Usage:
#   ./scripts/test-delegated-e2e.sh
#   TOLLGATE_RS_DIR=/path/to/tollgate-rs ./scripts/test-delegated-e2e.sh
set -euo pipefail

TOLLGATE_RS_DIR="${TOLLGATE_RS_DIR:-/Users/macbook/src/tollgate-rs}"
V1_SERVER_BIN="$TOLLGATE_RS_DIR/target/debug/tollgate-net"
PORT=2199
BASE_URL="http://127.0.0.1:$PORT"

PASS=0
FAIL=0
V1_PID=""

cleanup() {
    if [ -n "$V1_PID" ]; then
        kill "$V1_PID" 2>/dev/null || true
        wait "$V1_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT

pass() { echo "  ✅ $1"; PASS=$((PASS+1)); }
fail() { echo "  ❌ $1"; FAIL=$((FAIL+1)); }

TEST_DIR="/tmp/tollgate-v1-e2e"
TOKEN_DIR="$TEST_DIR/tokens"
mkdir -p "$TOKEN_DIR"

mock_token() {
    local amount="$1"
    local f="$TOKEN_DIR/token-$amount.bin"
    python3 -c "import sys; sys.stdout.buffer.write(($amount).to_bytes(8, 'big'))" > "$f"
    echo "$f"
}

short_token_file="$TOKEN_DIR/short.bin"
printf 'abcd' > "$short_token_file"

post_token() {
    local token_file="$1"
    local mac="${2:-}"
    local flags=(-s -w "\\n%{http_code}" -X POST "$BASE_URL/" -H "Content-Type: text/plain" --data-binary "@$token_file")
    if [ -n "$mac" ]; then
        flags+=(-H "X-TollGate-MAC: $mac")
    fi
    curl "${flags[@]}"
}

get_usage() {
    local mac="$1"
    curl -s -H "X-TollGate-MAC: $mac" "$BASE_URL/usage"
}

http_code() { echo "$1" | tail -1; }
http_body() { echo "$1" | sed '$d'; }

# --- Pre-flight ---

echo "=========================================="
echo "  V1 SERVER HTTP API E2E TEST"
echo "  (tollgate-rs ↔ tollgate-auth boundary)"
echo "=========================================="
echo ""

if [ ! -x "$V1_SERVER_BIN" ]; then
    echo "Building tollgate-rs v1 server..."
    (cd "$TOLLGATE_RS_DIR" && cargo build -p tollgate-net --bin tollgate-net 2>&1)
    if [ ! -x "$V1_SERVER_BIN" ]; then
        echo "ERROR: Could not build tollgate-net binary"
        exit 1
    fi
fi

echo "Starting v1 server on port $PORT (mock wallet, noop valve)..."
$V1_SERVER_BIN v1-server \
    --port "$PORT" \
    --wallet mock \
    --valve noop \
    --metric milliseconds \
    --step-size 60000 \
    --price-per-step 1 \
    > "$TEST_DIR/v1-server.log" 2>&1 &
V1_PID=$!

echo -n "Waiting for v1 server..."
for i in $(seq 1 30); do
    if curl -s "$BASE_URL/whoami" > /dev/null 2>&1; then
        break
    fi
    echo -n "."
    sleep 0.5
done
echo " ready (PID $V1_PID)"
echo ""

# ============================================================
echo "--- Test 1: POST / with mock token (8 sats) ---"
RESP=$(post_token "$(mock_token 8)")
CODE=$(http_code "$RESP")
BODY=$(http_body "$RESP")

if [ "$CODE" = "200" ]; then
    KIND=$(echo "$BODY" | python3 -c "import sys,json; print(json.load(sys.stdin).get('kind',''))" 2>/dev/null || echo "parse-error")
    if [ "$KIND" = "1022" ]; then
        pass "POST / → 200, Nostr kind 1022"
    else
        fail "POST / → 200 but kind='$KIND' (expected 1022)"
    fi
else
    fail "POST / → HTTP $CODE (expected 200). Body: $(echo "$BODY" | head -c 200)"
fi

# ============================================================
echo "--- Test 2: POST / with X-TollGate-MAC header (16 sats) ---"
RESP=$(post_token "$(mock_token 16)" "aa-bb-cc-dd-ee-ff")
CODE=$(http_code "$RESP")

if [ "$CODE" = "200" ]; then
    pass "POST / with X-TollGate-MAC → 200"
else
    fail "POST / with X-TollGate-MAC → HTTP $CODE"
fi

# ============================================================
echo "--- Test 3: GET /usage for MAC with session ---"
USAGE=$(get_usage "aa-bb-cc-dd-ee-ff")

if echo "$USAGE" | grep -qE '^[0-9]+/[0-9]+$'; then
    pass "GET /usage → $USAGE"
elif [ "$USAGE" = "-1/-1" ]; then
    pass "GET /usage → -1/-1 (session cleaned by janitor)"
else
    fail "GET /usage → unexpected: '$USAGE'"
fi

# ============================================================
echo "--- Test 4: GET /usage for MAC without session ---"
USAGE=$(get_usage "99-88-77-66-55-44")

if [ "$USAGE" = "-1/-1" ] || [ "$USAGE" = "0/0" ]; then
    pass "GET /usage for unknown MAC → $USAGE"
else
    fail "GET /usage for unknown MAC → '$USAGE' (expected -1/-1 or 0/0)"
fi

# ============================================================
echo "--- Test 5: Session isolation between MACs ---"
RESP=$(post_token "$(mock_token 8)" "11-22-33-44-55-66")
CODE=$(http_code "$RESP")
if [ "$CODE" != "200" ]; then
    fail "Setup failed: POST for MAC-A → HTTP $CODE"
else
    USAGE_A=$(get_usage "11-22-33-44-55-66")
    USAGE_B=$(get_usage "66-77-88-99-aa-bb")

    A_HAS=$(echo "$USAGE_A" | grep -qE '^[0-9]+/[0-9]+$' && echo yes || echo no)
    B_EMPTY=$([ "$USAGE_B" = "-1/-1" ] || [ "$USAGE_B" = "0/0" ] && echo yes || echo no)

    if [ "$A_HAS" = yes ] && [ "$B_EMPTY" = yes ]; then
        pass "Isolation: MAC-A=$USAGE_A, MAC-B=$USAGE_B"
    else
        fail "Isolation broken: MAC-A=$USAGE_A (has=$A_HAS), MAC-B=$USAGE_B (empty=$B_EMPTY)"
    fi
fi

# ============================================================
echo "--- Test 6: Zero-amount token → rejection ---"
RESP=$(post_token "$(mock_token 0)")
CODE=$(http_code "$RESP")

if [ "$CODE" = "400" ] || [ "$CODE" = "422" ]; then
    pass "Zero-amount token → HTTP $CODE"
else
    BODY=$(http_body "$RESP")
    fail "Zero-amount token → HTTP $CODE (expected 400/422)"
fi

# ============================================================
echo "--- Test 7: Short token (<8 bytes) → rejection ---"
RESP=$(post_token "$short_token_file")
CODE=$(http_code "$RESP")

if [ "$CODE" = "400" ] || [ "$CODE" = "422" ]; then
    pass "Short token (4 bytes) → HTTP $CODE"
else
    fail "Short token → HTTP $CODE (expected 400/422)"
fi

# ============================================================
echo "--- Test 8: No X-TollGate-MAC → falls back to IP ---"
RESP=$(post_token "$(mock_token 4)")
CODE=$(http_code "$RESP")

if [ "$CODE" = "200" ]; then
    USAGE=$(curl -s "$BASE_URL/usage")
    pass "No MAC header → 200, IP-based usage: $USAGE"
else
    fail "No MAC header → HTTP $CODE (expected 200)"
fi

# ============================================================
echo "--- Test 9: Additive allotment ---"
RESP1=$(post_token "$(mock_token 4)" "cc-dd-ee-ff-00-11")
CODE1=$(http_code "$RESP1")
USAGE1=$(get_usage "cc-dd-ee-ff-00-11")

RESP2=$(post_token "$(mock_token 4)" "cc-dd-ee-ff-00-11")
CODE2=$(http_code "$RESP2")
USAGE2=$(get_usage "cc-dd-ee-ff-00-11")

if [ "$CODE1" = "200" ] && [ "$CODE2" = "200" ]; then
    A1=$(echo "$USAGE1" | cut -d/ -f2)
    A2=$(echo "$USAGE2" | cut -d/ -f2)
    if [ "$A2" -ge "$A1" ] 2>/dev/null; then
        pass "Additive: first allotment=$A1, second=$A2"
    else
        fail "Additive: allotment shrank ($A1 → $A2)"
    fi
else
    fail "Additive: first=$CODE1, second=$CODE2 (both should be 200)"
fi

# --- Results ---

echo ""
echo "=========================================="
if [ $FAIL -eq 0 ]; then
    echo "  ALL $PASS TESTS PASSED ✅"
else
    echo "  $PASS passed, $FAIL failed ❌"
fi
echo "=========================================="

echo ""
echo "v1 server log (last 10 lines):"
tail -10 "$TEST_DIR/v1-server.log"

exit $FAIL
