# Operator Identity

How tollgate-auth identifies the gateway operator — the person collecting payments from WiFi/VPN users.

## Design Principles

1. **`npub` is a public identifier, not a secret.** It's a Nostr public key used for payout routing, similar to a Bitcoin address. Anyone can see it. It proves nothing about identity.
2. **Lightning address is a public payout identifier.** Like an email address — public, routable, not secret.
3. **Domain/realm identifies the operator, not proof of control.** `user@example.com` tells you which domain, not that the user controls it.
4. **RadSec client certificates provide verified identity.** TLS mutual auth proves the operator controls the private key associated with the certificate.
5. **Unknown operators are allowed in demo mode.** Ledger entries are marked `self-asserted` / `unsettleable`.

## Identity Sources (Priority Order)

| Source | Trust Level | Config | How it works |
|--------|-------------|--------|-------------|
| Registered operator config | **Verified** | `TOLLGATE_OPERATOR_REGISTRY` | JSON file with operator accounts, match criteria (client IP, NAS-ID) |
| RadSec client certificate | **Verified** | FreeRADIUS TLS config | Client cert fingerprint or CN matched to registered operator |
| Environment variable | **Self-asserted** | `TOLLGATE_OPERATOR_NPUB` | Single npub set via env var |
| RADIUS attributes (NAS-ID, realm) | **Self-asserted** | Auto-detected | NAS-Identifier, NAI realm from User-Name |
| Default/anonymous | **Anonymous** | Fallback | No operator configured |

## Operator Resolution Flow

```
1. Load registry from config file (if exists)
2. Check env var TOLLGATE_OPERATOR_NPUB
3. For each auth request:
   a. Try registry.Resolve(clientIP, nasID)
   b. If no match, check env npub
   c. If no match, use "anonymous" operator
4. Record operator_id in ledger entries
5. Include operator_id in Class attribute (if configured)
```

## Operator Account Fields

```json
{
  "id": "op-coffee-shop",
  "payout_npub": "npub1...",
  "payout_lnurl": "shop@lnwallet.com",
  "payout_address": "bc1q...",
  "match": {
    "client_ip": "192.168.1.1",
    "nas_id": "coffee-shop-ap",
    "default": false
  }
}
```

## Settlement Eligibility

| Operator Source | Settlement | Notes |
|----------------|-----------|-------|
| Registered + verified cert | ✅ Settleable | Operator identity proven via TLS |
| Registered + matching IP | ⚠️ Conditional | IP could be spoofed |
| Registered + matching NAS-ID | ⚠️ Conditional | NAS-ID could be forged |
| Env var npub only | ❌ Self-asserted | No proof of npub ownership |
| Anonymous | ❌ Unsettleable | Payments accumulate in local wallet |

## Environment Variables

| Variable | Purpose | Default |
|----------|---------|---------|
| `TOLLGATE_OPERATOR_REGISTRY` | Path to operators.json | (empty = no registry) |
| `TOLLGATE_OPERATOR_NPUB` | Fallback operator npub | (empty = anonymous) |
| `TOLLGATE_CLASS_HMAC_KEY` | Key for signing Class attribute | (random = per-restart) |
| `TOLLGATE_ALLOW_UNREGISTERED_OPERATORS` | Accept traffic from unknown NAS | `true` |
| `TOLLGATE_REQUIRE_VERIFIED_OPERATOR_FOR_SETTLEMENT` | Block settlement for unverified | `false` |
| `TOLLGATE_DEFAULT_OPERATOR_ACCOUNT` | Default operator ID | `anonymous` |
| `TOLLGATE_OPERATOR_ID_ATTRS` | Which RADIUS attrs to check | `nas_identifier,client_ip` |

## npub Validation Rules

- Must start with `npub1`
- Must be exactly 63 characters
- After prefix: only bech32 characters (a-z, 0-9, A-Z)
- **Never** used as a shared secret
- **Never** used as proof of identity without a signature mechanism

## Security Considerations

- **Never** use `npub` as the shared RADIUS secret — they serve different purposes
- **Never** treat `Operator-Name` RADIUS attribute as trusted without verification
- The RADIUS shared secret (`clients.conf`) authenticates the NAS device, not the operator
- For production settlement, require RadSec with verified client certificates
- The `Class` attribute is HMAC-signed to prevent forgery — it contains operator_id hash, not the full identity
