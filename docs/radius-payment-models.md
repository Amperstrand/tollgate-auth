# RADIUS Payment Models: Session Management, Accounting, and Infrastructure

## Current State: What the RADIUS Client Sees

### Reply-Message (human-readable, included in Access-Accept)

The Go binary outputs `Reply-Message` via stdout, which FreeRADIUS parses and includes in Access-Accept. The connecting device (or NAS) receives:

```
Reply-Message = "Valid Cashu token: 8 sat = 8m access from https://testnut.cashu.exchange"
```

This tells the RADIUS client:
- **How many sats were paid** (8 sat)
- **How long the session lasts** (8 minutes)
- **Which mint issued the token** (testnut.cashu.exchange)

### Session-Timeout (derived from payment amount)

The Go binary outputs `Session-Timeout = N` to stdout (alongside Reply-Message). FreeRADIUS's exec module with `output_pairs = reply` parses both attributes into the Access-Accept. The NAS enforces this timeout — when it expires, the session is terminated.

Value is derived as: `Session-Timeout = tokenAmount × RateSecPerSat` (default: 1 sat = 60 seconds). For reconnections, the remaining time is calculated from the session start and duration.

Edge cases handled:
- **Zero-amount tokens**: Rejected before Session-Timeout is set (would give "no timeout" per RFC 2865 §5.27)
- **Reconnection with <1s remaining**: `Session-Timeout` is clamped to minimum 1 second

### Acct-Interim-Interval (NAS reports usage periodically)

The Go binary outputs `Acct-Interim-Interval = 60` in every Access-Accept. The NAS sends accounting Interim-Update packets every 60 seconds containing `Acct-Input-Octets`, `Acct-Output-Octets`, and `Acct-Session-Time`. This enables real-time usage metering.

### What's Still Missing

| RADIUS Attribute | Current Status | Purpose |
|---|---|---|
| `Reply-Message` | **Included** — sats, duration, mint | Human-readable payment confirmation |
| `Session-Timeout` | **Included** — derived from payment amount | NAS enforces session duration |
| `Acct-Interim-Interval` | **Included** — 60 seconds | NAS sends usage updates during session |
| `Class` | Not configured | Operator account identifier for accounting |
| `CoA / Disconnect` | Not configured | Mid-session extension or termination |
| `Accounting (RFC 2866)` | Not configured | Session start/stop/usage tracking |

## RADIUS Session Lifecycle

### How RADIUS Sessions Work

```
┌─────────────────────────────────────────────────────────────────────┐
│ RADIUS Session Lifecycle                                            │
│                                                                     │
│  1. Access-Request  ──►  Server validates payment                  │
│  2. Access-Accept   ◄──  Session-Timeout, Reply-Message           │
│     (NAS starts session timer)                                      │
│                                                                     │
│  3. Acct-Request (Start)   ──►  Session began, NAS = ...          │
│     (server records session start)                                  │
│                                                                     │
│  4. Acct-Request (Interim) ──►  Usage so far: X bytes, Y seconds  │
│     (periodic, every Acct-Interim-Interval seconds)                │
│                                                                     │
│  5. Acct-Request (Stop)    ──►  Session ended, total: X bytes     │
│     (server records final usage)                                    │
│                                                                     │
│  ── OR ──                                                           │
│                                                                     │
│  5a. CoA-Request (server → NAS)                                    │
│      "Extend this session by N more seconds"                       │
│      "Terminate this session now"                                   │
│                                                                     │
│  5b. Disconnect-Request (server → NAS)                             │
│      "Kill this session immediately"                                │
└─────────────────────────────────────────────────────────────────────┘
```

### RFC 2866: RADIUS Accounting

RADIUS Accounting packets are sent **from the NAS to the server** on UDP port 1813:

| Acct-Status-Type | When Sent | Key Attributes |
|---|---|---|
| `Start` (1) | Session begins | User-Name, NAS-IP-Address, Acct-Session-Id, Framed-IP-Address |
| `Stop` (2) | Session ends | + Acct-Input-Octets, Acct-Output-Octets, Acct-Session-Time, Acct-Terminate-Cause |
| `Interim-Update` (3) | Periodic usage report | Same as Stop, sent every `Acct-Interim-Interval` seconds |
| `Accounting-On` (7) | NAS reboots | "All previous sessions are dead" |
| `Accounting-Off` (8) | NAS shuts down | "End all sessions" |

**For tollgate-auth**: Accounting Start/Stop tells the server when a session actually begins and ends (not just when auth happened). Interim-Update provides real-time usage metering — the server could track bytes transferred and terminate sessions that exceed bandwidth limits.

### RFC 5176: Dynamic Authorization (CoA and Disconnect)

The RADIUS server can **push changes to an active session** mid-flight:

| Message | Direction | Purpose |
|---|---|---|
| CoA-Request | Server → NAS | Change session attributes (extend timeout, change bandwidth) |
| Disconnect-Request | Server → NAS | Terminate session immediately |
| CoA-ACK/NAK | NAS → Server | Accept or reject the change |

**CoA-Request** can carry any session attribute, including:
- `Session-Timeout` — extend or reduce remaining time
- `Filter-Id` — change firewall/bandwidth rules
- `Reply-Message` — show a message to the user

**For tollgate-auth**: When a user tops up (pays again via captive portal or HTTP API), the server sends a CoA-Request to the NAS with a new `Session-Timeout`. The NAS extends the session without disconnecting the user.

### How Commercial Hotspots Handle Session Extension

Hotel/airport WiFi (Cisco ISE, Aruba ClearPass, MikroTik HotSpot) uses a combination of these mechanisms:

1. **Initial auth**: RADIUS Access-Accept with `Session-Timeout` (e.g., 60 minutes)
2. **Captive portal top-up**: User clicks "add more time" → portal sends new auth request → RADIUS responds with new `Session-Timeout`
3. **CoA extension**: For mid-session extension, the portal triggers a CoA-Request to the NAS controller
4. **Accounting tracking**: Interim-Update every 60 seconds reports usage back to the billing system

## Accounting: Operator Credit Collection

### Problem: Who Gets Paid?

In the current model, the RADIUS server redeems Cashu tokens to its own wallet. But what if there are multiple operators? A WiFi hotspot operator, a VPN provider, and a bandwidth wholesaler might all need to split payments.

### Solution: Class Attribute for Operator Account

RADIUS `Class` (attribute 25) is an opaque blob sent in Access-Accept that the NAS echoes back in Accounting packets. It can carry an operator identifier:

```
# In Access-Accept:
Class = "acct:satoshi@getalby.com"       # Lightning address
Class = "acct:npub1abc...xyz"             # Nostr public key
Class = "acct:bc1q...wallet"              # Bitcoin on-chain address
```

The accounting system uses this to route collected payments to the correct operator.

### Lightning Addresses as Account Identifiers

A Lightning address (`user@domain`) is an LNURL-pay endpoint:

| Property | Value |
|---|---|
| Format | `user@domain.com` |
| Length | 10-50 chars (fits RADIUS 253-byte limit) |
| Resolution | `https://domain.com/.well-known/lnurlp/user` → LNURL-pay JSON |
| Payment | Any Lightning wallet can pay to it |
| RADIUS attribute | User-Name or Class |

The RADIUS server could accept a Lightning address in the User-Name field alongside the payment token, then route redeemed sats to that address via Lightning (using `cdk-cli melt` → pay invoice to the LNURL-pay endpoint).

### npub (Nostr Public Keys) as Account Identifiers

Nostr public keys (`npub1...`) are bech32-encoded 32-byte secp256k1 public keys:

| Property | Value |
|---|---|
| Format | `npub1` + bech32 (58 chars) |
| Length | ~63 chars (fits RADIUS 253-byte limit) |
| Use case | Decentralized identity, Nostr zaps |
| Payment | Lightning zap to npub via Nostr relay |
| RADIUS attribute | User-Name or Class |

### Account Format in RADIUS

```
# User sends token + account identifier:
Access-Request
    User-Name = "npub1abcdef..."           # Account for credit
    User-Password = "cashuBo2FteB5odHR..." # Payment token

# Server responds:
Access-Accept
    Reply-Message = "Valid Cashu token: 8 sat = 8m, credit to npub1abcdef..."
    Session-Timeout = 480
    Class = "npub1abcdef..."                # Echoed in accounting
```

**Note**: This only works when the token is in the password field (not username). When the token is in the username field (PEAP+MSCHAPv2 path), there's no separate field for the account identifier. A dedicated prefix could work: `cashuBo2...|acct:npub1...` but the RADIUS 253-byte limit makes this tight for full DLEQ tokens.

## RADIUS Session Renewal and Top-Up

### Current Model: Bootstrap Token (Pay-Per-Session)

The user pays once upfront. When time expires:
1. NAS sends Session-Timeout event → disconnects user
2. User reconnects → sends new token → new session

This is the simplest model. No in-session payment. Works today.

### Future Model 1: Captive Portal Top-Up

```
1. User connects via Cashu token (bootstrap)
2. Session-Timeout = 480 seconds (8 minutes)
3. At minute 7, captive portal shows "Add more time?"
4. User pastes another Cashu token in web form
5. Server sends CoA-Request to NAS with new Session-Timeout
6. Session extends without disconnect
```

This uses the standard captive portal + CoA pattern that hotel WiFi uses today. The HTTP API handles payment (no RADIUS attribute size limits). RADIUS handles session management only.

### Future Model 2: Spilman Channel (Streaming Payment)

After the bootstrap token gets the user online, an HTTP API establishes a Cashu [Spilman payment channel](https://github.com/cashubtc/nuts/pull/229):

```
1. Bootstrap: Cashu token in RADIUS password → 8 min access
2. HTTP API: User opens Spilman channel with provider
3. Streaming: Channel sends micropayments every second (1 sat/sec)
4. RADIUS CoA: Server extends Session-Timeout as payments arrive
5. Disconnect: If channel stops paying, server sends Disconnect-Request
```

This is the full tollgate spec vision. RADIUS handles only the bootstrap; HTTP handles ongoing payment.

### Future Model 3: Prepaid Metering via Accounting

```
1. User pays 100 sat upfront → gets Session-Timeout = 6000 (100 min)
2. NAS sends Interim-Update every 60 seconds with:
   - Acct-Input-Octets (bytes downloaded)
   - Acct-Output-Octets (bytes uploaded)
   - Acct-Session-Time (seconds connected)
3. Server tracks usage: if bytes > threshold, send CoA with bandwidth throttle
4. If time runs out, NAS disconnects → user pays again
```

This allows the server to meter in real-time and enforce both time-based AND data-based limits.

## RADIUS + Tollgate: Use Cases

### WiFi (WPA2-Enterprise) — Current Implementation

The primary use case. Any access point that supports WPA2-Enterprise (802.1X) can use tollgate-auth:

| Vendor | Products | RADIUS Support |
|---|---|---|
| UniFi | UAP, UDM | Yes — WPA2-Enterprise with RADIUS |
| OpenWRT | Any supported hardware | Yes — hostapd RADIUS backend |
| MikroTik | RouterOS APs | Yes — RADIUS client with accounting |
| Cisco | Aironet, Meraki | Yes — ISE integration |
| Aruba | Instant, Mobility Controller | Yes — ClearPass RADIUS |

**Who would pay**: Café owner, hotel, co-working space, conference venue. No payment processor needed — just ecash tokens.

### Wired 802.1X — Smart Switches

Network switches use 802.1X port-based authentication. When a device plugs into an Ethernet port, the switch sends a RADIUS Access-Request before enabling the port:

```
Device ──plug──► Switch Port ──RADIUS──► tollgate-auth ──accept──► Port Enabled
                                         ──reject──► Port Disabled
```

**Use cases**:
- **Co-working spaces**: Pay per hour for desk Ethernet
- **Conference rooms**: Meeting room network access
- **Server racks**: Colocation facilities — pay for uplink time
- **EV charging stations**: Charging stations use 802.1X for network access (the charging protocol is separate, but the station needs backhaul)

**Key RADIUS attributes**:
- `NAS-Port-Type` = Ethernet (15)
- `NAS-Port` = physical port number
- `Calling-Station-Id` = device MAC

### VPN — Remote Access

VPN concentrators (OpenVPN, WireGuard+plugin, IPsec/StrongSwan, pfSense) authenticate users via RADIUS:

```
VPN Client ──connect──► VPN Server ──RADIUS──► tollgate-auth ──accept──► Tunnel Established
```

**Use cases**:
- **Disposable VPN access**: Pay per session, no account needed
- **Bandwidth marketplace**: VPN operators sell access by the minute
- **Privacy service**: No registration, no email — just ecash

**Key RADIUS attributes**:
- `Framed-Protocol` = PPP (1)
- `Framed-IP-Address` = assigned VPN IP
- `Acct-Input-Octets` / `Acct-Output-Octets` = bandwidth metering

### PAM-RADIUS — SSH/System Login via RADIUS Backend

[FreeRADIUS/pam_radius](https://github.com/FreeRADIUS/pam_radius) allows any Linux system to authenticate SSH logins via RADIUS. This is effectively tollgate-auth-ssh but with a RADIUS backend:

```
SSH Client ──login──► Linux Server ──PAM──► pam_radius ──RADIUS──► tollgate-auth
```

**Setup**:
```bash
# Install PAM RADIUS module
apt install libpam-radius-auth

# Configure RADIUS server
echo "127.0.0.1 tollgate 3" >> /etc/pam_radius_auth.conf

# Enable for SSH (add before @include common-auth)
sed -i '1i auth sufficient pam_radius_auth.so' /etc/pam.d/sshd
```

**Critical limitation — 128-byte PAP password limit**: RADIUS `User-Password` is limited to 128 bytes by the protocol. Cashu tokens are 230 bytes (no-DLEQ) and cannot fit through plain PAP. Only `lnurlw` codes (~60 bytes) work. Cashu tokens require EAP-TTLS+PAP (tunneled inside TLS), which WiFi clients use but PAM/OpenVPN do not.

| Credential | Size | PAM-RADIUS | WiFi (EAP-TTLS) |
|---|---|---|---|
| `lnurlw` code | ~60 bytes | ✅ Fits | ✅ Fits |
| Cashu no-DLEQ | 230 bytes | ❌ Truncated at 128 | ✅ Fits (no 253-byte limit inside TLS) |
| Cashu with DLEQ | 378 bytes | ❌ Truncated at 128 | ✅ Split across two fields |

**PAM-RADIUS vs tollgate-auth-ssh comparison**:

| Feature | tollgate-auth-ssh | PAM-RADIUS |
|---|---|---|
| Cashu token auth | ✅ Full 230-byte tokens | ❌ Only lnurlw (128-byte limit) |
| User creation | ✅ Ephemeral guest accounts | ❌ User must exist in /etc/passwd |
| Chroot jail | ✅ Busybox chroot | ❌ Needs separate PAM modules |
| Session timeout | ✅ Goroutine timer | ❌ PAM doesn't enforce Session-Timeout |
| Cleanup on disconnect | ✅ Account deleted | ❌ Manual cleanup needed |

**Verdict**: PAM-RADIUS is not a viable replacement for tollgate-auth-ssh. It can only authenticate `lnurlw` codes, cannot create restricted users, and cannot enforce timeouts. The custom SSH server approach is strictly superior for ecash-based SSH access.

### 5G / Cellular Core (Diameter Credit Control)

4G/5G cellular networks use **Diameter** (not RADIUS) for charging, but the concepts map directly:

| RADIUS | Diameter (4G/5G) | Purpose |
|---|---|---|
| Access-Request | CCR (Credit-Control-Request) | "Can this user connect?" |
| Access-Accept | CCA (Credit-Control-Answer) | "Yes, N units granted" |
| Session-Timeout | Granted-Service-Unit | "You have N seconds/bytes" |
| Acct-Interim-Update | CCR Update | "Usage report: X bytes used" |
| CoA-Request | RAR (Re-Auth-Request) | "Change this session's rules" |

**DCCA** (Diameter Credit Control Application, [RFC 4006](https://datatracker.ietf.org/doc/html/rfc4006)) is how carriers do real-time prepaid charging:

1. Subscriber connects → PCEF (Policy and Charging Enforcement Function) sends CCR to OCS (Online Charging System)
2. OCS checks balance → grants N megabytes in CCA
3. PCEF meters data usage → sends CCR Update when N MB is consumed
4. OCS grants more MB (if balance allows) or terminates

**For tollgate-auth**: A Cashu-over-Diameter gateway could plug into the same billing model. Instead of checking a prepaid balance in a carrier database, the gateway redeems Cashu tokens. The metering is identical — bytes or seconds granted, usage tracked, top-up on exhaustion.

**Practical path**: Use a Diameter-to-RADIUS gateway (most 4G/5G core vendors support this) to bridge tollgate-auth into the cellular charging chain.

### PPPoE / ISP Access

PPPoE (PPP over Ethernet) is how many ISPs authenticate subscribers. The PPPoE concentrator sends RADIUS Access-Request:

```
Subscriber ──PPPoE──► Concentrator ──RADIUS──► tollgate-auth ──accept──► Internet Access
```

**Use cases**:
- **Prepaid ISP**: Pay per minute/GB of internet access
- **Community networks**: Mesh networks where members pay for uplink
- **Developing regions**: Micro-ISPs where users buy connectivity in small increments

### Satellite Internet

Satellite terminals (Starlink, HughesNet, etc.) authenticate via RADIUS or Diameter. The latency is high (500-700ms for GEO, 20-40ms for LEO) but the auth flow is the same.

**Use case**: Pay-per-use satellite bandwidth for maritime, remote sites, or disaster response.

### EV Charging and Smart Energy

EV charging stations use OCPP (Open Charge Point Protocol) for charging control, but many use 802.1X/RADIUS for the backhaul network connection. The charging station itself needs network access to talk to the central system.

More interestingly: some energy metering systems use RADIUS for authenticating energy delivery. Prepaid electricity meters could theoretically use a RADIUS-like auth flow:

```
Meter ──auth──► Energy Controller ──RADIUS──► tollgate-auth ──accept──► Energy flows
```

This is speculative — real energy distribution uses different protocols (DLMS/COSEM, IEC 62056) — but the pattern (authenticate → meter → bill) is universal.

### IoT Device Authentication

IoT gateways authenticate devices via RADIUS before allowing them on the network. Pay-per-device or pay-per-connection:

```
Sensor ──connect──► IoT Gateway ──RADIUS──► tollgate-auth ──accept──► Data flows
```

**Use case**: Shared IoT infrastructure (community weather stations, environmental sensors) where each device pays for its own uplink.

## RADIUS Attributes for Tollgate Accounting

### Attributes the Go Binary Should Output

```go
// Payment confirmation (human-readable)
fmt.Printf("Reply-Message = \"Valid Cashu token: %d sat = %dm access from %s\"\n", amount, minutes, mint)

// Session duration (NAS-enforced)
fmt.Printf("Session-Timeout = %d\n", seconds)

// Accounting interval (NAS sends usage updates every N seconds)
fmt.Printf("Acct-Interim-Interval = 60\n")

// Operator account (echoed in accounting packets for payment routing)
if operatorAccount != "" {
    fmt.Printf("Class = \"acct:%s\"\n", operatorAccount)
}
```

### Accounting Flow

```
NAS                              FreeRADIUS                    tollgate-auth
 │                                   │                              │
 │──Access-Request──────────────────►│──exec cashu-auth────────────►│
 │                                   │                              │──decode/verify/redeem
 │                                   │◄──stdout─────────────────────│
 │                                   │  Reply-Message = "8 sat..."  │
 │                                   │  Session-Timeout = 480       │
 │                                   │  Acct-Interim-Interval = 60  │
 │                                   │  Class = "acct:npub1..."     │
 │◄──Access-Accept──────────────────│                              │
 │   Session-Timeout = 480           │                              │
 │   Class = "acct:npub1..."         │                              │
 │                                   │                              │
 │──Acct-Request (Start)────────────►│──log to accounting file───►  │
 │   Acct-Session-Time = 0           │                              │
 │   Class = "acct:npub1..."         │                              │
 │                                   │                              │
 │──Acct-Request (Interim)──────────►│──log usage─────────────────► │
 │   Acct-Session-Time = 60          │                              │
 │   Acct-Input-Octets = 1.2MB       │                              │
 │   Acct-Output-Octets = 800KB      │                              │
 │                                   │                              │
 │──Acct-Request (Stop)─────────────►│──log final usage───────────► │
 │   Acct-Session-Time = 480         │                              │
 │   Acct-Input-Octets = 9.6MB       │                              │
 │   Acct-Terminate-Cause = Timeout  │                              │
```

### Payment Settlement

When the server redeems a Cashu token and needs to credit an operator:

1. **Direct redemption**: Token redeemed to server wallet → operator's account is a label in the accounting log
2. **Lightning settlement**: Server melts ecash → pays to operator's Lightning address
3. **Nostr zap**: Server sends zap to operator's npub
4. **On-chain**: Server sweeps to operator's Bitcoin address (high friction, batch settlement)

## Summary: RADIUS + Tollgate Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         Payment Layer                                   │
│  Cashu token → RADIUS password → FreeRADIUS → Go binary → wallet       │
│  Lightning preimage → RADIUS password → hash verification → access     │
│  LNURLw → RADIUS password → claim payment → access                     │
│                                                                         │
│  Operator account (Lightning address, npub) in User-Name or Class      │
│  Server collects payments, settles to operator accounts                 │
├─────────────────────────────────────────────────────────────────────────┤
│                      Session Layer                                      │
│  Session-Timeout = amount × rate (derived from payment)                │
│  Acct-Interim-Interval = 60 (NAS reports usage every minute)           │
│  Accounting Start/Stop/Interim → usage tracking                        │
│  CoA → mid-session extension (top-up via captive portal)               │
│  Disconnect-Request → terminate on non-payment                         │
├─────────────────────────────────────────────────────────────────────────┤
│                     Infrastructure Layer                                │
│  WiFi (WPA2-Enterprise)   → access points                              │
│  VPN                      → concentrators                              │
│  Wired 802.1X             → switches                                   │
│  PAM-RADIUS               → SSH/system login                           │
│  PPPoE                    → ISP concentrators                           │
│  5G/4G (via Diameter gw)  → cellular core                              │
│  Captive portal           → hotel/airport WiFi                         │
│  Satellite terminal       → remote connectivity                        │
│  IoT gateway              → device authentication                      │
│  EV charging backhaul     → station network access                     │
└─────────────────────────────────────────────────────────────────────────┘
```

## See Also

### RADIUS RFC Reference

| RFC | Title | Key Sections for Tollgate |
|---|---|---|
| [RFC 2865](https://datatracker.ietf.org/doc/html/rfc2865) | RADIUS Authentication | §5.27 Session-Timeout, §5.18 Reply-Message, §5.25 Class, §5.2 User-Password (253-byte limit) |
| [RFC 2866](https://datatracker.ietf.org/doc/html/rfc2866) | RADIUS Accounting | §5.1 Acct-Status-Type (Start/Stop/Interim), §5.3 Acct-Session-Time, §5.26 Acct-Input-Octets, §5.27 Acct-Output-Octets |
| [RFC 2869](https://datatracker.ietf.org/doc/html/rfc2869) | RADIUS Extensions | §5.9 Acct-Interim-Interval |
| [RFC 3576](https://datatracker.ietf.org/doc/html/rfc3576) | Dynamic Authorization Extensions | CoA-Request, Disconnect-Request (obsoleted by RFC 5176 but widely implemented) |
| [RFC 5176](https://datatracker.ietf.org/doc/html/rfc5176) | Dynamic Authorization Extensions | CoA-Request with Session-Timeout, Disconnect-Request |
| [RFC 4006](https://datatracker.ietf.org/doc/html/rfc4006) | Diameter Credit-Control Application | Real-time prepaid charging, Granted-Service-Unit (maps to Session-Timeout) |
| [RFC 3588](https://datatracker.ietf.org/doc/html/rfc3588) | Diameter Base Protocol | NASREQ application, RADIUS-to-Diameter gateway mapping |
| [IEEE 802.1X](https://standards.ieee.org/standard/802_1X-2020.html) | Port-Based Network Access Control | EAPOL, supplicant/authenticator/authentication server model |

### Other References

- [FreeRADIUS CoA Guide](https://www.freeradius.org/documentation/freeradius-server/4.0.0/howto/protocols/radius/using_coa.html)
- [FreeRADIUS/pam_radius](https://github.com/FreeRADIUS/pam_radius) — PAM module for RADIUS authentication
- [radius-token-size.md](radius-token-size.md) — Token size analysis and encoding approaches
- [radius-testing.md](radius-testing.md) — Testing guide with eapol_test examples
- [OpenTollGate Bootstrap Spec](https://github.com/OpenTollGate/tollgate-rs/blob/master/docs/design/core/tollgate-bootstrap.md)
- [OpenTollGate/tollgate](https://github.com/OpenTollGate/tollgate) — Captive portal approach with OpenNDS + BTCPayServer
