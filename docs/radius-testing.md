# tollgate-auth-radius — E2E Testing Guide

**Live instance**: `radius.nodns.shop:1812`
**Shared secret**: `tollgate`
**Tokens**: Testnut only (worthless test tokens from [testnut.cashu.exchange](https://testnut.cashu.exchange))

## What's deployed

- FreeRADIUS 3 on `radius.nodns.shop:1812/1813`
- PEAP (EAP-TLS tunnel with self-signed cert for `radius.nodns.shop`)
- Inner auth: Cashu token in `User-Name` field, validated by `tollgate-auth-radius` binary
- Token validation: decode → verify unspent with mint → redeem to wallet → accept
- Session tracking: MAC-based reconnection (skip token check for active sessions)
- Token in `User-Password` (PAP): **not yet supported** — would require plain-text password mode instead of MSCHAPv2 challenge/response (see below)

---

## Test 1: radtest (basic RADIUS, no EAP)

The simplest test. Sends a plain Access-Request without the EAP/PEAP tunnel.
This bypasses the full WiFi auth flow but validates the token validation pipeline.

```bash
# Install radtest (part of freeradius-utils)
sudo apt install freeradius-utils

# Get a test token from the faucet:
#   https://amperstrand.github.io/tollgate-ssh/
# Or mint one from testnut.cashu.exchange

# Test with a Cashu token as the username
radtest "cashuB..." "" radius.nodns.shop 0 tollgate
#                                    ^port  ^shared-secret

# Expected: Access-Accept if token is valid
#           Access-Reject if token is invalid/spent
```

**What this tests**: Token decode, mint verification, redemption, replay protection.
**What this does NOT test**: EAP/PEAP, TLS tunnel, WiFi supplicant behavior.

---

## Test 2: eapol_test (full PEAP flow)

`eapol_test` simulates a real WiFi supplicant performing WPA2-Enterprise auth
with the full EAP-PEAP handshake. This is the closest you can get to testing
with a real phone without having a real access point.

```bash
# Install wpa_supplicant (includes eapol_test)
sudo apt install wpasupplicant

# Create a test config file
cat > /tmp/eapol-test.conf << 'EOF'
network={
    ssid="test-radius"
    key_mgmt=WPA-EAP
    eap=PEAP
    identity="cashuB...your-token-here..."
    password=""
    phase2="autheap=MSCHAPV2"
    # Accept self-signed cert
    ca_cert=""
}
EOF

# Run the test
sudo eapol_test -c /tmp/eapol-test.conf -a radius.nodns.shop -p 1812 -s tollgate

# Expected output on success:
#   EAP: EAP entering state RECEIVED
#   EAP: EAP entering state IDENTITY
#   ...
#   RADIUS: Received RADIUS message: Access-Accept
```

**What this tests**: Full EAP-PEAP handshake, TLS tunnel establishment, inner auth
with Cashu token as username, Access-Accept/Reject flow.
**What this does NOT test**: Actual WiFi association, 802.1X port control, real device behavior.

---

## Test 3: Real access point + real phone

Connect a WiFi access point configured for WPA2-Enterprise to use `radius.nodns.shop`
as its RADIUS server, then connect a phone.

### OpenWRT

```bash
# /etc/config/wireless
config wifi-iface 'default_radio0'
    option device 'radio0'
    option network 'lan'
    option mode 'ap'
    option ssid 'Cashu-WiFi'
    option encryption 'wpa2'
    option server 'radius.nodns.shop'
    option key 'tollgate'
    option port '1812'
```

### Ubiquiti UniFi

1. Settings → WiFi → Create new network
2. Security: WPA Enterprise
3. RADIUS server: `radius.nodns.shop`
4. RADIUS port: 1812
5. Shared secret: `tollgate`

### Phone configuration

1. Connect to the WiFi network
2. When prompted for credentials:
   - **Username**: paste a Cashu ecash token (`cashuB...`)
   - **Password**: anything (not validated for cashu usernames)
3. Accept the self-signed certificate warning (CN=radius.nodns.shop)
4. If token is valid → Access-Accept → device gets network access

**What this tests**: Everything end-to-end with real devices.

---

## Known limitations

### Token length in RADIUS

RADIUS `User-Name` attribute is limited to **253 bytes** (per RFC 2865 §5.4).
Cashu V4 tokens (`cashuB...`) are typically 150-400+ characters depending on amount
and number of proofs. Small tokens (8-64 sat) should fit. Large tokens may exceed
the limit and be silently truncated by the RADIUS client (AP/supplicant).

**Mitigation options** (future work):
- Accept token in `User-Password` instead (requires PAP, not MSCHAPv2)
- Use a short lookup code: user pays on a web page, gets a 6-char code, uses that as RADIUS username
- LNURL-withdraw URL as username (shorter than Cashu token)

### Password field / PAP mode

Currently the token goes in the **username** field because PEAP's default inner
auth (MSCHAPv2) does challenge-response hashing that prevents dynamic password
validation. To use the password field, we'd need:

1. **EAP-TTLS with PAP inner auth** — FreeRADIUS can be configured for this.
   The password arrives as plaintext inside the TLS tunnel and can be validated
   dynamically. This is actually simpler than PEAP-MSCHAPv2 for our use case.

2. **Future: challenge-response → payment proof** — If we could tie the
   MSCHAPv2 challenge to a Cashu mint proof or Lightning HTLC proof, the
   challenge-response protocol itself becomes a payment authorization. This is
   a groundbreaking research direction but not yet implemented.

**Status**: Username field works today. Password field / PAP support is future work.

### Self-signed certificate

The server uses a self-signed certificate. Phones will show a "trust this
certificate?" dialog. For production, use a real CA-signed cert (Let's Encrypt
or a proper PKI).

---

## Server management

```bash
# Check FreeRADIUS status
systemctl status freeradius

# View RADIUS logs
journalctl -u freeradius -f

# View token validation logs
tail -f /opt/cashu-tollgate/radius-tokens.log

# Check active sessions
ls /opt/cashu-tollgate/radius-sessions/

# Test the validation binary directly
/usr/local/bin/tollgate-auth-radius "cashuB..." "aa:bb:cc:dd:ee:ff"

# Check wallet balance
sudo -u cashu-wallet cdk-cli --work-dir /var/lib/cashu-wallet balance

# Debug FreeRADIUS (foreground, verbose)
freeradius -X
```
