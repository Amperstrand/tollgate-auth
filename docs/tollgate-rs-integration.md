# tollgate-auth + tollgate-rs Integration

## Summary

`tollgate-rs` is the TollGate engine: protocol, wallet, pricing, metering, bootstrap balance, Spilman upgrade, and session ledger.

`tollgate-auth` is an access integration package: RADIUS, RadSec, FreeRADIUS, SSH, token field extraction, RADIUS accounting, CoA, Disconnect, and NAS compatibility.

This document describes how `tollgate-auth` should integrate with `tollgate-rs` so that top-up logic, bootstrap-token balance, metering, and session lifecycle are maintained in exactly one place — `tollgate-rs` / `tollgate-sessiond` — instead of being duplicated across RADIUS, captive portal, SSH, and native TollGate flows.

The current `tollgate-auth` implementation is useful and working. It converts Cashu token value directly to RADIUS `Session-Timeout` and SSH session duration. This document describes how it should evolve: from an independent payment+access adapter into a thin access integration layer that delegates payment and session state to `tollgate-rs`.

## Problem

`tollgate-auth` currently converts Cashu token value directly to `Session-Timeout`:

```
Cashu token → verify/redeem → amount → seconds → Session-Timeout
```

This works for one-shot prepaid sessions. A user pays, gets N minutes, and reconnects with a new token when time runs out.

It does not fully match the TollGate bootstrap model where tokens add to a running balance that is consumed by continuous metering. The current approach treats each token as an isolated session rather than as credit added to a persistent metered session.

Top-up cannot be maintained independently in captive portal and RADIUS code. If both the captive portal and `tollgate-auth` implement their own top-up ledger, the balances diverge. A user could top up in one flow and the other flow would not know about it.

A normal WPA2-Enterprise supplicant cannot send another password/token mid-session. The 802.1X/EAP exchange happens once at association. After that, the session is identified by MAC and RADIUS state — there is no mechanism for the client to present new credentials without reassociating.

Therefore, mid-session RADIUS top-up requires an out-of-band payment channel (captive portal, HTTP API, or native TollGate client) plus RADIUS CoA (Change of Authorization, [RFC 5176](https://datatracker.ietf.org/doc/html/rfc5176)) or reconnect-with-new-token as a fallback.

## Design Principle

**There must be exactly one implementation of top-up and bootstrap balance accounting.**

That implementation lives in `tollgate-rs` / `tollgate-sessiond`.

`tollgate-auth` must not maintain an independent top-up ledger. It may cache session state for reconnection handling and NAS compatibility, but the canonical balance and top-up logic belongs to `tollgate-rs`.

All access adapters — RADIUS, captive portal, SSH, and future native TollGate clients — use the same session daemon and top-up API.

## Project Boundaries

| Responsibility | `tollgate-rs` / `tollgate-sessiond` | `tollgate-auth` |
|---|---|---|
| Cashu token receive/redeem (Wallet abstraction) | **owns** | no, eventually delegates |
| `BootstrapSession` creation | **owns** | no |
| Scaled balance (milli-sat precision) | **owns** | no |
| Additive top-up | **owns** | no |
| Metering interval accounting | **owns** | adapter feeds usage |
| Exhaustion actions: Terminate, Restrict, Allow | **owns** | enforces decision |
| Session ledger | **owns** | no |
| Pricing model | **owns** | reads configuration |
| Spilman upgrade path | **owns** | no |
| Captive portal top-up API | **owns** (endpoint) | no |
| RADIUS credential extraction | no | **owns** |
| RADIUS attribute size constraints (253-byte limit) | no | **owns** |
| FreeRADIUS integration (exec module, inner-tunnel) | no | **owns** |
| RADIUS Access-Accept/Reject formatting | no | **owns** |
| `Session-Timeout`, `Acct-Interim-Interval`, `Class` | no | **owns** |
| RADIUS Accounting ingestion (parse/forward) | session API consumes normalized events | **owns** (parse + forward) |
| RADIUS CoA and Disconnect requests | decides desired action | **owns** (sends RADIUS packets) |
| SSH access adapter behavior | session API decides duration/access | **owns** (enforce shell lifetime) |
| NAS compatibility quirks (vendor-specific CoA behavior) | no | **owns** |

## Target Architecture

```
                       ┌──────────────────────────────┐
                       │        tollgate-rs            │
                       │                              │
Cashu token ─────────► │  Wallet::receive_token()      │
Usage update ────────► │  BootstrapSession             │
Top-up token ────────► │  Session ledger               │
                       │  Exhaustion policy            │
                       └──────────────┬───────────────┘
                                      │
              ┌───────────────────────┴───────────────────────┐
              │                                               │
   captive portal adapter                         tollgate-auth RADIUS adapter
   HTTP form / local app                          FreeRADIUS exec / REST call
   firewall/OpenNDS rules                         Session-Timeout / CoA / Accounting
```

The integration point is `tollgate-sessiond`: a local daemon or API that runs beside FreeRADIUS and/or a captive portal. `tollgate-sessiond` owns session state. `tollgate-auth` calls it for session creation, top-up, usage reporting, and termination decisions.

`tollgate-sessiond` can be:

- A standalone process on the same host as FreeRADIUS.
- A library linked into a future Rust-based `tollgate-auth`.
- A Unix socket service with JSON-over-HTTP or gRPC.

For prototyping, HTTP over localhost (or Unix socket) is simplest. The protocol can be upgraded to gRPC later without changing the semantics.

## Shared Session API

The proposed local API runs over HTTP on localhost or a Unix domain socket. HTTP/Unix socket is documented first because it is easiest to prototype and debug.

### Design principle: daemon is access-method-agnostic

The session daemon returns **TollGate-domain data** — balance, quota, enforcement actions — not RADIUS-specific attributes. Access adapters (RADIUS, SSH, captive portal) translate daemon responses into their own enforcement format. This keeps the daemon reusable across all access methods and avoids coupling it to RADIUS or any other specific protocol.

### Relationship to existing `tollgate-net` v1 server

`tollgate-net` already has a v1 HTTP server with endpoints like `POST /` (Cashu token payment), `GET /usage`, `GET /whoami`, `GET /balance`. These are TIP-03 endpoints designed for the OpenWRT captive portal use case (port 2121).

The session daemon API proposed here is a **different interface**. The v1 server speaks the TIP-03 protocol (Nostr event payloads). The session daemon speaks a session-management API designed for access adapters. Both wrap `tollgate-core`, but for different consumers. The session daemon could be built as a new crate (`tollgate-sessiond`) or as additional routes on the existing v1 server. The wire format is the main difference — the session daemon uses plain JSON, not Nostr events.

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
  "session_id": "02a1b2c3...33bytes",
  "peer_pubkey": "02a1b2c3d4e5f6...33byte_compressed_secp256k1",
  "remaining_quota_scaled": 480000,
  "remaining_seconds_estimate": 480,
  "access_level": "active",
  "exhaustion_action": "terminate",
  "is_final": false
}
```

`session_id` is the hex-encoded 33-byte compressed secp256k1 public key generated for this session. `tollgate-auth` formats this into RADIUS attributes (e.g. `Class = "tollgate:02a1b2c3..."`), but the daemon itself returns only the raw TollGate-domain values.

`access_level` maps to `tollgate-core`'s `AccessLevel` enum (`active`, `suspended`, `restricted`). `exhaustion_action` maps to `ExhaustionAction` (`terminate`, `restrict`, `allow`). `is_final` maps to the `is_final` field in `MeteringReportResponse`.

### Top-up

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
  "session_id": "02a1b2c3...33bytes",
  "remaining_quota_scaled": 720000,
  "remaining_seconds_estimate": 720,
  "access_level": "active",
  "exhaustion_action": "terminate",
  "is_final": false
}
```

The top-up endpoint is the **only canonical top-up path**. Captive portal, RADIUS, SSH, and future native TollGate clients must all call this same endpoint. No access adapter maintains its own top-up ledger.

`tollgate-auth` reads `remaining_seconds_estimate` and decides how to enforce the change (CoA with new `Session-Timeout`, firewall update, shell deadline extension). The daemon does not prescribe RADIUS attributes.

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

Response:

```json
{
  "status": "ok",
  "remaining_quota_scaled": 360000,
  "remaining_seconds_estimate": 360,
  "access_level": "active",
  "exhaustion_action": "terminate",
  "is_final": false
}
```

The session daemon runs `BootstrapSession::process_interval()` on the usage data and returns the resulting `BootstrapIntervalResult`. The `is_final` field is set when the next interval would exhaust the quota — this maps directly to `tollgate-core`'s `MeteringReportResponse.is_final`.

If the session is exhausted:

```json
{
  "status": "exhausted",
  "remaining_quota_scaled": 0,
  "exhaustion_action": "terminate",
  "access_level": "suspended"
}
```

`tollgate-auth` translates this into a RADIUS Disconnect-Request or CoA depending on the `exhaustion_action`.

The session daemon consumes normalized usage events. It does not need to know whether they came from RADIUS accounting, captive portal metering, or an SSH heartbeat.

### Terminate

Request:

```json
{
  "reason": "nas-disconnect",
  "acct_terminate_cause": "User-Request"
}
```

### Session Query

Response:

```json
{
  "session_id": "02a1b2c3...33bytes",
  "access_method": "radius",
  "access_level": "active",
  "remaining_quota_scaled": 360000,
  "remaining_seconds_estimate": 360,
  "exhaustion_action": "terminate",
  "created_at": "2025-06-11T10:00:00Z",
  "subject": {
    "mac": "aa:bb:cc:dd:ee:ff",
    "nas_id": "ap-1"
  }
}
```

## Captive Portal Flow

```
Client joins open/walled-garden SSID
        ↓
Captive portal asks for Cashu token
        ↓
Portal calls tollgate-sessiond /bootstrap
        ↓
tollgate-rs verifies/redeems token and creates session
        ↓
Portal/backend opens firewall for MAC/IP
        ↓
User later submits top-up token to same portal
        ↓
Portal calls /topups
        ↓
Same BootstrapSession gets credited
```

The captive portal is the easiest UX for top-up because:

- It avoids RADIUS attribute size limits entirely. Tokens go in HTTP POST bodies with no size constraint.
- It supports full Cashu tokens (including multi-proof tokens) without splitting.
- The user has a browser — they can paste tokens, scan QR codes, or use Lightning wallets.
- It should still use the same session daemon and top-up API. No separate captive-portal-only balance.

When the portal calls `/topups`, `tollgate-sessiond` credits the existing session. If the session has a RADIUS component (same MAC, same NAS), the session daemon signals that a CoA is needed, and `tollgate-auth` sends it. This is how captive portal top-up reaches a non-captive-portal RADIUS session.

## RADIUS / Non-Captive Portal Flow

```
Client enters Cashu token as WPA2-Enterprise username/password
        ↓
AP/NAS sends RADIUS Access-Request
        ↓
tollgate-auth extracts token from username/password/cleartext-password
        ↓
tollgate-auth calls tollgate-sessiond /bootstrap
        ↓
tollgate-rs verifies/redeems token and creates session
        ↓
tollgate-auth returns Access-Accept:
    Session-Timeout = N
    Acct-Interim-Interval = 60
    Class = tollgate:<session_id>
```

This mode works without a captive portal. Initial payment happens through RADIUS — the Cashu token goes in the username or password field of the WPA2-Enterprise credential prompt.

Subsequent top-up **cannot** be done through the already-completed Access-Request. The 802.1X exchange is over. The client has no mechanism to present new credentials without reassociating.

Mid-session extension requires:

1. An **out-of-band top-up API** — the user submits a new Cashu token via a captive portal, web form, or native TollGate client.
2. **RADIUS CoA** — `tollgate-auth` sends a Change of Authorization request to the NAS with the new `Session-Timeout`.
3. **Reconnect-with-new-token** — the fallback if CoA is not supported by the NAS or controller.

## Top-Up Semantics

In TollGate terms:

- A **top-up** is an additional accepted Cashu token.
- Its value is added to the existing bootstrap balance.
- It does **not** renegotiate pricing. The product and pricing were chosen at session creation.
- Balance is consumed by metering at the original rate.
- In RADIUS time-only mode, the remaining scaled balance may be projected into an estimated `Session-Timeout`.

```
Top-up changes the TollGate session balance. Access adapters translate that new balance into enforcement actions.
```

Examples:

| Access Method | Top-Up Enforcement |
|---|---|
| Captive portal | Updates firewall rule expiry for MAC/IP |
| RADIUS | Sends CoA-Request with new `Session-Timeout` |
| SSH | Extends the process/session deadline |
| Native TollGate | Sends `BootstrapAck` / `MeteringReportResponse` |

## RADIUS Accounting and CoA

RADIUS requires these pieces for proper top-up:

### 1. Class attribute in Access-Accept

```
Access-Accept:
    Reply-Message = "Valid Cashu token: 8 sat = 8m access"
    Session-Timeout = 480
    Acct-Interim-Interval = 60
    Class = "tollgate:02a1b2c3d4e5f67890abcdef...66hexchars"
```

The `Class` attribute is an opaque blob that the NAS echoes back in accounting packets. Using `tollgate:<session_id>` lets accounting packets correlate with the correct TollGate session even if MAC randomization, reconnects, or NAS quirks are involved.

### 2. Accounting Start/Interim/Stop ingestion

The NAS sends accounting packets to FreeRADIUS. `tollgate-auth` parses them and forwards normalized events to `tollgate-sessiond`:

| Acct-Status-Type | What `tollgate-auth` sends to session daemon |
|---|---|
| `Start` | Session began at NAS. Record `acct_session_id`, NAS IP, framed IP. |
| `Interim-Update` | Usage report: `acct_session_time`, `acct_input_octets`, `acct_output_octets`. |
| `Stop` | Session ended. Reason from `acct_terminate_cause`. |

### 3. Mapping RADIUS accounting to TollGate session IDs

The `Class` attribute in the original Access-Accept is echoed back in accounting packets. `tollgate-auth` parses `Class = "tollgate:<session_id>"` to map the accounting event to the correct session.

### 4. CoA-Request for active session extension

When `tollgate-sessiond` accepts a top-up and returns a response with `access_level = "active"` and updated `remaining_seconds_estimate`, `tollgate-auth` translates this into a CoA-Request:

```
CoA-Request:
    Class = "tollgate:02a1b2c3d4e5f67890abcdef...66hexchars"
    Session-Timeout = 720
```

The daemon does not include RADIUS-specific fields. `tollgate-auth` reads `remaining_seconds_estimate` from the daemon response and formats the CoA with the appropriate RADIUS attributes.

### 5. Disconnect-Request for termination

When the session daemon signals termination (balance exhausted, fraud detected, admin action):

```
Disconnect-Request:
    Class = "tollgate:02a1b2c3d4e5f67890abcdef...66hexchars"
```

### 6. Reconnect fallback

If CoA is not supported by the NAS or controller, the top-up is still credited in the session daemon. The balance is preserved. Options:

- **Disconnect + reconnect**: Send Disconnect-Request. Client reassociates. `tollgate-auth` recognizes the MAC as having an active session and returns remaining balance as `Session-Timeout`.
- **Wait for timeout**: Let the current `Session-Timeout` expire. Client reconnects. New Access-Request maps to the existing session with credited balance.

### NAS/controller behavior varies

CoA and Disconnect behavior varies significantly across vendors. `tollgate-auth` must support vendor-specific testing for:

- **UniFi** (UDM, UAP)
- **OpenWRT / hostapd**
- **MikroTik** (RouterOS)
- **Cisco** (Aironet, Meraki, ISE)
- **Aruba** (Instant, Mobility Controller, ClearPass)

The session engine should not contain vendor-specific RADIUS logic. That belongs in `tollgate-auth`.

## Session Identity

MAC-only session identity is not enough. MAC addresses can be randomized, spoofed, or reused across VLANs.

Canonical session identity uses **generated secp256k1 keypairs**, aligned with `tollgate-core`'s `PeerSession` model:

```
session_id = hex(33-byte compressed secp256k1 public key)
```

The session daemon generates a keypair per session. The **nsec** (secret key) is stored server-side. The **npub** (compressed public key, 33 bytes) becomes the peer identifier — this is the `session_id`. This matches `tollgate-core`'s `PubKey([u8; 32])` peer identity type and `PeerSession`'s use of `PubKey` as the session key.

This design enables a future upgrade path: when the peer upgrades from bootstrap tokens to native TollGate/Spilman, the same identity can be used. The daemon already knows the session's keypair — it can sign challenges, authenticate Nostr events, and participate in the TollGate protocol without requiring a new identity.

Tracked fields:

| Field | Description |
|---|---|
| `session_id` | Hex-encoded 33-byte compressed secp256k1 public key (the npub) |
| `access_method` | `radius`, `captive_portal`, `ssh`, `native` |
| `subject_mac` | Client MAC from NAS or portal |
| `username` | RADIUS User-Name or portal username |
| `nas_id` | NAS-Identifier from Access-Request |
| `nas_ip` | NAS-IP-Address from Access-Request |
| `acct_session_id` | RADIUS Acct-Session-Id (NAS-generated) |
| `class` | `tollgate:<session_id>` — the value sent in RADIUS Class attribute |
| `product_id` | Pricing/product selected at session creation |
| `created_at` | Timestamp of session creation |
| `balance_scaled` | Current scaled balance (`i128` in `tollgate-core`) |
| `access_level` | `active`, `suspended`, `restricted` — maps to `tollgate-core` `AccessLevel` |
| `exhaustion_action` | `terminate`, `restrict`, `allow` — maps to `tollgate-core` `ExhaustionAction` |

RADIUS should use:

```
Class = "tollgate:<session_id>"
```

Where `<session_id>` is the hex-encoded 33-byte public key. Example:

```
Class = "tollgate:02a1b2c3d4e5f67890abcdef...66hexchars"
```

This lets accounting packets correlate with the correct session even if MAC randomization, reconnects, or NAS quirks are involved.

## Responsibilities

| Responsibility | `tollgate-rs` / `tollgate-sessiond` | `tollgate-auth` |
|---|---|---|
| Cashu token verification/redeem | **yes** | no, eventually delegate |
| Bootstrap balance | **yes** | no |
| Additive top-up | **yes** | no |
| Pricing | **yes** | no |
| Metering calculation | **yes** | adapter feeds usage |
| RADIUS field extraction | no | **yes** |
| RADIUS Access-Accept/Reject | no | **yes** |
| RADIUS Accounting | session API consumes normalized events | **yes** — parse/forward |
| RADIUS CoA/Disconnect | decides desired action | **yes** — sends RADIUS packets |
| Captive portal UX | maybe adapter/frontend | no |
| SSH access | session API decides duration/access | **yes** — enforce shell lifetime |

## Migration Plan

### Phase 0: Current state

`tollgate-auth` independently validates/redeems Cashu tokens and maps amount to `Session-Timeout` (RADIUS) and session duration (SSH). Token verification uses `cdk-cli` subprocess calls. Session state is stored as JSON files per MAC address. There is no top-up, no CoA, no accounting processing.

This is useful and working. It handles the bootstrap-only use case correctly.

### Phase 1: Document boundary

Add this design document. No behavior change.

The purpose is to establish the target architecture before writing integration code. All future work should move toward the target, not away from it.

### Phase 2: Add session daemon prototype

In `tollgate-rs`, create `tollgate-sessiond` with:

- `POST /v1/sessions/bootstrap`
- `POST /v1/sessions/{session_id}/topups`
- `POST /v1/sessions/{session_id}/usage`
- `POST /v1/sessions/{session_id}/terminate`
- `GET /v1/sessions/{session_id}`

The daemon should support:

- Cashu token receive via the canonical Wallet abstraction.
- `BootstrapSession` creation with scaled balance.
- Additive top-up.
- Time-based metering with configurable interval.
- Exhaustion policy (Terminate, Restrict, Allow).
- Session ledger (durable storage if real value is used).

### Phase 3: Add optional delegation mode to tollgate-auth

Add configuration:

```
TOLLGATE_SESSIOND_URL=http://127.0.0.1:2121
TOLLGATE_AUTH_MODE=local|delegated
```

In `local` mode, keep current behavior. Token verification and session creation happen locally as they do today.

In `delegated` mode, `tollgate-auth-radius` extracts RADIUS credentials but delegates token verification and session creation to `tollgate-sessiond`. It sends the extracted token and subject information to `POST /v1/sessions/bootstrap` and translates the response into RADIUS Access-Accept attributes.

This allows gradual migration: existing deployments continue in `local` mode while new deployments test `delegated` mode.

### Phase 4: Add Class attribute

Return `Class` in RADIUS Access-Accept:

```
Class = "tollgate:<session_id>"
```

This is required for correlating accounting packets with TollGate sessions. In `local` mode, `tollgate-auth` generates a local session ID. In `delegated` mode, the session ID comes from `tollgate-sessiond`.

### Phase 5: Add RADIUS accounting ingestion

Forward Accounting Start/Interim/Stop to `tollgate-sessiond`:

1. FreeRADIUS receives accounting packets on port 1813.
2. FreeRADIUS exec module calls `tollgate-auth` with accounting attributes.
3. `tollgate-auth` parses `Class`, `Acct-Status-Type`, `Acct-Session-Time`, `Acct-Input-Octets`, `Acct-Output-Octets`, `Acct-Terminate-Cause`.
4. `tollgate-auth` sends normalized usage events to `POST /v1/sessions/{session_id}/usage`.

In `local` mode, accounting events are logged but not processed for metering.

### Phase 6: Add CoA extension

When `tollgate-sessiond` accepts a top-up and returns a response with updated `remaining_seconds_estimate` and `exhaustion_action`:

```json
{
  "status": "accepted",
  "session_id": "02a1b2c3...33bytes",
  "remaining_quota_scaled": 720000,
  "remaining_seconds_estimate": 720,
  "access_level": "active",
  "exhaustion_action": "terminate",
  "is_final": false
}
```

`tollgate-auth` reads `remaining_seconds_estimate` and sends a CoA-Request to the NAS with the new `Session-Timeout`. The NAS extends the session without disconnecting the user.

The daemon returns TollGate-domain data. `tollgate-auth` translates it into RADIUS enforcement.

This requires:

- CoA client configuration (NAS IP, CoA port, shared secret).
- Vendor-specific CoA attribute formatting.
- Fallback to Disconnect-Request if CoA-NAK is received.

### Phase 7: Captive portal integration

The captive portal uses the same `/bootstrap` and `/topups` API. It does not implement separate top-up logic.

Flow:

1. User connects to open/walled-garden SSID.
2. Firewall redirects HTTP to captive portal.
3. Portal accepts Cashu token via web form.
4. Portal calls `POST /v1/sessions/bootstrap`.
5. Portal opens firewall for the user's MAC/IP.
6. User submits top-up token to portal.
7. Portal calls `POST /v1/sessions/{session_id}/topups`.
8. Same `BootstrapSession` gets credited.
9. If the user also has a RADIUS session (same MAC, same NAS), `tollgate-auth` sends CoA.

### Phase 8: Deprecate duplicate local token/session logic

Once `delegated` mode is stable and tested across real deployments:

- Local Cashu redemption in `tollgate-auth` becomes dev/demo fallback only.
- `cdk-cli` subprocess calls are replaced by `tollgate-sessiond` calls.
- Local JSON session files are replaced by session daemon queries.
- `local` mode remains available for offline development and single-host demos.

## Failure Modes and Fallbacks

### `tollgate-sessiond` unavailable

- `delegated` mode: Reject new Access-Requests with `Reply-Message = "Payment service unavailable"`.
- Fallback: If `TOLLGATE_FALLBACK_MODE=local` is configured, fall back to local token verification. This is a degraded mode — top-up will not work, and session state will not be shared with captive portal.
- Existing sessions: Rely on NAS `Session-Timeout` enforcement. No metering updates until session daemon recovers.

### Mint unreachable

- Reject new bootstrap/top-up tokens. The session daemon cannot verify the token without mint communication.
- Existing verified balance continues. Sessions that were already created with a verified token continue to be metered. The session daemon does not need mint access to decrement a balance.
- RADIUS reconnections: If a user with an active session reconnects and the session daemon is available, remaining balance is returned. If the session daemon is down, `tollgate-auth` falls back to local session records (if available).

### CoA unsupported by NAS

- Top-up is still credited in the session daemon.
- Fallback options (configurable):
  1. **Disconnect + reconnect**: Send Disconnect-Request. Client reassociates. New Access-Request maps to existing session with credited balance.
  2. **Wait for timeout**: Let current `Session-Timeout` expire naturally. Client reconnects and gets remaining + credited balance.
- The credited balance is never lost — it is always recorded in the session ledger.

### RADIUS Accounting missing

- If the NAS does not send accounting packets (misconfigured, unsupported):
  - Time-only enforcement still works through `Session-Timeout`. The NAS enforces the timeout independently.
  - Precise metering is unavailable. Data-based billing is not possible without `Acct-Input-Octets` / `Acct-Output-Octets`.
  - The session daemon can still track time-based balance using `Session-Timeout` and reconnection events.

### NAS does not echo `Class`

- Some NAS implementations do not echo the `Class` attribute in accounting packets.
- Fallback: Match sessions using tuple: `(Calling-Station-Id, NAS-IP-Address, User-Name, Acct-Session-Id)`.
- This is less reliable than `Class` (MAC randomization, username changes) but works for most deployments.

### Client cannot access top-up portal

- If the user cannot reach the captive portal or HTTP top-up API (DNS blocked, firewall misconfigured, browser issues):
  - Reconnect with a new token. The old session balance is preserved and merged with the new token.
  - This is the current behavior and always works.

### Token too large for RADIUS

- Full DLEQ Cashu tokens (378 bytes) exceed the 253-byte RADIUS attribute limit.
- Options:
  1. **No-DLEQ token** (230 bytes) — fits in single attribute. Already supported.
  2. **Split-token EAP-TTLS mode** — 200b password + 178b username. Already supported.
  3. **Token reference system** — user locks token at a web service, receives short reference, sends reference via RADIUS. Future.
  4. **Captive portal** — token goes in HTTP POST body, no size limit. Recommended for large tokens.

## Security Considerations

- **`tollgate-sessiond` API should bind to localhost or Unix socket by default.** The session daemon handles Cashu tokens and balance state. It should not be exposed on public interfaces.
- **If exposed beyond localhost, require authentication/mTLS.** In multi-host deployments (session daemon on a separate machine from FreeRADIUS), use TLS with mutual authentication.
- **Never log full Cashu tokens.** Log token hashes, amounts, and mint URLs. Full tokens are bearer instruments.
- **Use token hashes for replay tracking.** SHA-256 hash of the token (or proof secrets) for the spent-token list. Already implemented in `tollgate-auth` as `radius-spent.txt`.
- **Treat RADIUS shared secrets and CoA secrets as sensitive.** These protect RADIUS communication. Rotate them if compromised.
- **Do not trust MAC address alone as identity.** MAC addresses can be spoofed and randomized. Use `Class` + `Acct-Session-Id` + MAC for session matching.
- **Validate `Class` values and session IDs.** Only accept `Class` values that match the pattern `tollgate:<66-hex-char-compressed-secp256k1-pubkey>`. Reject malformed values.
- **Ensure top-up is idempotent or replay-safe.** If the same top-up request is received twice (network retry, client double-submit), the balance should only be credited once. Use token hash deduplication.
- **Store session ledger durably if real value is used.** Test/demo mode can use in-memory or file-based storage. Production mode with real Cashu tokens must persist the ledger to survive restarts.
- **Avoid accepting unverified tokens.** Every token must be verified with the mint (`checkstate`) and preferably redeemed (NUT-03 swap) before granting access.
- **Demo-only LNURL-withdraw behavior must not be confused with production payment receipt.** The current `lnurlw` demo mode grants access without claiming the underlying Lightning payment. This must be clearly flagged in configuration and never enabled for real-value deployments.

## Anti-Goals

- **Do not make `tollgate-rs` depend on `tollgate-auth`.** The session engine is protocol-agnostic. It does not know about RADIUS, FreeRADIUS, EAP, or NAS vendor quirks.
- **Do not implement separate top-up ledgers for captive portal and RADIUS.** One session daemon, one balance, one top-up API.
- **Do not make RADIUS the canonical TollGate protocol.** RADIUS is one access adapter. The canonical protocol is defined in TIP-01/TIP-02 and uses HTTP + Nostr.
- **Do not require captive portal for all deployments.** WPA2-Enterprise + Cashu token in the password field is a valid standalone deployment. No portal needed.
- **Do not require RADIUS for all deployments.** Captive portal + OpenNDS is a valid standalone deployment. No RADIUS needed.
- **Do not solve all vendor-specific CoA quirks in the core session engine.** Vendor-specific RADIUS behavior belongs in `tollgate-auth`, not in `tollgate-rs`.
- **Do not put RADIUS token-size hacks into `tollgate-core`.** Token splitting, DLEQ stripping, and attribute packing are RADIUS problems. The core engine deals with full Cashu tokens.
- **Do not change existing behavior in this document.** This is a design document. No code changes.

## Open Questions

- **Should `tollgate-sessiond` use HTTP, gRPC, or Unix socket JSON-RPC?** HTTP over localhost is easiest to prototype. gRPC offers better performance and schema evolution. Unix socket avoids TCP overhead. Decision should be made in Phase 2.
- **Should `tollgate-auth` be rewritten in Rust later or remain Go?** A Rust rewrite would enable native CDK integration (no `cdk-cli` subprocess) and tighter coupling with `tollgate-rs`. But Go works well for the current RADIUS/SSH adapter. No urgency.
- **Should Cashu redemption move entirely to `tollgate-rs` immediately, or remain dual-mode during migration?** Dual-mode (`local` + `delegated`) is safer for migration. Full delegation can be enforced once the session daemon is proven in production.
- **How should remaining bootstrap balance be handled when upgrading to Spilman?** Resolved: the bootstrap residual is abandoned at Spilman upgrade. This is already documented in `tollgate-bootstrap.md` in the `tollgate-rs` design docs. The bootstrap balance is consumed by metering until exhausted or until the session ends — it is not transferred into the Spilman channel. The Spilman channel starts with its own funding transaction.
- **What exact RADIUS attributes should be sent in CoA for UniFi, MikroTik, OpenWRT, Cisco, and Aruba?** Each vendor has different CoA support and attribute requirements. This needs per-vendor testing. Start with `Session-Timeout` in CoA-Request (most widely supported).
- **Should `Session-Timeout` represent remaining time from now or total session lease?** Remaining time from now is more intuitive for top-up. Total session lease is easier for NAS enforcement. Current behavior: remaining time from now.
- **How should data-based billing map to RADIUS accounting octets?** `Acct-Input-Octets` and `Acct-Output-Octets` are cumulative counters. The session daemon needs to track deltas between Interim-Updates to compute per-interval usage.
- **Should a token-reference system be added for large Cashu tokens?** A web service that locks a token and returns a short reference hash would eliminate the RADIUS size constraint. But it adds infrastructure and a network round-trip before RADIUS auth.
- **What should the default fallback be when CoA fails: disconnect, wait for timeout, or allow reconnect credit?** This should be configurable per deployment. Recommended default: wait for timeout, then reconnect credit (lowest disruption).
