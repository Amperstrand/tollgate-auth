# Production Readiness Assessment

## Can a charging provider point their chargers at us today?

**Technically yes, commercially no.**

A CPO with an OCPI 2.2.1-compatible backend could connect to
`https://ocpi.nodns.shop/ocpi/versions` today, complete the credentials
handshake, and send Authorize requests. Our server would verify Cashu
tokens and return ALLOWED/DISALLOWED. Sessions and CDRs would flow.

But: there's no settlement pipeline (we can't pay the CPO), no driver app
(drivers can't easily get tokens), and no persistent storage (restart loses
state). A CPO connecting today would get a working protocol layer with no
business infrastructure behind it.

## What's ready vs what's missing

### ✅ Ready for production (protocol layer)

| Component | Status | Notes |
|---|---|---|
| OCPI 2.2.1 Receiver | ✅ Deployed | Versions, Credentials, Tokens, Sessions, CDRs |
| OCPI 2.2.1 Sender | ✅ Coded | START_SESSION, STOP_SESSION |
| Cashu verification | ✅ Working | NUT-07 checkstate via hashcurve.go |
| Dashboard | ✅ Live | Virtual charger, state machine, CDR display |
| TLS + public access | ✅ Live | Caddy + Let's Encrypt at ocpi.nodns.shop |
| E2E tests | ✅ Passing | 7 Playwright tests + 24 Go unit tests |

### ❌ Missing for production (infrastructure layer)

| Gap | Severity | Effort | Notes |
|---|---|---|---|
| **Persistent storage** | Critical | 2-3 days | Server restart loses all state. SQLite minimum. |
| **Token redemption** | Critical | 1 day | Verify-only mode means tokens aren't claimed. Need cdk-cli + `TOLLGATE_OCPI_REDEEM=true`. |
| **Settlement pipeline** | Critical | 1-2 weeks | Cashu → wallet → NOK → CPO payment. Needs exchange API. |
| **Driver-facing app** | High | 2-4 weeks | Mobile or web app for token acquisition, payment, session history. |
| **Multi-user system** | High | 1-2 weeks | Registration, per-user wallets, auth on dashboard. |
| **Production Cashu mint** | High | 1 week | Self-hosted cdk-mintd with LND/CLN backend. |
| **OCPI Tariffs** | Medium | 2-3 days | Per-location pricing, time-of-use, step pricing. |
| **Monitoring/alerting** | Medium | 2-3 days | Prometheus metrics, uptime checks, log aggregation. |
| **Rate limiting** | Medium | 1 day | Auth endpoint abuse prevention. |
| **High availability** | Low (PoC) | 1-2 weeks | Multi-instance, load balancer, shared DB. |

### ⛔ Blockers (cannot ship without)

| Blocker | Type | Resolution time | Notes |
|---|---|---|---|
| **VASP registration** | Regulatory | 3-6 months | Finanstilsynet registration required for Bitcoin payment services in Norway. #1 blocker. |
| **Settlement rail** | Financial | 1-2 weeks | No way to convert redeemed Cashu to NOK and pay CPO. Need Firi/NBX API integration. |
| **Persistent storage** | Technical | 2-3 days | Current in-memory store loses everything on restart. |
| **Production mint** | Technical | 1 week | testnut.cashu.space is for testing only. Need self-hosted mint with real Bitcoin backing. |
| **Keyset mismatch (issue #14)** | Technical | Unknown | Delegated mode broken. Workaround: local mode. Needs CDK fix. |

## Integration path for a real CPO

### Path A: CPO with OCPI 2.2.1 backend (e.g., Wattif, Mer, Fortum)

```
1. Bilateral roaming agreement (legal + commercial)
   - We register as eMSP with party ID NO/TGA
   - Settlement terms (per-kWh rate, payment cycle)

2. Technical integration
   - CPO points their system at https://ocpi.nodns.shop/ocpi/versions
   - We complete credentials handshake
   - CPO starts sending Authorize requests

3. Token provisioning for drivers
   - We issue OCPI tokens via mobile app
   - Drivers prepay with Cashu → token activated
   - Driver plugs in → CPO authorizes → we ALLOW → charge starts

4. Session lifecycle
   - CPO pushes Sessions (start, updates)
   - CPO sends CDR at end
   - We reconcile CDR against prepay
   - Settle: redeem Cashu → NOK → pay CPO invoice
```

**Estimated time to production-ready for Path A:** 8-12 weeks (engineering) +
3-6 months (regulatory).

### Path B: CPO without OCPI (e.g., standalone chargers with OCPP)

```
1. CPO registers their chargers with us as the CSMS backend
   - Chargers connect to our OCPP server (not yet built)
   - We become both CSMS and eMSP

2. OR: CPO uses a hosted CSMS (Driivz, Spirii, has·to·be)
   - CSMS has OCPI support
   - We connect via OCPI as an eMSP (same as Path A)
```

Path B requires an OCPP CSMS, which is a significantly larger undertaking.

### Path C: Via roaming hub (Hubject, Gireve, eMIP)

```
1. Register on hub as eMSP
2. One OCPI integration with hub → access to all CPOs on hub
3. Hub handles settlement between parties
```

This is the fastest path to broad coverage but requires hub membership fees
and compliance with hub-specific requirements.

## Recommended next steps (prioritized)

### Phase 1: Solidify the demo (1 week)

1. **Add persistent storage** — SQLite for tokens, sessions, CDRs. Survives
   restarts. 2 days.

2. **Build a landing page** — Presentation-ready page explaining the system,
   with a "Try the Demo" button. 1 day.

3. **Connect OCPPLab** — Complete the credentials handshake against
   OCPPLab's CPO simulator. Proves protocol compliance. 1 day.

4. **Record a demo video** — Screen recording of the full flow: Cashu faucet
   → dashboard → charger starts → kWh → CDR. 1 day.

### Phase 2: Make it investible (2-4 weeks)

5. **Driver-facing web app** — Simple PWA: create account, mint Cashu from
   Lightning, see session history. 2 weeks.

6. **Production Cashu mint** — Self-hosted cdk-mintd with CLN backend on
   the VPS. Real Bitcoin, not testnut. 1 week.

7. **Tariffs module** — OCPI-compliant per-location pricing. 3 days.

8. **Monitoring** — Prometheus exporter, Grafana dashboard, alerting. 3 days.

### Phase 3: Make it real (1-3 months)

9. **VASP registration** — Start Finanstilsynet application. This is the
   long pole — begin immediately.

10. **Settlement pipeline** — Firi or NBX API integration for Cashu → NOK
    conversion. Automated CPO payment. 2 weeks.

11. **First CPO partnership** — Bilateral OCPI agreement with a Norwegian
    CPO (Wattif or similar via your insider contact). 2-4 weeks.

12. **OCPI hub membership** — Apply to Hubject or Gireve for roaming access
    to 600K+ European charge points. 4-8 weeks.

### Phase 4: Scale (3-6 months)

13. **High availability** — Multi-instance deployment, PostgreSQL, Redis
    for session state.

14. **Mobile apps** — Native iOS/Android for driver UX.

15. **ISO 15118 Plug & Charge** — Certificate-based auto-authentication.

16. **Multi-mint support** — Accept Cashu from multiple mints (user choice).

## What the Wattif insider demo should look like

1. **Open landing page** — Explains architecture, shows components
2. **Open dashboard** — Virtual charger in AVAILABLE state
3. **Get test Cashu token** — Click link to faucet, mint 5 test sats
4. **Paste token → charger turns green** — State transition visible
5. **kWh counter runs** — 3-4 seconds of simulated charging
6. **Click Stop** — CDR appears with kWh + NOK cost
7. **Show OCPPLab panel** — "This exact server speaks OCPI 2.2.1. Here's
   the version_details endpoint. You can point Wattif at it today."
8. **Show the ask** — "All we need from Wattif: expose your OCPI CPO
   endpoint. We handle Bitcoin acceptance, KYC, NOK settlement to you."

## Bottom line

**For a demo to investors/partners: ready now.** The virtual charger + dashboard
+ OCPI protocol endpoints + Playwright tests = a complete story.

**For a real CPO integration: 8-12 weeks engineering + 3-6 months regulatory.**
The protocol layer is done. The business infrastructure (settlement, user
management, production mint, VASP license) is the gap.

**The #1 thing to start today:** VASP registration with Finanstilsynet.
Everything else is engineering and can be parallelized. The regulatory clock
is the long pole.
