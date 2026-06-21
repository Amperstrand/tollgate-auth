# Operator Deployment Guide

## Overview

This system provides Cashu ecash tokens for WiFi, SSH, and VPN access. Users pay with Cashu tokens from test mints (testnut.cashu.exchange). Each AP has a unique Nostr identity for revenue attribution. Current implementation supports bootstrap-only payments, single token per session.

## Architecture (Current State)

```
Client (phone) → WiFi AP (WPA2-Enterprise) → FreeRADIUS (UDP 1812)
    → tollgate-shim (exec) → tollgate-daemon (Unix socket + HTTP :8091)
    → auth.ProcessAuth (decode → verify → redeem → accept/reject)
    → cdk-cli (Cashu token redemption to operator wallet)

Client (laptop) → WireGuard (:51820) → daemon /v1/wg/connect
    → peer allocation + auth.ProcessAuth

Client (terminal) → SSH (:2222) → chroot jail + timer
```

**Components and ports:**

- FreeRADIUS: UDP 1812 (auth), TCP 2083 (RadSec), UDP 1813 (accounting)
- tollgate-daemon: HTTP :8091, Unix socket /run/tollgate/tollgate.sock
- tollgate-shim: called by FreeRADIUS exec (no listener)
- SSH: port 2222
- WireGuard: UDP 51820

## Gateway Setup

Fresh Debian 12 VPS installation.

**1. Prerequisites:**

Go 1.25+, cdk-cli, FreeRADIUS 3.x, wireguard-tools

```bash
apt-get update
apt-get install -y golang freeradius wireguard-tools
```

**2. Clone and build all binaries:**

```bash
git clone https://github.com/Amperstrand/tollgate-auth.git
cd tollgate-auth

make build-linux
make build-radius
make build-daemon
make build-shim
```

**3. Create cashu-wallet user and set up cdk-cli:**

```bash
curl -sL -o /usr/local/bin/cdk-cli \
  https://github.com/cashubtc/cdk/releases/latest/download/cdk-cli-$(uname -m)
chmod +x /usr/local/bin/cdk-cli

useradd -r -m -d /var/lib/cashu-wallet -s /usr/sbin/nologin cashu-wallet
chmod 700 /var/lib/cashu-wallet
sudo -u cashu-wallet cdk-cli --work-dir /var/lib/cashu-wallet balance
```

**4. Create /etc/tollgate/secrets.env with operator identity:**

```bash
mkdir -p /etc/tollgate
cat > /etc/tollgate/secrets.env << 'EOF'
TOLLGATE_OPERATOR_NPUB=npub1yourpublickeyhere
EOF
chmod 600 /etc/tollgate/secrets.env
```

**5. Deploy daemon:**

```bash
mkdir -p /opt/cashu-tollgate
cp bin/tollgate-daemon /usr/local/bin/
cp config/systemd/tollgate-daemon.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now tollgate-daemon.service
```

**6. Deploy shim:**

```bash
cp bin/tollgate-shim /usr/local/bin/
chmod +x /usr/local/bin/tollgate-shim
```

**7. Configure FreeRADIUS exec module:**

```bash
cp config/freeradius/mods-available/cashu-exec /etc/freeradius/3.0/mods-available/
ln -s /etc/freeradius/3.0/mods-available/cashu-exec /etc/freeradius/3.0/mods-enabled/
```

**8. Configure inner tunnel for EAP-TTLS+PAP:**

```bash
cp config/freeradius/sites-available/inner-tunnel /etc/freeradius/3.0/sites-available/
ln -s /etc/freeradius/3.0/sites-available/inner-tunnel /etc/freeradius/3.0/sites-enabled/
```

**9. Set up EAP-TTLS authentication:**

```bash
cp config/freeradius/mods-available/eap /etc/freeradius/3.0/mods-available/
ln -s /etc/freeradius/3.0/mods-available/eap /etc/freeradius/3.0/mods-enabled/
```

**10. Configure FreeRADIUS to allow all clients (for testing):**

```bash
cp config/freeradius/clients.conf /etc/freeradius/3.0/clients.conf
```

**11. Start FreeRADIUS and verify with radtest:**

```bash
systemctl enable --now freeradius.service
radtest "cashuBo2FteB5odHRwczovL3Rlc3RudXQuY2FzaHUuZXhjaGFuZ2VhdWNo..." "anything" 127.0.0.1 0 tollgate
```

**12. Set up RadSec (TCP 2083) with Let's Encrypt cert:**

```bash
apt-get install -y certbot
certbot certonly --standalone -d your-gateway-domain.com
cp /etc/letsencrypt/live/your-gateway-domain.com/fullchain.pem /etc/freeradius/3.0/certs/server.crt
cp /etc/letsencrypt/live/your-gateway-domain.com/privkey.pem /etc/freeradius/3.0/certs/server.key
chown freerad:freerad /etc/freeradius/3.0/certs/*.pem
chmod 640 /etc/freeradius/3.0/certs/*.pem
```

Update `/etc/freeradius/3.0/radsec.conf` with your domain paths.

## AP Setup with conwrt

How to flash an AP with Nostr identity for revenue attribution.

**1. Install conwrt:**

```bash
pip install conwrt
```

Or clone from GitHub and install manually.

**2. Generate Nostr keypair:**

```bash
python3 scripts/generate_nostr_keypair.py
```

This outputs:
- nsec1... (private key, keep secret)
- npub1... (public key, AP identity)

**3. Add to config.toml:**

```toml
[use_cases.ap-nostr-id]
npub = "npub1abc..."  # from keypair generation
nsec = "nsec1xyz..."  # from keypair generation
```

**4. Flash the AP:**

```bash
python3 scripts/conwrt.py flash --model <model> --method recovery-http
```

Replace `<model>` with your AP model (e.g., tl-wr841n-v13).

**5. Verify AP configuration:**

After flashing, the AP will have:
- `/etc/tollgate/ap-nsec` containing the private key (mode 600)
- `/etc/tollgate/ap-npub` containing the public key
- All wifi-iface sections with `nas_identifier` set to the npub
- RADIUS Access-Requests will include NAS-Identifier: npub1abc...

## Payment Flow (How It Works Today)

Current implementation is bootstrap-only.

**1. Acquisition:**

Client gets a Cashu token from faucet, friend, or any Cashu wallet.

**2. Connection:**

Client connects to WiFi, enters token as password (EAP-TTLS+PAP).

**3. Verification:**

Daemon decodes token (V3 JSON or V4 CBOR). Checks mint allowlist (test mints only). Verifies with mint via /v1/checkstate. Redeems via cdk-cli (NUT-03 swap). Token is spent, new tokens minted to operator wallet.

**4. Session:**

Daemon returns Access-Accept with Session-Timeout (1 sat = 60 seconds).

**5. Timeout:**

When Session-Timeout expires, AP disconnects client.

**6. Reconnection:**

Client needs a NEW token to reconnect.

**Important limitations:**

- No in-session top-up
- No recurring billing
- No bandwidth-based pricing
- Token is consumed on use (spent via NUT-03 swap)
- LNURLw codes are demo-only (grant 1 hour, Lightning payment not claimed)

## Revenue Attribution (Per-AP Identification)

Each AP has a unique Nostr npub in its hostapd nas_identifier.

**1. AP identification:**

Each AP has a unique Nostr npub in its hostapd nas_identifier. RADIUS Access-Request includes NAS-Identifier: npub1abc...

**2. Operator resolution:**

Daemon's operator registry resolves the npub. Checks registered operators first (config file match). Falls back to ad-hoc resolution (Source="nas-id-self") for unknown npubs.

**3. Ledger recording:**

Ledger records nas_id (the npub) for every auth event.

**4. Settlement:**

tollgate-settle groups ledger by npub, calculates each AP's share. Settlement sent via NIP-17 gift-wrap DM to each AP's npub.

**Revenue model:**

- All tokens redeem to the gateway operator's wallet (single cdk-cli wallet)
- Settlement is a server-side calculation. APs trust the gateway to report honestly
- Revenue split is configurable (e.g., 80% AP, 20% gateway)
- For trustless operation, each AP would need its own wallet (future work)

## Upgrade Path (Beyond Bootstrap)

When bootstrap tokens aren't enough, three options.

**Option A: Captive Portal + RADIUS CoA**

Client gets bootstrap access via Cashu token. While connected, browser hits captive portal (OpenNDS on the AP). Portal shows remaining time, offers top-up via Cashu or Lightning. Daemon extends session via RADIUS CoA (RFC 5176) without disconnecting.

Requires: OpenNDS/CoovaChilli on AP, HTTP payment API on daemon, CoA support in FreeRADIUS.

**Option B: Spilman Payment Channel**

Bootstrap token gets client online. Client opens Lightning payment channel with gateway. Per-second micropayments stream over the channel. No Session-Timeout. Connection lasts as long as payments flow. Payment moves from RADIUS to HTTP (no 253-byte attribute limit).

Requires: LND or CLN node, Spilman channel protocol.

**Option C: L402-over-RADIUS (Lightning Preimage)**

Phase 1: Client sends "request-invoice" as password, gets BOLT11 invoice in Reply-Message. Phase 2: Client pays invoice, returns 64-char hex preimage as password. Daemon verifies sha256(preimage) == payment_hash → Access-Accept. 64 hex chars fits trivially in RADIUS (vs 230-byte Cashu token). No external mint calls, pure local verification.

Requires: LND or CLN node with hold invoice support.

## Limitations and Intentional Design Decisions

**Not implemented:**

- In-session top-up (requires CoA or captive portal)
- Multi-proof token support (tokens >64 sat exceed 253-byte RADIUS limit)
- Per-AP wallet redemption (all tokens go to gateway wallet)
- Automated settlement execution (settlement tool exists but requires manual trigger)
- Rate limiting

**Intentional design decisions:**

- clients.conf accepts 0.0.0.0/0, any AP can authenticate without pre-registration
- No rate limiting, frictionless access, test tokens are free
- LNURLw codes grant access without claiming the Lightning payment (demo feature)
- Test mints only (real-money support requires changing the mint allowlist regex)

**Security considerations:**

- Guests on SSH get arbitrary code execution in a chroot jail
- No resource limits on guest processes
- RADIUS shared secret is "tollgate" (change for production)
- The daemon HTTP endpoint has no authentication (bound to localhost)

## Operational Tasks

**Check daemon health:**

```bash
curl http://localhost:8091/healthz
curl http://localhost:8091/readyz
```

**Check wallet balance:**

```bash
sudo -u cashu-wallet cdk-cli --work-dir /var/lib/cashu-wallet balance
```

**View recent auth events from ledger:**

```bash
tail -100 /opt/cashu-tollgate/ledger.jsonl | jq 'select(.event_type=="auth_accept")'
```

**Count revenue per AP (by npub):**

```bash
cat /opt/cashu-tollgate/ledger.jsonl | jq -r 'select(.nas_id) | .nas_id' | sort | uniq -c | sort -rn
```

**Run settlement (dry run first):**

```bash
tollgate-settle --dry-run
tollgate-settle
```

**Add a new AP to the operator registry:**

Edit `/etc/tollgate/operators.json`:

```json
{"operators":[{"id":"ap-lobby","payout_npub":"npub1abc...","match":{"nas_id":"npub1abc..."}}]}
```

## Troubleshooting

**radtest returns Access-Reject:**

Token may be spent or from wrong mint. Check daemon logs for detailed rejection reason.

**daemon unavailable:**

Check systemctl status tollgate-daemon. Verify socket at /run/tollgate/tollgate.sock.

**FreeRADIUS can't find shim:**

Verify /usr/local/bin/tollgate-shim exists and is executable.

**AP not sending npub:**

Check hostapd config has nas_identifier set. Verify conwrt flashed successfully.

**Cashu token too long for RADIUS:**

Strip DLEQ proof (230 bytes, fits in single attribute).