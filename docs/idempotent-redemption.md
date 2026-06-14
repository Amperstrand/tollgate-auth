# Idempotent Token Redemption — Crash Recovery

## Problem

Token redemption (NUT-03 swap) is a **two-party atomic operation**. Once the mint
invalidates input proofs and issues new BlindSignatures, the original token is
permanently spent. If the Go binary crashes after redeem succeeds but before
emitting Accept, the user's token is consumed with no session granted.

## Implemented State Machine

```
    DECODE_TOKEN
         |
    +----v------+
    |IsSpent()  |        replay guard: SHA256 in spent-hashes?
    +----+------+
         |
    +----+----+
    |         |
   YES        NO
    |         |
    v         v
    CheckTokenState()     CheckAndMark()     NUT-07: POST /v1/checkstate
    +---------+---+----+    +--SUCCESS--> verify mint --> redeem --> save session --> ACCEPT
    |         |   |    |    |
  SPENT    PEND  ERR UNSPENT FAILURE--> REJECT
    |         |   |    |
    |         v   v    v
    |      REJECT REJECT REJECT
    |
    sessions.Get(mac)?
    |
    YES --> ACCEPT (reconnect, remaining time)
    NO  --> REJECT
```

### State Transitions

| Prior State | IsSpent? | CheckTokenState | Session? | Result | Rationale |
|---|---|---|---|---|---|
| Fresh token | NO | UNSPENT | n/a | CheckAndMark → redeem | Normal first-use flow |
| Fresh token | NO | SPENT | n/a | REJECT | Token already spent elsewhere |
| Replay attempt | YES | UNSPENT | n/a | REJECT | Testnut returns UNSPENT after redeem; falling through allows replay |
| Crash recovery | YES | SPENT | YES | ACCEPT (remaining time) | Token redeemed by us, session exists |
| Spent elsewhere | YES | SPENT | NO | REJECT | Cannot distinguish from replay; fail-safe |
| Concurrent swap | YES | PENDING | n/a | REJECT | Another swap in progress; retry |
| Mint error | YES | ERROR | n/a | REJECT | Fail-safe |

### Why UNSPENT + IsSpent = REJECT

The testnut mint returns `UNSPENT` from `/v1/checkstate` even after `cdk-cli receive`
successfully swaps the token (NUT-03 invalidation doesn't fully propagate to checkstate
for all mint implementations). If we fell through to normal redemption, the same token
could be replayed indefinitely. Rejecting is fail-safe — test tokens are free.

For production mints that correctly report SPENT after redemption, the SPENT+session
recovery path handles crash recovery.

### Recovery via MAC-Based Session Lookup

Recovery uses `sessions.Get(mac)` — the Calling-Station-Id (MAC address) — not a
deterministic session ID derived from the token. This is simpler and works with the
existing session architecture:

1. User submits token T from device with MAC `aa:bb:cc:dd:ee:ff`
2. Binary: CheckAndMark → redeem → save session keyed on MAC → ACCEPT
3. **CRASH** (after redeem, before or after Accept emit)
4. User reconnects from same device with same token T
5. Replay guard: SHA256 in spent-hashes → YES
6. CheckTokenState: SPENT at mint
7. `sessions.Get("aa:bb:cc:dd:ee:ff")` → session exists with remaining time
8. ACCEPT with `Session-Timeout` = remaining seconds

If the same token is submitted from a **different MAC** (step 7 returns NO session),
the binary rejects — it cannot distinguish "crash lost the session" from "different
user replaying the token."

## Crash Window

Session file is written **before** emitting Accept, minimizing the unrecoverable
window to approximately 24 lines of code between redeem success and session save.
A crash in this window results in:

- Token spent at mint (SPENT)
- No session file
- Recovery lookup fails → REJECT (user loses token)

This is an accepted trade-off. Eliminating the window entirely would require writing
a "pending redemption" entry before the redeem call, then upgrading it after — but
this adds complexity for a rare edge case with free test tokens.

## Two-Layer Architecture

The state machine is mirrored in two code paths:

| Layer | Function | Crash Behavior | Purpose |
|---|---|---|---|
| Production | `handleCashu()` in `main.go` | `os.Exit(1)` on reject | Runs via FreeRADIUS exec module |
| Testable | `processCashu()` in `auth.go` | Returns `AuthResult{Accept: false}` | Unit/integration tested with `FakeVerifier` |

Both layers use the same `deps.Verifier.CheckState()` and `deps.Replay.IsSpent()`
interfaces, ensuring test coverage reflects production behavior.

## NUT References

- **NUT-07** (`/v1/checkstate`): Implemented via `CheckTokenState()` — aggregates per-proof
  state into UNSPENT/SPENT/PENDING. Non-test mints skip the API call and return UNSPENT.
- **NUT-09** (`/v1/restore`): Not implemented. `cdk-cli` handles interrupted swap recovery
  internally. Future enhancement for Go-native CDK integration.
- **NUT-13** (deterministic secrets): Not used. Recovery relies on MAC-based session lookup
  rather than token-derived identifiers.

## Status

**Implemented and deployed.** All 13 Go packages pass with `-race`. CI E2E tests pass
including replay rejection (Test 10) and session reconnection (Test 9).
