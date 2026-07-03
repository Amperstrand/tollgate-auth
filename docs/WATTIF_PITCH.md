# Wattif Insider Pitch — Email Template

## Subject lines (pick one)

- Partnership proposal — Bitcoin/Lightning payment option for Wattif EV chargers, zero integration cost
- OCPI 2.2.1 eMSP ready: accept Bitcoin at Wattif stations via standard roaming
- Quick proposal: prepaid EV charging with Bitcoin ecash (5-min demo)

---

## Email body

Hi [Name],

I've been working on a system that lets EV drivers pay for charging with
Bitcoin — using the same OCPI 2.2.1 roaming protocol Wattif already speaks.
No changes to your app, branding, or consumer flow. You'd be paid in NOK
as if it were a normal roaming partner.

**The demo is live right now:** https://ocpi.nodns.shop

Paste a test Cashu token (I'll send you some) → the virtual charger
activates → kWh flows → a CDR is generated. The same server speaks standard
OCPI 2.2.1 — you can point Wattif's CPO at `https://ocpi.nodns.shop/ocpi/versions`
today.

**What we handle:**
- Bitcoin/Lightning acceptance from drivers (via Cashu ecash)
- KYC/AML (driver-side, handled by the Cashu mint)
- NOK settlement to Wattif per CDR
- All OCPI protocol compliance (credentials, tokens, sessions, CDRs)

**What Wattif does:**
- Expose your OCPI CPO endpoint
- Sign a bilateral roaming agreement
- Receive NOK settlement per billing cycle

**Two models available:**

1. **BTC Cashu** — drivers pay in Bitcoin, we settle in NOK via a regulated
   exchange (Firi/NBX). Requires VASP registration on our side.

2. **EUR gift card** (recommended) — Wattif runs its own Cashu mint
   denominated in EUR. Drivers buy prepaid credit via Vipps. No Bitcoin,
   no VASP, no exchange. Just a cryptographic gift card system using
   standard Cashu wallets. See: https://ocpi.nodns.shop/about

**Technical proof:**
- Working OCPI 2.2.1 eMSP with 32 unit tests + 7 e2e tests
- OCPP 1.6 CSMS deployed (chargers connect via WebSocket)
- OpenCPO platform running with 10 virtual chargers
- Cashu wallet collected 2377 sat from live sessions
- Audit trail: every Cashu token linked to session → kWh → CDR

Can we book 30 minutes to walk through the demo? I can screen-share the
full flow in under 5 minutes.

Best,
[Your name]

---

## Demo script for the meeting

1. **Open https://ocpi.nodns.shop/about** — landing page explains architecture
2. **Mint a test token:** `cashu -h https://testnut.cashu.space invoice 5 && cashu send 5`
3. **Paste at https://ocpi.nodns.shop** → charger turns green → kWh flows
4. **Click "CPO Simulator" buttons** — show real OCPI protocol messages
5. **Open https://opencpo.nodns.shop** — log in: admin@tollgate.dev / TollgateDemo2026!
6. **Show the OCPI versions URL:** `https://ocpi.nodns.shop/ocpi/versions`
7. **The ask:** "All we need is your OCPI CPO endpoint"

---

## FAQ prep

**"Is this legal?"**
EUR gift card model: yes, it's a prepaid card, not money transmission.
BTC model: requires VASP registration with Finanstilsynet (in progress).

**"What's your revenue model?"**
Transaction fee on each charging session (e.g., 2-5% of kWh cost).

**"Do we need to run a Lightning node?"**
No. The EUR gift card model uses a FakeWallet mint — no Lightning, no Bitcoin
price exposure. Drivers pay in NOK via Vipps, receive EUR-denominated ecash.

**"How long to integrate?"**
Bilateral OCPI agreement: 2-4 weeks (legal + commercial).
Technical: 1 day (point your CPO at our versions URL).

**"What if we want to do it ourselves?"**
All code is open source. You can run the eMSP yourself. We charge for the
Cashu mint operation and settlement service.
