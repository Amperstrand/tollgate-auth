# tollgate-auth vs OpenTollGate Protocol: Comparative Analysis

This document compares tollgate-auth's RADIUS-based payment gateway with the [OpenTollGate protocol](https://github.com/OpenTollGate/tollgate) and its reference implementations ([tollgate-module-basic-go](https://github.com/OpenTollGate/tollgate-module-basic-go), [tollgate-rs](https://github.com/OpenTollGate/tollgate-rs)). We map concepts between the two systems, identify where RADIUS extends or constrains the protocol, and document gaps and opportunities.

## Overview

| Aspect | tollgate-auth (RADIUS) | OpenTollGate Protocol |
|---|---|---|
| **Transport** | RADIUS (UDP 1812, TCP 2083/RadSec) | HTTP (LAN port 2121), Nostr relay (port 4242) |
| **Payment credential** | Cashu token in RADIUS password field | Cashu token in HTTP POST body or Nostr event |
| **Device identity** | MAC address (Calling-Station-Id) | MAC address (from HTTP context or device-identifier tag) |
| **Session management** | RADIUS Session-Timeout + MAC-based JSON files | Chandler module (time/data usage tracker) + valve (firewall rules) |
| **Metering** | Acct-Interim-Interval (60s NAS reports) | MeteringReport every 5 seconds |
| **Top-up** | Not yet (requires CoA or captive portal) | Additive — send another Cashu token via POST |
| **Billing metric** | Time (1 sat = 60 seconds) | Time (milliseconds) or data (bytes) per TIP-01 |
| **Pricing discovery** | Hardcoded `RateSecPerSat = 60` | Nostr kind 10021 event with price_per_step tags |
| **Operator payout** | Wallet redemption (cdk-cli) | Lightning address payout with profit sharing |
| **Infrastructure** | FreeRADIUS (enterprise RADIUS server) | OpenWRT router (embedded Linux) |

## Protocol Layer Comparison

The OpenTollGate protocol is organized into three layers:

```
┌──────────────────────────────────────────────────────────────────┐
│ Protocol Layer (TIP-01, TIP-02)                                  │
│   Base events, Cashu payment semantics                            │
├──────────────────────────────────────────────────────────────────┤
│ Interface Layer (HTTP-01, HTTP-02, HTTP-03, NOSTR-01)           │
│   How events are exchanged between customer and TollGate          │
├──────────────────────────────────────────────────────────────────┤
│ Medium Layer (WIFI-01)                                           │
│   Physical/link-layer specifics (beacon frames, etc.)             │
└──────────────────────────────────────────────────────────────────┘
```

tollgate-auth effectively defines a new **Interface Layer**: **RADIUS-01** — Cashu payment over RADIUS attributes.

### TIP-01: Base Events → RADIUS Mapping

| TIP-01 Concept | OpenTollGate Implementation | tollgate-auth RADIUS |
|---|---|---|
| **TollGate Discovery** (kind 10021) | Nostr event with metric, step_size, price tags | Not applicable — NAS doesn't discover pricing. RADIUS is a direct auth protocol. |
| **Session** (kind 1022) | Nostr event with device-identifier, allotment, metric | RADIUS Access-Accept with Session-Timeout, Reply-Message |
| **Notice** (kind 21023) | Nostr event with level, code, content | RADIUS Reply-Message in Access-Reject (error description) |
| **metric** | `milliseconds` or `bytes` | `milliseconds` only (Session-Timeout in seconds) |
| **step_size** | e.g., 60000 (1 minute) | `RateSecPerSat = 60` (1 sat = 60 seconds) |
| **device-identifier** | MAC address from HTTP context | Calling-Station-Id (MAC from NAS) |
| **allotment** | Amount of metric granted | `Session-Timeout = amount × RateSecPerSat` |

### TIP-02: Cashu Payments → RADIUS Mapping

| TIP-02 Concept | OpenTollGate Implementation | tollgate-auth RADIUS |
|---|---|---|
| **Payment** | Cashu token in HTTP POST body | Cashu token in User-Password or User-Name |
| **price_per_step** | Nostr tag: 210 sats/min, mint URL, min_steps | Hardcoded: 1 sat = 60 seconds. Mint allowlist via regex. |
| **Multiple mints** | Multiple price_per_step tags | `testMintPattern` regex (currently `(?i)test`) |
| **Multiple currencies** | Tags with different units | Not supported — sats only |
| **Token verification** | Provider verifies with mint | Mint `/v1/checkstate` (verify) + `cdk-cli receive` (redeem) |

### HTTP-01 → RADIUS Equivalence

| HTTP-01 Endpoint | RADIUS Equivalent |
|---|---|
| `POST /` (Cashu token in body) | Access-Request with Cashu token in password field |
| `200 OK` + kind 1022 session event | Access-Accept with Session-Timeout + Reply-Message |
| `400/402` + kind 21023 notice event | Access-Reject with Reply-Message (error description) |
| `GET /` (discovery) | Not applicable — RADIUS has no discovery mechanism |

### HTTP-02 → RADIUS Gap

| HTTP-02 Endpoint | RADIUS Equivalent |
|---|---|
| `GET /whoami` (returns MAC address) | Not needed — NAS provides Calling-Station-Id automatically |

RADIUS natively provides device identity (MAC) in every Access-Request via Calling-Station-Id. The OpenTollGate HTTP-02 spec exists because HTTP doesn't inherently know the client's MAC — the server must infer it from the network context. RADIUS solves this by design.

### HTTP-03 → RADIUS Accounting Gap

| HTTP-03 Endpoint | RADIUS Equivalent |
|---|---|
| `GET /usage` (current usage/allotment) | Acct-Interim-Update with Acct-Session-Time, Acct-Input/Output-Octets |

OpenTollGate HTTP-03 provides a usage endpoint where the customer can check remaining time/data. In RADIUS, this is handled by the accounting subsystem (RFC 2866) — the NAS periodically sends Interim-Update packets with usage counters. tollgate-auth outputs `Acct-Interim-Interval = 60` so the NAS reports every 60 seconds.

**Gap**: tollgate-auth doesn't yet process accounting packets. The NAS sends them but FreeRADIUS doesn't log them to a file that tollgate-auth reads. Future: parse accounting packets to track real-time usage and enable data-based metering.

### NOSTR-01 → No RADIUS Equivalent

Nostr relay integration (port 4242) is for out-of-band payment notification and remote management. RADIUS has no equivalent — it's a strictly request/response protocol between NAS and server. Nostr features (relay events, NIP-61 nutzap payments, profit sharing) would need a separate HTTP/Nostr service alongside FreeRADIUS.

## Architecture Comparison

### OpenTollGate tollgate-module-basic-go

```
┌─────────────────────────────────────────────────────────┐
│ OpenWRT Router (GL.iNet MT-3000)                         │
│                                                          │
│  main.go ──► HTTP server (port 2121)                     │
│     │          ├── POST / → Cashu token validation       │
│     │          ├── GET /  → Discovery event              │
│     │          ├── GET /whoami → MAC address              │
│     │          └── GET /usage → usage/allotment           │
│     │                                                     │
│     ├── merchant/    → Payment processing, pricing        │
│     ├── tollwallet/  → Cashu wallet operations            │
│     ├── chandler/    → Time/data usage tracking           │
│     ├── valve/       → Firewall rules (iptables/nftables) │
│     ├── lightning/   → Lightning address payouts          │
│     ├── crowsnest/   → Network monitoring, discovery      │
│     ├── janitor/     → Auto-updates                       │
│     └── wireless_gateway_manager/ → AP management         │
│                                                          │
│  Config: /etc/tollgate/config.json                        │
│  Pricing: price_per_step, metric, step_size               │
│  Payout: Lightning address with profit sharing             │
└─────────────────────────────────────────────────────────┘
```

### tollgate-auth

```
┌─────────────────────────────────────────────────────────┐
│ VPS / Server (Debian 12)                                 │
│                                                          │
│  FreeRADIUS (port 1812/2083)                             │
│     └── exec cashu-auth → tollgate-auth-radius (Go)      │
│          ├── extractPayment() → Cashu/LNURLw detection    │
│          ├── handleCashu() → decode/verify/redeem         │
│          ├── handleLNURLw() → demo accept                 │
│          └── SessionStore → JSON files per MAC            │
│                                                          │
│  tollgate-auth-ssh (port 2222)                           │
│     └── Cashu token as username → chroot jail + timer     │
│                                                          │
│  Shared: internal/cashu/                                  │
│     ├── token.go → V3/V4 decode                          │
│     ├── mint.go → checkstate verification                │
│     ├── wallet.go → cdk-cli redemption                   │
│     ├── replay.go → spent hash guard                     │
│     └── token.go → token hashing                         │
│                                                          │
│  Wallet: /var/lib/cashu-wallet (cdk-cli, group access)   │
│  Config: Go constants (hardcoded)                         │
│  Payout: Manual (cdk-cli melt/transfer)                   │
└─────────────────────────────────────────────────────────┘
```

## Feature-by-Feature Comparison

### Payment Verification

| Feature | OpenTollGate | tollgate-auth |
|---|---|---|
| **Verify with mint** | Yes (tollwallet) | Yes (mint `/v1/checkstate`) |
| **Redeem token** | Yes (NUT-03 swap) | Yes (`cdk-cli receive`) |
| **Mint allowlist** | Per price_per_step mint URL | `testMintPattern` regex |
| **Multiple mints** | Yes (multiple price_per_step tags) | Yes (regex matches multiple) |
| **Replay protection** | Token spent check via mint | SHA256 hash file (`radius-spent.txt`) |

### Session Management

| Feature | OpenTollGate | tollgate-auth |
|---|---|---|
| **Time-based metering** | Chandler time_usage_tracker (5s intervals) | RADIUS Session-Timeout (NAS-enforced) |
| **Data-based metering** | Chandler data_usage_tracker (bytes) | Not yet (Acct-Input/Output-Octets available) |
| **Balance precision** | Milli-sats (pricing_scale=1000) | Whole sats × 60 seconds |
| **Top-up (additive)** | Send another Cashu token → balance adds | Not yet (requires CoA or reconnect) |
| **Session reconnection** | Valve tracks MAC → allows reconnect | SessionStore JSON files per MAC |
| **Usage reporting** | MeteringReport every 5s | Acct-Interim-Interval = 60s |
| **Usage endpoint** | `GET /usage` → `used/allotted` | Not exposed to end user |

### Operator Payouts

| Feature | OpenTollGate | tollgate-auth |
|---|---|---|
| **Lightning address payout** | Automatic (lightning package) | Manual (`cdk-cli melt`) |
| **Profit sharing** | Configurable factors per Lightning address | Not implemented |
| **Payout threshold** | Configurable min balance | Not implemented |
| **Automatic payout** | Periodic routine | Manual |

### Pricing and Discovery

| Feature | OpenTollGate | tollgate-auth |
|---|---|---|
| **Pricing model** | price_per_step + metric + step_size | `RateSecPerSat = 60` (hardcoded) |
| **Dynamic pricing** | Nostr kind 10021 event (updatable) | Recompile Go binary |
| **Multi-currency** | Yes (sat, eur) | No (sat only) |
| **Customer discovery** | GET / → Nostr event | Not applicable (RADIUS) |

### Infrastructure Requirements

| Feature | OpenTollGate | tollgate-auth |
|---|---|---|
| **Hardware** | OpenWRT router (GL.iNet MT-3000) | Any Linux server with FreeRADIUS |
| **Runtime** | Go binary on OpenWRT | Go binary + FreeRADIUS |
| **Cashu wallet** | Embedded (tollwallet) | External (`cdk-cli`) |
| **Network control** | iptables/nftables (valve) | RADIUS Access-Accept/Reject |
| **WiFi management** | wireless_gateway_manager | External AP (UniFi, Cisco, etc.) |

## RADIUS Capabilities Not in OpenTollGate

RADIUS provides several features that OpenTollGate's HTTP/Nostr model doesn't address:

### RFC 2866: Accounting

RADIUS accounting provides standardized session tracking that HTTP doesn't natively offer:

| Acct Attribute | What It Provides | Use in tollgate-auth |
|---|---|---|
| `Acct-Status-Type Start` | Session began notification | Server knows when NAS activates the session |
| `Acct-Status-Type Stop` | Session ended + reason | Server knows why session ended (timeout, user disconnect, admin reset) |
| `Acct-Status-Type Interim-Update` | Periodic usage report | Real-time metering: bytes in/out, seconds elapsed |
| `Acct-Session-Time` | Total seconds connected | Cross-check against Session-Timeout |
| `Acct-Input-Octets` | Bytes downloaded | Data-based billing (price per MB) |
| `Acct-Output-Octets` | Bytes uploaded | Data-based billing |
| `Acct-Terminate-Cause` | Why session ended | User-Request, Session-Timeout, Admin-Reset, etc. |

### RFC 5176: CoA and Disconnect

Dynamic authorization lets the server change active sessions without the client reconnecting:

| Message | What It Does | tollgate-auth Use Case |
|---|---|---|
| **CoA-Request** | Change session attributes mid-flight | Extend Session-Timeout on top-up (user pays more without disconnect) |
| **Disconnect-Request** | Terminate session immediately | Kill session on payment failure or abuse |

OpenTollGate handles top-up by accepting another token via HTTP POST — the router's valve extends the firewall rule. RADIUS achieves the same via CoA, but the NAS (not the client) receives the instruction.

### RADIUS Transport Security

| Feature | RADIUS | OpenTollGate HTTP |
|---|---|---|
| **Shared secret auth** | UDP 1812 with shared secret | No auth on LAN HTTP |
| **TLS transport** | RadSec (TCP 2083 with TLS) | HTTPS possible but not in default config |
| **EAP encryption** | EAP-TTLS/PEAP tunnels credentials | HTTP on LAN (unencrypted) |
| **Message-Authenticator** | HMAC-MD5 integrity check on every packet | No integrity check on HTTP |

RADIUS provides cryptographic integrity and optional confidentiality at the transport layer. OpenTollGate's HTTP interface is typically unencrypted on LAN (the router's own HTTP server). This is acceptable for a local router but worth noting for enterprise deployments.

## RADIUS Constraints Not in OpenTollGate

RADIUS has fundamental limitations that OpenTollGate's HTTP/Nostr model avoids:

### Attribute Size Limit (253 bytes)

The single biggest constraint. RADIUS attributes are limited to 253 bytes (RFC 2865 §5). Cashu tokens are 230 bytes (no-DLEQ) or 378 bytes (with DLEQ). This means:

- No-DLEQ tokens (230 bytes) **barely fit** in a single RADIUS attribute
- Full DLEQ tokens (378 bytes) **cannot fit** — must be split or stripped
- Multi-proof tokens (128+ sat, ~1800 bytes) **impossible** via RADIUS attributes

OpenTollGate HTTP has no such limit — tokens go in HTTP POST bodies (unlimited size).

### No Discovery Mechanism

RADIUS is a direct auth protocol. The NAS sends an Access-Request, the server responds. There's no way for a client to "discover" available pricing or supported mints through RADIUS alone. OpenTollGate's Nostr events (kind 10021) provide rich service discovery.

### No Direct Client Communication

In the RADIUS model, the NAS (access point, switch) intermediates between client and server. The client never speaks RADIUS directly — it speaks EAP or PPPoE to the NAS, which translates to RADIUS. OpenTollGate's HTTP interface lets the client talk directly to the TollGate.

This means RADIUS can't do:
- Show the client a captive portal page
- Return a BOLT11 invoice for Lightning payment
- Provide a `/whoami` or `/usage` endpoint
- Accept payment via Nostr event

All of these require an HTTP or captive portal layer alongside RADIUS.

### Fixed Session Duration

RADIUS sessions are duration-based (Session-Timeout). When the timeout expires, the NAS disconnects the client. To extend the session, either:
1. The client reconnects with a new token (current tollgate-auth behavior)
2. The server sends a CoA-Request with a new Session-Timeout (future)

OpenTollGate uses continuous metering — the Chandler module tracks usage every 5 seconds and the valve updates firewall rules dynamically. There's no hard cutoff; the session continues as long as balance is positive.

## Mapping: Bootstrap Token Spec → RADIUS → OpenTollGate

The [tollgate-rs bootstrap spec](https://github.com/OpenTollGate/tollgate-rs/blob/master/docs/design/core/tollgate-bootstrap.md) defines the bootstrap token mechanism. Here's how each concept maps to RADIUS and OpenTollGate HTTP:

| Bootstrap Spec Concept | tollgate-auth (RADIUS) | OpenTollGate (HTTP) |
|---|---|---|
| Peer sends `BootstrapToken` | User-Password = cashuB... | POST / body = cashuB... |
| Provider verifies with mint | `/v1/checkstate` API call | tollwallet verify |
| Provider redeems token | `cdk-cli receive` (NUT-03) | tollwallet redeem |
| Balance tracked at scaled precision | No — whole sats × 60 seconds | Yes — milli-sats with pricing_scale |
| MeteringReport (every 5s) | Acct-Interim-Interval = 60s | Chandler module (5s) |
| Top-up (additive balance) | Not yet — requires CoA or reconnect | Send another POST / |
| Balance exhaustion → Reject | NAS disconnects at Session-Timeout | valve removes firewall rule |
| Upgrade to Spilman channel | Future: HTTP API after bootstrap | Future: same |
| No service without verification | Yes — strict verify-before-accept | Same policy |

## TollGate Protocol Spec Inventory

The OpenTollGate protocol defines these specifications (from [v0.1.0 release](https://github.com/OpenTollGate/tollgate/releases/tag/v0.1.0)):

### Protocol Layer

| Spec | Title | Key Concepts | tollgate-auth Coverage |
|---|---|---|---|
| **TIP-01** | Base Events | TollGate Discovery (kind 10021), Session (kind 1022), Notice (kind 21023), metric, step_size, device-identifier | Session → Access-Accept, Notice → Reply-Message, metric → milliseconds, device-id → Calling-Station-Id |
| **TIP-02** | Cashu Payments | price_per_step, multiple mints/currencies, Cashu token in transport | Full: Cashu token in password, mint allowlist, token verify + redeem |

### Interface Layer

| Spec | Title | Key Concepts | tollgate-auth Coverage |
|---|---|---|---|
| **HTTP-01** | HTTP Server | POST / (payment), GET / (discovery), port 2121 | POST / → Access-Request, response → Access-Accept/Reject |
| **HTTP-02** | Restrictive OS | GET /whoami (MAC address) | Not needed — RADIUS NAS provides MAC automatically |
| **HTTP-03** | Usage Endpoint | GET /usage (current usage/allotment) | Gap — needs accounting processing |
| **NOSTR-01** | Nostr Relay | Out-of-band events, port 4242 | Not applicable to RADIUS |

### Medium Layer

| Spec | Title | Key Concepts | tollgate-auth Coverage |
|---|---|---|---|
| **WIFI-01** | Beacon Frame | AP beacon advertisement | RADIUS handles all WiFi APs regardless of beacon |

### Proposed: RADIUS-01

Based on this analysis, tollgate-auth implicitly defines a new interface spec — **RADIUS-01**:

```
RADIUS-01 - RADIUS Authentication Interface

Payment:
  Access-Request with Cashu token in User-Password or User-Name
  Access-Accept with Session-Timeout, Reply-Message, Acct-Interim-Interval
  Access-Reject with Reply-Message (error description)

Device Identity:
  Calling-Station-Id (MAC address, provided by NAS)

Session Management:
  Session-Timeout (derived from payment amount)
  MAC-based reconnection (session record per MAC)
  Acct-Interim-Interval (periodic usage reports)

Top-Up:
  Not supported natively — requires CoA-Request (RFC 5176)
  or client reconnection with new token

Limitations:
  Attribute size: 253 bytes max (no-DLEQ tokens only)
  No client discovery (RADIUS is auth-only)
  No direct client communication (NAS intermediates)
  Fixed session duration (no continuous metering)

Transport:
  UDP 1812 (shared secret)
  TCP 2083 (RadSec / TLS)
```

## TollGate Ecosystem Components

| Component | Repo | Purpose | tollgate-auth Equivalent |
|---|---|---|---|
| **tollgate** | [OpenTollGate/tollgate](https://github.com/OpenTollGate/tollgate) | Protocol specs (TIP-01/02, HTTP-01/02/03, NOSTR-01, WIFI-01) | `docs/` (radius-payment-models.md, radius-token-size.md) |
| **tollgate-module-basic-go** | [OpenTollGate/tollgate-module-basic-go](https://github.com/OpenTollGate/tollgate-module-basic-go) | Reference Go implementation for OpenWRT | `cmd/tollgate-auth-radius/`, `config/freeradius/` |
| **tollgate-rs** | [OpenTollGate/tollgate-rs](https://github.com/OpenTollGate/tollgate-rs) | Rust implementation with Spilman channels | Future: Rust rewrite with native CDK |
| **tollgate-merchant-rs** | [OpenTollGate/tollgate-merchant-rs](https://github.com/OpenTollGate/tollgate-merchant-rs) | Merchant service with NIP-61 nutzap | Not implemented (manual wallet management) |
| **tollgate-captive-portal-site** | [OpenTollGate/tollgate-captive-portal-site](https://github.com/OpenTollGate/tollgate-captive-portal-site) | Web UI for Cashu/Lightning payment | `docs/index.html` (faucet only, no payment UI) |
| **tollgate-os** | Referenced in tollgate repo | Pre-built OpenWRT image | Not applicable (different platform) |

## Key Architectural Insight: Gateway vs Router

The fundamental difference is **where the payment gateway runs**:

- **OpenTollGate**: Runs **on the router itself** (OpenWRT). The router IS the payment gateway, firewall, and session manager. Direct control over iptables, WiFi, DHCP.
- **tollgate-auth**: Runs **alongside a RADIUS server** on a separate machine. The router (NAS) delegates auth decisions to the RADIUS server, which delegates to tollgate-auth. Indirect control via RADIUS attributes.

This has implications:

| Aspect | Router-native (OpenTollGate) | RADIUS-backend (tollgate-auth) |
|---|---|---|
| **Deployment** | Flash router with TollGateOS | Install FreeRADIUS on any Linux |
| **Firewall control** | Direct (iptables/nftables) | Indirect (RADIUS Access-Accept/Reject, CoA) |
| **Scalability** | Single AP | Centralized server for hundreds of APs |
| **Enterprise integration** | Standalone | Integrates with existing RADIUS infrastructure |
| **Session granularity** | 5-second metering intervals | 60-second accounting intervals (NAS-dependent) |
| **Top-up** | Immediate (valve updates rules) | CoA round-trip to NAS |
| **Multi-AP support** | Per-router instance | One server handles all APs |
| **Vendor lock-in** | OpenWRT only | Any RADIUS-speaking NAS (Cisco, Aruba, UniFi, etc.) |

## Recommendations for tollgate-auth

Based on this comparison, here are the highest-impact improvements aligned with the TollGate protocol:

### Priority 1: Accounting Processing (TIP-01 metering)

Process RFC 2866 accounting packets to track real session usage. This enables:
- Data-based billing (price per MB, not just time)
- Session termination on usage threshold
- Accurate usage reporting

### Priority 2: Top-Up via CoA (TIP-02 additive balance)

Implement CoA-Request (RFC 5176) to extend sessions mid-flight:
1. User pays via captive portal or HTTP API
2. Server sends CoA-Request to NAS with new Session-Timeout
3. Session extends without disconnect

This matches OpenTollGate's additive top-up model.

### Priority 3: Dynamic Pricing (TIP-01 price discovery)

Move pricing from hardcoded Go constants to a config file:
```json
{
  "rate_sec_per_sat": 60,
  "mint_pattern": "(?i)test",
  "lnurlw_default_sec": 3600
}
```

Eventually align with TIP-02 price_per_step format for multi-mint/multi-currency support.

### Priority 4: Lightning Address Payouts (OpenTollGate profit sharing)

Implement automatic Lightning address payouts:
1. Melt Cashu tokens via Lightning
2. Pay to configured Lightning addresses
3. Support profit sharing (70/30 split like tollgate-module-basic-go v0.0.3)

### Priority 5: Captive Portal Integration (HTTP-01 + HTTP-02)

Deploy alongside OpenNDS or CoovaChilli:
1. User connects to open WiFi
2. Redirected to captive portal
3. Portal accepts Cashu token via HTTP POST (no RADIUS attribute size limit)
4. Portal calls FreeRADIUS to authorize MAC
5. NAS enforces session

This combines OpenTollGate's HTTP interface with tollgate-auth's RADIUS backend — best of both worlds.

## See Also

- [TIP-01 — Base Events](https://github.com/OpenTollGate/tollgate/blob/main/TIP-01.md) — TollGate discovery, sessions, notices
- [TIP-02 — Cashu Payments](https://github.com/OpenTollGate/tollgate/blob/main/TIP-02.md) — Multi-mint pricing, Cashu token payment
- [HTTP-01 — HTTP Server](https://github.com/OpenTollGate/tollgate/blob/main/HTTP-01.md) — Minimal LAN payment interface
- [HTTP-02 — Restrictive OS](https://github.com/OpenTollGate/tollgate/blob/main/HTTP-02.md) — /whoami for MAC discovery
- [HTTP-03 — Usage Endpoint](https://github.com/OpenTollGate/tollgate/blob/main/HTTP-03.md) — Current usage/allotment query
- [NOSTR-01 — Nostr Relay](https://github.com/OpenTollGate/tollgate/blob/main/NOSTR-01.md) — Out-of-band event interface
- [WIFI-01 — Beacon Frame](https://github.com/OpenTollGate/tollgate/blob/main/WIFI-01.md) — WiFi beacon advertisement
- [TollGate Bootstrap Token Spec](https://github.com/OpenTollGate/tollgate-rs/blob/master/docs/design/core/tollgate-bootstrap.md) — Bootstrap token flow, balance tracking, top-up, Spilman upgrade
- [tollgate-module-basic-go](https://github.com/OpenTollGate/tollgate-module-basic-go) — Reference Go implementation for OpenWRT
- [tollgate-merchant-rs](https://github.com/OpenTollGate/tollgate-merchant-rs) — Merchant service with NIP-61 nutzap support
- [tollgate-captive-portal-site](https://github.com/OpenTollGate/tollgate-captive-portal-site) — Web UI for Cashu/Lightning payment
- [radius-payment-models.md](radius-payment-models.md) — RADIUS session lifecycle, accounting, CoA, infrastructure use cases
- [radius-token-size.md](radius-token-size.md) — Token size analysis and encoding approaches
- [RFC 2865](https://datatracker.ietf.org/doc/html/rfc2865) — RADIUS Authentication
- [RFC 2866](https://datatracker.ietf.org/doc/html/rfc2866) — RADIUS Accounting
- [RFC 5176](https://datatracker.ietf.org/doc/html/rfc5176) — Dynamic Authorization Extensions (CoA)
