# tollgate-auth — Live Demo & Testing Guide

**Live instance**: `nodns.shop:1812` (RADIUS UDP) + `nodns.shop:2083` (RadSec TLS) + `nodns.shop:2222` (SSH)
**Shared secret**: `tollgate` (UDP 1812) / `radsec` (TCP 2083)
**CI status**: [![E2E Demo](https://github.com/Amperstrand/tollgate-auth/actions/workflows/e2e-demo.yml/badge.svg)](https://github.com/Amperstrand/tollgate-auth/actions/workflows/e2e-demo.yml)

## What's live

- **RADIUS** (WiFi auth): FreeRADIUS 3 on `nodns.shop:1812/1813` (UDP, shared secret)
- **RadSec** (encrypted RADIUS): FreeRADIUS 3 on `nodns.shop:2083` (TCP/TLS, Let's Encrypt cert)
- **SSH** (shell access): tollgate-auth-ssh on `nodns.shop:2222`
- **Accepted payment methods**:
  - **Cashu tokens** (`cashuA...` / `cashuB...`) — full decode → verify → redeem
  - **LNURL-withdraw** (`lnurlw...` / `LNURLW...`) — pass-through accept (TODO: claim payment)
- **EAP methods**:
  - **EAP-TTLS+PAP** (recommended) — no-DLEQ token (230b) in password field, identity = "anonymous". Validated end-to-end on real Android hardware. Fallback: split full DLEQ token (378b) across password (200b) + username (178b).
  - **PEAP+MSCHAPv2** (legacy) — payment in username field only, <253 byte limit
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

## RadSec (RADIUS over TLS, TCP port 2083)

The server also accepts RadSec connections — RADIUS over TLS with a valid Let's Encrypt certificate for `nodns.shop`. This encrypts the entire RADIUS conversation (including the Cashu token), unlike plain RADIUS where the shared secret only obfuscates the password.

**Why RadSec matters:**
- Plain RADIUS (UDP 1812): password is MD5-obfuscated with shared secret — weak if secret is public
- EAP-TTLS+PAP: token is inside TLS tunnel between client and FreeRADIUS — already encrypted
- **RadSec**: encrypts the NAS → FreeRADIUS hop — protects everything, including non-EAP requests

Most enterprise NAS devices support RadSec (Cisco, Aruba, Juniper, MikroTik RouterOS 7+).

### Test RadSec with socat + radclient

FreeRADIUS 3.x `radclient` doesn't support TLS natively. Use `socat` as a TLS tunnel:

```bash
sudo apt install socat freeradius-utils

# Create local TLS tunnel to RadSec port
socat TCP-LISTEN:11812,reuseaddr,fork SSL:nodns.shop:2083,verify=0 &
SOCAT_PID=$!
sleep 1

# Test through the tunnel (secret = radsec for RadSec)
echo 'User-Name = "lnurlw1radsectest111kx2ar0veekzar0wd5xjtnrdakj7z"
User-Password = "anything"
NAS-IP-Address = 10.0.0.1
Calling-Station-Id = "aa-00-11-22-33-44"
NAS-Port = 0' | radclient -x -P tcp -r 1 -t 5 127.0.0.1:11812 auth radsec

# → Access-Accept with Reply-Message and Session-Timeout

kill $SOCAT_PID
```

### RadSec with real NAS devices

**Cisco (IOS-XE):**
```
radius server tollgate-radsec
 address ipv4 <server-ip> auth-port 2083 acct-port 2083
 key radsec
 transport tls
```

**Aruba (AOS-CX):**
```
radius-server host <server-ip> key radsec tls
```

**MikroTik (RouterOS 7.x+):**
```
/radius add address=<server-ip> secret=radsec service=login transport=tls
```

---

## Full E2E test with eapol_test

`eapol_test` simulates a real WiFi supplicant performing WPA2-Enterprise auth. This is the **only way** to test Cashu tokens over RADIUS — tokens are 378 bytes and exceed FreeRADIUS's `diameter2vp` 253-byte limit inside EAP-TTLS tunnels.

The token must be split: first 200 bytes in password, remaining 178 bytes in username. The `scripts/mint-testnut.js` script handles this automatically. See [radius-token-size.md](radius-token-size.md) for details on why the split is necessary.

### EAP-TTLS+PAP with Cashu token (split)

```bash
# Install eapol_test (Ubuntu)
sudo apt install eapoltest

# Mint a test token and write eapol_test config (token is split automatically)
node scripts/mint-testnut.js --write-eapol-config /tmp/eapol.conf
# Output: Wrote eapol_test config to /tmp/eapol.conf (password=200b, identity=178b)

# Resolve IP first — eapol_test requires IP address, not hostname
RADIUS_IP=$(dig +short nodns.shop A | head -1)

# Run (flags: -a=server IP, -p=port, -s=shared secret)
eapol_test -c /tmp/eapol.conf -a "$RADIUS_IP" -p 1812 -s tollgate -M aa:bb:cc:dd:ee:ff -r 1 -t 30
# Success: "MPPE keys OK: 1  mismatch: 0" and "SUCCESS"
```

> **Note**: eapol_test flags differ between builds. Ubuntu's `eapoltest` package uses `-p` for port and `-s` for shared secret. Builds from source (`wpasupplicant` source package) may use different flags.

### EAP-TTLS+PAP with LNURLw (no split needed)

LNURLw codes are short (~50 bytes) and fit in a single field:

```bash
cat > /tmp/eapol-lnurlw.conf << 'EOF'
network={
    ssid="Cashu-WiFi"
    key_mgmt=IEEE8021X
    eap=TTLS
    identity="tollgate"
    password="lnurlw1dp68gurn8ghj7ampd3kx2ar0veekzar0wd5xjtnrdakj7"
    phase2="auth=PAP"
    anonymous_identity="anonymous"
}
EOF

eapol_test -c /tmp/eapol-lnurlw.conf -a "$RADIUS_IP" -p 1812 -s tollgate -M aa:bb:cc:dd:ee:ff -r 1 -t 30
```

### PEAP+MSCHAPv2 (legacy — Cashu tokens do NOT work)

Token must fit in User-Name alone (253-byte limit). Cashu tokens (378 bytes) cannot be sent this way. Only LNURLw codes or other short credentials work with PEAP.

```bash
cat > /tmp/eapol-peap.conf << 'EOF'
network={
    ssid="Cashu-WiFi"
    key_mgmt=IEEE8021X
    eap=PEAP
    identity="lnurlw...short-code..."
    password="anything"
    phase2="autheap=MSCHAPV2"
    anonymous_identity="anonymous"
}
EOF

eapol_test -c /tmp/eapol-peap.conf -a "$RADIUS_IP" -p 1812 -s tollgate -M aa:bb:cc:dd:ee:ff -r 1 -t 30
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

The [E2E workflow](../../actions/workflows/e2e-demo.yml) runs on every push to `main`. All tests are strict — a single failure stops the pipeline.

1. Fresh `lnurlw` → Accept + Reply-Message
2. Replay same code (different MAC) → Reject (replay protection)
3. Same MAC, different code → Accept (session reconnection)
4. `lnurlw` in password field → Accept
5. Uppercase `LNURLW` → Accept
6. Invalid credentials → Reject
7. **Cashu token via EAP-TTLS+PAP** (8 sats, minted fresh in CI, split token sent through TLS tunnel) → Access-Accept
8. **Cashu token replay** (same token, different MAC) → Access-Reject
9. **RadSec** (TLS on port 2083 via socat tunnel) → Accept via encrypted transport
10. **SSH tollgate** responds with SSH banner on port 2222

Tests 7-8 use `eapol_test` with split Cashu tokens (200b password + 178b identity). The token is minted fresh in CI using `@cashu/cashu-ts` from `testnut.cashu.exchange`.

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

## Beyond WiFi: RADIUS Use Cases

RADIUS isn't just for WiFi. Any device that speaks RADIUS can use tollgate-auth to accept ecash payments for access. The same FreeRADIUS + tollgate-auth-radius setup works for all of these simultaneously.

### VPN authentication (OpenVPN)

OpenVPN can authenticate users against RADIUS via `openvpn-plugin-auth-pam` + PAM RADIUS module.

**User experience:** User pastes a `lnurlw` code as their VPN username (any password) in the OpenVPN client. If valid, they get VPN access for 1 hour.

> **Important:** Cashu tokens (230 bytes) exceed the RADIUS PAP `User-Password` limit of 128 bytes. Only `lnurlw` codes (~60 bytes) work through the direct PAM path. For Cashu tokens, use EAP-TTLS+PAP (WiFi) which tunnels inside TLS and bypasses this limit.

**Tested and working** on `nodns.shop` (Debian 12, OpenVPN 2.6.19, FreeRADIUS 3.2.5).

**Setup:**

```bash
# 1. Install packages
apt install -y openvpn easy-rsa libpam-radius-auth

# 2. Generate PKI
mkdir -p /etc/openvpn/easy-rsa && cd /etc/openvpn/easy-rsa
cp -r /usr/share/easy-rsa/* .
./easyrsa init-pki
EASYRSA_BATCH=1 ./easyrsa build-ca nopass
EASYRSA_BATCH=1 ./easyrsa build-server-full server nopass
EASYRSA_BATCH=1 ./easyrsa gen-dh
openvpn --genkey secret /etc/openvpn/ta.key

# 3. Configure PAM RADIUS
cat > /etc/pam_radius_auth.conf << 'EOF'
127.0.0.1 tollgate 3
EOF
chmod 600 /etc/pam_radius_auth.conf

cat > /etc/pam.d/openvpn << 'EOF'
auth    sufficient  pam_radius_auth.so
account sufficient  pam_permit.so
EOF

# 4. OpenVPN server config
cat > /etc/openvpn/server.conf << 'EOF'
port 1194
proto udp
dev tun
ca /etc/openvpn/easy-rsa/pki/ca.crt
cert /etc/openvpn/easy-rsa/pki/issued/server.crt
key /etc/openvpn/easy-rsa/pki/private/server.key
dh /etc/openvpn/easy-rsa/pki/dh.pem
tls-auth /etc/openvpn/ta.key 0
server 10.9.0.0 255.255.255.0
keepalive 10 120
cipher AES-256-GCM
data-ciphers AES-256-GCM:AES-128-GCM:CHACHA20-POLY1305
persist-key
persist-tun
status /etc/openvpn/openvpn-status.log
verb 3
explicit-exit-notify 1

# PAM auth → RADIUS → tollgate-auth-radius
plugin /usr/lib/openvpn/openvpn-plugin-auth-pam.so openvpn

verify-client-cert none
username-as-common-name
EOF

# 5. NAT for VPN clients
iptables -t nat -A POSTROUTING -s 10.9.0.0/24 -o eth0 -j MASQUERADE

# 6. Start
openvpn --config /etc/openvpn/server.conf --daemon
```

**Client config** (download from server at `/etc/openvpn/client-tollgate.ovpn`):

```
client
dev tun
proto udp
remote YOUR_SERVER_IP 1194
resolv-retry infinite
nobind
persist-key
persist-tun
remote-cert-tls server
cipher AES-256-GCM
data-ciphers AES-256-GCM:AES-128-GCM:CHACHA20-POLY1305
auth-user-pass
verb 3

<ca>
(paste CA cert from /etc/openvpn/easy-rsa/pki/ca.crt)
</ca>

<tls-crypt>
(paste TA key from /etc/openvpn/ta.key)
</tls-crypt>
```

**Connect:** Paste `lnurlw` code as username, anything as password.

**FreeRADIUS client config** — must disable BlastRADIUS checks for PAM clients:

```
client all_clients {
    ipaddr = 0.0.0.0/0
    secret = tollgate
    require_message_authenticator = no
    limit_proxy_state = no
}
```

### VPN authentication (WireGuard)

WireGuard doesn't natively speak RADIUS, but there are two approaches:

1. **wg-easy + RADIUS** — Use [wg-easy](https://github.com/wg-easy/wg-easy) or a WireGuard management platform that supports RADIUS auth
2. **Script-based** — WireGuard `PostUp` script calls radclient to validate a token before adding the peer

**Proof of concept — script-based:**

```bash
#!/bin/bash
# /usr/local/bin/wg-tollgate-auth.sh
# Called by WireGuard PostUp with peer public key
# User sends token out-of-band (e.g., via HTTP API)

TOKEN="$1"
RESULT=$(echo "User-Name = \"$TOKEN\"
User-Password = \"anything\"
NAS-IP-Address = 10.0.0.1
Calling-Station-Id = \"wg-${PEER_PUBLIC_KEY:0:12}\"
NAS-Port = 0" | radclient -r 1 -t 2 127.0.0.1 auth tollgate 2>&1)

if echo "$RESULT" | grep -q "Access-Accept"; then
    # Add peer to WireGuard
    wg set wg0 peer "$PEER_PUBLIC_KEY" allowed-ips 10.0.0.x/32
    echo "Accepted"
else
    echo "Rejected"
fi
```

### VPN authentication (IPsec / StrongSwan)

StrongSwan has native RADIUS support via the `eap-radius` plugin.

```bash
# /etc/strongswan.d/charon/eap-radius.conf
eap-radius {
    load = yes
    servers {
        tollgate {
            address = 127.0.0.1
            secret = tollgate
            auth_port = 1812
            acct_port = 1813
        }
    }
}

# /etc/swanctl/swanctl.conf — connection profile
connections {
    tollgate-vpn {
        remote {
            auth = eap-md5   # or eap-ttls
            eap_id = %any
        }
    }
}
```

### Wired 802.1X (switch port authentication)

Enterprise switches enforce authentication before enabling a port. Any device plugging in must authenticate via RADIUS. The switch sends an Access-Request when it detects link-up.

**User experience:** Plug in Ethernet → OS prompts for credentials → paste Cashu token as password → port enables for N minutes.

**Cisco (IOS):**

```
! Global RADIUS config
radius server tollgate
 address ipv4 10.0.0.1 auth-port 1812 acct-port 1813
 key tollgate

! AAA config
aaa new-model
aaa authentication dot1x default group radius
aaa authorization network default group radius

! Switch port config
interface GigabitEthernet1/0/1
 switchport mode access
 authentication port-control auto
 dot1x pae authenticator
```

**HP/Aruba (AOS-CX):**

```
radius-server host 10.0.0.1 key tollgate
aaa authentication dot1x default group radius
interface 1/1/1
 dot1x-port-control auto
```

**MikroTik (RouterOS):**

```
/radius add address=10.0.0.1 secret=tollgate service=login
/interface ethernet set ether3 poe-out=enabled
# Or use dot1x server (RouterOS 7.x+)
/interface dot1x server add name=tollgate interface=ether3 auth-types=mac-based
```

### Captive portal (hotspot / café / hotel)

Captive portals intercept HTTP traffic and show a login page. The form submits credentials to a RADIUS backend. This is the most user-friendly option — no WPA2-Enterprise, no EAP, just a web page.

**User experience:** Connect to open WiFi → browser redirects to portal → paste Cashu token in the form → get internet access.

**MikroTik hotspot + RADIUS:**

```
/radius add address=10.0.0.1 secret=tollgate service=hotspot
/ip hotspot profile set default use-radius=yes
/ip hotspot setup
  hotspot interface: wlan1
  local address: 192.168.1.1
  address pool: 192.168.1.2-192.168.1.254
  DNS servers: 8.8.8.8
  DNS name: wifi.example.com
```

The user sees a login page and enters the Cashu token as their password. MikroTik sends a RADIUS Access-Request with the form fields.

**OpenWRT (with nodogsplash or CoovaChilli):**

```bash
# Install CoovaChilli
opkg install coova-chilli

# /etc/chilli/config
HS_RAD_PROTO=radius
HS_RADIUS=10.0.0.1
HS_RADIUS2=10.0.0.1
HS_RADSECRET=tollgate
HS_UAMFORMAT=http://wifi.example.com/login
```

**pfSense captive portal:**

1. Services → Captive Portal → Enable
2. Authentication → RADIUS server: `127.0.0.1:1812`, secret `tollgate`
3. Users paste Cashu token in the portal login form

### eduroam (academic / research networks)

[eduroam](https://eduroam.org/) is a federated RADIUS infrastructure used by universities worldwide (100M+ users). It uses WPA2-Enterprise with RADIUS proxying between institutions.

**How tollgate-auth fits:** An institution configures FreeRADIUS to route `@cashu` realm users to tollgate-auth-radius instead of their normal identity store. Students from other universities get normal eduroam auth; Cashu users get token-based access.

```
# FreeRADIUS proxy.conf — add a realm for cashu users
realm cashu {
    authpool = local_auth    # route to local server
}
```

This allows guests at conferences, visiting researchers, or public campus WiFi to pay for access with ecash instead of requiring institutional credentials.

### PPPoE / ISP

PPPoE concentrators (BRAS/NAS) authenticate subscribers via RADIUS. An ISP could accept Cashu tokens instead of (or in addition to) traditional username/password.

**User experience:** PPPoE client (router or OS) sends the Cashu token as the password. Valid token → IP address assigned → internet access.

This is primarily relevant for prepaid ISP models common in developing markets.

---

## Known limitations

- **Bootstrap token only** — no automated payment renewal yet
- **Self-signed cert** — phones show certificate warning
- **LNURL-w claiming not implemented** — lnurlw codes are accepted without claiming the payment
- **No IP addresses in this project** — only domain names (`nodns.shop`, `radius.nodns.shop`)
- **Cashu tokens require split for EAP-TTLS** — 378-byte tokens must be split at byte 200 across password (head) and username (tail) due to FreeRADIUS's diameter2vp 253-byte limit
- **Split tokens impractical for real phones** — users would need to paste two separate strings into identity and password fields manually
- **Cleartext-Password handling** — FreeRADIUS inner-tunnel delivers PAP password as `Cleartext-Password` (not `User-Password`), requiring explicit config to copy it
- **cdk-cli requires sudoers config** — FreeRADIUS runs as `freerad`, needs NOPASSWD sudo to run `cdk-cli` as `cashu-wallet` user

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
