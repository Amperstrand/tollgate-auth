#!/bin/bash
# test-daemon-parity.sh — Feature parity between exec binary and daemon+shim
#
# Tests that tollgate-shim (via daemon) produces identical RADIUS exec output
# to tollgate-auth-radius (direct exec binary) for the same inputs.
#
# Also includes concurrent stress test for the daemon.
#
# Usage: ssh root@nodns.shop 'bash /tmp/test-daemon-parity.sh'
set -euo pipefail

EXEC=/usr/local/bin/tollgate-auth-radius
SHIM=/usr/local/bin/tollgate-shim
SOCKET=/run/tollgate/tollgate.sock
HEALTH_URL=http://localhost:8091/healthz
AUTH_URL=http://localhost:8091/v1/auth
METRICS_URL=http://localhost:8091/metrics

PASS=0
FAIL=0
SKIP=0

green() { printf "\033[32m%s\033[0m\n" "$1"; }
red()   { printf "\033[31m%s\033[0m\n" "$1"; }
yellow(){ printf "\033[33m%s\033[0m\n" "$1"; }

# Run exec binary with secrets sourced. Captures stdout only (stderr → /dev/null).
run_exec() {
    bash -c "set -a; source /etc/tollgate/secrets.env; set +a; $EXEC '$1' '$2' '$3' '$4'" 2>/dev/null || true
}

# Run shim. Captures stdout only.
run_shim() {
    $SHIM "$1" "$2" "$3" "$4" 2>/dev/null || true
}

# Compare two outputs. Ignore Class attribute (HMAC-signed, differs by operator key timing).
compare_output() {
    local name="$1"
    local exec_out="$2"
    local shim_out="$3"
    
    # Strip Class line (contains timestamp-dependent HMAC)
    local exec_clean=$(echo "$exec_out" | grep -v '^Class = ' || true)
    local shim_clean=$(echo "$shim_out" | grep -v '^Class = ' || true)
    
    if [ "$exec_clean" == "$shim_clean" ]; then
        green "  PASS: $name"
        PASS=$((PASS+1))
    else
        red "  FAIL: $name"
        echo "    exec: $(echo "$exec_clean" | tr '\n' '|')"
        echo "    shim: $(echo "$shim_clean" | tr '\n' '|')"
        FAIL=$((FAIL+1))
    fi
}

echo "========================================"
echo "  Daemon Parity Test Suite"
echo "========================================"
echo ""

# --- Section 1: Daemon health ---
echo "--- Section 1: Daemon Health ---"

if systemctl is-active --quiet tollgate-daemon; then
    green "  PASS: daemon is active"
    PASS=$((PASS+1))
else
    red "  FAIL: daemon is NOT active"
    FAIL=$((FAIL+1))
    echo "  Aborting — daemon must be running."
    exit 1
fi

health=$(curl -sf "$HEALTH_URL" 2>/dev/null || echo "")
if [ "$health" == '{"status":"ok"}' ]; then
    green "  PASS: /healthz returns ok"
    PASS=$((PASS+1))
else
    red "  FAIL: /healthz unexpected response: $health"
    FAIL=$((FAIL+1))
fi

if [ -S "$SOCKET" ]; then
    green "  PASS: Unix socket exists at $SOCKET"
    PASS=$((PASS+1))
else
    red "  FAIL: Unix socket missing"
    FAIL=$((FAIL+1))
fi
echo ""

# --- Section 2: Reject case parity ---
echo "--- Section 2: Reject Case Parity (exec vs shim output) ---"

# Both should reject identically (no payment credential)
EXEC_OUT=$(run_exec "user1" "aa:bb:cc:dd:ee:10" "password123" "")
SHIM_OUT=$(run_shim "user1" "aa:bb:cc:dd:ee:10" "password123" "")
compare_output "Invalid password (no token)" "$EXEC_OUT" "$SHIM_OUT"

# Empty username
EXEC_OUT=$(run_exec "" "aa:bb:cc:dd:ee:11" "" "")
SHIM_OUT=$(run_shim "" "aa:bb:cc:dd:ee:11" "" "")
compare_output "Empty username" "$EXEC_OUT" "$SHIM_OUT"

# Invalid Cashu token format
EXEC_OUT=$(run_exec "cashuBgarbage" "aa:bb:cc:dd:ee:12" "" "")
SHIM_OUT=$(run_shim "cashuBgarbage" "aa:bb:cc:dd:ee:12" "" "")
compare_output "Invalid Cashu token format" "$EXEC_OUT" "$SHIM_OUT"

# Invalid MAC format
EXEC_OUT=$(run_exec "user1" "../../etc/passwd" "" "")
SHIM_OUT=$(run_shim "user1" "../../etc/passwd" "" "")
compare_output "Invalid MAC (path traversal)" "$EXEC_OUT" "$SHIM_OUT"

echo ""

# --- Section 3: Accept case parity (LNURLw) ---
echo "--- Section 3: Accept Case Parity (LNURLw) ---"

TS=$(date +%s)
CODE_EXEC="lnurlwdp68gup6jhjumue2nn${TS}e"
CODE_SHIM="lnurlwdp68gup6jhjumue2nn${TS}s"
MAC_EXEC=$(printf "aa:bb:cc:dd:%02x:%02x" $((TS % 256)) $(((TS >> 8) % 256)))
MAC_SHIM=$(printf "aa:bb:cc:dd:%02x:%02x" $(((TS + 1) % 256)) $(((TS >> 8) % 256)))

EXEC_OUT=$(run_exec "$CODE_EXEC" "$MAC_EXEC" "" "")
SHIM_OUT=$(run_shim "$CODE_SHIM" "$MAC_SHIM" "" "")

# For accept cases, we can't compare exact Reply-Message (different codes)
# but we can compare the STRUCTURE (same attributes present, same format)
EXEC_HAS_REPLY=$(echo "$EXEC_OUT" | grep -c '^Reply-Message = ' || true)
SHIM_HAS_REPLY=$(echo "$SHIM_OUT" | grep -c '^Reply-Message = ' || true)
EXEC_HAS_TIMEOUT=$(echo "$EXEC_OUT" | grep -c '^Session-Timeout = ' || true)
SHIM_HAS_TIMEOUT=$(echo "$SHIM_OUT" | grep -c '^Session-Timeout = ' || true)
EXEC_HAS_ACCT=$(echo "$EXEC_OUT" | grep -c '^Acct-Interim-Interval = ' || true)
SHIM_HAS_ACCT=$(echo "$SHIM_OUT" | grep -c '^Acct-Interim-Interval = ' || true)
EXEC_HAS_CLASS=$(echo "$EXEC_OUT" | grep -c '^Class = ' || true)
SHIM_HAS_CLASS=$(echo "$SHIM_OUT" | grep -c '^Class = ' || true)

if [ "$EXEC_HAS_REPLY" == "1" ] && [ "$SHIM_HAS_REPLY" == "1" ] && \
   [ "$EXEC_HAS_TIMEOUT" == "1" ] && [ "$SHIM_HAS_TIMEOUT" == "1" ] && \
   [ "$EXEC_HAS_ACCT" == "1" ] && [ "$SHIM_HAS_ACCT" == "1" ] && \
   [ "$EXEC_HAS_CLASS" == "1" ] && [ "$SHIM_HAS_CLASS" == "1" ]; then
    green "  PASS: LNURLw accept — same RADIUS attributes present"
    PASS=$((PASS+1))
else
    red "  FAIL: LNURLw accept — attribute mismatch"
    echo "    exec: reply=$EXEC_HAS_REPLY timeout=$EXEC_HAS_TIMEOUT acct=$EXEC_HAS_ACCT class=$EXEC_HAS_CLASS"
    echo "    shim: reply=$SHIM_HAS_REPLY timeout=$SHIM_HAS_TIMEOUT acct=$SHIM_HAS_ACCT class=$SHIM_HAS_CLASS"
    FAIL=$((FAIL+1))
fi

# Verify Session-Timeout value matches (both should be 3600)
EXEC_TIMEOUT=$(echo "$EXEC_OUT" | grep '^Session-Timeout = ' | awk '{print $3}' || true)
SHIM_TIMEOUT=$(echo "$SHIM_OUT" | grep '^Session-Timeout = ' | awk '{print $3}' || true)
if [ "$EXEC_TIMEOUT" == "$SHIM_TIMEOUT" ] && [ "$EXEC_TIMEOUT" == "3600" ]; then
    green "  PASS: Session-Timeout matches ($EXEC_TIMEOUT)"
    PASS=$((PASS+1))
else
    red "  FAIL: Session-Timeout mismatch (exec=$EXEC_TIMEOUT shim=$SHIM_TIMEOUT)"
    FAIL=$((FAIL+1))
fi

# Replay protection: same code through shim should be rejected
MAC_REPLAY=$(printf "aa:bb:cc:dd:%02x:%02x" $(((TS + 2) % 256)) $(((TS >> 8) % 256)))
SHIM_REPLAY=$($SHIM "$CODE_SHIM" "$MAC_REPLAY" "" "" 2>/dev/null || true)
if echo "$SHIM_REPLAY" | grep -q "already used"; then
    green "  PASS: Replay protection works through shim"
    PASS=$((PASS+1))
else
    red "  FAIL: Replay protection broken through shim: $SHIM_REPLAY"
    FAIL=$((FAIL+1))
fi

echo ""

# --- Section 4: Concurrent stress test ---
echo "--- Section 4: Concurrent Stress Test (20 parallel requests) ---"

# Send 20 concurrent requests via HTTP API
BEFORE_TOTAL=$(curl -sf "$METRICS_URL" | grep '^tollgate_auth_total ' | awk '{print $2}')
PIDS=""
for i in $(seq 1 20); do
    curl -sf -X POST "$AUTH_URL" \
        -H "Content-Type: application/json" \
        -d "{\"username\":\"baduser$i\",\"mac\":\"aa:bb:cc:dd:ee:5$i\",\"password\":\"badpass\"}" \
        -o /dev/null 2>/dev/null &
    PIDS="$PIDS $!"
done

# Wait for all
WAIT_OK=true
for pid in $PIDS; do
    if ! wait $pid 2>/dev/null; then
        WAIT_OK=false
    fi
done

if $WAIT_OK; then
    green "  PASS: 20 concurrent requests completed"
    PASS=$((PASS+1))
else
    yellow "  SKIP: some concurrent requests had non-zero exit (may be expected for rejects)"
    SKIP=$((SKIP+1))
fi

# Verify metrics incremented
AFTER_TOTAL=$(curl -sf "$METRICS_URL" | grep '^tollgate_auth_total ' | awk '{print $2}')
DIFF=$((AFTER_TOTAL - BEFORE_TOTAL))
if [ "$DIFF" -ge 20 ]; then
    green "  PASS: metrics incremented by $DIFF (expected >= 20)"
    PASS=$((PASS+1))
else
    red "  FAIL: metrics only incremented by $DIFF (expected >= 20)"
    FAIL=$((FAIL+1))
fi

# Verify daemon is still alive
if systemctl is-active --quiet tollgate-daemon; then
    green "  PASS: daemon survived stress test"
    PASS=$((PASS+1))
else
    red "  FAIL: daemon crashed during stress test"
    FAIL=$((FAIL+1))
fi

# Check for errors in metrics
ERRORS=$(curl -sf "$METRICS_URL" | grep '^tollgate_auth_errors ' | awk '{print $2}')
if [ "$ERRORS" == "0" ]; then
    green "  PASS: zero auth errors during stress test"
    PASS=$((PASS+1))
else
    red "  FAIL: $ERRORS auth errors during stress test"
    FAIL=$((FAIL+1))
fi

echo ""

# --- Section 5: Daemon failure resilience ---
echo "--- Section 5: Shim behavior when daemon is unreachable ---"

# Temporarily stop daemon
systemctl stop tollgate-daemon
sleep 1

SHIM_DOWN=$($SHIM "lnurlwdp68gup6jhjumue2nn99" "aa:bb:cc:dd:ee:ff" "" "" 2>/dev/null || true)
SHIM_RC=$?
if echo "$SHIM_DOWN" | grep -q "daemon unavailable"; then
    green "  PASS: shim shows graceful error when daemon down"
    PASS=$((PASS+1))
else
    red "  FAIL: shim response when daemon down: $SHIM_DOWN (rc=$SHIM_RC)"
    FAIL=$((FAIL+1))
fi

# Restart daemon
systemctl start tollgate-daemon
sleep 2

if systemctl is-active --quiet tollgate-daemon; then
    green "  PASS: daemon restarted successfully"
    PASS=$((PASS+1))
else
    red "  FAIL: daemon failed to restart"
    FAIL=$((FAIL+1))
fi

echo ""

# --- Summary ---
echo "========================================"
echo "  RESULTS: $PASS passed, $FAIL failed, $SKIP skipped"
echo "========================================"

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
