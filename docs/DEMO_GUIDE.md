# Demo Guide — Cashu-Gated EV Charging

## What you're about to show

A working EV charging system where drivers pay with Cashu ecash tokens
instead of credit cards or apps. The same payment primitive that gates SSH
and WiFi access now gates EV charging via the OCPI 2.2.1 roaming protocol.

**Live URL:** https://ocpi.nodns.shop

## The 60-second pitch

> "This is an OCPI 2.2.1 eMSP server that accepts Cashu ecash for EV charging.
> A driver pastes a Cashu token — the server verifies it cryptographically
> against the mint, authorizes the charger, and tracks kWh until the driver
> stops. The charger state, session data, and CDRs all flow through standard
> OCPI. No credit card, no app download, no account — just ecash."

## Demo flow (5 minutes)

### Part 1: Show the landing page (30 seconds)

Open: **https://ocpi.nodns.shop/about**

This explains:
- How Cashu-gated charging works (4-step diagram)
- Architecture (components and data flow)
- Two models: BTC Cashu vs EUR Gift Card
- Integration path for CPOs

### Part 2: Get a test Cashu token (30 seconds)

Open: **https://testnut.cashu.space** (or use the cashu CLI)

```bash
cashu -h https://testnut.cashu.space -w demo invoice 5
cashu -h https://testnut.cashu.space -w demo send 5
```

This gives you a `cashuB...` token worth 5 test sats. No real money —
testnut.cashu.space is a FakeWallet mint that auto-pays all invoices.

**What to say:** "This token is cryptographic ecash. It's like digital cash —
the proofs inside are blind-signed by the mint. Anyone holding the token can
spend it. No account, no identity."

### Part 3: Start the charge (1 minute)

Open: **https://ocpi.nodns.shop**

1. Paste the `cashuB...` token into the "Plug In" input
2. Click "⚡ Plug In"
3. The charger transitions from **AVAILABLE** (gray) to **CHARGING** (green,
   pulsing)

**What happens behind the scenes:**
```
Dashboard → POST /api/charger/start {cashu_token: "cashuB..."}
    ↓
auth.ProcessAuth (same pipeline as SSH + RADIUS)
    ├─ Decode Cashu V4 (CBOR → proofs + mint URL)
    ├─ Compute Y = hash_to_curve(secret) per NUT-12
    ├─ POST testnut.cashu.space/v1/checkstate {Ys: [...]}
    ├─ All proofs UNSPENT → accept
    └─ Return allotment: 5 sat × 10 sec/sat = 50 seconds
```

**What to say:** "The server verified the token against the mint in real-time.
The mint confirmed these proofs are unspent — the ecash is genuine. The charger
is now authorized."

### Part 4: Watch the energy flow (30 seconds)

The dashboard shows:
- **Charger state:** CHARGING (green, pulsing border)
- **Live kWh counter:** increments at 7.4 kW (typical AC charger rate)
- **Power:** 7.4 kW
- **Paid:** 5 sat from testnut.cashu.space

**What to say:** "The kWh counter is live. At 7.4 kilowatts, every second
delivers about 0.002 kWh. In a real deployment this would be metered from the
actual charger via OCPP."

### Part 5: Stop and show the CDR (30 seconds)

1. Click "⏹ Stop"
2. Charger returns to **AVAILABLE** (gray)
3. Scroll down to the Sessions + CDRs panel
4. The CDR shows: total kWh, cost in NOK, session ID

**What happens behind the scenes:**
```
Dashboard → POST /api/charger/stop
    ↓
Server calculates: kWh = 7.4kW × elapsed_hours
Generates CDR: {kwh: 0.05, cost_nok: 0.13, currency: "NOK"}
Stores session as COMPLETED
```

**What to say:** "The CDR — Charge Detail Record — is the OCPI billing
record. In production this would flow to the CPO's billing system. The
driver paid with ecash, the charger delivered energy, the CDR reconciles
both."

### Part 6: Show the OCPI protocol layer (1 minute)

Open a terminal and run:

```bash
# Our server speaks standard OCPI 2.2.1
curl -s https://ocpi.nodns.shop/ocpi/versions | jq

# 5 receiver modules — this is what a CPO connects to
curl -s https://ocpi.nodns.shop/ocpi/emsp/2.2.1/version_details | jq

# Health check
curl -s https://ocpi.nodns.shop/healthz
```

**What to say:** "Any OCPI 2.2.1-compatible CPO can connect to this server
today. The versions endpoint, credentials handshake, token authorization,
session updates, and CDR ingestion are all standard OCPI. A charging provider
like Wattif would point their CPO backend at this URL."

### Part 7: The OCPPLab connection (if API token available)

```bash
export OCPPLAB_TOKEN="your-token"
python3 e2e/ocpplab_demo.py
```

This runs the full automated flow:
1. Creates a virtual CPO on OCPPLab
2. Completes the OCPI credentials handshake
3. Deploys a virtual Alfen charger
4. Mints Cashu → pays → charger activates
5. kWh flows in OCPPLab's visualization
6. CDR arrives at our server

## Architecture cheat sheet

```
┌─────────────────────────────────────────────────────┐
│  nodns.shop VPS                                     │
│                                                     │
│  Caddy :443 ─ TLS ──┐                              │
│                      ▼                              │
│  tollgate-auth-ocpi (:8093) ── Go                   │
│    ├─ OCPI 2.2.1 eMSP Receiver                      │
│    ├─ Virtual charger simulator                     │
│    ├─ Dashboard + REST API                          │
│    ├─ File persistence (ocpi-state/)                │
│    └─ auth.ProcessAuth (shared pipeline)            │
│         │                                           │
│         ▼                                           │
│  internal/cashu ── NUT-07 checkstate via hashcurve  │
│         │                                           │
│         ▼                                           │
│  testnut.cashu.space ── FakeWallet test mint        │
│                                                     │
│  Cashu wallet: /var/lib/cashu-wallet/ (2176 sat)    │
│  Session files: /opt/tollgate-auth/ocpi-sessions/   │
│  OCPI state: /opt/tollgate-auth/ocpi-state/         │
│                                                     │
│  Also running:                                      │
│    tollgate-auth-ssh (:2222) ── Cashu-gated SSH     │
│    tollgate-daemon (:8091) ── RADIUS/WireGuard      │
│    tollgate-net (:2121) ── CDK wallet (Rust)        │
└─────────────────────────────────────────────────────┘
```

## Component roles during the demo

| Component | What it does during the demo | Visible to audience? |
|---|---|---|
| **Dashboard** | User-facing charger control + visualization | ✅ Primary view |
| **Virtual charger** | Simulates a physical EVSE (state machine) | ✅ Green/gray box |
| **auth.ProcessAuth** | Verifies Cashu token with mint | ❌ Behind the scenes |
| **hashcurve.go** | Computes NUT-12 Y values for checkstate | ❌ Behind the scenes |
| **testnut.cashu.space** | Test mint that issues and verifies ecash | ❌ Remote API |
| **CDR generator** | Creates billing record on session end | ✅ Shown in dashboard |
| **File persistence** | Saves CDRs/sessions/charger state to disk | ❌ Behind the scenes |
| **OCPI protocol layer** | Standard roaming endpoints for CPO connection | ✅ Shown via curl |

## Key numbers

| Metric | Value |
|---|---|
| Cashu verification time | ~600ms (local mode, direct mint checkstate) |
| Charger power | 7.4 kW (configurable, typical AC Level 2) |
| Pricing | NOK 2.50/kWh (configurable) |
| Rate | 10 sec per sat (test rate, configurable) |
| Wallet balance | 2176 sat collected from various sessions |
| OCPI modules | 5 (credentials, locations, sessions, CDRs, tokens) |
| Test coverage | 24 Go unit tests + 7 Playwright e2e tests |

## What to say if asked "Can this work with real chargers?"

> "Yes. The virtual charger simulates what a real OCPP charger does — it's a
> stand-in for the physical hardware. For a real deployment, a charging
> provider connects their OCPI 2.2.1 CPO backend to our eMSP endpoint. Their
> chargers send Authorize requests through OCPI, our server verifies Cashu
> tokens, and returns ALLOWED. The driver experience is identical — paste
> ecash, charger starts."

## What to say if asked "What about Bitcoin?"

> "The system currently uses Cashu ecash — cryptographic tokens backed by a
> mint. In the test demo, the mint is testnut.cashu.space (free test tokens).
> In production, we'd either:
>
> 1. Use a BTC-backed Cashu mint (requires Lightning node, VASP registration)
> 2. Use a provider-specific EUR Cashu mint (gift card model — no Bitcoin, no
>    VASP, provider issues EUR credit that can only be spent at their chargers)
>
> The gift card model eliminates all regulatory concerns. It's prepaid
> charging credit with cryptographic privacy."

## What to say if asked "How is this different from a normal charging app?"

> "Normal apps require: account creation, credit card entry, app download,
> personal data. Cashu requires: none of that. The token IS the credential.
> No account, no identity, no tracking. You pay with ecash, you charge, you
> leave. The cryptographic proofs prove you paid without revealing who you
> are."

## Running the automated E2E tests

```bash
cd ~/src/tollgate-ssh

# Go unit tests (store, handlers, authorize dispatch)
go test ./internal/ocpi/... -v

# Playwright e2e tests (against live server)
npx playwright test --reporter=list

# Expected: 7/7 pass
# 1. Dashboard loads with charger panel
# 2. Charger shows AVAILABLE state
# 3. OCPI protocol endpoints correct
# 4. Charger status API valid
# 5. Full charge cycle (Cashu → CHARGING → kWh → Stop → CDR)
# 6. Invalid token keeps charger AVAILABLE
# 7. Health endpoint OK
```
