# Cashu Redemption & Accounting Design

## How Cashu Double-Spend Prevention Works

Cashu ecash uses **blind signatures** from a mint. Each proof (token unit)
contains a secret `s` and a blinded signature `C`. The mint signs `C` without
knowing `s`, creating an anonymous IOU.

### The lifecycle of a Cashu proof

```
1. MINT ISSUES:  Mint signs blinded messages → user unblinds → has {secret, C}
2. USER SPENDS:  User gives {secret, C} to recipient (pastes token)
3. RECIPIENT REDEEMS (NUT-03 swap):
   a. Recipient sends {secret, C} to mint
   b. Mint checks: are these proofs UNSPENT? (NUT-07 checkstate)
   c. If UNSPENT: mint marks them SPENT, creates NEW blinded signatures
   d. Recipient gets NEW proofs with NEW secrets
   e. Original proofs are now worthless (SPENT at mint)
4. DOUBLE-SPEND CHECK: Any future attempt to redeem the same proofs
   fails — mint returns SPENT status
```

### Why hash-list replay guard is insufficient

Our current code (`internal/cashu/mint.go` → `CheckTokenState`) only **checks**
whether proofs are UNSPENT. It does NOT redeem them. This means:

- The token remains spendable by anyone who has a copy
- A copied token could be redeemed elsewhere before we try
- The hash list (`ocpi-spent.txt`) is application-level, not cryptographic

**True replay protection requires redemption (NUT-03 swap).**

---

## Current Architecture

```
Driver pastes Cashu token
       │
       ▼
tollgate-auth-ocpi → auth.ProcessAuth
       │
       ├─ Decode token (CBOR/JSON → proofs)
       ├─ hash_to_curve(secret) → Y values (NUT-12)
       ├─ POST mint/v1/checkstate {Ys: [...]} (NUT-07)
       │   └─ Returns UNSPENT → proceed
       ├─ Add token hash to replay guard (ocpi-spent.txt)
       ├─ [VERIFY-ONLY MODE: skip redemption]
       └─ Return allotment to driver
```

### What happens with redemption enabled (TOLLGATE_OCPI_REDEEM=true)

```
       ├─ POST mint/v1/checkstate → UNSPENT
       ├─ cdk-cli receive --work-dir /var/lib/cashu-wallet <token>
       │   └─ NUT-03 swap: old proofs → SPENT, new proofs → our wallet
       ├─ Token is now cryptographically unspendable by anyone else
       └─ Wallet balance increases
```

---

## Accounting: Linking Cashu to Charging Sessions

### The audit trail we need

Every charge session must produce a chain of evidence:

```
OCPI Token UID (e.g., OCPI-abc12345)
    └─ Cashu Token Hash: SHA256(cashuB...)
        └─ Mint URL: https://testnut.cashu.space
            └─ Amount: 5 sat
                └─ Session ID: sess-csms-1001
                    └─ Charger ID: CP-TEST-001
                        └─ kWh delivered: 5.000
                            └─ Cost: NOK 12.50
                                └─ CDR ID: cdr-csms-1001
```

### Existing data structures

**LedgerEntry** (`internal/ledger/models.go`):
```go
type LedgerEntry struct {
    Timestamp   string    // RFC3339
    EventType   EventType // auth_accept, accounting_stop, etc.
    MAC         string    // session identifier (replicates to charger ID)
    PaymentType string    // "cashu", "delegated"
    AmountSat   int       // token amount
    DurationSec int       // allotment in seconds
    MintURL     string    // where the token came from
    TokenHash   string    // SHA256 of the token
    SessionClass string   // "radius", "ocpi", "csms"
    NASID       string    // charger identity
}
```

**SessionRecord** (`internal/auth/auth.go`):
```go
type SessionRecord struct {
    MAC      string    // session identifier
    Token    string    // token hash
    Guest    string    // guest account name
    Mint     string    // mint URL
    Amount   int       // sat amount
    Started  time.Time
    Duration int       // seconds
    PayType  PaymentType
}
```

### The gap

The ledger exists and works for RADIUS sessions. But for OCPI:

1. **OCPI server doesn't write to the ledger** — the `cmd/tollgate-auth-ocpi/main.go`
   opens a ledger file but `auth.ProcessAuth` doesn't receive it in the deps
   struct properly for OCPI flows. The RADIUS path writes entries; the OCPI
   path doesn't.

2. **No charger_id field** — the ledger has `MAC` (used for RADIUS client MAC)
   but for OCPI we need a charger identifier. We could use `NASID` for this.

3. **No kWh/cost field** — the ledger tracks `AmountSat` and `DurationSec` but
   not energy delivered or monetary cost. For CDR reconciliation we need these.

4. **CDR → ledger link** — CDRs are stored in the OCPI store but not written
   to the ledger. We need a `ledger.RecordAccounting` call when a CDR arrives.

---

## Proposed Accounting Flow

### Phase 1: Wire the ledger into the OCPI path (1 day)

```go
// In charger.go HandleChargeStart:
func (s *Server) HandleChargeStart(...) {
    result := auth.ProcessAuth(a.authz.Deps, cashuToken, ...)
    if result.Accept {
        // Record in ledger
        a.store.SaveState("charger", s.charger) // already exists

        // NEW: write ledger entry
        if a.ledger != nil {
            a.ledger.RecordAuth(ledger.LedgerEntry{
                EventType:   ledger.EventAuthAccept,
                MAC:         sessionID,       // OCPI session ID
                PaymentType: result.PayType,  // "cashu"
                AmountSat:   result.AmountSat,
                DurationSec: result.SessionTimeout,
                MintURL:     result.MintURL,
                TokenHash:   result.TokenHash,
                SessionClass: "ocpi",
                NASID:       "virtual-charger-001", // or real charger ID
            })
        }
    }
}
```

### Phase 2: CDR → ledger (0.5 day)

```go
// In handlers.go HandleCDRs:
func (h *Handlers) HandleCDRs(...) {
    // ... existing CDR storage ...
    if h.ledger != nil {
        h.ledger.RecordAccounting(ledger.LedgerEntry{
            EventType:   ledger.EventAcctStop,
            MAC:         cdr.AuthID,      // links to session
            AmountSat:   0,               // CDR has kWh, not sat
            MintURL:     "",              // from session lookup
            TokenHash:   "",              // from session lookup
            SessionClass: "ocpi",
            Metadata:    fmt.Sprintf(`{"kwh":%.4f,"cost_nok":%.2f,"currency":"%s"}`,
                            cdr.Kwh, cdr.TotalCost, cdr.Currency),
        })
    }
}
```

### Phase 3: Per-charger accounting via session metadata (0.5 day)

Add charger ID to the OCPI prepay record and CDR:

```go
type PrepayRecord struct {
    UID            string
    CashuTokenHash string
    AllotmentSec   int
    AmountSat      int
    MintURL        string
    ChargerID      string    // NEW: which charger consumed this token
    SessionID      string    // NEW: OCPI session ID
    StartedAt      time.Time
}
```

---

## Settlement Model

For each CDR, the settlement chain is:

```
CDR arrives → look up AuthID → find PrepayRecord → find CashuTokenHash
    → verify token was redeemed (mint checkstate = SPENT)
    → verify wallet balance includes this amount
    → mark CDR as settled
    → calculate operator share (e.g., 90% to CPO, 10% to eMSP)
```

The ledger JSONL provides the queryable audit trail. For production scale,
migrate to PostgreSQL with indexes on `token_hash`, `session_id`, and
`charger_id`.

---

## Implementation Priority

| Step | Effort | Blocks |
|---|---|---|
| Wire ledger into OCPI auth path | 1 day | Settlement, audit |
| Add charger_id to prepay records | 0.5 day | Per-charger accounting |
| CDR → ledger entry with kWh metadata | 0.5 day | Billing reconciliation |
| Enable TOLLGATE_OCPI_REDEEM=true with cdk-cli | 1 day | True replay protection |
| Settlement reconciliation endpoint | 2 days | Operator payouts |

Total: 5 days for complete accounting + settlement.

The most critical item is enabling redemption. Without it, tokens are not
cryptographically spent, and the same token could theoretically be used at
multiple charging stations before the replay guard catches it.
