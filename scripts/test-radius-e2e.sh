#!/bin/bash
# test-radius-e2e.sh — E2E test suite for tollgate-auth-radius
# Run on the RADIUS server (nodns.shop) or any machine with radtest access.
set -euo pipefail

RADIUS_HOST="${1:-localhost}"
RADIUS_SECRET="${2:-tollgate}"
RADIUS_PORT="${3:-0}"

PASS=0
FAIL=0

# Clean state (only on localhost)
if [ "$RADIUS_HOST" = "localhost" ]; then
    rm -f /opt/cashu-tollgate/radius-sessions/*.json 2>/dev/null
    echo "" > /opt/cashu-tollgate/radius-spent.txt 2>/dev/null
fi

echo "=========================================="
echo "  RADIUS E2E TEST SUITE"
echo "  Server: $RADIUS_HOST  Secret: $RADIUS_SECRET"
echo "=========================================="
echo ""

# Test 1: lnurlw in username
RESULT=$(radtest "lnurlw1dp68gurn8ghj7ampd3kx2ar0veekzar0wd5xjtnrdakj7" "anything" "$RADIUS_HOST" "$RADIUS_PORT" "$RADIUS_SECRET" 2>&1)
if echo "$RESULT" | grep -q "Access-Accept"; then
    echo "✅ TEST 1: lnurlw in username → Accept"
    PASS=$((PASS+1))
else
    echo "❌ TEST 1: lnurlw in username → $(echo "$RESULT" | grep -o "Access-[A-Za-z]*")"
    FAIL=$((FAIL+1))
fi

# Test 2: lnurlw in password
RESULT=$(radtest "wifiuser" "lnurlw1aa68gurn8ghj7ampf3kx2ar0veekzar0wd5xjtnrdakj7" "$RADIUS_HOST" "$RADIUS_PORT" "$RADIUS_SECRET" 2>&1)
if echo "$RESULT" | grep -q "Access-Accept"; then
    echo "✅ TEST 2: lnurlw in password → Accept"
    PASS=$((PASS+1))
else
    echo "❌ TEST 2: lnurlw in password → $(echo "$RESULT" | grep -o "Access-[A-Za-z]*")"
    FAIL=$((FAIL+1))
fi

# Test 3: uppercase LNURLW
RESULT=$(radtest "LNURLW1DP68GURN8GHJ7AMPD3KX2AR0VEEKZAR0WD5XJTNRDAKJ7" "anything" "$RADIUS_HOST" "$RADIUS_PORT" "$RADIUS_SECRET" 2>&1)
if echo "$RESULT" | grep -q "Access-Accept"; then
    echo "✅ TEST 3: uppercase LNURLW → Accept"
    PASS=$((PASS+1))
else
    echo "❌ TEST 3: uppercase LNURLW → $(echo "$RESULT" | grep -o "Access-[A-Za-z]*")"
    FAIL=$((FAIL+1))
fi

# Test 4: invalid credentials
RESULT=$(radtest "baduser" "badpassword" "$RADIUS_HOST" "$RADIUS_PORT" "$RADIUS_SECRET" 2>&1)
if echo "$RESULT" | grep -q "Access-Reject"; then
    echo "✅ TEST 4: invalid credentials → Reject"
    PASS=$((PASS+1))
else
    echo "❌ TEST 4: invalid credentials → $(echo "$RESULT" | grep -o "Access-[A-Za-z]*")"
    FAIL=$((FAIL+1))
fi

# Binary-level tests (localhost only)
if [ "$RADIUS_HOST" = "localhost" ]; then
    BINARY="/usr/local/bin/tollgate-auth-radius"

    # Test 5: replay protection (different MACs)
    $BINARY "lnurlw1replaytest12345kx2ar0veekzar0wd5xjtnrdakj7" "aa:aa:aa:aa:aa:aa" >/dev/null 2>&1
    OUT=$($BINARY "lnurlw1replaytest12345kx2ar0veekzar0wd5xjtnrdakj7" "bb:bb:bb:bb:bb:bb" 2>&1)
    if echo "$OUT" | grep -q "already used"; then
        echo "✅ TEST 5: replay protection (different MAC) → Reject"
        PASS=$((PASS+1))
    else
        echo "❌ TEST 5: replay protection → did not trigger"
        FAIL=$((FAIL+1))
    fi

    # Test 6: session reconnection (same MAC, active session)
    $BINARY "lnurlw1sessiontest1234kx2ar0veekzar0wd5xjtnrdakj7" "cc:cc:cc:cc:cc:cc" >/dev/null 2>&1
    OUT=$($BINARY "lnurlw1totallydifferent1234567890abcdefghijklmnop" "cc:cc:cc:cc:cc:cc" 2>&1)
    if echo "$OUT" | grep -q "Reconnection.*session active"; then
        echo "✅ TEST 6: session reconnection (same MAC) → Accept"
        PASS=$((PASS+1))
    else
        echo "❌ TEST 6: session reconnection → did not trigger"
        FAIL=$((FAIL+1))
    fi

    # Test 7: reject short/invalid strings
    $BINARY "short" "dd:dd:dd:dd:dd:dd" >/dev/null 2>&1
    EXIT=$?
    if [ $EXIT -ne 0 ]; then
        echo "✅ TEST 7: short string → Reject (exit $EXIT)"
        PASS=$((PASS+1))
    else
        echo "❌ TEST 7: short string → Accepted (should reject)"
        FAIL=$((FAIL+1))
    fi
fi

echo ""
echo "=========================================="
if [ $FAIL -eq 0 ]; then
    echo "  ALL $PASS TESTS PASSED ✅"
else
    echo "  $PASS passed, $FAIL failed ❌"
fi
echo "=========================================="

exit $FAIL
