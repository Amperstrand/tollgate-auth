# Architecture — Cashu-Gated EV Charging

## Overview

tollgate-auth is a multi-protocol access gateway that accepts Cashu ecash as
payment for infrastructure access. The same Cashu verification pipeline that
powers SSH shells and WiFi/VPN sessions now gates EV charging via OCPI 2.2.1.

```
                         ┌─────────────────────────────────┐
                         │        nodns.shop VPS           │
                         │                                 │
   EV driver ──────────► │  Caddy (:443) ── TLS ──┐       │
   (Cashu wallet)        │                         │       │
                         │  ┌──────────────────────▼────┐  │
                         │  │ tollgate-auth-ocpi (:8093)│  │
                         │  │  OCPI 2.2.1 eMSP Receiver │  │
                         │  │  Virtual Charger sim      │  │
                         │  │  Dashboard + REST API     │  │
                         │  └───────────┬───────────────┘  │
                         │              │ auth.ProcessAuth │
                         │              ▼                  │
                         │  ┌──────────────────────────┐   │
                         │  │ internal/auth (Go)        │   │
                         │  │  Decode → Verify → Redeem │   │
                         │  └───────────┬───────────────┘   │
                         │              │                   │
                         │     ┌────────┴────────┐          │
                         │     ▼                 ▼          │
                         │  local mode      delegated mode  │
                         │  (Go calls       (Go calls       │
                         │   mint directly)  tollgate-net)  │
                         │     │                 │          │
                         │     ▼                 ▼          │
   SSH guest ──────────► │  tollgate-auth-ssh   tollgate-net│
   (paste token as user) │  (:2222)             (:2121)     │
                         │                      CDK wallet  │
   WiFi/VPN guest ─────► │  tollgate-daemon                 │
   (paste token as pass) │  (:8091) → FreeRADIUS            │
                         │                      │           │
                         └──────────────────────┼───────────┘
                                                ▼
                                    testnut.cashu.space
                                    (test mint, FakeWallet)
```

## Components

### tollgate-auth-ocpi (Go, :8093)

OCPI 2.2.1 eMSP Receiver + virtual charger + dashboard.

| Endpoint | Method | Purpose |
|---|---|---|
| `/ocpi/versions` | GET | Advertise OCPI 2.2.1 |
| `/ocpi/emsp/2.2.1/version_details` | GET | List receiver module endpoints |
| `/ocpi/emsp/2.2.1/credentials` | GET/POST/PUT/DELETE | OCPI handshake (Token A → Token C) |
| `/ocpi/emsp/2.2.1/tokens/{uid}/authorize` | POST | Real-time authorize (CPO calls this) |
| `/ocpi/emsp/2.2.1/sessions/{id}` | PUT/PATCH | Receive session updates from CPO |
| `/ocpi/emsp/2.2.1/cdrs` | POST | Receive CDRs from CPO |
| `/ocpi/emsp/2.2.1/commands/{name}/{id}` | POST | Async command result callback |
| `/api/charger/start` | POST | Start virtual charger (direct Cashu) |
| `/api/charger/stop` | POST | Stop virtual charger, generate CDR |
| `/api/charger/status` | GET | Current charger state + live kWh |
| `/api/prepay` | POST | Issue short OCPI UID from Cashu (OCPPLab compat) |
| `/api/snapshot` | GET | JSON state for dashboards/tests |
| `/` | GET | HTML dashboard |
| `/healthz` | GET | Health check |

**Auth modes:**
- `local` (current): Go code verifies Cashu tokens directly via NUT-07
  `/v1/checkstate`. Uses `internal/cashu/hashcurve.go` for NUT-12
  hash_to_curve (CDK variant). No external binary required.
- `delegated`: Forwards to tollgate-net (:2121) which uses CDK Rust wallet.
  Currently blocked by keyset ID mismatch (see issue #14).

**Verify-only mode** (`TOLLGATE_OCPI_REDEEM=false`, default): Tokens are
verified against the mint but not redeemed (NUT-03 swap skipped). Suitable
for PoC demos. Production requires `TOLLGATE_OCPI_REDEEM=true` + cdk-cli.

### tollgate-net / tollgate-rs (Rust, :2121)

Cashu payment primitive. Implements the TollGate protocol for metered
resource payment via Cashu ecash. Runs a CDK wallet that verifies and
redeems tokens.

V1 HTTP API (port 2121):
- `GET /pay` → 402 + NUT-18 creqA payment request
- `POST /` + Cashu token body → Nostr kind 1022 session event
- `GET /balance` → `{remaining, allotment, metric}`
- `GET /usage` → `"used_ms/allotment_ms"`

### tollgate-auth-ssh (Go, :2222)

Cashu-gated SSH access. Users paste a Cashu token as username, get a
throwaway chroot jail shell for N minutes (1 sat = 1 min).

### tollgate-daemon (Go, :8091)

Persistent auth server for FreeRADIUS and WireGuard. Keeps the Cashu
wallet warm, serves auth requests at ~5-20ms latency via Unix socket.

### Caddy (Go, :443/:80)

TLS termination + reverse proxy. Wildcard cert for `*.nodns.shop`.
Routes:
- `ocpi.nodns.shop` → `localhost:8093` (OCPI server)
- `ssh.cashu.email` → `localhost:8092` (WebSSH)
- `ssh.nodns.shop` → `localhost:8092` (WebSSH)
- `faucet.cashu.email` → static files (token faucet)

## Data flow: Cashu → Charger

### Direct mode (dashboard demo)

```
1. Driver gets Cashu token from testnut faucet
       │
2. Pastes token into dashboard charger input
       │
3. POST /api/charger/start {cashu_token: "cashuB..."}
       │
4. Server: auth.ProcessAuth(token)
   ├─ Decode Cashu V4 (CBOR → proofs + mint URL)
   ├─ Compute Y = hash_to_curve(secret) per NUT-12
   ├─ POST mint/v1/checkstate {Ys: [...]}  (NUT-07)
   ├─ All proofs UNSPENT → accept
   └─ Return allotment: 5 sat × 60 sec/sat = 300 sec
       │
5. Charger state → CHARGING
   Dashboard shows green pulsing charger + kWh counter
       │
6. kWh accumulates: 7.4 kW × elapsed_time
       │
7. Driver clicks Stop → POST /api/charger/stop
       │
8. Server calculates final kWh, cost (NOK 2.50/kWh)
   Generates CDR, stores session
   Charger → AVAILABLE
```

### OCPI roaming mode (for CPO integration)

```
1. Driver plugs cable into CPO's charger
       │
2. Charger reads RFID/app token → sends to CPO
       │
3. CPO: POST /ocpi/emsp/2.2.1/tokens/{uid}/authorize
   (calls our eMSP Receiver)
       │
4. Our server: lookup token UID → Cashu prepay?
   ├─ If prepay: return ALLOWED + authorization_reference
   └─ If no prepay: return BLOCKED + info_url (payment page)
       │
5. If BLOCKED: driver pays Cashu at info_url
   ├─ Payment verified → token UID activated
   └─ CPO retries authorize → ALLOWED
       │
6. Charger starts, energy flows
       │
7. Session ends → CPO sends CDR
   POST /ocpi/emsp/2.2.1/cdrs
       │
8. Our server: reconcile CDR against Cashu payment
   Settlement: redeem Cashu → sell for NOK → pay CPO
```

## OCPI 2.2.1 module coverage

| Module | Our role | Implemented | Notes |
|---|---|---|---|
| Versions | Receiver | ✅ | Advertises 2.2.1 |
| Credentials | Receiver | ✅ | Full handshake (Token A/B/C) |
| Tokens | Receiver | ✅ | POST /authorize real-time |
| Tokens | Sender | ⚠️ | Stub — needs push to CPO |
| Sessions | Receiver | ✅ | PUT/PATCH from CPO |
| CDRs | Receiver | ✅ | POST from CPO |
| Locations | Receiver | ✅ | Acknowledges pushes |
| Locations | Sender | ❌ | Need pull from CPO |
| Commands | Sender | ✅ | START_SESSION, STOP_SESSION |
| Commands | Receiver | ✅ | Async result callback |
| Tariffs | Receiver | ❌ | Not implemented |
| Charging Profiles | — | ❌ | Not implemented |
| Hub Client Info | — | ❌ | For hub roaming |

## Security model

- **Cashu token IS the credential.** No passwords, no API keys. The token's
  cryptographic proofs prove the user paid.
- **Replay protection**: SHA256 of token stored in replay guard. Double-spend
  rejected (token already in spent list → check mint state → reject if SPENT).
- **SSRF protection**: Mint URLs validated against private/local IP ranges
  before HTTP calls.
- **Token format validation**: Strict prefix check (`cashuA`/`cashuB`), length
  cap (4096 bytes), CBOR/JSON decode validation.
- **Mint allowlist**: Configurable regex. Currently accepts all mints (PoC).
  Production should restrict to trusted mints.
- **OCPI Token A/B/C**: Standard OCPI credential rotation. Token A (bootstrap)
  → Token B (peer's outbound) → Token C (our outbound). Each party rotates
  independently.

## Known limitations

1. **In-memory state** — Server restart loses all sessions, CDRs, prepay
   records. Production needs persistent storage.
2. **Verify-only mode** — Cashu tokens are verified but not redeemed (no value
   transfer to our wallet). Same token can be reused until replay guard blocks.
3. **Single instance** — No HA, no load balancing, no clustering.
4. **No user management** — Dashboard is public, no auth, no per-user state.
5. **No tariff negotiation** — Price hardcoded at NOK 2.50/kWh.
6. **No settlement** — CDRs are logged but not billed or reconciled.
7. **Keyset ID mismatch** (issue #14) — Delegated mode fails when mint rotates
   keysets. Workaround: local mode.

## File inventory

```
tollgate-ssh/
├── cmd/tollgate-auth-ocpi/     OCPI binary entry point
│   ├── main.go                 Config, deps, server bootstrap
│   └── verify_only.go          PoC verify-only verifier wrapper
├── internal/ocpi/              OCPI protocol implementation
│   ├── types.go                OCPI 2.2.1 DTOs + status codes
│   ├── store.go                In-memory state (peer, tokens, sessions, CDRs)
│   ├── authorize.go            Token authorize handler (prepay + cashu-direct)
│   ├── handlers.go             Versions, Credentials, Sessions, CDRs, Locations
│   ├── server.go               HTTP server, routing, middleware
│   ├── sender.go               OCPI Sender (START_SESSION, STOP_SESSION)
│   ├── charger.go              Virtual charger simulation
│   ├── dashboard.go            HTML dashboard + template
│   ├── store_test.go           8 unit tests (store concurrency, snapshot isolation)
│   ├── handlers_test.go        8 unit tests (protocol endpoint correctness)
│   └── authorize_test.go       8 unit tests (dispatch logic, edge cases)
├── internal/cashu/             Cashu token handling (shared with RADIUS/SSH)
│   ├── hashcurve.go            NUT-12 hash_to_curve (CDK variant)
│   ├── mint.go                 NUT-07 checkstate (fixed to use Ys)
│   ├── token.go                V3/V4 decode, CheckStateRequest
│   └── wallet.go               cdk-cli redemption wrapper
├── config/
│   ├── caddy/ocpi.conf         Caddy site block for ocpi.nodns.shop
│   └── systemd/tollgate-auth-ocpi.service
├── e2e/charger.spec.ts         7 Playwright e2e tests
├── docs/ocpi-testing.md        Testing guide + OCPPLab onboarding
└── docs/ARCHITECTURE.md        This file
```
