# Wattif OCPI Project — Complete System Guide

> Last updated: 2026-07-03. Private repo: https://github.com/Amperstrand/wattif-ocpi

## All URLs

| URL | What | Auth |
|---|---|---|
| https://ocpi.nodns.shop | eMSP dashboard — virtual charger, Cashu payment, CPO simulator | None (public) |
| https://ocpi.nodns.shop/about | Landing page — architecture explanation | None (public) |
| https://ocpi.nodns.shop/ocpi/versions | OCPI 2.2.1 protocol endpoint | None |
| https://opencpo.nodns.shop | OpenCPO admin dashboard | admin@tollgate.dev / TollgateDemo2026! |
| https://farm.nodns.shop | OpenCPO virtual charger farm (10 chargers) | None |

## All Credentials

| Credential | Value | Where |
|---|---|---|
| OpenCPO admin login | admin@tollgate.dev / TollgateDemo2026! | https://opencpo.nodns.shop |
| OpenCPO management API key | Read from VPS: `grep MANAGEMENT_API_KEY /opt/opencpo/.env` | VPS only |
| Cashu wallet balance | 2377 sat (testnut.cashu.exchange) | `/var/lib/cashu-wallet/cdk-cli.sqlite` |
| EUR mint URL | http://127.0.0.1:3340 (localhost only) | VPS only |
| EUR mint pubkey | 0375284bf7a64a6d5733e88fadb9054debba6f651c7188a98cbf3cb8a01616bf2c | — |
| SSH access | root@nodns.shop port 22 | Your SSH key |
| Private repo | https://github.com/Amperstrand/wattif-ocpi | GitHub auth |

**Do NOT commit secrets to git.** All secrets live in `/etc/tollgate/secrets.env` on the VPS.

---

## All Moving Parts

### On nodns.shop VPS (46.224.104.12)

```
┌──────────────────────────────────────────────────────────────────┐
│  nodns.shop VPS — 2 CPU, 3.7GB RAM, Debian 12                   │
│                                                                  │
│  ┌──────────────────────────────────────────────────────┐       │
│  │  Caddy (:80/:443) — TLS termination                   │       │
│  │  Routes: ocpi.nodns.shop → :8093                     │       │
│  │          opencpo.nodns.shop → :8080                  │       │
│  │          farm.nodns.shop → :8087                     │       │
│  │          ssh.cashu.email → :8092                     │       │
│  │          faucet.cashu.email → static                 │       │
│  │          *.nodns.shop → on-demand TLS redirect       │       │
│  └──────────────────────┬───────────────────────────────┘       │
│                         │                                        │
│  ┌──────────────────────▼─────────────────────────────────┐     │
│  │  tollgate-auth-ocpi (:8093, 127.0.0.1)                 │     │
│  │  User: tollgate | Systemd hardened                     │     │
│  │  ├─ OCPI 2.2.1 eMSP Receiver (5 modules)              │     │
│  │  ├─ Virtual charger (AVAILABLE/CHARGING/stop→CDR)      │     │
│  │  ├─ CPO Simulator panel (real OCPI HTTP messages)     │     │
│  │  ├─ Dashboard (html/template + HTMX)                   │     │
│  │  ├─ Landing page at /about                             │     │
│  │  ├─ Ledger (JSONL: auth_accept + accounting_stop)      │     │
│  │  ├─ File persistence (CDRs, prepay, charger state)     │     │
│  │  └─ auth.ProcessAuth → Cashu verify via hashcurve.go  │     │
│  └──────────────────────┬─────────────────────────────────┘     │
│                         │                                        │
│  ┌──────────────────────▼─────────────────────────────────┐     │
│  │  tollgate-csms (:8887, 0.0.0.0)                        │     │
│  │  User: tollgate | OCPP 1.6 WebSocket server             │     │
│  │  Forwards Authorize → eMSP via OCPI                    │     │
│  │  Pushes sessions + CDRs to eMSP                        │     │
│  └────────────────────────────────────────────────────────┘     │
│                                                                  │
│  ┌────────────────────────────────────────────────────────┐     │
│  │  tollgate-eur-mint (:3340, 127.0.0.1)                  │     │
│  │  User: tollgate | cdk-mintd FakeWallet                  │     │
│  │  EUR gift card mint — prepaid charging credit          │     │
│  └────────────────────────────────────────────────────────┘     │
│                                                                  │
│  ┌────────────────────────────────────────────────────────┐     │
│  │  tollgate-auth-ssh (:2222) | root (documented)         │     │
│  │  Cashu-gated SSH — paste token as username             │     │
│  └────────────────────────────────────────────────────────┘     │
│                                                                  │
│  ┌────────────────────────────────────────────────────────┐     │
│  │  tollgate-daemon (:8091) | root                         │     │
│  │  RADIUS + WireGuard auth (existing, needs hardening)    │     │
│  └────────────────────────────────────────────────────────┘     │
│                                                                  │
│  ┌────────────────────────────────────────────────────────┐     │
│  │  tollgate-net (:2121) | root                            │     │
│  │  Cashu wallet CDK (Rust) — delegated mode backend       │     │
│  └────────────────────────────────────────────────────────┘     │
│                                                                  │
│  ┌────────────────────────────────────────────────────────┐     │
│  │  OpenCPO Docker (7 containers, all 127.0.0.1)           │     │
│  │  ├─ ocpp-core (:8000 CSMS + :9100/:9201 OCPP WS)       │     │
│  │  ├─ cpo-admin (:8080 React dashboard)                  │     │
│  │  ├─ charge-app (:8003 driver PWA)                      │     │
│  │  ├─ charger-farm (:8087 10 virtual chargers)           │     │
│  │  ├─ compliance-tester (:18090)                        │     │
│  │  ├─ postgres (:5432 TimescaleDB)                      │     │
│  │  └─ redis (:6379)                                     │     │
│  │  OCPI partner: NO/TGA → our eMSP (health: ok)          │     │
│  └────────────────────────────────────────────────────────┘     │
│                                                                  │
│  ┌────────────────────────────────────────────────────────┐     │
│  │  Cashu wallet: /var/lib/cashu-wallet/                   │     │
│  │  cdk-cli.sqlite (2377 sat from testnut.cashu.exchange)  │     │
│  │  Groups: cashu-wallet:tollgate (shared access)          │     │
│  └────────────────────────────────────────────────────────┘     │
│                                                                  │
│  ┌────────────────────────────────────────────────────────┐     │
│  │  Other services                                         │     │
│  │  FreeRADIUS (:1812/:2083) | Knot DNS (:53)             │     │
│  │  nodns-bot (:9090) | webssh (:8092)                    │     │
│  │  fips (:8443) | WireGuard (:51820)                     │     │
│  └────────────────────────────────────────────────────────┘     │
└──────────────────────────────────────────────────────────────────┘
```

### On Cloudflare

| Service | Domain | What |
|---|---|---|
| lnforward | lnurl.psbt.me | Lightning Address → Cashu ecash |
| plugpay (Hermes) | hermes.silent.energy | IoT device control (PlugPay/Easee) |

### On Local Machine

| Project | Path | What |
|---|---|---|
| tollgate-ssh (tollgate-auth) | ~/src/tollgate-ssh | Main Go repo (this codebase) |
| wattif-ocpi | GitHub private | Push target for this project |
| silent.energy | ~/src/silent.energy | Cashu EV charging (Cloudflare Worker) |
| plugpay (Hermes) | ~/plugpay | IoT device control plane |
| hackathon-tooling | ~/src/hackathon-tooling | Security best practices guide |

---

## How Cashu Collection Works

### Current flow (tollgate-auth-ocpi)

```
1. Driver pastes Cashu token (cashuB...) into dashboard
2. POST /api/charger/start → auth.ProcessAuth
3. ProcessAuth pipeline:
   a. Decode token (CBOR → proofs + mint URL)
   b. Compute Y = hash_to_curve(secret) per NUT-12
   c. POST mint/v1/checkstate {Ys: [...]} → UNSPENT
   d. [If TOLLGATE_OCPI_REDEEM=true]:
      cdk-cli receive --work-dir /var/lib/cashu-wallet <token>
      → NUT-03 swap: proofs → SPENT at mint, new proofs → our wallet
   e. [If redemption fails]: log warning, accept with replay guard only
   f. Record auth_accept in ledger JSONL
4. Charger state → CHARGING
5. Driver clicks Stop → CDR generated → accounting_stop in ledger
```

### What "collecting Cashu" means technically

When `cdk-cli receive` succeeds:
1. Our wallet sends the user's proofs to the mint
2. Mint marks them SPENT (double-spend prevention)
3. Mint signs NEW blinded messages for us
4. We unblind → new proofs in our wallet SQLite
5. Our balance increases

The wallet at `/var/lib/cashu-wallet/cdk-cli.sqlite` holds all collected proofs.
Check balance: `cdk-cli --work-dir /var/lib/cashu-wallet balance`

### Accounting trail

Every charge produces a ledger entry:
```json
{
  "timestamp": "2026-07-03T12:16:44Z",
  "event_type": "auth_accept",
  "mac": "sess-310ddd1f",
  "payment_type": "cashu",
  "amount_sat": 3,
  "mint_url": "https://testnut.cashu.space",
  "token_hash": "310ddd1f...",
  "nas_id": "virtual-charger-001",
  "session_class": "ocpi"
}
```

And a CDR accounting entry with kWh/cost metadata.

### Replay protection

Three layers:
1. **Replay guard** (always on): SHA256(token) in `ocpi-spent.txt`. Same token can't be used twice.
2. **Mint checkstate** (always on): verifies proofs are UNSPENT at mint.
3. **Cryptographic redemption** (TOLLGATE_OCPI_REDEEM=true): cdk-cli swaps proofs, making them permanently SPENT at the mint. Currently fails due to cdk-cli 0.16.0 keyset mismatch (Issue #14). Falls back to layers 1+2.

---

## Integration with silent.energy

### What silent.energy does today

silent.energy is a **Cashu-powered EV charging platform** running on Cloudflare Workers:

- **Live**: easee.silent.energy (staging) + dev.silent.energy (dev)
- **Charger control**: Easee chargers via API + webhook chargers
- **Payment**: NUT-18/NUT-24 — HTTP 402 → X-Cashu header → CocoWalletDO receives proofs
- **Session management**: ChargerSessionsDO (Durable Object) tracks prepaid sats, time/kWh consumption
- **Frontend**: Svelte dashboard with integrated browser wallet
- **Wallet**: CocoWalletDO (Durable Object) holds server-side Cashu proofs

### How silent.energy collects Cashu today

```
1. Driver visits easee.silent.energy
2. Clicks "Start charging"
3. Server returns 402 + X-Cashu payment request (NUT-18)
4. Driver pays from Cashu wallet (X-Cashu header)
5. CocoWalletDO.receive() — NUT-03 swap at mint
6. ChargerSessionsDO.start() — prepaid balance set
7. Provider.onStart() → Easee API or webhook → charger activates
8. Cron tick → consume sats over time → stop when balance depleted
```

### Integration options

**Option A: silent.energy as OCPI eMSP frontend, tollgate-auth as backend**

```
Driver → silent.energy UI → POST /api/webhook-chargers/:id/start
  → silent.energy creates NUT-18 payment request
  → Driver pays Cashu
  → silent.energy receives in CocoWalletDO
  → silent.energy calls tollgate-auth-ocpi OCPI Authorize
    POST https://ocpi.nodns.shop/ocpi/emsp/2.2.1/tokens/{uid}/authorize
  → tollgate-auth returns ALLOWED
  → silent.energy starts charger via Easee API
```

Benefit: silent.energy gets OCPI protocol compliance. Can roam to any CPO.

**Option B: tollgate-auth replaces silent.energy's payment layer**

```
Driver → silent.energy UI → POST /api/webhook-chargers/:id/start
  → Redirect to tollgate-auth-ocpi dashboard for payment
  → Driver pays Cashu at ocpi.nodns.shop
  → tollgate-auth returns OCPI ALLOWED
  → silent.energy starts charger
```

Benefit: single payment backend. silent.energy focuses on charger UX.

**Option C: silent.energy accepts EUR mint tokens**

```
Driver → silent.energy UI → paste EUR Cashu token from tollgate-eur-mint
  → silent.energy's CocoWalletDO receives EUR token
  → silent.energy verifies against tollgate-eur-mint (:3340)
  → Charger starts
```

Benefit: EUR-denominated gift cards. No Bitcoin price exposure. Drivers buy
EUR credit via Vipps, receive EUR Cashu, spend at any connected charger
(silent.energy Easee chargers OR OCPI-roamed Wattif chargers).

**Option D: silent.energy uses tollgate-auth CSMS**

```
Easee charger → OCPP WebSocket → tollgate-csms (:8887)
  → CSMS calls tollgate-auth-ocpi Authorize
  → Authorize checks Cashu payment → ALLOWED
  → CSMS sends StartTransaction to charger
  → MeterValues flow → CDR generated
  → CDR pushed to silent.energy via webhook
```

Benefit: silent.energy doesn't need its own OCPP/OCPI stack. tollgate-auth
handles protocol, payment, session tracking. silent.energy handles UX and
charger configuration.

### Recommended integration: Option C (EUR mint)

Simplest path to a unified product:

1. **tollgate-eur-mint** runs as the gift card issuer (already deployed on :3340)
2. **silent.energy** adds `http://127.0.0.1:3340` to its accepted mints
3. **tollgate-auth-ocpi** accepts EUR tokens for OCPI-roamed chargers
4. **Drivers** buy EUR credit via a buy page, spend at any charger in the network
5. **Settlement**: EUR tokens redeemed by either silent.energy's CocoWalletDO
   or tollgate-auth's cdk-cli wallet

This creates a **unified charging network** where:
- silent.energy handles Easee chargers (direct API control)
- tollgate-auth handles OCPI-roamed chargers (Wattif, OpenCPO, etc.)
- Both accept the same EUR Cashu tokens
- Drivers see one balance, one payment method

---

## How to Test Everything

### Test 1: Virtual charger with real Cashu (30 seconds)

```bash
# Get a test token
cashu -h https://testnut.cashu.space -w test invoice 5
cashu -h https://testnut.cashu.space -w test send 5

# Paste the cashuB... token at https://ocpi.nodns.shop/
# Click "Plug In" → charger turns green → kWh flows
# Click "Stop" → CDR appears
```

### Test 2: OCPI protocol compliance (10 seconds)

```bash
# At https://ocpi.nodns.shop/, scroll to "OCPI CPO Simulator"
# Click "CPO Handshake" → real POST to credentials endpoint
# Click "Send Authorize" → real POST to tokens/authorize
# Click "Send CDR" → real POST to cdrs
```

### Test 3: OCPP charger → CSMS → eMSP chain

```bash
cd ~/src/tollgate-ssh
go build -o csms-test ./cmd/tollgate-csms/test-client/
./csms-test
# Connects to ws://nodns.shop:8887
# BootNotification → Authorize → StartTransaction → MeterValues → StopTransaction
# CDR arrives at eMSP (check dashboard)
```

### Test 4: Ledger audit trail

```bash
ssh root@nodns.shop 'cat /opt/tollgate-auth/ocpi-ledger.jsonl | python3 -m json.tool'
# Should show auth_accept + accounting_stop entries with token_hash, kWh, cost
```

### Test 5: OpenCPO charger farm

```bash
# Visit https://farm.nodns.shop/ — 10 virtual chargers running
# Visit https://opencpo.nodns.shop/ — admin dashboard
# Login: admin@tollgate.dev / TollgateDemo2026!
```

### Test 6: EUR mint

```bash
ssh root@nodns.shop 'curl -s http://127.0.0.1:3340/v1/info'
# Returns mint info with name "Tollgate EUR Charging Credit"
```

### Test 7: Unit + e2e tests

```bash
cd ~/src/tollgate-ssh
go test ./internal/ocpi/... -v     # 32 Go unit tests
npx playwright test --reporter=list # 7 Playwright e2e tests
```

### Test 8: Cashu wallet balance

```bash
ssh root@nodns.shop 'cdk-cli --work-dir /var/lib/cashu-wallet balance'
# Returns collected sat from all sessions
```
