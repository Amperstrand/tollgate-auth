# User Stories — Cashu-Gated EV Charging

## Personas

| Persona | Who they are | What they want |
|---|---|---|
| **Eva (EV Driver)** | Drives an EV, has a Cashu wallet on her phone | Charge her car without an app or credit card |
| **Owen (CPO Operator)** | Runs a charging network (like Wattif) | Accept Bitcoin payments without infrastructure changes |
| **Sam (eMSP Provider)** | Runs the tollgate-auth-ocpi server | Collect Cashu, authorize charges, generate CDRs |
| **Ada (Security Auditor)** | Reviews the deployment | Verify no funds can be stolen or replayed |
| **Pia (Demo Presenter)** | Shows the system to investors/partners | 5-minute demo that proves the concept |

---

## Eva — EV Driver

### US-D01: Pay with Cashu, charge starts
> As an EV driver, I want to paste a Cashu token so that the charger starts delivering energy.

**Acceptance criteria:**
- Paste `cashuB...` token into the dashboard charger input
- Click "Plug In"
- Charger transitions from AVAILABLE → CHARGING within 2 seconds
- kWh counter starts incrementing at 7.4 kW
- Dashboard shows "X sat from mint_url"

**Test:** `e2e/charger.spec.ts` — "full charge cycle" test (already passing)

### US-D02: Stop charging early, pay only for energy used
> As an EV driver, I want to stop charging whenever I want and only pay for the energy I consumed.

**Acceptance criteria:**
- Click "Stop" button
- Charger returns to AVAILABLE
- CDR shows actual kWh delivered (not the full allotment)
- Cost = kWh × NOK 2.50/kWh

**Test:** `e2e/charger.spec.ts` — "full charge cycle" verifies kWh > 0 and cost > 0

### US-D03: Invalid token — retry without blocking
> As an EV driver, when I paste an invalid token, I want to try again without the charger getting stuck.

**Acceptance criteria:**
- Paste garbage or invalid `cashuBfake` token
- Charger remains AVAILABLE (not BLOCKED)
- Input field clears and shows error message
- Can immediately paste a valid token

**Test:** `e2e/charger.spec.ts` — "invalid token keeps charger AVAILABLE" (already passing)

### US-D04: Mobile-responsive dashboard
> As an EV driver using my phone, I want the dashboard to work on a small screen.

**Acceptance criteria:**
- Dashboard renders correctly at 375px width (iPhone SE)
- Charger panel is visible and usable
- Plug In button is reachable without horizontal scrolling
- kWh counter is readable

**Test:** Playwright mobile viewport test (MISSING — need to add)

### US-D05: Landing page explains what's happening
> As someone visiting the site for the first time, I want to understand what this system does.

**Acceptance criteria:**
- `/about` page loads
- Contains: architecture diagram, how-it-works steps, BTC vs EUR models
- "Try the Demo" button links to `/`

**Test:** `e2e/charger.spec.ts` — "dashboard loads with virtual charger panel" verifies hero text

---

## Owen — CPO Operator

### US-C01: Connect to eMSP via OCPI credentials handshake
> As a CPO, I want to complete the OCPI credentials handshake so my chargers can route Authorize requests to the eMSP.

**Acceptance criteria:**
- POST to `/ocpi/emsp/2.2.1/credentials` with Token A
- Server returns Token C (our credential)
- Peer state shows "connected" on dashboard
- Subsequent requests authenticated with Token C

**Test:** Go handler test — `TestHandleCredentials_HandshakeSuccessReturnsTokenC` (already passing)

### US-C02: Authorize a paid token — ALLOWED
> As a CPO, when a driver with a valid Cashu-paid token plugs in, I want to receive ALLOWED from the eMSP.

**Acceptance criteria:**
- Driver pre-pays with Cashu → gets OCPI token UID (e.g., "OCPI-abc12345")
- CPO sends `POST /ocpi/emsp/2.2.1/tokens/OCPI-abc12345/authorize`
- Response: `{"allowed": "ALLOWED", "authorization_reference": "abc12345..."}`
- Charger starts

**Test:** Go handler test — `TestHandleAuthorize_PrepayAllowed` (already passing)

### US-C03: Authorize an unknown token — DISALLOWED
> As a CPO, when a driver with no payment plugs in, I want to receive DISALLOWED.

**Acceptance criteria:**
- CPO sends `POST /ocpi/emsp/2.2.1/tokens/UNKNOWN/authorize`
- Response: `{"allowed": "DISALLOWED"}`
- Charger does not start

**Test:** Go handler test — `TestHandleAuthorize_UnknownUID` (already passing)

### US-C04: Receive CDR after session
> As a CPO, I want to receive a Charge Detail Record after each session for billing reconciliation.

**Acceptance criteria:**
- CPO sends `POST /ocpi/emsp/2.2.1/cdrs` with session data
- Server stores CDR, returns OCPI 1000
- CDR appears in dashboard snapshot
- CDR persists across server restarts

**Test:** Go handler test — `TestHandleCDRs_PostStoresAndMarksPrepayUsed` + file persistence test (already passing)

### US-C05: CPO Simulator shows real OCPI messages
> As a CPO operator evaluating the system, I want to see the actual OCPI protocol messages flowing.

**Acceptance criteria:**
- Click "CPO Handshake" → real POST to credentials endpoint
- Click "Send Authorize" → real POST to tokens/authorize
- Click "Send CDR" → real POST to cdrs
- Each message appears in the protocol log with direction arrows and timestamps

**Test:** Manual QA — verified via dashboard CPO Simulator panel

---

## Sam — eMSP Provider

### US-E01: Cashu token redemption prevents replay
> As an eMSP, I want redeemed Cashu tokens to be unspendable, so the same token can't authorize two charges.

**Acceptance criteria:**
- First use of token: verified + redeemed (NUT-03 swap at mint)
- Second use of same token: mint checkstate returns SPENT → DISALLOWED
- Operator wallet balance increases by token amount
- Ledger records: token hash, amount, mint, session ID

**Current state:** Verify-only mode (TOLLGATE_OCPI_REDEEM=false). Replay blocked by hash list, not by mint redemption.
**Required:** Enable redemption + verify mint rejects spent tokens.

**Test:** MISSING — need test that verifies mint returns SPENT after redemption

### US-E02: Accounting trail — which charger got which Cashu
> As an eMSP, I want an audit trail linking each Cashu token to the charging session that consumed it.

**Acceptance criteria:**
- Each redeemed token logged with: timestamp, session_id, charger_id, token_hash, amount_sat, mint_url, new_proof_hash
- Queryable by session_id or token_hash
- Survives server restart (persistent storage)

**Current state:** Session records stored at `/opt/tollgate-auth/ocpi-sessions/*.json` with token_hash and amount. Ledger at `/opt/tollgate-auth/ocpi-ledger.jsonl` (currently empty — needs wiring).
**Required:** Wire ledger to OCPI auth pipeline.

**Test:** MISSING — need test that verifies ledger entry after charge

### US-E03: OCPP charger connects through CSMS
> As an eMSP, I want OCPP chargers to connect through my CSMS so their Authorize calls reach the Cashu verification pipeline.

**Acceptance criteria:**
- Charger connects via WebSocket to `ws://csms:8887/{cpId}`
- BootNotification accepted
- Authorize forwards to eMSP via OCPI
- StartTransaction/StopTransaction generate CDRs pushed to eMSP

**Test:** CSMS test client verified full OCPP cycle (already done)

### US-E04: Concurrent sessions don't interfere
> As an eMSP, I want multiple simultaneous charges to work independently.

**Acceptance criteria:**
- Two drivers paste different Cashu tokens at the same time
- Both chargers activate independently
- Each CDR has correct kWh for its own session
- No race conditions in session state

**Test:** MISSING — need concurrent charge test

### US-E05: Server restart preserves all state
> As an eMSP, I want a server restart to not lose sessions, CDRs, or prepay records.

**Acceptance criteria:**
- Stop server, start server
- Previous CDRs visible in dashboard
- Previous sessions visible
- Charger state restored to last known value

**Test:** Go store persistence test — `TestStoreWithDir_*` (already passing for CDRs/prepay)

---

## Ada — Security Auditor

### US-S01: Services run as non-root
> As a security auditor, I want all services to run as unprivileged users.

**Acceptance criteria:**
- `ps -u tollgate` shows tollgate-auth-ocpi and tollgate-csms
- systemd units have `User=tollgate`
- No Go service runs as root (except tollgate-auth-ssh, documented)

**Test:** Bash audit script — `ps` + `systemctl show` verification

### US-S02: Databases not internet-exposed
> As a security auditor, I want PostgreSQL and Redis to only be accessible from localhost.

**Acceptance criteria:**
- `ss -tlnp | grep 5432` shows `127.0.0.1:5432`
- `ss -tlnp | grep 6379` shows `127.0.0.1:6379`
- No Docker container exposes ports to `0.0.0.0`

**Test:** Bash audit script — port binding verification

### US-S03: No secrets in logs or git
> As a security auditor, I want to verify no secrets are leaked in logs or committed to git.

**Acceptance criteria:**
- Search logs for `nsec`, `password`, `secret`, `token_a`, `token_b` — no values, only prefixes
- `.gitignore` includes `.env`, `secrets.env`, `*.key`
- No secrets in git history

**Test:** Bash audit script — grep logs + git log

### US-S04: Replay protection via Cashu redemption
> As a security auditor, I want verified tokens to be cryptographically spent, not just hash-listed.

**Acceptance criteria:**
- After Authorize, token proofs are SWAPPED at the mint (NUT-03)
- Subsequent checkstate returns SPENT
- Operator wallet receives new proofs
- Double-spend is impossible at the protocol level, not just application level

**Current state:** Hash-list replay guard only. Full redemption needs cdk-cli.
**Required:** Enable TOLLGATE_OCPI_REDEEM=true with working cdk-cli.

**Test:** MISSING — need test that verifies mint state changes after redemption

---

## Pia — Demo Presenter

### US-P01: 5-minute full demo
> As a presenter, I want to show the entire concept in under 5 minutes.

**Acceptance criteria:**
- Open landing page → 10 seconds to explain
- Get test Cashu token → 30 seconds
- Paste token → charger activates → 10 seconds
- Watch kWh flow → 30 seconds
- Stop → CDR appears → 10 seconds
- Show OCPI protocol buttons → 30 seconds
- Show OpenCPO farm → 30 seconds
- Total: under 3 minutes of active demo

**Test:** Manual QA — documented in `docs/DEMO_GUIDE.md`

### US-P02: Demo works even if Cashu mint is slow
> As a presenter, I want the demo to work even if testnut.cashu.space is slow or down.

**Acceptance criteria:**
- Verify-only mode works without mint (just decodes token, accepts)
- Dashboard shows meaningful state transitions
- CPO Simulator buttons work without Cashu

**Current state:** Verify-only mode is default, so demo works without live mint.
**Test:** Playwright tests pass without mint connectivity (they use the API directly)

### US-P03: Investor can try it themselves
> As a presenter, I want to hand the keyboard to an investor and let them drive.

**Acceptance criteria:**
- Dashboard is self-explanatory (minimal instructions needed)
- Error messages are human-readable
- No crashes on invalid input
- Mobile-responsive for phone-based trial

**Test:** Manual QA — verified during demo sessions
