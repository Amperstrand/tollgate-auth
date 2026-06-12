# Delegated Mode Testing Plan

## What Changed

Two new pieces are deployed to production (nodns.shop):

1. **tollgate-net v1 server** (Rust, port 2121) — CDK wallet connected to testnut.cashu.exchange. Accepts Cashu tokens via `POST /`, manages sessions, returns allotment via Nostr kind 1022 events.

2. **Delegated mode in tollgate-auth** (Go) — Both `tollgate-auth-radius` and `tollgate-auth-ssh` now support `TOLLGATE_AUTH_MODE=delegated` which forwards token processing to the v1 server instead of doing local cdk-cli redemption.

### Architecture (Delegated Mode)

```
  Phone (ADB)                         nodns.shop (cloud VPS)
       │                                     │
       │  802.1X EAP-TTLS+PAP               │
       │  Password = Cashu token             │
       ▼                                     │
  OpenWrt AP (physical, nearby)              │
  SSID: TollGate-9EB5 (or similar)          │
       │                                     │
       │  RADIUS Access-Request (UDP 1812)   │
       ▼                                     │
  FreeRADIUS ──exec──► tollgate-auth-radius ──POST /──► tollgate-net v1 server
                        (Go binary)            X-TollGate-Mac    (Rust, :2121)
                                              ◄──kind 1022────
                        Session-Timeout      CDK wallet receives
                        = allotment_sec      token (NUT-03 swap)
```

### What's Different from Existing Tests

The existing `physical-router-test-automation` test suite tests the **captive portal flow**:

- Phone connects to TollGate AP
- OpenNDS intercepts HTTP → shows captive portal (React SPA)
- User pastes Cashu token in portal input field
- Portal submits token to router backend (`:2121`)
- Router backend validates + opens gate via ndsctl

**Delegated mode is a completely different flow:**

- Phone connects to TollGate AP using WPA2-Enterprise (EAP-TTLS+PAP)
- Cashu token is the WiFi password (not pasted in a portal)
- AP's hostapd sends RADIUS Access-Request to FreeRADIUS on nodns.shop
- FreeRADIUS exec module calls `tollgate-auth-radius` which POSTs token to v1 server
- v1 server (CDK wallet) receives token, returns allotment
- FreeRADIUS returns Access-Accept with Session-Timeout
- AP enforces session timeout, phone gets internet

**Key differences for testing:**

| Aspect | Captive Portal (existing) | Delegated RADIUS (new) |
|---|---|---|
| WiFi type | Open (no auth) | WPA2-Enterprise (EAP-TTLS+PAP) |
| Token delivery | Paste in web portal | WiFi password field |
| Token processing | Router backend (local) | nodns.shop v1 server (delegated) |
| Wallet | Router's local wallet | CDK wallet on nodns.shop |
| Session management | OpenNDS + ndsctl | FreeRADIUS Session-Timeout |
| Gate enforcement | ndsctl iptables | AP hostapd enforcement |
| Backend API | `:2121` on router | `:2121` on nodns.shop |

---

## Prerequisites

### Router Setup (must be done once)

The physical OpenWrt router needs WPA2-Enterprise configured. See `docs/demo-openwrt.md` for the full guide. Quick version:

```sh
# On router (SSH):
opkg remove wpad-basic-mbedtls
opkg install wpad-mbedtls

uci set wireless.default_radio0.encryption='wpa2'
uci set wireless.default_radio0.auth_server='<NODNS_SHOP_IP>'
uci set wireless.default_radio0.auth_port='1812'
uci set wireless.default_radio0.auth_secret='tollgate'
uci commit wireless
wifi reload
```

Note: The router's RADIUS config must point to nodns.shop's IP (not hostname — some OpenWrt builds can't resolve DNS from hostapd context). Get the IP: `dig +short nodns.shop A`.

**IMPORTANT:** The existing test suite configures the router for open WiFi + captive portal. Running delegated mode tests requires changing the router's WiFi encryption to WPA2-Enterprise. This will break captive portal tests. You need to either:
- Use a separate radio/SSID for RADIUS testing, or
- Reconfigure the router between test suites

### Server Status (nodns.shop — already deployed)

These services should be running:

```sh
systemctl is-active tollgate-net freeradius tollgate-auth-ssh
# Expected: active, active, active
```

- `tollgate-net` — v1 server on port 2121, CDK wallet, testnut mint
- `freeradius` — FreeRADIUS on port 1812, exec module → `tollgate-auth-radius`
- `tollgate-auth-ssh` — SSH tollgate on port 2222

### Environment

```sh
# In .env or environment:
TOLLGATE_AUTH_MODE=delegated           # Already set in /etc/default/freeradius
TOLLGATE_SESSIOND_URL=http://127.0.0.1:2121  # Already set
```

### Token Minter

```sh
# From tollgate-ssh directory:
node scripts/mint-testnut.js --no-dleq 8   # 8 sat → ~7 min after fee
```

No-DLEQ tokens are 230 bytes — fit in a single RADIUS attribute inside EAP-TTLS tunnel.

---

## Test Plan

### Tier 1: Server-Side Validation (no phone needed)

These verify the full pipeline from RADIUS to v1 server. Run from any machine with SSH access to nodns.shop.

#### 1.1 Services Health Check

```sh
ssh root@nodns.shop '
  echo "=== Services ==="
  systemctl is-active tollgate-net freeradius tollgate-auth-ssh
  echo "=== Ports ==="
  ss -tlnp | grep -E "2121|2222"
  ss -ulnp | grep 1812
  echo "=== FreeRADIUS env ==="
  cat /etc/default/freeradius | grep TOLLGATE
'
```

**Expected:** All services active, ports listening, `TOLLGATE_AUTH_MODE=delegated` set.

#### 1.2 v1 Server Direct Test

```sh
TOKEN=$(node scripts/mint-testnut.js --no-dleq 8)
ssh root@nodns.shop "
  curl -s -w '\nHTTP %{http_code}\n' \
    -X POST http://127.0.0.1:2121/ \
    -H 'X-TollGate-MAC: test-mac-001' \
    -H 'Content-Type: application/octet-stream' \
    --data-binary '$TOKEN'
"
```

**Expected:** HTTP 200, response contains allotment (Nostr kind 1022 event).

#### 1.3 RADIUS Fresh Auth (radclient)

```sh
TOKEN=$(node scripts/mint-testnut.js --no-dleq 8)
ssh root@nodns.shop "
  echo 'User-Name=test-user' > /tmp/rad.txt
  echo 'User-Password=$TOKEN' >> /tmp/rad.txt
  echo 'Calling-Station-Id=aa-bb-cc-dd-ee-ff' >> /tmp/rad.txt
  echo 'NAS-IP-Address=127.0.0.1' >> /tmp/rad.txt
  radclient -x -r 1 -t 15 127.0.0.1:1812 auth tollgate < /tmp/rad.txt
"
```

**Expected:** Access-Accept, `Session-Timeout` ≥ 400, `Acct-Interim-Interval = 60`, `Reply-Message` contains amount and mint info.

#### 1.4 RADIUS Reconnection (same MAC + same token)

```sh
# Re-send the same request (same MAC, same token)
ssh root@nodns.shop "
  radclient -x -r 1 -t 15 127.0.0.1:1812 auth tollgate < /tmp/rad.txt
"
```

**Expected:** Access-Accept, `Session-Timeout` slightly less than first request (time consumed), `Reply-Message` contains "Session resumed".

#### 1.5 RADIUS Top-Up (new token, same MAC)

```sh
TOKEN2=$(node scripts/mint-testnut.js --no-dleq 8)
ssh root@nodns.shop "
  echo 'User-Name=test-user' > /tmp/rad2.txt
  echo 'User-Password=$TOKEN2' >> /tmp/rad2.txt
  echo 'Calling-Station-Id=aa-bb-cc-dd-ee-ff' >> /tmp/rad2.txt
  echo 'NAS-IP-Address=127.0.0.1' >> /tmp/rad2.txt
  radclient -x -r 1 -t 15 127.0.0.1:1812 auth tollgate < /tmp/rad2.txt
"
```

**Expected:** Access-Accept. Note: current implementation returns remaining time from first session, not extended time. The second token is NOT redeemed — this is a known limitation (local session check fires before payment extraction).

#### 1.6 SSH Delegated Test

```sh
TOKEN=$(node scripts/mint-testnut.js --no-dleq 8)
timeout 15 ssh -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null \
  -p 2222 "${TOKEN}@nodns.shop" "echo SUCCESS; whoami" 2>&1
```

**Expected:** Banner showing amount and time, BusyBox shell prompt, `SUCCESS` printed.

#### 1.7 Invalid Token Rejection

```sh
ssh root@nodns.shop "
  echo 'User-Name=test-user' > /tmp/rad-bad.txt
  echo 'User-Password=invalid-not-a-token' >> /tmp/rad-bad.txt
  echo 'Calling-Station-Id=aa-bb-cc-dd-ee-ff' >> /tmp/rad-bad.txt
  echo 'NAS-IP-Address=127.0.0.1' >> /tmp/rad-bad.txt
  radclient -x -r 1 -t 10 127.0.0.1:1812 auth tollgate < /tmp/rad-bad.txt
"
```

**Expected:** Access-Reject.

---

### Tier 2: Router + eapol_test (no phone needed)

Requires the physical OpenWrt router configured for WPA2-Enterprise (see Prerequisites).

#### 2.1 eapol_test from Router

```sh
# On the router:
TOKEN=$(...mint a token from laptop...)
cat > /tmp/eapol.conf << EOF
network={
    ssid="TollGate-Test"
    key_mgmt=WPA-EAP
    eap=TTLS
    identity="test-user"
    password="$TOKEN"
    phase1="peaplabel=0"
    phase2="auth=PAP"
}
EOF

eapol_test -c /tmp/eapol.conf -a <NODNS_SHOP_IP> -s tollgate -r 1 -t 30
```

**Expected:** `RADIUS message: code=2 (Access-Accept)`, `Session-Timeout` in response.

#### 2.2 Replay Protection

```sh
# Re-run with same config but different MAC
eapol_test -c /tmp/eapol.conf -a <NODNS_SHOP_IP> -s tollgate -M bb:99:88:77:66:55 -r 1 -t 30
```

**Expected:** Access-Reject (token already spent by first eapol_test).

---

### Tier 3: Phone Tests (ADB)

Requires: phone connected via ADB, phone near the physical OpenWrt AP, router configured for WPA2-Enterprise.

The phone test is fundamentally different from captive portal tests. The phone must:
1. Connect to a **WPA2-Enterprise** network (not open WiFi)
2. The Cashu token goes in the **password** field (not a web portal input)
3. The phone caches EAP credentials — reconnection sends the same token

#### 3.1 Connect Phone to TollGate WiFi

Use the existing ADB tooling from `physical-router-test-automation`. The `WiFi` class in `lib/clients/wifi.py` handles open WiFi + captive portal. For WPA2-Enterprise, you need a different approach:

```python
# Option A: Use Android settings intent + UI automation
# The phone can't programmatically configure WPA2-Enterprise without root.
# Open WiFi settings and let the user configure manually, or automate via UI.

# Option B: Configure the network via adb shell (requires root or content provider)
# This is NOT possible without root on Android 10.

# Option C: Pre-configure the network on the phone before testing.
# Go to Settings → WiFi → TollGate-9EB5 → EAP: TTLS, Phase 2: PAP,
# Identity: test-user, Password: <paste token>
```

**Steps for the test LLM:**
1. Mint a token: `node scripts/mint-testnut.js --no-dleq 8` from tollgate-ssh dir
2. Copy token to phone clipboard: `adb shell "am broadcast -a clipper.set -e text '<TOKEN>'"`
3. Open WiFi settings: `adb shell "am start -a android.settings.WIFI_SETTINGS"`
4. The phone UI will show available networks. Find the TollGate SSID.
5. Tap it, configure: EAP method = TTLS, Phase 2 = PAP, Password = paste from clipboard
6. Wait for connection (check with `adb shell "dumpsys wifi | grep mWifiInfo"`)

#### 3.2 Verify Internet Access

```python
# After WiFi connects, verify internet:
adb.shell("ping -c 3 -W 5 1.1.1.1")
# Or use curl:
adb.shell("curl -s --connect-timeout 5 -o /dev/null -w '%{http_code}' https://google.com")
```

**Expected:** HTTP 200 or ping success.

#### 3.3 Check Server Logs

```sh
ssh root@nodns.shop "journalctl -u tollgate-net --since '5 min ago' --no-pager"
# Should show: "[NUT-00] Receiving Cashu token (230 bytes...)"
# Should show: "[NUT-00] Token received successfully: 7 sat"

ssh root@nodns.shop "tail -20 /var/log/freeradius/radius.log"
# Should show the Access-Accept processing
```

#### 3.4 Session Reconnection (Sleep/Wake)

```python
# Put phone to sleep
adb.shell("input keyevent KEYCODE_POWER")
time.sleep(5)
# Wake phone
adb.wake_and_unlock()
time.sleep(5)
# Check if still connected
adb.shell("dumpsys wifi | grep mWifiInfo")
# Verify internet still works
adb.shell("curl -s --connect-timeout 5 -o /dev/null -w '%{http_code}' https://google.com")
```

**Expected:** Phone reconnects using cached EAP credentials (same token). FreeRADIUS processes reconnection — `Reply-Message` shows "Session resumed" with remaining time. No new token redeemed.

#### 3.5 Session Expiry

Wait for the session to expire (Session-Timeout seconds). Then:

```python
# After session expires, verify internet is cut off
result = adb.shell("curl -s --connect-timeout 5 -o /dev/null -w '%{http_code}' https://google.com")
# Expected: failure (timeout or connection refused by AP)
```

**Expected:** AP enforces Session-Timeout — phone loses internet when time expires.

---

## Known Issues / Limitations

1. **Top-up doesn't extend session**: When a second token is sent with the same MAC while a session is active, the Go binary finds the existing local session first and returns remaining time. The second token is NOT redeemed. This is a known limitation — the local session check fires before payment extraction. Fix: skip local session check when `authMode == "delegated"` and the password contains a valid Cashu token prefix.

2. **Comma bug fixed**: FreeRADIUS exec module treats commas as attribute separators. The `replyMessage()` function now replaces commas with semicolons. Deployed to nodns.shop.

3. **Phone can't auto-configure WPA2-Enterprise via ADB**: Android 10 requires root for programmatic WPA2-Enterprise configuration. The phone must be configured manually via Settings UI, or the test must use UI automation (`adb shell input tap` etc.).

4. **Router must be reconfigured**: The existing test suite configures the router for open WiFi + captive portal. Delegated mode requires WPA2-Enterprise. These are mutually exclusive on the same radio/SSID.

5. **Token size limit**: No-DLEQ tokens are 230 bytes (fits in RADIUS attribute). DLEQ tokens are 378 bytes (exceeds 253-byte FreeRADIUS limit inside EAP-TTLS tunnel). Always use `--no-dleq` for RADIUS testing.

---

## Quick Reference: Key Files

| File | Purpose |
|---|---|
| `cmd/tollgate-auth-radius/main.go` | RADIUS validator with delegated mode |
| `cmd/tollgate-auth-ssh/main.go` | SSH server with delegated mode |
| `internal/sessiond/client.go` | HTTP client for v1 server |
| `scripts/mint-testnut.js` | Mint test tokens from testnut |
| `scripts/test-delegated-e2e.sh` | 9-test E2E script against v1 server |
| `docs/demo-openwrt.md` | OpenWrt router setup guide |
| `docs/tollgate-rs-integration.md` | Architecture and integration design |

## Quick Reference: Key Commands

```sh
# Mint a token
node scripts/mint-testnut.js --no-dleq 8

# Test RADIUS directly
ssh root@nodns.shop "radclient -x -r 1 -t 15 127.0.0.1:1812 auth tollgate < /tmp/rad.txt"

# Test SSH
ssh -p 2222 "<TOKEN>@nodns.shop"

# Watch server logs
ssh root@nodns.shop "journalctl -u tollgate-net -f"

# Watch FreeRADIUS logs
ssh root@nodns.shop "tail -f /var/log/freeradius/radius.log"

# Check v1 server wallet balance
ssh root@nodns.shop "curl -s http://127.0.0.1:2121/balance"

# Phone WiFi status
adb shell "dumpsys wifi | grep mWifiInfo"
```
