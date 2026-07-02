# OCPI testing with OCPPLab and Cashu testnuts

End-to-end testing guide for `tollgate-auth-ocpi` — the OCPI 2.2.1 eMSP receiver
that gates EV charging sessions behind Cashu ecash payments.

## Architecture under test

```
        OCPPLab (https://app.ocpplab.com)
        ├─ CPO simulator
        └─ Virtual chargers (Alfen, ABB, Easee, etc.)
                    │
                    │ OCPI 2.2.1
                    ▼
        ┌───────────────────────────────────────────┐
        │ tollgate-auth-ocpi (Go, :8093)            │
        │ ├─ /ocpi/emsp/2.2.1/versions              │
        │ ├─ /ocpi/emsp/2.2.1/credentials           │
        │ ├─ /ocpi/emsp/2.2.1/tokens/{uid}/authorize│ ──┐
        │ ├─ /ocpi/emsp/2.2.1/sessions              │   │
        │ ├─ /ocpi/emsp/2.2.1/cdrs                  │   │ calls
        │ ├─ /ocpi/emsp/2.2.1/commands/{name}/{id}  │   │
        │ └─ / (dashboard)                          │   ▼
        └────────────┬─────────────────────────────┘  auth.ProcessAuth
                     │                                     │
                     │ NUT-07 /v1/checkstate               │
                     ▼                                     ▼
              Cashu mint                            (shared with
              testnut.cashu.space                   RADIUS / SSH paths)
```

The Authorize endpoint runs the same `internal/auth.ProcessAuth` pipeline as
`tollgate-auth-radius` and `tollgate-auth-ssh`. Cashu decoding, NUT-12
hash_to_curve, NUT-07 checkstate, and replay protection are all shared.

## Prerequisites

### Local (PoC mode, no value transfer)

```bash
# 1. Go 1.25+
go version

# 2. Cashu Python CLI (for minting test tokens)
pip install cashu

# 3. Test mint reachable
curl -s https://testnut.cashu.space/v1/info | jq -r .name
# → "Testnut mint"

# 4. Operator nsec (any valid Nostr private key — used only for HMAC key
#    derivation in the PoC; the value doesn't affect Cashu verification).
#    The test value used in our smoke tests:
export TOLLGATE_OPERATOR_NSEC=nsec180cvv07tjdrrgpa0j7j7tmnyl2yr6yr7l8j4s3evf6u64th6gkwsgyumg0
```

### Production (real value transfer)

Add to the above:
- `cdk-cli` installed at `/usr/local/bin/cdk-cli` (download from
  https://github.com/cashubtc/cdk/releases — Linux only, build from source on
  macOS via `cargo install cdk-cli`)
- A real Cashu mint (or testnut, which auto-pays Lightning invoices via
  FakeWallet so it's fine for value-transfer PoCs too)
- A funded wallet directory at `$TOLLGATE_WALLET_DIR`

## Running the OCPI server locally

```bash
cd ~/src/tollgate-ssh
make build-ocpi-local

# Run in verify-only PoC mode (no cdk-cli required)
TOLLGATE_BASE_DIR=/tmp/tollgate-ocpi-test \
TOLLGATE_WALLET_DIR=/tmp/tollgate-ocpi-test/wallet \
TOLLGATE_AUTH_MODE=local \
TOLLGATE_OPERATOR_NSEC=$TOLLGATE_OPERATOR_NSEC \
TOLLGATE_OCPI_PUBLIC_URL=http://localhost:8093 \
./tollgate-auth-ocpi
```

Dashboard: http://localhost:8093/

## End-to-end test against testnut.cashu.space

### 1. Mint a Cashu test token

```bash
# Create a test wallet pointing at testnut
cashu -h https://testnut.cashu.space -w ocpi-test invoice 8
# → "Invoice paid. Balance: 8 sat"  (FakeWallet auto-pays all invoices)

# Send tokens as a portable Cashu V4 string
cashu -h https://testnut.cashu.space -w ocpi-test send 8 -v 2>&1 \
  | grep -o 'cashuB[A-Za-z0-9_-]*' | tail -1 > /tmp/token.txt

cat /tmp/token.txt
# → cashuBo2F0gaJhaUgBhCN...
```

### 2. Issue an OCPI token UID via the dashboard

Either paste the token into the dashboard form at http://localhost:8093/, or
POST directly:

```bash
TOKEN=$(cat /tmp/token.txt)
curl -s -X POST http://localhost:8093/api/prepay \
  -H "Content-Type: application/json" \
  -d "{\"cashu_token\":\"$TOKEN\"}" | jq

# Expected response:
# {
#   "status": 1000,
#   "data": {
#     "uid": "OCPI-390888e3",
#     "cashu_token_hash": "390888e3ba94868c...",
#     "allotment_sec": 80,
#     "amount_sat": 8,
#     "mint_url": "https://testnut.cashu.space",
#     "contract_id": "NPC-OCPI-390888e3",
#     ...
#   }
# }
```

The server verifies the token against `testnut.cashu.space/v1/checkstate`
(NUT-07) and stores the prepay record keyed by UID.

### 3. Simulate a CPO Authorize call

This is the exact request OCPPLab (or any OCPI 2.2.1 CPO) would send when a
driver plugs in with this token UID:

```bash
curl -s -X POST http://localhost:8093/ocpi/emsp/2.2.1/tokens/OCPI-390888e3/authorize | jq

# Expected:
# {
#   "status": 1000,
#   "data": {
#     "allowed": "ALLOWED",
#     "authorization_reference": "390888e3ba94868c",
#     "info_url": "http://localhost:8093/"
#   }
# }
```

`allowed: ALLOWED` means the charger would start. `DISALLOWED` means payment
missing, expired, or already used.

### 4. Simulate end of session (CDR)

```bash
curl -s -X POST http://localhost:8093/ocpi/emsp/2.2.1/cdrs \
  -H "Content-Type: application/json" \
  -d '{
    "id": "cdr-test-001",
    "auth_id": "OCPI-390888e3",
    "kwh": 5.5,
    "total_cost": 0.001,
    "currency": "BTC",
    "location_id": "loc-test",
    "start_date": "2026-07-02T15:00:00Z",
    "stop_date": "2026-07-02T15:05:00Z",
    "last_updated": "2026-07-02T15:05:00Z"
  }' | jq
# → {"status": 1000, ...}
```

### 5. Inspect the dashboard state

Open http://localhost:8093/ — the prepay token, authorize log, and CDR all
appear in their respective panels.

`GET /api/snapshot` returns the same data as JSON for integration with external
dashboards or tests.

## Connecting OCPPLab

Once the local flow works, point OCPPLab at the running server.

### Prerequisites for OCPPLab connectivity

The OCPI server needs a publicly-reachable HTTPS URL. Two options:

**A) Deploy to a VPS** (recommended for OCPPLab testing):

The repo ships with all deploy artifacts staged:

| Artifact | Path | Purpose |
|---|---|---|
| Systemd unit | `config/systemd/tollgate-auth-ocpi.service` | Runs on `:8093`, advertises `https://ocpi.nodns.shop` |
| Caddy site | `config/caddy/ocpi.conf` | TLS termination + reverse proxy to `:8093` |
| Makefile target | `deploy-ocpi` | Cross-compiles linux/amd64, scp's all three artifacts, reloads services |

**Deploy checklist (user-supplied prerequisites)**:

1. **Operator nsec** must be in `/etc/tollgate/secrets.env` on the VPS:
   ```bash
   ssh root@nodns.shop 'echo "TOLLGATE_OPERATOR_NSEC=nsec1..." >> /etc/tollgate/secrets.env; chmod 600 /etc/tollgate/secrets.env'
   ```
   (Use your real Nostr private key — not the test value from this repo.)

2. **DNS**: `ocpi.nodns.shop` must resolve to the VPS. Add an A record (managed
   via your DNS provider). Caddy obtains the TLS cert automatically via
   Let's Encrypt HTTP-01 when Caddy reloads.

3. **Run the deploy** (one command):
   ```bash
   cd ~/src/tollgate-ssh
   make deploy-ocpi
   # → CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build
   # → scp binary + systemd unit + caddy config to root@nodns.shop
   # → systemctl daemon-reload + enable + restart tollgate-auth-ocpi
   # → systemctl reload caddy
   ```

4. **Verify**:
   ```bash
   curl -s https://ocpi.nodns.shop/healthz                              # → {"status":"ok"}
   curl -s https://ocpi.nodns.shop/ocpi/versions | jq -r .data.versions # → ["2.2.1"]
   ```

5. **Open the dashboard**: https://ocpi.nodns.shop/

For PoC testing without deploying to a VPS, the Caddy config and systemd unit
also work locally — just adjust `TOLLGATE_OCPI_PUBLIC_URL` and skip the DNS step.

**B) Use a tunnel** (for local-dev-with-OCPPLab):

```bash
cloudflared tunnel --url http://localhost:8093
# → creates https://random-words-xxxx.trycloudflare.com
# Use that URL as TOLLGATE_OCPI_PUBLIC_URL when starting the server.
```

### OCPPLab setup steps

1. Log in at https://app.ocpplab.com (org: `tollgate`, ID
   `2b3347d9-b401-4224-95e9-17291f7b6f3c`)
2. Navigate to OCPI roaming testing → "Connect new eMSP"
3. Enter the versions URL of your running tollgate-auth-ocpi:
   ```
   https://<your-host>/ocpi/versions
   ```
4. Set party identity to match the server config:
   - Country code: `NO` (or whatever `TOLLGATE_OCPI_COUNTRY` is set to)
   - Party ID: `TGA` (or whatever `TOLLGATE_OCPI_PARTY` is set to)
5. OCPPLab initiates the credentials handshake:
   - GET `/ocpi/versions` → server returns `[{version: "2.2.1", url: "..."}]`
   - GET `/ocpi/emsp/2.2.1/version_details` → server returns module endpoints
   - POST `/ocpi/emsp/2.2.1/credentials` with Token A in Authorization header
     → server responds with Token C
6. After successful handshake, the dashboard at `/` shows the peer as connected.
7. Use OCPPLab's "Send authorize" tool to POST to
   `/ocpi/emsp/2.2.1/tokens/{uid}/authorize` with one of your prepay UIDs.
8. OCPPLab's virtual chargers can now start sessions using tokens issued via
   the dashboard.

## Modes

| Mode | Flag | Behavior |
|---|---|---|
| Verify-only (PoC) | `TOLLGATE_OCPI_REDEEM=false` (default) | Verifies tokens against mint via NUT-07 checkstate but does not claim them. Same token can be reused until replay-guard blocks it. Use this for demos without cdk-cli. |
| Full redeem | `TOLLGATE_OCPI_REDEEM=true` + `cdk-cli` installed | Verifies AND redeems (NUT-03 swap into the configured wallet). Token cannot be reused. Required for production. |
| Local | `TOLLGATE_AUTH_MODE=local` | Server verifies directly with the mint via HTTP. |
| Delegated | `TOLLGATE_AUTH_MODE=delegated` | Server forwards to tollgate-rs (running at `TOLLGATE_SESSIOND_URL`, default `http://127.0.0.1:2121`) which handles verification and metering. Production path. |

## Troubleshooting

### `cashu verify failed: Rejected: mint verification failed — Mint error: HTTP 422`

Server is posting `{proofs:[{secret}]}` to `/v1/checkstate` instead of the
NUT-07 spec format `{Ys:[...]}`. This was a pre-existing bug fixed in
`internal/cashu/hashcurve.go`. If you see this, you have an old build — rebuild.

### `cashu verify failed: Rejected: token redemption failed`

You're in full-redeem mode (`TOLLGATE_OCPI_REDEEM=true`) without `cdk-cli`
installed. Either:
- Install cdk-cli at `/usr/local/bin/cdk-cli`, or
- Set `TOLLGATE_OCPI_REDEEM=false` (default) for verify-only PoC mode

### `cashu verify failed: Rejected: token already used`

The token was already spent at the mint (or already redeemed by a previous
test run). Mint a fresh one:

```bash
cashu -h https://testnut.cashu.space -w freshwallet invoice 8
cashu -h https://testnut.cashu.space -w freshwallet send 8 -v | grep -o 'cashuB[A-Za-z0-9_-]*' | tail -1 > /tmp/token.txt
```

### OCPPLab handshake fails

- Confirm the server is reachable from the public internet (use cloudflared
  or deploy to a VPS).
- Confirm TLS is valid — OCPPLab rejects self-signed certs.
- Check `TOLLGATE_OCPI_COUNTRY` and `TOLLGATE_OCPI_PARTY` match what you
  configured in OCPPLab.
- Check the server log at `/var/log/journalctl -u tollgate-auth-ocpi` for
  the incoming POST.

### Authorize returns `DISALLOWED` unexpectedly

- The prepay record's `authorized_at` may be stale (>2 min old). Re-issue.
- The prepay record may be marked `used=true` (CDR arrived). Mint a new token.
- The token's proofs may now be SPENT at the mint (someone else redeemed them).

## Reference: env vars

| Variable | Default | Purpose |
|---|---|---|
| `TOLLGATE_OCPI_ADDR` | `:8093` | Listen address |
| `TOLLGATE_OCPI_PUBLIC_URL` | `http://localhost:8093` | Externally-reachable URL (advertised in OCPI responses) |
| `TOLLGATE_OCPI_COUNTRY` | `NO` | ISO 3166-1 alpha-2 |
| `TOLLGATE_OCPI_PARTY` | `TGA` | eMA ID party (4 chars) |
| `TOLLGATE_OCPI_TOKEN_A` | (empty) | Bootstrap token; empty accepts any (PoC) |
| `TOLLGATE_OCPI_DASH_URL` | = `TOLLGATE_OCPI_PUBLIC_URL` | Dashboard URL shown to drivers |
| `TOLLGATE_OCPI_REDEEM` | `false` | Set `true` to enable cdk-cli redemption |
| `TOLLGATE_BASE_DIR` | `/opt/tollgate-auth` | State directory |
| `TOLLGATE_WALLET_DIR` | `/var/lib/cashu-wallet` | cdk-cli wallet dir (when redeem enabled) |
| `TOLLGATE_AUTH_MODE` | `delegated` | `local` (direct mint verify) or `delegated` (via tollgate-rs) |
| `TOLLGATE_SESSIOND_URL` | `http://127.0.0.1:2121` | tollgate-rs URL (delegated mode) |
| `TOLLGATE_OPERATOR_NSEC` | (required) | Operator Nostr private key for HMAC key derivation |
| `TOLLGATE_LEDGER_PATH` | `$BASE_DIR/ocpi-ledger.jsonl` | Append-only audit log |
