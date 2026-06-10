# tollgate-auth — Live Demo & Testing Guide

**Live instance**: `nodns.shop:1812` (RADIUS) + `nodns.shop:2222` (SSH)
**Shared secret**: `tollgate`
**CI status**: [![E2E Demo](https://github.com/Amperstrand/tollgate-auth/actions/workflows/e2e-demo.yml/badge.svg)](https://github.com/Amperstrand/tollgate-auth/actions/workflows/e2e-demo.yml)

## What's live

- **RADIUS** (WiFi auth): FreeRADIUS 3 on `nodns.shop:1812/1813`
- **SSH** (shell access): tollgate-auth-ssh on `nodns.shop:2222`
- **Accepted payment methods**:
  - **Cashu tokens** (`cashuA...` / `cashuB...`) — full decode → verify → redeem
  - **LNURL-withdraw** (`lnurlw...` / `LNURLW...`) — pass-through accept (TODO: claim payment)
- **EAP methods**:
  - **EAP-TTLS+PAP** (recommended) — payment in password field, no length limit
  - **PEAP+MSCHAPv2** (legacy) — payment in username field, <253 byte limit
- Payment accepted from **username OR password** — whichever starts with `cashu` or `lnurlw`
- Session tracking: MAC-based reconnection (skip payment check for active sessions)
- **Reply-Message**: Payment info included in Access-Accept (amount, duration, mint)
- **Mint allowlist**: Only test mints accepted (regex `(?i)test`)
- Token source: [testnut.cashu.space](https://testnut.cashu.space) (worthless test tokens)

## Architecture: Bootstrap Token → Sustained Access

The system uses a **bootstrap token** model:

1. **Bootstrap**: Device connects to WiFi with a payment credential (Cashu token or lnurlw) → gets N minutes of network access
2. **Sustain**: While connected, the device submits further payments to extend the session
3. **Channel**: Future — Cashu payment channel for continuous micropayment

A small bootstrap token (e.g. 8 sat = 8 minutes) buys enough time to get on the network and set up ongoing payments. LNURL-withdraw codes give 1 hour (default, until actual claiming is implemented).

---

## Try it now — copy/paste

These commands work against the live server. Install `freeradius-utils` first:

```bash
sudo apt install freeradius-utils   # Debian/Ubuntu
# or: brew install freeradius       # macOS (Homebrew)
```

### lnurlw in username → Accept

```bash
$ radtest "lnurlw1dp68gurn8ghj7ampd3kx2ar0veekzar0wd5xjtnrdakj7" "anything" nodns.shop 0 tollgate

Received Access-Accept
        Reply-Message = "Valid LNURLw code: 60m access (TODO: claim Lightning payment)"
        Session-Timeout = 3600
```

### lnurlw in password → Accept

```bash
$ radtest "wifi-user" "lnurlw1aa68gurn8ghj7ampf3kx2ar0veekzar0wd5xjtnrdakj7" nodns.shop 0 tollgate

Received Access-Accept
        Reply-Message = "Valid LNURLw code: 60m access (TODO: claim Lightning payment)"
        Session-Timeout = 3600
```

### Uppercase LNURLW → Accept

```bash
$ radtest "LNURLW1DP68GURN8GHJ7AMPD3KX2AR0VEEKZAR0WD5XJTNRDAKJ7" "anything" nodns.shop 0 tollgate

Received Access-Accept
        Session-Timeout = 3600
```

### Invalid credentials → Reject

```bash
$ radtest "not-a-token" "bad-password" nodns.shop 0 tollgate

Received Access-Reject
```

---

## Testing with radclient (fake MAC addresses)

`radtest` doesn't send `Calling-Station-Id`, so all requests share one anonymous session. Use `radclient` to send custom MAC addresses for proper replay/reconnection testing:

### Fresh payment → Accept

```bash
echo 'User-Name = "lnurlw1testmac12345kx2ar0veekzar0wd5xjtnrdakj7"
User-Password = "anything"
NAS-IP-Address = 10.0.0.1
Calling-Station-Id = "aa-bb-cc-dd-ee-ff"
NAS-Port = 0' | radclient -x nodns.shop auth tollgate
```

### Replay same code, different MAC → Reject

```bash
echo 'User-Name = "lnurlw1testmac12345kx2ar0veekzar0wd5xjtnrdakj7"
User-Password = "anything"
NAS-IP-Address = 10.0.0.1
Calling-Station-Id = "11-22-33-44-55-66"
NAS-Port = 0' | radclient -x nodns.shop auth tollgate
# → Access-Reject + Reply-Message = "Rejected: LNURLw code already used"
```

### Same MAC, different code → Accept (reconnection)

```bash
echo 'User-Name = "lnurlw1differentcode7kx2ar0veekzar0wd5xjtnrdakj7"
User-Password = "anything"
NAS-IP-Address = 10.0.0.1
Calling-Station-Id = "aa-bb-cc-dd-ee-ff"
NAS-Port = 0' | radclient -x nodns.shop auth tollgate
# → Access-Accept + Reply-Message = "Session resumed: 59m remaining..."
```

---

## Full E2E test with eapol_test

`eapol_test` simulates a real WiFi supplicant performing WPA2-Enterprise auth.
This is the closest you can get to testing with a real phone.

### EAP-TTLS+PAP (recommended)

Token goes in the **password** field. No length limit.

```bash
sudo apt install wpasupplicant

cat > /tmp/eapol-ttls-pap.conf << 'EOF'
network={
    ssid="Cashu-WiFi"
    key_mgmt=WPA-EAP
    eap=TTLS
    identity="tollgate"
    password="cashuB...paste-your-token-here..."
    phase2="auth=PAP"
    ca_cert=""
    altsubject_match="DNS:radius.nodns.shop"
}
EOF

sudo eapol_test -c /tmp/eapol-ttls-pap.conf -a nodns.shop -p 1812 -s tollgate
# Success: "RADIUS: Received RADIUS message: Access-Accept"
```

### PEAP+MSCHAPv2 (legacy)

Token goes in the **username** field. Limited to <253 bytes.

```bash
cat > /tmp/eapol-peap.conf << 'EOF'
network={
    ssid="Cashu-WiFi"
    key_mgmt=WPA-EAP
    eap=PEAP
    identity="cashuB...short-token..."
    password="anything"
    phase2="autheap=MSCHAPV2"
    ca_cert=""
}
EOF

sudo eapol_test -c /tmp/eapol-peap.conf -a nodns.shop -p 1812 -s tollgate
```

---

## Real access point + phone

### OpenWRT

```bash
# /etc/config/wireless
config wifi-iface 'default_radio0'
    option device 'radio0'
    option network 'lan'
    option mode 'ap'
    option ssid 'Cashu-WiFi'
    option encryption 'wpa2'
    option server 'nodns.shop'
    option key 'tollgate'
    option port '1812'
```

### Ubiquiti UniFi

1. Settings → WiFi → Create new network
2. Security: WPA Enterprise
3. RADIUS server: `nodns.shop`, port 1812, secret `tollgate`

### Phone: EAP-TTLS+PAP (recommended)

1. Connect to WiFi → credential prompt appears
2. **Username**: anything (e.g. `tollgate`)
3. **Password**: paste a Cashu token (`cashuB...`) or lnurlw code
4. EAP method: **TTLS**, Phase 2: **PAP**
5. Accept certificate warning (CN=radius.nodns.shop)
6. Valid payment → Access-Accept → network access

**Android**: Settings → WiFi → Advanced → EAP: TTLS → Phase 2: PAP → token as password
**iOS**: Configure via Apple Configurator or mobileconfig profile

---

## What the CI checks

The [E2E workflow](../../actions/workflows/e2e-demo.yml) runs on every push to `main`. It uses `radclient` with fake MAC addresses to test the full RADIUS flow:

1. Fresh `lnurlw` → Accept + Reply-Message
2. Replay same code (different MAC) → Reject (replay protection)
3. Same MAC, different code → Accept (session reconnection)
4. `lnurlw` in password field → Accept
5. Uppercase `LNURLW` → Accept
6. Invalid credentials → Reject
7. SSH tollgate responds on port 2222

---

## Payment method details

### Cashu tokens

Full validation pipeline:
1. Decode token (V3 JSON `cashuA` / V4 CBOR `cashuB`)
2. Replay check (SHA256 hash against spent list)
3. Mint allowlist (only test mints matching `(?i)test`)
4. Verify unspent with mint API (`POST /v1/checkstate`)
5. Redeem to wallet (`cdk-cli receive`)
6. Create session: 1 sat = 60 seconds

### LNURL-withdraw (lnurlw)

Pass-through accept for proof of concept. TODO:
1. Decode bech32 (HRP=`lnurlw`) → extract callback URL
2. GET callback → receive withdraw parameters
3. Generate Lightning invoice → submit to callback
4. Wait for settlement → determine amount → set session duration

Currently: any `lnurlw...` string gets 1 hour access, replay-protected by hash.

---

## Known limitations

- **Bootstrap token only** — no automated payment renewal yet
- **Self-signed cert** — phones show certificate warning
- **LNURL-w claiming not implemented** — lnurlw codes are accepted without claiming the payment
- **No IP addresses in this project** — only domain names (`nodns.shop`, `radius.nodns.shop`)

---

## Server management

```bash
# Check FreeRADIUS status
systemctl status freeradius

# View token validation logs
tail -f /opt/tollgate-auth/radius-tokens.log

# Check active sessions
ls /opt/tollgate-auth/radius-sessions/

# Test binary directly
/usr/local/bin/tollgate-auth-radius "lnurlw1test..." "aa:bb:cc:dd:ee:ff"
/usr/local/bin/tollgate-auth-radius "any-user" "aa:bb:cc:dd:ee:ff" "cashuB..."

# Check wallet balance
sudo -u cashu-wallet cdk-cli --work-dir /var/lib/cashu-wallet balance

# Debug FreeRADIUS (foreground, verbose)
freeradius -X
```
