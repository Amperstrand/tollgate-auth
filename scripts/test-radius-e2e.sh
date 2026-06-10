#!/bin/bash
# test-radius-e2e.sh — E2E test suite for tollgate-auth-radius
# Run on the RADIUS server (localhost) or any machine with radclient access.
# Uses radclient with fake MAC addresses for proper replay/reconnection testing.
set -euo pipefail

RADIUS_HOST="${1:-localhost}"
RADIUS_SECRET="${2:-tollgate}"

PASS=0
FAIL=0

# Helper: send radclient request with a fake MAC
radtest_mac() {
    local user="$1" pass="$2" mac="$3"
    echo "User-Name = \"$user\"
User-Password = \"$pass\"
NAS-IP-Address = 10.0.0.1
Calling-Station-Id = \"$mac\"
NAS-Port = 0" | radclient -x "$RADIUS_HOST" auth "$RADIUS_SECRET" 2>&1
}

# Clean state (only on localhost)
if [ "$RADIUS_HOST" = "localhost" ]; then
    rm -f /opt/tollgate-auth/radius-sessions/*.json 2>/dev/null || true
    rm -f /opt/cashu-tollgate/radius-sessions/*.json 2>/dev/null || true
    echo "" > /opt/tollgate-auth/radius-spent.txt 2>/dev/null || true
    echo "" > /opt/cashu-tollgate/radius-spent.txt 2>/dev/null || true
fi

echo "=========================================="
echo "  RADIUS E2E TEST SUITE"
echo "  Server: $RADIUS_HOST  Secret: $RADIUS_SECRET"
echo "=========================================="
echo ""

# Test 1: Fresh lnurlw → Accept
RESULT=$(radtest_mac "lnurlw1e2efresh11111kx2ar0veekzar0wd5xjtnrdakj7" "anything" "aa-e2-11-22-33-44")
if echo "$RESULT" | grep -q "Access-Accept"; then
    echo "✅ TEST 1: Fresh lnurlw → Accept"
    echo "   Reply: $(echo "$RESULT" | grep 'Reply-Message' | head -1)"
    PASS=$((PASS+1))
else
    echo "❌ TEST 1: Fresh lnurlw → $(echo "$RESULT" | grep -o "Access-[A-Za-z]*")"
    FAIL=$((FAIL+1))
fi

# Test 2: Replay same code with different MAC → Reject
RESULT=$(radtest_mac "lnurlw1e2efresh11111kx2ar0veekzar0wd5xjtnrdakj7" "anything" "bb-e2-22-33-44-55")
if echo "$RESULT" | grep -q "Access-Reject"; then
    echo "✅ TEST 2: Replay (different MAC) → Reject"
    echo "   Reply: $(echo "$RESULT" | grep 'Reply-Message' | head -1)"
    PASS=$((PASS+1))
else
    echo "❌ TEST 2: Replay (different MAC) → $(echo "$RESULT" | grep -o "Access-[A-Za-z]*")"
    FAIL=$((FAIL+1))
fi

# Test 3: Same MAC, different code → Accept (session reconnection)
RESULT=$(radtest_mac "lnurlw1e2ediffcode99kx2ar0veekzar0wd5xjtnrdakj7" "anything" "aa-e2-11-22-33-44")
if echo "$RESULT" | grep -q "Access-Accept"; then
    echo "✅ TEST 3: Same MAC, different code → Accept (reconnection)"
    echo "   Reply: $(echo "$RESULT" | grep 'Reply-Message' | head -1)"
    PASS=$((PASS+1))
else
    echo "❌ TEST 3: Same MAC reconnection → $(echo "$RESULT" | grep -o "Access-[A-Za-z]*")"
    FAIL=$((FAIL+1))
fi

# Test 4: lnurlw in password field → Accept
RESULT=$(radtest_mac "wifi-user" "lnurlw1e2epassfield7kx2ar0veekzar0wd5xjtnrdakj7" "cc-e2-33-44-55-66")
if echo "$RESULT" | grep -q "Access-Accept"; then
    echo "✅ TEST 4: lnurlw in password → Accept"
    PASS=$((PASS+1))
else
    echo "❌ TEST 4: lnurlw in password → $(echo "$RESULT" | grep -o "Access-[A-Za-z]*")"
    FAIL=$((FAIL+1))
fi

# Test 5: Uppercase LNURLW → Accept
RESULT=$(radtest_mac "LNURLW1E2EUPPERCASEKX2AR0VEEKZAR0WD5XJTNRDAKJ7" "anything" "dd-e2-44-55-66-77")
if echo "$RESULT" | grep -q "Access-Accept"; then
    echo "✅ TEST 5: Uppercase LNURLW → Accept"
    PASS=$((PASS+1))
else
    echo "❌ TEST 5: Uppercase LNURLW → $(echo "$RESULT" | grep -o "Access-[A-Za-z]*")"
    FAIL=$((FAIL+1))
fi

# Test 6: Invalid credentials → Reject
RESULT=$(radtest_mac "baduser" "badpassword" "ee-e2-55-66-77-88")
if echo "$RESULT" | grep -q "Access-Reject"; then
    echo "✅ TEST 6: Invalid credentials → Reject"
    PASS=$((PASS+1))
else
    echo "❌ TEST 6: Invalid credentials → $(echo "$RESULT" | grep -o "Access-[A-Za-z]*")"
    FAIL=$((FAIL+1))
fi

# Binary-level tests (localhost only)
if [ "$RADIUS_HOST" = "localhost" ]; then
    BINARY="/usr/local/bin/tollgate-auth-radius"

    if [ -x "$BINARY" ]; then
        # Test 7: replay protection at binary level (different MACs)
        $BINARY "lnurlw1e2replaytest5kx2ar0veekzar0wd5xjtnrdakj7" "ff-e2-66-77-88-99" >/dev/null 2>&1
        OUT=$($BINARY "lnurlw1e2replaytest5kx2ar0veekzar0wd5xjtnrdakj7" "ff-e2-66-77-88-99b" 2>&1)
        if echo "$OUT" | grep -q "already used"; then
            echo "✅ TEST 7: Binary replay protection → Reject"
            PASS=$((PASS+1))
        else
            echo "❌ TEST 7: Binary replay protection → did not trigger"
            FAIL=$((FAIL+1))
        fi

        # Test 8: session reconnection at binary level (same MAC)
        $BINARY "lnurlw1e2sessiontest4kx2ar0veekzar0wd5xjtnrdakj7" "ff-e2-77-88-99-aa" >/dev/null 2>&1
        OUT=$($BINARY "lnurlw1e2totallydiff3kx2ar0veekzar0wd5xjtnrdakj7" "ff-e2-77-88-99-aa" 2>&1)
        if echo "$OUT" | grep -q "session active\|Session resumed"; then
            echo "✅ TEST 8: Binary session reconnection → Accept"
            PASS=$((PASS+1))
        else
            echo "❌ TEST 8: Binary session reconnection → did not trigger"
            FAIL=$((FAIL+1))
        fi

        # Test 9: reject short/invalid strings
        $BINARY "short" "ff-e2-88-99-aa-bb" >/dev/null 2>&1
        EXIT=$?
        if [ $EXIT -ne 0 ]; then
            echo "✅ TEST 9: Short string → Reject (exit $EXIT)"
            PASS=$((PASS+1))
        else
            echo "❌ TEST 9: Short string → Accepted (should reject)"
            FAIL=$((FAIL+1))
        fi
    else
        echo "⚠️  Binary tests skipped (tollgate-auth-radius not found)"
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
