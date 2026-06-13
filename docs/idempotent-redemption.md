# Idempotent Token Redemption — Crash Recovery Design

## Problem

Current flow: decode token → checkstate → redeem (cdk-cli receive) → emit Accept.
If the Go binary crashes after redeem succeeds but before emitting Accept, the user's
token is consumed with no session granted.

## Root Cause

Token redemption (NUT-03 swap) is a **two-party atomic operation** between the wallet
and the mint. Once the mint invalidates the input proofs and issues new BlindSignatures,
the original token is permanently spent. There is no rollback.

## Cashu Protocol Recovery Mechanisms

Three NUTs provide the building blocks for idempotent redemption:

### NUT-07: Token State Check (`POST /v1/checkstate`)

Returns the state of a proof: `UNSPENT`, `PENDING`, or `SPENT`.

- `Y = hash_to_curve(secret)` — derived from the proof's secret field
- Mints MUST track `PENDING` state using a mutex keyed on `Y` to prevent concurrent swaps

### NUT-09: Signature Restore (`POST /v1/restore`)

Mints store `BlindedMessage` → `BlindSignature` pairs. A wallet can re-request
signatures for outputs of an interrupted swap.

### NUT-13: Deterministic Secrets

Wallets can generate secrets and blinding factors deterministically from a seed.
Same seed → same `BlindedMessages` → same `Y` values.

## Proposed State Machine

```
    DECODE_TOKEN
         |
    +----v-----+
    |CHECKSTATE|        NUT-07: POST /v1/checkstate
    +----+-----+
         |
    +----+--------+--------------+
    |    |        |              |
 UNSPENT  SPENT  PENDING      ERROR
    |      |        |              |
    |   session     retry        reject
    |   exists?
    |   |
    |   YES -> ACCEPT (recovery: crash after redeem, session was created)
    |   NO  -> REJECT (spent elsewhere, not by us)
    |
    v
  REDEEM (NUT-03 swap via cdk-cli receive)
    |
    +-- SUCCESS -> create session(session_id) -> ACCEPT
    |
    +-- FAILURE -> reject (token still unspent, safe)
```

### Deterministic Session ID

```
session_id = HMAC-SHA256(hmac_key, sha256(token_bytes))
```

- `hmac_key`: derived from operator nsec via HKDF (already implemented)
- Same token → same session_id → idempotent recovery

### Recovery Scenario

1. User submits token T
2. Go binary: checkstate → UNSPENT → redeem → SUCCESS → **CRASH** (before emit Accept)
3. Token T is now SPENT at the mint. Session file may or may not be written.
4. User reconnects with same token T (from their wallet, still in clipboard)
5. Replay guard: token hash already in spent-hashes file → **REJECT** (current behavior)

**Problem**: In step 5, we reject a user who already paid.

### Fixed Recovery Scenario

1. User submits token T
2. Go binary: checkstate → UNSPENT → redeem → SUCCESS → write session(session_id) → **CRASH**
3. Token T is SPENT at the mint. Session file exists with session_id.
4. User reconnects with same token T
5. Replay guard: token hash in spent-hashes → **DON'T immediately reject**
6. Compute session_id from token T
7. Check: does session file exist for this session_id?
8. If YES → session is active → ACCEPT with remaining time (recovery!)
9. If NO → token spent but not by us → REJECT

### Implementation Notes

- The checkstate call is the idempotency checkpoint
- Session file must be written BEFORE emitting Accept (move-emitted-last)
- Replay guard must check for session recovery before rejecting
- NUT-09 restore is a future enhancement for recovering the new tokens from
  an interrupted swap (currently cdk-cli handles this internally)

## Status

**Not yet implemented.** Current behavior: crash after redeem = user loses token.
This document describes the design for future implementation.

## References

- [NUT-03: Swapping tokens](https://cashubtc.github.io/nuts/03/)
- [NUT-07: Token state check](https://cashubtc.github.io/nuts/07/)
- [NUT-09: Signature restore](https://cashubtc.github.io/nuts/09/)
- [NUT-13: Deterministic secrets](https://cashubtc.github.io/nuts/13/)
