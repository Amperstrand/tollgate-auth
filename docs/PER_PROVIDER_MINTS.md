# Per-Provider Cashu Mints: Design and Current Status

## Concept

Each EV charger operator gets their own Cashu "currency" on a shared mint
infrastructure. Tokens issued by operator A are only accepted at operator A's
chargers. This creates per-provider gift cards without running separate mint
processes.

## Three Implementation Tiers

### Tier 1: Application-layer isolation (works now)

Single mint (`mint.cashu.exchange`), all tokens are in `sat` unit.
Provider isolation enforced by charger configuration:

```json
// Wattif's charger in silent.energy
{
  "acceptedMints": ["https://wattif.cashu.exchange:sat"]
}

// Mer's charger
{
  "acceptedMints": ["https://mer.cashu.exchange:sat"]
}
```

Both subdomains point to the same cdk-mintd instance via Caddy wildcard.
Tokens are technically fungible (same keyset), but chargers only accept
from their operator's subdomain.

**Pros:** Zero infrastructure changes. Works today.
**Cons:** No cryptographic isolation. If someone gets a token from
`wattif.cashu.exchange`, they could theoretically spend it at
`mer.cashu.exchange` if they changed the mint URL.

### Tier 2: Per-provider keysets (needs CDK fix)

Single mint, each provider gets their own keyset with unique signing keys:

```
mint.cashu.exchange/v1/keysets
  → sat (keyset 00AB) — general purpose
  → wattif-eur (keyset 00CD) — only Wattif chargers accept
  → mer-eur (keyset 00EF) — only Mer chargers accept
```

Tokens from different keysets are cryptographically distinct. A charger
configured to accept keyset 00CD rejects 00EF tokens.

**Blocker:** CDK FakeWallet can't mint non-sat units. Error:
`Invalid payment request` because BOLT11 invoices only support sat/msat.

**Fix needed:** CDK FakeWallet should create a 1:1 fake BOLT11 in sat
regardless of the requested unit, then mint proofs in the requested unit.
This is a CDK feature request: https://github.com/cashubtc/cdk/issues

**Workaround:** The `/api/buy` endpoint mints sat tokens, then the charger
checks the keyset ID in the decoded token to verify it came from the right
provider's keyset. This requires the mint to issue tokens from specific
keysets on demand, which CDK supports via the keyset_id parameter in the
mint endpoint.

### Tier 3: Per-provider mint instances (deployed for our EUR mint)

Each provider gets their own cdk-mintd process:
- `wattif.cashu.exchange` → container :3341
- `mer.cashu.exchange` → container :3342
- Each has its own seed, database, signing keys

**Pros:** Full isolation. Each provider controls their own keys. Can
self-host (export seed, run elsewhere).
**Cons:** One process per provider. ~50MB RAM each. Container orchestration.

## Current State on nodns.shop

```
eur-mint.nodns.shop (:3340) — single cdk-mintd instance
  Keysets:
    sat        — working, minting verified
    msat       — keyset exists
    eur        — keyset exists
    wattif-eur — keyset exists but CAN'T MINT (FakeWallet BOLT11 limitation)
```

## Recommendation

1. **Now:** Use Tier 1 (application-layer filtering via mint URL subdomains).
   Create `wattif.cashu.exchange` as a Caddy alias to the existing mint.
   Configure Wattif's chargers to only accept `wattif.cashu.exchange:sat`.

2. **Next:** File CDK feature request for FakeWallet arbitrary unit support.
   This unblocks Tier 2 (true per-provider currency types).

3. **When needed:** Deploy Tier 3 for providers who want full key isolation
   (separate seeds). This is the most secure but heaviest option.

## How the charger sees it

```typescript
// silent.energy charger config
{
  "providerType": "hermes",
  "providerConfig": { "hermesUuid": "..." },
  "publicConfig": {
    "name": "Bergen Bay 1",
    "acceptedMints": ["https://wattif.cashu.exchange:sat"]
  }
}
```

The charger only accepts tokens minted at `wattif.cashu.exchange`.
The minting is done via `POST https://wattif.cashu.exchange/api/buy`
(which routes through Caddy to the shared `/api/buy` endpoint on
tollgate-auth-ocpi).

The driver experience:
1. Driver visits `wattif.cashu.exchange` (or Wattif's app)
2. Pays €10 via Vipps
3. Receives a `cashuB...` token
4. Plugs into any Wattif charger
5. Charger verifies token against `wattif.cashu.exchange`
6. Accepted → charge starts

## DNS setup needed

```
*.cashu.exchange → CNAME → nodns.shop (or A record to 46.224.104.12)
```

This allows unlimited per-provider subdomains without adding DNS records
for each one.
