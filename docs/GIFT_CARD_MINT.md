# Provider-Specific Fiat Cashu — Closed-Loop Gift Card Model

## Concept

Instead of BTC-denominated Cashu (which needs Lightning, exchanges, VASP
registration), the charging provider runs their own **EUR-denominated Cashu
mint**. Users buy EUR credit via normal payment (Vipps, card), receive EUR
Cashu tokens, and spend them at chargers. The tokens are cryptographic gift
cards — they can only be spent at the issuing provider's chargers.

## Why this is better than BTC Cashu for EV charging

| Concern | BTC Cashu | EUR Cashu (gift card) |
|---|---|---|
| Price volatility | ✗ BTC fluctuates | ✗ None — EUR is EUR |
| Lightning node | Required | Not needed |
| Exchange integration | Required (BTC→NOK) | Not needed |
| VASP registration | Required (Finanstilsynet) | Not needed — it's a gift card |
| User payment method | Bitcoin/Lightning only | Vipps, card, bank — anything |
| Regulatory burden | High (money transmission) | Low (prepaid gift cards) |
| Provider control | Shared with mint operator | Full — provider runs the mint |
| Settlement | Complex (redeem, sell, pay CPO) | Trivial — CPO owns the mint |

## Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│  Provider (e.g., Wattif)                                          │
│                                                                  │
│  ┌──────────┐     ┌──────────────┐     ┌─────────────────────┐  │
│  │  Vipps   │────▶│  Buy Credit  │────▶│  cdk-mintd (EUR)    │  │
│  │  / Card  │     │  Endpoint    │     │  Provider's mint    │  │
│  └──────────┘     │  /api/buy    │     │  mint.provider.com  │  │
│                   └──────────────┘     └─────────┬───────────┘  │
│                                                  │               │
│                                                  │ mints EUR    │
│                                                  │ Cashu tokens │
│                                                  ▼               │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  Driver receives EUR Cashu token                         │   │
│  │  cashuB... (denominated in EUR-cent)                     │   │
│  └──────────────────────┬───────────────────────────────────┘   │
│                         │                                        │
│                         ▼                                        │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  Driver plugs in at charger                              │   │
│  │  Pastes EUR Cashu token                                  │   │
│  └──────────────────────┬───────────────────────────────────┘   │
│                         │                                        │
│                         ▼                                        │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  tollgate-auth-ocpi (eMSP)                               │   │
│  │  • Verifies token with provider's mint (NUT-07)          │   │
│  │  • Burns token (NUT-03 swap — provider gets proofs back) │   │
│  │  • Authorizes charger: EUR balance → kWh allotment       │   │
│  └──────────────────────┬───────────────────────────────────┘   │
│                         │                                        │
│                         ▼                                        │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  Charger starts                                          │   │
│  │  kWh flows until EUR balance exhausted or driver stops   │   │
│  │  CDR generated with EUR cost                             │   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                  │
└──────────────────────────────────────────────────────────────────┘
```

## How EUR Cashu works technically

cdk-mintd supports arbitrary units. A EUR mint issues tokens denominated in
"EUR-cent" (1 token unit = €0.01). The Cashu protocol doesn't care what the
unit represents — it's just a number with a label.

```toml
# cdk-mintd config for EUR gift card mint
[unit]
default_unit = "eur"

[[mint_info]]
name = "Wattif Charging Credit"
description = "Prepaid EUR credit for Wattif EV chargers"
description_long = "These tokens are gift cards redeemable at Wattif charging stations. Not transferable to other mints."

# No Lightning backend needed — this is a gift card, not BTC-backed
[wallet]
type = "fake"   # FakeWallet: always "pays" invoices instantly
                # The backing is the provider's prepaid promise, not Bitcoin
```

The mint URL is the key to the closed loop. Our eMSP only accepts tokens
from `mint.provider.com`. A token from this mint can only be verified and
burned by someone who trusts this specific mint — which is only our eMSP.

## Buy credit flow

```
1. Driver opens dashboard → clicks "Buy €10 credit"
2. Dashboard → POST /api/buy {amount_eur: 10, method: "vipps"}
3. Server creates Vipps payment request (or redirects to Vipps)
4. Driver pays €10 via Vipps
5. Server: call provider's mint POST /v1/mint/quote/bolt11 {amount: 1000, unit: "eur"}
6. Mint creates a quote (FakeWallet auto-pays)
7. Server: call mint POST /v1/mint/bolt11 {quote, outputs}
8. Mint returns blinded signatures
9. Server unblinds → has EUR Cashu proofs
10. Server returns Cashu token to driver
11. Driver now has €10 in Cashu tokens, spendable at any Wattif charger
```

## Pricing model

Instead of the current "1 sat = 60 seconds", the model becomes:

```
Price: €0.25/kWh (configurable per location)
Token: 1000 EUR-cent (€10)
Allotment: 1000 / 25 = 40 kWh

Charger authorizes for min(allotment_kwh, charger_max_kwh)
Session bills actual kWh consumed
CDR: {kwh: 15.3, cost_eur: 3.83, remaining_credit: 6.17}
```

## Implementation plan

### Step 1: Deploy provider mint (1 day)

Deploy cdk-mintd on the VPS with EUR unit + FakeWallet. Configure the mint
allowlist in tollgate-auth-ocpi to accept this mint.

### Step 2: Buy credit endpoint (2 days)

Add `POST /api/buy` to tollgate-auth-ocpi:
- Accept Vipps payment (or mock for PoC)
- Call provider mint to issue EUR tokens
- Return token to user

### Step 3: EUR-based charger pricing (1 day)

Update charger.go to price in EUR/kWh instead of sat/second:
- `PricePerKwhEur` config (default 0.25)
- Allotment = token_amount_eur_cent / price_per_kwh_eur_cent
- CDR cost in EUR

### Step 4: Mint allowlist (1 hour)

Add mint URL allowlist to config — only accept tokens from the provider's
mint (and optionally testnut for development).

### Step 5: Dashboard "Buy Credit" button (1 day)

Add a "Buy €10" / "Buy €25" / "Buy €50" button set to the dashboard.
For PoC: mock the payment step, just mint tokens directly.

## What this eliminates

- ❌ No VASP registration (it's a gift card, not money transmission)
- ❌ No Lightning node (FakeWallet backing)
- ❌ No exchange integration (no BTC→fiat conversion)
- ❌ No price volatility (EUR-denominated)
- ❌ No Bitcoin knowledge required from users
- ❌ No external mint dependency (provider runs their own)

## What this keeps

- ✅ Cashu's cryptographic privacy (blinded signatures, no tracking)
- ✅ Offline-capable tokens (works without internet at the charger)
- ✅ Replay protection (spent proofs can't be reused)
- ✅ Standard Cashu wallet compatibility (any CDK wallet can hold the tokens)
- ✅ The existing tollgate-auth-ocpi infrastructure (just new mint URL + pricing)

## For the Wattif pitch

> "We run a Cashu mint that issues EUR-denominated charging credit. Drivers
> buy credit via Vipps — they get cryptographic tokens worth real EUR. They
> paste the token at any Wattif charger. Our system verifies it, burns it,
> and authorizes the charge. The driver sees kWh flow, you see EUR collected.
>
> No Bitcoin, no crypto volatility, no regulatory headaches. Just prepaid
> charging credit with bank-grade cryptographic privacy. The same Cashu
> protocol that secures Bitcoin ecash wallets, applied to gift cards."
