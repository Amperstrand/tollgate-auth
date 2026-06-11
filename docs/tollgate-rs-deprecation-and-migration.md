# tollgate-auth Go Deprecation and Migration Plan

## Decision

Deprecate the Go payment/session/wallet implementation in `tollgate-auth` in favor of `tollgate-rs` as the canonical TollGate engine. Keep Go RADIUS/SSH adapter code during migration, and introduce delegated mode before removing local mode.

## Not Now

- Do not delete Go code yet.
- Do not rewrite all of `tollgate-auth` in Rust yet.
- Do not require captive portal for all deployments.
- Do not require RADIUS for all deployments.
- Do not implement CoA in this documentation-only task.
- Do not implement `tollgate-sessiond` in this repository.

## Why Deprecate the Go Payment Stack

`tollgate-auth` was built as a proof of concept to demonstrate that Cashu ecash tokens work as payment credentials for SSH and RADIUS/WiFi access. It proved the idea. It works today.

But `tollgate-auth` now contains a second, independent implementation of Cashu wallet logic — token decoding, mint verification, replay protection, wallet redemption, amount-to-timeout mapping, and session tracking. `tollgate-rs` has its own implementation of all of these plus scaled balance, additive top-up, pricing, metering economics, exhaustion policy, session ledger, and Spilman payment channels.

Two independent implementations of the same payment logic is a maintenance problem. Every fix, every new feature (top-up, metering, Spilman upgrade), every mint protocol change must be implemented twice. If captive portal top-up is added to `tollgate-auth` independently from `tollgate-rs`, balances diverge. The top-up paths are not shared.

The Go code that should remain is the RADIUS/SSH access adapter logic. That code is unique to `tollgate-auth` — no other project implements Cashu-over-RADIUS credential extraction, FreeRADIUS exec module integration, EAP-TTLS token splitting, or RADIUS attribute size workarounds. That adapter logic is the permanent value of `tollgate-auth`.

## Current-State Inventory

### Go Source Files

| File | Lines | What It Does | Classification |
|---|---|---|---|
| `cmd/tollgate-auth-radius/main.go` | ~448 | RADIUS validator binary. Token extraction from username/password, Cashu/LNURLw detection, split-token reassembly, replay guard, mint verification, wallet redemption, amount-to-Session-Timeout, session store, reconnection logic, Access-Accept/Reject formatting. | **Mixed** — RADIUS adapter code stays; payment/session logic deprecates |
| `cmd/tollgate-auth-ssh/main.go` | ~340 | SSH server binary. Cashu token as username, guest user creation, chroot jail, PTY shell, timer goroutine, cleanup on timeout/disconnect. | **Mixed** — SSH adapter code stays; payment logic deprecates |
| `internal/cashu/token.go` | ~197 | Cashu V3/V4 token decoding. CBOR parsing, base64url, proof extraction, amount calculation, mint URL extraction. | **Deprecate** — duplicates `tollgate-core` protocol codec and `cdk` native parsing |
| `internal/cashu/mint.go` | ~63 | Mint verification. `POST /v1/checkstate` to verify proofs are unspent. | **Deprecate** — duplicates `cdk` wallet `receive_token()` which does verify+redeem atomically |
| `internal/cashu/wallet.go` | ~75 | Token redemption via `cdk-cli receive --allow-untrusted`. Parses cdk-cli output for success indicators. | **Deprecate** — subprocess approach replaced by native `cdk` integration in `tollgate-rs` (`CdkWallet`) |
| `internal/cashu/replay.go` | ~72 | Replay guard. File-based SHA-256 hash store for spent tokens. Thread-safe with mutex. | **Deprecate** — `cdk` wallet handles spent proof tracking natively; `tollgate-rs` session ledger covers cross-session replay |

### Configuration Files

| File/Diretory | What It Configures | Classification |
|---|---|---|
| `config/freeradius/` | FreeRADIUS exec module, EAP, inner-tunnel, clients, RadSec (TLS) | **Keep long-term** |
| `scripts/setup-freeradius.sh` | FreeRADIUS installation and configuration | **Keep long-term** |
| `scripts/mint-testnut.js` | Test token minting for CI | **Keep long-term** |
| `scripts/e2e-*.sh` | End-to-end test scripts | **Keep long-term** |
| `docs/index.html` | Faucet — static page that mints free test tokens | **Keep long-term** |
| `Makefile` | Build and deploy targets | **Keep long-term** |
| `timeleft` | Shell script for SSH guest time display | **Keep long-term** |

### Documentation Files

| File | Content | Classification |
|---|---|---|
| `docs/radius-testing.md` | Live demo guide with copy-paste examples | **Keep long-term** |
| `docs/radius-payment-models.md` | Session management, accounting, infrastructure use cases | **Keep long-term** |
| `docs/radius-token-size.md` | Token size analysis, payment approaches, bootstrap spec | **Keep long-term** |
| `docs/demo-openwrt.md` | OpenWRT deployment guide | **Keep long-term** |
| `docs/tollgate-opentollgate-comparison.md` | Comparative analysis with OpenTollGate protocol | **Keep long-term** |
| `docs/tollgate-rs-integration.md` | Integration architecture design | **Keep long-term** |
| `docs/testing-plan.md` | Testing strategy | **Keep long-term** |

### Dependencies

| Dependency | Purpose | Classification |
|---|---|---|
| `github.com/fxamacker/cbor/v2` | CBOR decoding for Cashu V4 tokens | **Deprecate** with `internal/cashu/` |
| `cdk-cli` (external binary) | Token redemption subprocess | **Deprecate** with wallet delegation |

### What Does Not Exist Yet (Needed for Migration)

| Gap | Description | Where It Should Be Built |
|---|---|---|
| `tollgate-sessiond` | Local daemon exposing session API (`/bootstrap`, `/topups`, `/usage`, `/terminate`) | `tollgate-rs` repo — new `crates/tollgate-sessiond` or extend `tollgate-net` |
| Delegated mode in `cmd/tollgate-auth-radius/main.go` | Config flag + HTTP client calling session daemon API | This repo |
| Delegated mode in `cmd/tollgate-auth-ssh/main.go` | Config flag + HTTP client calling session daemon API | This repo |
| RADIUS `Class` attribute emission | `Class = "tollgate:<session_id>"` in Access-Accept | This repo |
| RADIUS accounting forwarding | Parse Start/Interim/Stop, forward to session daemon | This repo |
| RADIUS CoA client | Send CoA-Request on top-up, Disconnect-Request on termination | This repo |
| RADIUS accounting handler in FreeRADIUS config | exec module for port 1813 accounting packets | This repo (`config/freeradius/`) |

## Deprecation Map

### Keep Long-Term (RADIUS/SSH Adapter Code)

These files and code paths constitute the permanent value of `tollgate-auth`. They implement infrastructure access integration that no other project provides.

| Code/Config | What Stays |
|---|---|
| `cmd/tollgate-auth-radius/main.go`: `extractPayment()`, split-token detection, `PaymentCredential` type, Access-Accept/Reject formatting, `Session-Timeout` / `Acct-Interim-Interval` emission | RADIUS credential extraction and response formatting |
| `cmd/tollgate-auth-radius/main.go`: MAC-based reconnection logic, session JSON file reading | Reconnection handling (refactored to query session daemon in delegated mode) |
| `cmd/tollgate-auth-ssh/main.go`: SSH server, guest account management, chroot jail, PTY bridge, timer, cleanup | SSH access enforcement |
| `config/freeradius/*` | All FreeRADIUS configuration |
| `scripts/*` | Build, deploy, and test scripts |
| `docs/*` | All documentation |
| `Makefile` | Build and deploy targets |

### Deprecate Eventually (Go Payment/Session/Wallet Logic)

These files duplicate functionality that `tollgate-rs` already implements or will implement. They should be wrapped behind `TOLLGATE_AUTH_MODE=local` and eventually demoted to demo/fallback status.

| Code | What It Does | `tollgate-rs` Equivalent |
|---|---|---|
| `internal/cashu/token.go` | Cashu V3/V4 decode, proof extraction, amount calculation | `cdk` native token parsing via `CdkWallet::receive_token()` |
| `internal/cashu/mint.go` | Mint `/v1/checkstate` verification | `cdk` wallet handles verification atomically during receive |
| `internal/cashu/wallet.go` | `cdk-cli receive` subprocess for token redemption | `CdkWallet` — native Rust, no subprocess |
| `internal/cashu/replay.go` | File-based spent token hash guard | `cdk` wallet tracks spent proofs natively; `tollgate-rs` session ledger |
| `cmd/tollgate-auth-radius/main.go`: `handleCashu()`, `VerifyWithMint()`, `RedeemToken()`, amount-to-timeout calculation | Payment validation, redemption, and session duration logic | `tollgate-sessiond` `/bootstrap` endpoint |
| `cmd/tollgate-auth-radius/main.go`: session JSON store (`radius-sessions/`) | Local session tracking per MAC | `tollgate-sessiond` session ledger |
| `cmd/tollgate-auth-ssh/main.go`: Cashu decode/verify/redeem, amount calculation | Payment processing for SSH access | `tollgate-sessiond` `/bootstrap` endpoint |
| `go.mod` dependency: `github.com/fxamacker/cbor/v2` | CBOR parsing for Cashu V4 tokens | Not needed once `internal/cashu/` is deprecated |

### Keep Temporarily (Local/Demo/Fallback Mode)

The entire Go payment stack stays functional during migration. It becomes the `local` mode fallback:

```
TOLLGATE_AUTH_MODE=local    # Current behavior: Go handles everything
TOLLGATE_AUTH_MODE=delegated  # New: Go extracts credentials, delegates to tollgate-sessiond
```

`local` mode is useful for:
- Demos without a running `tollgate-sessiond`
- Development and testing on a single machine
- Emergency fallback if `tollgate-sessiond` is unavailable
- CI environments where only `tollgate-auth` is deployed

## What `tollgate-rs` Already Has

The `tollgate-rs` repository (at `/Users/macbook/src/tollgate-rs`) contains:

### `tollgate-core` Crate

| Module | What It Implements |
|---|---|
| `bootstrap.rs` (847 lines) | `BootstrapSession` — scaled balance (`i128`), additive top-up, metering interval processing, exhaustion policy (Terminate/Restrict/Allow), interval cost calculation |
| `wallet.rs` | `Wallet` trait — `receive_token()`, `create_token()`, `mint_reachable()`, `balance()`. Implemented by `CdkWallet` in `tollgate-net`. |
| `adapter.rs` | `ResourceAdapter` trait — `set_peer_access()`, `peer_metrics()`, `subscribe_meter()`. For IP forwarding, firewall rules, etc. |
| `pricing.rs` (538 lines) | Dual pricing (time + units), `compute_interval_cost_scaled()`, product definitions, deterministic product IDs |
| `session.rs` (416 lines) | `PeerSession` — coordinates state machine, bootstrap, wallet, and adapter |
| `peer.rs` | Peer state machine |
| `protocol.rs` | CBOR wire protocol codec |
| `metering.rs` | Metering types and counters |
| `access.rs` | `AccessLevel` enum (None, Active, Suspended) |
| `types.rs` | `Amount`, `AmountResult`, core types |
| `config.rs` | Configuration types |
| `error.rs` | Error types |

### `tollgate-net` Crate

| Module | What It Implements |
|---|---|
| `cdk_wallet.rs` (477 lines) | `CdkWallet` — production `Wallet` implementation using `cdk` crate natively (no subprocess) |
| `v1/server/handlers.rs` (1140 lines) | All 7 v1 HTTP endpoints: `GET /`, `POST /`, `GET /usage`, `GET /balance`, `GET /whoami`, `POST /ln-invoice`, `GET /ln-invoice` |
| `v1/session_manager.rs` (713 lines) | Multi-gateway session manager with usage tracking, renewal, and auto-recovery |
| `v1/server/session_store.rs` | Session persistence |
| `v1/usage_tracker.rs` | Usage tracking with configurable intervals |
| `v1/pricing.rs` | V1 pricing from config |
| `spilman_wallet.rs` | Spilman channel manager |
| `spilman_service.rs` | Spilman service with HTTP orchestration |

### What `tollgate-rs` Needs Before Delegated Mode Can Work

| Gap | Description | Priority |
|---|---|---|
| **`tollgate-sessiond` binary** | A standalone daemon that exposes a session API over localhost HTTP/Unix socket. Wraps `tollgate-core`'s `BootstrapSession`, `Wallet`, and pricing. Currently `tollgate-net` is an OpenWRT router binary — we need a lighter daemon for the RADIUS/SSH use case. | **P0** |
| **Bootstrap endpoint** | `POST /v1/sessions/bootstrap` — accept token + subject, verify/redeem via `CdkWallet`, create `BootstrapSession`, return session ID + RADIUS attributes | **P0** |
| **Top-up endpoint** | `POST /v1/sessions/{id}/topups` — accept additional token, credit balance, return enforcement action | **P0** |
| **Usage endpoint** | `POST /v1/sessions/{id}/usage` — accept normalized usage events (from RADIUS accounting) | **P1** |
| **Terminate endpoint** | `POST /v1/sessions/{id}/terminate` — close session, final settlement | **P1** |
| **Session query** | `GET /v1/sessions/{id}` — return current session state | **P2** |
| **Session identity generation** | `tg_<random>` session IDs, stored in session ledger | **P0** |

## Proposed Session API Contract

The API runs over HTTP on localhost (`127.0.0.1:2121`) or a Unix domain socket. This matches the port used by `tollgate-net` v1 server for TIP-03 compatibility.

### Endpoints

```
POST /v1/sessions/bootstrap
POST /v1/sessions/{session_id}/topups
POST /v1/sessions/{session_id}/usage
POST /v1/sessions/{session_id}/terminate
GET  /v1/sessions/{session_id}
```

### Bootstrap

Request:

```json
{
  "access_method": "radius",
  "token": "cashuB...",
  "subject": {
    "mac": "aa:bb:cc:dd:ee:ff",
    "username": "cashuB...",
    "nas_id": "ap-1",
    "nas_ip": "192.0.2.10"
  },
  "product_id": "wifi-time",
  "requested_mint": "https://testnut.cashu.space"
}
```

Response:

```json
{
  "status": "accepted",
  "session_id": "tg_abc123",
  "remaining_quota_scaled": 480000,
  "remaining_seconds_estimate": 480,
  "access": {
    "level": "active",
    "session_timeout": 480,
    "acct_interim_interval": 60,
    "class": "tollgate:tg_abc123"
  }
}
```

### Top-Up

Request:

```json
{
  "token": "cashuB...",
  "source": "radius-topup-api",
  "subject": {
    "mac": "aa:bb:cc:dd:ee:ff",
    "nas_id": "ap-1",
    "acct_session_id": "123456"
  }
}
```

Response:

```json
{
  "status": "accepted",
  "session_id": "tg_abc123",
  "remaining_quota_scaled": 720000,
  "remaining_seconds_estimate": 720,
  "enforcement": {
    "action": "extend",
    "session_timeout": 720
  }
}
```

### Usage Report

Request:

```json
{
  "acct_session_time": 120,
  "acct_input_octets": 1048576,
  "acct_output_octets": 524288,
  "source": "radius-accounting"
}
```

### Terminate

Request:

```json
{
  "reason": "nas-disconnect",
  "acct_terminate_cause": "User-Request"
}
```

## Delegated Mode Design

### Configuration

```
TOLLGATE_AUTH_MODE=local|delegated
TOLLGATE_SESSIOND_URL=http://127.0.0.1:2121
```

### Local Mode (Current Behavior)

```
RADIUS Access-Request
  → tollgate-auth extracts token
  → Go: decode token (internal/cashu/token.go)
  → Go: verify with mint (internal/cashu/mint.go)
  → Go: redeem via cdk-cli (internal/cashu/wallet.go)
  → Go: check replay guard (internal/cashu/replay.go)
  → Go: amount × RateSecPerSat → Session-Timeout
  → Go: store session JSON per MAC
  → Access-Accept with Session-Timeout
```

### Delegated Mode (Target)

```
RADIUS Access-Request
  → tollgate-auth extracts token (same extraction code)
  → HTTP POST to tollgate-sessiond /v1/sessions/bootstrap
  → tollgate-sessiond: CdkWallet::receive_token() (native Rust, no subprocess)
  → tollgate-sessiond: create BootstrapSession, generate session_id
  → Response: session_id, remaining_seconds_estimate, RADIUS attributes
  → tollgate-auth formats Access-Accept with Session-Timeout, Class, Acct-Interim-Interval
```

### Code Paths in `cmd/tollgate-auth-radius/main.go`

The RADIUS binary's main function currently has this flow:

```
1. Parse CLI args (username, password, mac, etc.)
2. extractPayment() → detect Cashu/LNURLw
3. Check active session (reconnection)
4. handleCashu() or handleLNURLw()
   - Decode token (internal/cashu/token.go)
   - Check replay (internal/cashu/replay.go)
   - Verify with mint (internal/cashu/mint.go)
   - Redeem via cdk-cli (internal/cashu/wallet.go)
   - Calculate timeout
5. Store session JSON
6. Output RADIUS attributes to stdout
```

In delegated mode, steps 4-5 become:

```
4. POST to tollgate-sessiond /bootstrap with token + subject
5. Receive session_id + timeout + Class
```

Steps 1-3 (credential extraction, reconnection check) and step 6 (RADIUS attribute formatting) remain unchanged.

### FreeRADIUS Config Changes

No changes to FreeRADIUS configuration in Phase 3 (delegated mode). The exec module still calls the same `tollgate-auth-radius` binary with the same arguments. The binary internally decides whether to process locally or delegate.

Future phases may add:
- Accounting exec module (new `acct` section in `sites-enabled/default`)
- CoA listener configuration (for receiving CoA responses)

### RADIUS `Class` Attribute

In delegated mode, `Class` is emitted from the session daemon response:

```
Class = "tollgate:tg_abc123"
```

In local mode, `Class` can be generated locally from a `tg_`-prefixed random ID. The format is the same either way.

The `Class` attribute is echoed by the NAS in accounting packets, enabling correlation between accounting events and TollGate sessions.

### RADIUS Accounting Forwarding

New code path in `tollgate-auth`:

1. FreeRADIUS receives Accounting-Request on port 1813.
2. FreeRADIUS exec module calls `tollgate-auth-radius --mode=accounting` with accounting attributes.
3. `tollgate-auth` parses `Acct-Status-Type`, `Class`, `Acct-Session-Time`, `Acct-Input-Octets`, `Acct-Output-Octets`, `Acct-Terminate-Cause`.
4. `tollgate-auth` extracts `session_id` from `Class = "tollgate:<session_id>"`.
5. `tollgate-auth` sends normalized usage event to `POST /v1/sessions/{session_id}/usage`.
6. `tollgate-auth` sends termination event to `POST /v1/sessions/{session_id}/terminate` on `Acct-Status-Type = Stop`.

### RADIUS CoA Trigger

When a top-up is accepted out-of-band (captive portal, HTTP API) and the session daemon returns `enforcement.action = "extend"`:

1. `tollgate-auth` receives the enforcement action (polling, webhook, or triggered by the top-up API handler).
2. `tollgate-auth` sends CoA-Request to the NAS:
   ```
   CoA-Request:
       NAS-IP-Address = <nas_ip from session>
       Class = "tollgate:tg_abc123"
       Session-Timeout = <new value>
   ```
3. If CoA-ACK: session extended.
4. If CoA-NAK or timeout: fall back to Disconnect-Request or wait for current timeout.

CoA client configuration needed:

```
TOLLGATE_COA_ENABLED=true
TOLLGATE_COA_PORT=3799
TOLLGATE_COA_SECRET=<shared secret>
```

## Phased Migration Plan

### Phase 0: Current State (Now)

`tollgate-auth` independently validates/redeems Cashu tokens, maps amount to `Session-Timeout`, and stores sessions as JSON files. No top-up, no CoA, no accounting processing. `tollgate-rs` has `BootstrapSession`, `CdkWallet`, pricing, and v1 server but no `tollgate-sessiond` daemon.

### Phase 1: Documentation (This PR)

Add this design document and `docs/tollgate-rs-integration.md`. No behavior change. Establish the target architecture before writing integration code.

### Phase 2: Build `tollgate-sessiond` in `tollgate-rs`

Create `tollgate-sessiond` in the `tollgate-rs` repository. It wraps `tollgate-core`'s `BootstrapSession` and `CdkWallet` behind an HTTP API on localhost:

- `POST /v1/sessions/bootstrap` — verify/redeem token, create session, return RADIUS attributes
- `POST /v1/sessions/{id}/topups` — credit balance, return enforcement action
- `POST /v1/sessions/{id}/usage` — consume normalized usage events
- `POST /v1/sessions/{id}/terminate` — close session
- `GET /v1/sessions/{id}` — query session state

Session identity: `tg_<random>`. Session store: SQLite or in-memory with file persistence.

This phase happens entirely in the `tollgate-rs` repo. No changes to `tollgate-auth`.

### Phase 3: Add Delegated Mode to `tollgate-auth`

Add configuration and HTTP client to `cmd/tollgate-auth-radius/main.go` and `cmd/tollgate-auth-ssh/main.go`:

```go
var (
    authMode       = os.Getenv("TOLLGATE_AUTH_MODE")       // "local" or "delegated"
    sessiondURL    = os.Getenv("TOLLGATE_SESSIOND_URL")    // "http://127.0.0.1:2121"
)
```

In delegated mode:
- Token extraction remains in Go (same `extractPayment()` code).
- Payment validation and session creation call `POST /v1/sessions/bootstrap`.
- Response drives Access-Accept formatting (same stdout output format).
- No calls to `internal/cashu/mint.go`, `internal/cashu/wallet.go`, `internal/cashu/replay.go`.

In local mode: current behavior, no changes.

### Phase 4: Emit `Class` Attribute

Add `Class = "tollgate:<session_id>"` to RADIUS Access-Accept output in both modes:

- **Delegated mode**: `session_id` comes from `tollgate-sessiond` response.
- **Local mode**: `session_id` is generated locally (`tg_` + random hex).

FreeRADIUS passes `Class` through to the NAS, which echoes it in accounting packets.

### Phase 5: Add RADIUS Accounting Forwarding

1. Add accounting handler to `cmd/tollgate-auth-radius/main.go` (new entry point or flag).
2. Add FreeRADIUS accounting exec module in `config/freeradius/`.
3. Parse accounting attributes, extract `session_id` from `Class`.
4. Forward normalized events to `POST /v1/sessions/{session_id}/usage`.
5. Send `terminate` on `Acct-Status-Type = Stop`.

In local mode: accounting events are logged but not forwarded.

### Phase 6: Add CoA Client

1. Add CoA client to `tollgate-auth` (new package or in `cmd/tollgate-auth-radius/`).
2. When session daemon returns `enforcement.action = "extend"`, send CoA-Request to NAS.
3. Support Disconnect-Request for session termination.
4. Handle vendor-specific CoA differences (UniFi, OpenWRT, MikroTik, Cisco, Aruba).
5. Fallback: Disconnect-Request + reconnect if CoA fails.

### Phase 7: Captive Portal Integration

Captive portal uses the same `tollgate-sessiond` API:

1. User submits token via web form.
2. Portal calls `POST /v1/sessions/bootstrap`.
3. Portal opens firewall for MAC/IP.
4. Top-up calls `POST /v1/sessions/{id}/topups`.
5. If session has RADIUS component, CoA extends it.

No separate top-up ledger in the captive portal.

### Phase 8: Demote Go Payment Stack to Fallback

Once delegated mode is stable in real deployments:

- `internal/cashu/` remains available in `local` mode.
- Default mode shifts to `delegated` (or auto-detect based on `TOLLGATE_SESSIOND_URL`).
- Documentation recommends `delegated` mode for production.
- `local` mode documented as demo/dev/fallback.

### Phase 9 (Future): Remove or Archive Go Payment Code

Once `tollgate-sessiond` is the default for all deployments:

- `internal/cashu/` can be archived or removed.
- `cdk-cli` dependency can be dropped.
- `go.mod` dependency on `fxamacker/cbor` can be dropped.
- `local` mode could be reimplemented as a thin wrapper that calls `tollgate-sessiond` with a mock/local configuration, or removed entirely.

This is the furthest-out phase. No timeline commitment.

## Testing Strategy

### Local Mode Tests (Existing)

Current CI tests continue to work in `local` mode. No changes needed:

- Fresh `lnurlw` → Accept
- Replay protection → Reject
- Session reconnection → Accept
- Cashu no-DLEQ in password → Accept
- Cashu no-DLEQ in username → Accept
- Cashu split token → Accept
- Cashu replay → Reject
- RadSec → Accept
- SSH banner check

### Delegated Mode Tests (New)

| Test | What It Validates |
|---|---|
| `tollgate-sessiond` integration test | Start daemon, call `/bootstrap`, verify response contains session_id, timeout, Class |
| RADIUS with session daemon | Start FreeRADIUS + `tollgate-sessiond` + `tollgate-auth` in delegated mode, run existing test suite |
| Top-up integration | Bootstrap session, call `/topups` with new token, verify balance increase, verify CoA trigger |
| Accounting forwarding | Send Accounting-Start/Interim/Stop, verify events reach session daemon |
| Fallback test | Start in delegated mode with session daemon down, verify graceful rejection |

### Dual-Mode Compatibility

Both modes must pass the same RADIUS acceptance tests. The Access-Accept output format (Reply-Message, Session-Timeout, Acct-Interim-Interval, Class) must be identical regardless of whether `local` or `delegated` mode processes the request.

## Compatibility and Rollback Strategy

### Gradual Rollout

1. Deploy `tollgate-sessiond` alongside FreeRADIUS without changing `tollgate-auth` configuration.
2. Set `TOLLGATE_AUTH_MODE=delegated` on a test instance.
3. Run existing CI tests against the test instance.
4. If tests pass, deploy to production with `TOLLGATE_AUTH_MODE=delegated`.
5. If problems arise, revert to `TOLLGATE_AUTH_MODE=local` — no code changes needed.

### Rollback

Setting `TOLLGATE_AUTH_MODE=local` restores current behavior instantly. The Go payment stack remains functional throughout the migration. There is no flag day.

### API Stability

The session API (`/bootstrap`, `/topups`, `/usage`, `/terminate`) should be versioned (`/v1/`). Breaking changes get a new version prefix. `tollgate-auth` pins to the version it supports.

## Risks and Open Questions

### Risks

| Risk | Mitigation |
|---|---|
| `tollgate-sessiond` unavailable in production | `local` mode fallback. Always available. |
| Session daemon latency adds RADIUS auth delay | Session daemon runs on localhost; expected latency < 5ms. Profile and optimize if needed. |
| Dual-mode maintenance burden | Both modes share RADIUS extraction and formatting code. Only the payment path differs. |
| `tollgate-sessiond` API divergence between repos | Pin API version (`/v1/`). Integration tests in both repos. |
| Session state lost on daemon restart | Durable session store (SQLite) in `tollgate-sessiond`. Local JSON files as backup. |
| CoA not supported by some NAS vendors | Fallback to Disconnect-Request or wait-for-timeout. Vendor-specific testing. |

### Open Questions

1. **Should `tollgate-sessiond` be a new crate in `tollgate-rs` or extend `tollgate-net`?** A new crate keeps concerns separated. Extending `tollgate-net` reuses existing v1 server code. The v1 server already handles `POST /` for token payment — the session API is a generalization of that.

2. **Should `tollgate-auth` eventually be rewritten in Rust?** A Rust rewrite would enable native CDK integration (no `cdk-cli` subprocess) and could directly link to `tollgate-core`. But Go works well for RADIUS/SSH adapter code. No urgency — the HTTP API boundary works in any language.

3. **Should the Go `internal/cashu/` package be deleted or archived?** Archived in a `vendor/` or `_deprecated/` directory is safer. It remains useful as reference and fallback. Deletion can happen in Phase 9.

4. **How should SSH delegated mode work?** SSH has no `Class` attribute or accounting. The session daemon would track SSH sessions by username/MAC. Timer enforcement stays in Go (process management). Session duration comes from the daemon.

5. **What happens to the `cdk-cli` subprocess dependency?** In delegated mode, `tollgate-auth` never calls `cdk-cli`. The dependency can be removed from the install guide for delegated-mode deployments. It remains required for `local` mode.

6. **Should `Session-Timeout` represent remaining time from now or total session lease?** Currently remaining time from now. This should stay consistent across both modes.

7. **How should the session daemon handle multiple access methods for the same session?** A user might bootstrap via RADIUS, then top up via captive portal. The session daemon correlates by `session_id`. If the top-up comes from a different access method, the enforcement action (`extend`) is still consumed by the RADIUS adapter (CoA).

8. **Should the session daemon expose a streaming/SSE endpoint for real-time enforcement?** For CoA triggers, the current design has `tollgate-auth` polling or receiving webhooks. A streaming endpoint would be more efficient but adds complexity. Start with HTTP request/response.

## Relationship to `docs/tollgate-rs-integration.md`

The `tollgate-rs-integration.md` document (already in this repo) describes the **target architecture**: project boundaries, session API, captive portal flow, RADIUS flow, top-up semantics, accounting, CoA, session identity, failure modes, and security considerations.

This document (`tollgate-rs-deprecation-and-migration.md`) is the **execution plan**: which Go files to keep, which to deprecate, how delegated mode should be implemented, what `tollgate-rs` needs to build first, and the phased migration sequence.

Read `tollgate-rs-integration.md` for the "what" and "why". Read this document for the "how" and "when".

## See Also

- [docs/tollgate-rs-integration.md](tollgate-rs-integration.md) — Target architecture and integration design
- [docs/radius-payment-models.md](radius-payment-models.md) — RADIUS session lifecycle, accounting, CoA
- [docs/radius-token-size.md](radius-token-size.md) — Token size analysis and payment approaches
- [docs/tollgate-opentollgate-comparison.md](tollgate-opentollgate-comparison.md) — Comparative analysis with OpenTollGate protocol
- [tollgate-rs repository](https://github.com/Amperstrand/tollgate-rs-ai-research-and-experiments) — Rust TollGate engine
- [tollgate-core bootstrap.rs](https://github.com/Amperstrand/tollgate-rs-ai-research-and-experiments/blob/main/crates/tollgate-core/src/bootstrap.rs) — Rust `BootstrapSession` implementation
- [tollgate-core wallet.rs](https://github.com/Amperstrand/tollgate-rs-ai-research-and-experiments/blob/main/crates/tollgate-core/src/wallet.rs) — Rust `Wallet` trait
- [tollgate-net cdk_wallet.rs](https://github.com/Amperstrand/tollgate-rs-ai-research-and-experiments/blob/main/crates/tollgate-net/src/cdk_wallet.rs) — Native `CdkWallet` implementation
