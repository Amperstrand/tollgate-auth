#!/bin/bash
# demo.sh — Interactive walkthrough of the tollgate-auth payment flow.
#
# Mints a real Cashu test token, strips DLEQ, sends via RADIUS, shows result.
# Requires: cdk-cli, radtest, python3+cbor2, docker stack running.
set -euo pipefail

BOLD="\033[1m"
GREEN="\033[32m"
RED="\033[31m"
YELLOW="\033[33m"
CYAN="\033[36m"
RESET="\033[0m"

echo -e "${CYAN}"
echo "  ╔══════════════════════════════════════════════╗"
echo "  ║     TOLLGATE-AUTH LIVE DEMO                  ║"
echo "  ║     Cashu ecash → WiFi/SSH/EV access         ║"
echo "  ╚══════════════════════════════════════════════╝"
echo -e "${RESET}"

WALLET_DIR="${TOLLGATE_WALLET_DIR:-/var/lib/cashu-wallet}"
MINT_URL="${TOLLGATE_MINT_URL:-https://testnut.cashu.exchange}"

echo -e "${BOLD}Step 1: Check wallet balance${RESET}"
echo "  Mint: $MINT_URL"
if ! command -v cdk-cli >/dev/null 2>&1; then
    echo -e "  ${RED}cdk-cli not found. Install: https://github.com/cashubtc/cdk/releases${RESET}"
    exit 1
fi
BALANCE_OUTPUT=$(cdk-cli --work-dir "$WALLET_DIR" balance 2>&1 | tail -3)
echo "$BALANCE_OUTPUT" | sed 's/^/  /'
echo ""

echo -e "${BOLD}Step 2: Mint a 2-sat Cashu V4 token${RESET}"
TOKEN_FULL=$(cdk-cli --work-dir "$WALLET_DIR" send --amount 2 --mint-url "$MINT_URL" --unit sat 2>&1 | grep -E "^cashu" | tail -1)
if [ -z "$TOKEN_FULL" ]; then
    echo -e "  ${RED}Failed to mint token. Check wallet balance.${RESET}"
    exit 1
fi
echo "  Token length: ${#TOKEN_FULL} bytes (V4 with DLEQ)"
echo "  Token: ${TOKEN_FULL:0:60}..."
echo ""

echo -e "${BOLD}Step 3: Strip DLEQ proof to fit RADIUS 253-byte limit${RESET}"
python3 -c "
import cbor2, base64, sys
token = sys.argv[1]
raw = token[len('cashuB'):]
pad = 4 - len(raw) % 4
if pad != 4: raw += '=' * pad
data = cbor2.loads(base64.urlsafe_b64decode(raw))
for ts in data.get('t', []):
    for p in ts.get('p', []):
        p.pop('d', None)
stripped = base64.urlsafe_b64encode(cbor2.dumps(data)).rstrip(b'=').decode()
print('cashuB' + stripped)
" "$TOKEN_FULL" > /tmp/tollgate-token-stripped.txt 2>/dev/null || {
    echo -e "  ${YELLOW}Could not strip DLEQ (cbor2 not available). Using full token.${RESET}"
    echo "$TOKEN_FULL" > /tmp/tollgate-token-stripped.txt
}
TOKEN=$(cat /tmp/tollgate-token-stripped.txt)
rm -f /tmp/tollgate-token-stripped.txt
echo "  Stripped length: ${#TOKEN} bytes"
echo "  Fits in RADIUS: $([ ${#TOKEN} -le 253 ] && echo 'YES' || echo 'NO — will be truncated')"
echo ""

echo -e "${BOLD}Step 4: Send RADIUS Access-Request${RESET}"
echo "  FreeRADIUS → cashu-payment policy → shim → daemon → mint verify"
echo ""
if ! command -v radtest >/dev/null 2>&1; then
    echo -e "  ${RED}radtest not found. Install: apt install freeradius-utils${RESET}"
    echo "  Token for manual testing: $TOKEN"
    exit 1
fi
RESULT=$(radtest "$TOKEN" "anything" 127.0.0.1 0 tollgate 2>&1)
echo "$RESULT" | grep -E "Sent|Received|Reply-Message|Session-Timeout|User-Name" | sed 's/^/  /'
echo ""

if echo "$RESULT" | grep -q "Access-Accept"; then
    echo -e "${GREEN}${BOLD}  ✓ ACCESS GRANTED${RESET}"
    echo -e "${GREEN}  The Cashu token was accepted as payment for network access.${RESET}"
    TIMEOUT=$(echo "$RESULT" | grep "Session-Timeout" | awk '{print $3}')
    echo -e "${GREEN}  Session duration: ${TIMEOUT:-?} seconds${RESET}"
    echo ""
    echo -e "${BOLD}What happened:${RESET}"
    echo "  1. RADIUS client sent User-Name = Cashu token"
    echo "  2. FreeRADIUS matched cashu-payment policy"
    echo "  3. Shim forwarded to daemon over TCP"
    echo "  4. Daemon decoded V4 CBOR token"
    echo "  5. Daemon verified token with mint (checkstate)"
    echo "  6. Daemon attempted NUT-03 swap (redeem to operator wallet)"
    echo "  7. FreeRADIUS returned Access-Accept + Session-Timeout"
elif echo "$RESULT" | grep -q "Access-Reject"; then
    echo -e "${YELLOW}${BOLD}  ✗ ACCESS REJECTED${RESET}"
    echo -e "${YELLOW}  The token was rejected (expected for same-wallet tokens).${RESET}"
    REPLY=$(echo "$RESULT" | grep "Reply-Message" | head -1)
    echo -e "${YELLOW}  Reason: ${REPLY:-unknown}${RESET}"
    echo ""
    echo -e "${BOLD}Note:${RESET} Tokens from the same wallet show 'already spent'"
    echo "  because the wallet marked them when sending. A token from an"
    echo "  external wallet (e.g., the faucet) would succeed fully."
else
    echo -e "${RED}${BOLD}  ? NO RESPONSE${RESET}"
    echo -e "${RED}  Check that FreeRADIUS and the daemon are running.${RESET}"
fi

echo ""
echo -e "${CYAN}  Demo complete. Try the faucet: https://amperstrand.github.io/tollgate-auth/${RESET}"
echo -e "${RESET}"
