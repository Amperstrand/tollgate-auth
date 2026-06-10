# RADIUS Attribute Length vs Cashu Token Size

## The Problem

RADIUS attributes (User-Name, User-Password) are limited to **253 bytes**. Cashu V4 tokens with DLEQ proofs are **378 bytes** regardless of the token amount. This means full Cashu tokens **cannot be sent via raw RADIUS** (radclient, simple PAP auth).

## Token Size Measurements

All tokens minted from `testnut.cashu.exchange`, single proof:

| Amount | DLEQ | Token Length | Fits RADIUS (253 bytes)? |
|--------|------|-------------|--------------------------|
| 1 sat  | Yes  | **378 bytes** | No |
| 2 sat  | Yes  | **378 bytes** | No |
| 4 sat  | Yes  | **378 bytes** | No |
| 8 sat  | Yes  | **378 bytes** | No |
| 1 sat  | No   | **230 bytes** | **Yes** |
| 8 sat  | No   | **230 bytes** | **Yes** |
| 128 sat| Yes  | ~1800 bytes   | No |

### Why 378 vs 230

Token size is **fixed** for any single-proof token — the amount field is just a small integer. The difference comes from the **DLEQ proof**:

| Component | With DLEQ | Without DLEQ |
|-----------|-----------|--------------|
| Mint URL | ~31 bytes | ~31 bytes |
| Proof secret | 32 bytes | 32 bytes |
| Proof public key (C) | 33 bytes | 33 bytes |
| Keyset ID | 8 bytes | 8 bytes |
| **DLEQ proof (s, e, r)** | **96 bytes** | **0 bytes** |
| CBOR + base64url overhead | ~178 bytes | ~126 bytes |
| **Total** | **378 bytes** | **230 bytes** |

### DLEQ proofs are optional

DLEQ (Discrete Log Equality) proofs are defined in [NUT-12](https://github.com/cashubtc/nuts/blob/main/12.md). They allow the **receiver** to verify the mint didn't cheat during blind signing — a client-side integrity check. They are **NOT** required for:

- Mint `checkstate` endpoint (verifies proofs are unspent)
- NUT-03 swap (`cdk-cli receive` — redeems proofs to a new wallet)
- Token validity or spending

Stripping DLEQ proofs produces 230-byte tokens that **fit in a single RADIUS attribute** (230 < 253).

## Solutions

### Primary: No-DLEQ token in single field (230 bytes)

Strip the DLEQ proof from the token before encoding. The resulting 230-byte token fits entirely in a single RADIUS attribute — either password or username.

**Password-only** (simplest, recommended for real WiFi clients):
```
identity = "any-user"
password = "cashuBo2FteB5odHRwczovL3Rlc3RudXQuY2FzaHUuZXhjaGFuZ2VhdWNz..."
```

**Username-only** (for clients that send credentials in identity field):
```
identity = "cashuBo2FteB5odHRwczovL3Rlc3RudXQuY2FzaHUuZXhjaGFuZ2VhdWNz..."
password = "anything"
```

The Go binary's `extractPayment()` detects full tokens in either field — no special handling needed.

### Fallback: Split token via EAP-TTLS+PAP (378 bytes)

For full DLEQ tokens (378 bytes), FreeRADIUS's `diameter2vp` function enforces a 253-byte limit even inside EAP-TTLS tunnels. The token is split:

- **Password** (inner): first 200 bytes (starts with `cashuB` prefix)
- **Identity/Username** (inner): remaining 178 bytes (raw base64url)

```
┌─────────────────────────────────────────────────────┐
│ RADIUS Access-Request (outer)                        │
│   EAP-Message = <TLS tunnel data>                    │
│                                                      │
│   ┌──────────────────────────────────────────────┐  │
│   │ TLS Tunnel (encrypted)                        │  │
│   │   User-Name = "...kJdi8TOx3JhGgvJSMGwO0fD6U" │  │ ← 178 bytes (tail)
│   │   User-Password = "cashuBo2FteB5odHRwczov..."  │  │ ← 200 bytes (head)
│   └──────────────────────────────────────────────┘  │
│                                                      │
│   Server reassembles: password + username = 378 bytes│
└─────────────────────────────────────────────────────┘
```

The Go binary detects the split (password starts with `cashuB` + username is base64url-only, no known prefix) and concatenates them back into the full token.

## What Works and What Doesn't

| Transport | No-DLEQ (230 bytes) | Full DLEQ (378 bytes) | LNURLw (~50 bytes) |
|-----------|---------------------|-----------------------|--------------------|
| **Raw RADIUS (radclient)** | **Yes** — fits in attribute | No — exceeds 253-byte limit | Yes |
| **EAP-TTLS+PAP (single field)** | **Yes** — token in password or username | No — exceeds 253 in tunnel | Yes |
| **EAP-TTLS+PAP (split)** | N/A — no split needed | **Yes** — 200b + 178b | Yes |
| **PEAP+MSCHAPv2** | **Yes** — fits in User-Name | No — must fit in User-Name alone | Yes |
| **RadSec (TLS transport)** | **Yes** — fits in attribute | No — same 253-byte limit | Yes |

**RadSec encrypts the RADIUS transport but does NOT remove the 253-byte attribute limit.** RadSec = TLS for the RADIUS packet itself, not for the inner auth data.

## Testing with eapol_test

`eapol_test` (from `wpasupplicant` package or `eapoltest` on Ubuntu) simulates a real WiFi supplicant doing EAP-TTLS+PAP. The `scripts/mint-testnut.js` script handles all modes:

```bash
# Install (Ubuntu)
sudo apt install eapoltest

# Resolve IP first — eapol_test requires IP address, not hostname
RADIUS_IP=$(dig +short nodns.shop A | head -1)

# Mode 1: No-DLEQ token in password (recommended, simplest)
node scripts/mint-testnut.js --no-dleq --write-eapol-config /tmp/eapol.conf
eapol_test -c /tmp/eapol.conf -a "$RADIUS_IP" -s tollgate -M aa:bb:cc:dd:ee:ff -r 1 -t 30

# Mode 2: No-DLEQ token in identity (username)
node scripts/mint-testnut.js --no-dleq --token-in-username --write-eapol-config /tmp/eapol.conf
eapol_test -c /tmp/eapol.conf -a "$RADIUS_IP" -s tollgate -M aa:bb:cc:dd:ee:ff -r 1 -t 30

# Mode 3: Full DLEQ token with split (fallback for full tokens)
node scripts/mint-testnut.js --write-eapol-config /tmp/eapol.conf --eap-identity ci-split-user
eapol_test -c /tmp/eapol.conf -a "$RADIUS_IP" -s tollgate -M aa:bb:cc:dd:ee:ff -r 1 -t 30
```

Note: eapol_test flags differ between builds. Ubuntu's `eapoltest` package uses `-p` for port and `-s` for shared secret. Builds from source may differ.

Expected output on success:
```
MPPE keys OK: 1  mismatch: 0
SUCCESS
```

## Implications for Real WiFi Clients

| Mode | User experience | Practical for phones? |
|------|----------------|-----------------------|
| No-DLEQ in password | Paste token in password field, any username | **Yes** — single paste |
| No-DLEQ in username | Paste token in identity field | **Yes** — single paste |
| Split DLEQ token | Paste two separate strings | No — requires two pastes |

The no-DLEQ approach makes Cashu tokens practical for real WiFi clients. Users paste a single 230-character string into the password field — the same UX as typing a WiFi password.

## Beyond Cashu: Other Approaches

### LNURL-withdraw codes

LNURLw codes are bech32-encoded, typically 50-80 bytes. They trivially fit in any RADIUS attribute under any EAP method. No splitting, no DLEQ stripping, no special handling. This is the simplest payment method for RADIUS — but requires the server to actually claim the Lightning payment (not yet implemented in tollgate-auth).

### Shorter mint URLs

The mint URL `https://testnut.cashu.exchange` is 31 bytes. Running a mint at a shorter domain (e.g. `https://t.co`, 13 bytes) would reduce no-DLEQ tokens from 230 to ~210 bytes. Every byte of URL length saves ~1.33 bytes in the base64url-encoded token. Free optimization if you control the mint domain.

### Token reference system (eliminates size constraint)

A web service stores the full token and returns a short reference hash:

```
User pastes: cashuB...378 bytes... → POST /api/lock → returns "abc123def456"
User sends:  "cashu-ref:abc123def456" (25 bytes) via RADIUS password field
Server looks up "abc123def456" → retrieves full token → validates → accepts
```

This works for any token size (378 bytes, 1800 bytes for multi-proof tokens, anything). Requires a web service that the user hits before connecting to WiFi. The reference must be unguessable (cryptographic hash or UUID). Adds a network round-trip but eliminates the RADIUS size constraint entirely.

### Captive portal (sidesteps RADIUS entirely)

Instead of sending the token through RADIUS attributes, use a web portal:

1. User connects to open WiFi (no WPA2-Enterprise auth)
2. Firewall redirects all HTTP to captive portal
3. User pastes Cashu token in web form (HTTP POST — no size limit)
4. Portal validates token, tells RADIUS/firewall to authorize the MAC
5. Firewall lifts redirect for that MAC address

This is how hotel/airport WiFi works today. The token never touches RADIUS attributes — it goes in an HTTP POST body (unlimited size). The RADIUS layer handles only session management after authorization.

The [OpenTollGate/tollgate](https://github.com/OpenTollGate/tollgate) project uses this approach with OpenNDS (captive portal on OpenWRT) and BTCPayServer for payment processing. It also handles sustained connections — the portal manages ongoing sessions rather than requiring re-authentication for each RADIUS reconnection. This is the most practical path for real-world deployment with any payment size.

### Ark / Bark (Bitcoin over RADIUS?)

[Ark](https://ark-protocol.org/) is a client-server Bitcoin scaling protocol using Virtual Transaction Outputs (VTXOs) — off-chain pre-signed transaction trees anchored on-chain. [Bark](https://gitlab.com/ark-bitcoin/bark) (by [Second](https://second.tech/)) is the reference Rust wallet implementation.

**VTXO structure**: A VTXO is NOT a portable token like Cashu. It's a series of pre-signed bitcoin transactions in a tree structure. A VTXO "proof" (V-PACK) includes Merkle paths from the leaf to the on-chain anchor, ASP signatures at each level, and other outputs in the tree. Proof size depends on tree depth — each level adds ~64 bytes for a signature plus variable output data. Full proofs are hundreds to thousands of bytes.

**Private keys**: Bark uses BIP39 12-word mnemonic seeds (standard seed phrase format). Key sizes:

| Format | Length | Fits RADIUS (253)? |
|--------|--------|---------------------|
| 12-word seed phrase | ~160 chars | **Yes** |
| 24-word seed phrase | ~320 chars | No (split needed) |
| 256-bit hex entropy | 64 chars | **Yes** |
| Compressed pubkey | 66 chars (hex) | **Yes** |
| BIP32 xprv | ~111 chars | **Yes** |

**Could you do Bitcoin-over-Ark-over-RADIUS?** Theoretically yes, but it works differently from Cashu:

1. **Key transfer, not token transfer**: You'd transfer a private key (160 chars for 12-word seed) that controls a VTXO. The receiver sweeps the key to claim the VTXO.
2. **No portable payment**: Unlike Cashu, there's no self-contained "token" to hand someone. Ark payments require cooperative rounds with the Ark Service Provider (ASP).
3. **Receiver needs an Ark client**: The receiver must run a Bark wallet to participate in rounds or perform unilateral exit. It's not a simple "paste and go" flow like Cashu.
4. **VTXO proofs are too large**: A full V-PACK proof (tree path + signatures) exceeds 253 bytes for any non-trivial tree. You'd need a token reference system or captive portal.

A more practical approach: use Ark for the backend settlement (fast, low-cost Bitcoin transactions) but present a Cashu-like UX to the RADIUS layer. The Ark wallet holds funds, mints Cashu tokens on demand, and those Cashu tokens flow through RADIUS as described above.

### Lightning HTLC preimage (L402-over-RADIUS)

[L402](https://docs.lightning.engineering/the-lightning-network/l402) (formerly LSAT) is an HTTP 402 authentication protocol using Lightning payment preimages as bearer tokens. The core insight: **a Lightning preimage is only 32 bytes (64 hex characters)** — it trivially fits in any RADIUS attribute, under any EAP method, with zero size constraints.

#### How it works

In the Lightning Network, every payment is locked by `H = sha256(preimage)`. The recipient learns the preimage only when the payment settles. Possession of the preimage **proves** the payment was made. Verification is a single hash: `sha256(preimage) == H`.

```
Server creates hold invoice:
  preimage  = random 32 bytes (secret)
  payment_hash = sha256(preimage)
  invoice   = bolt11 encoding of payment_hash + amount + expiry

User pays invoice from any Lightning wallet:
  wallet settles payment → reveals preimage to user

User presents preimage as credential:
  sha256(preimage) == stored payment_hash → access granted
```

#### Two-phase RADIUS flow

Phase 1 — request an invoice:

```
→ Access-Request
    User-Name = "any-user"
    User-Password = "request-invoice"
← Access-Reject
    Reply-Message = "lnbc1500n1pw5kjhmpp..."    ← BOLT11 invoice
```

Phase 2 — present the preimage:

```
→ Access-Request
    User-Name = "any-user"
    User-Password = "a1b2c3d4e5f6...64hexchars"  ← the preimage
← Access-Accept
    Reply-Message = "Lightning payment verified: 15 sat, 15 min access"
    Session-Timeout = 900
```

The preimage is **64 hex characters** — 6x smaller than a no-DLEQ Cashu token (230 bytes), 17x smaller than a full DLEQ token (378 bytes). It fits in any RADIUS field, any EAP method, with room to spare.

#### Advantages over Cashu

| Property | Cashu ecash | Lightning preimage |
|----------|-------------|-------------------|
| Credential size | 230b (no-DLEQ) / 378b (full) | **64 hex chars** (32 bytes) |
| Verification | CBOR decode + mint API call | **Single SHA-256 hash** (stateless) |
| Payment network | Cashu mints (ecash) | **Lightning Network** (real Bitcoin) |
| Wallet requirement | Cashu wallet | **Any Lightning wallet** |
| Offline verification | No (needs mint checkstate) | **Yes** (hash is local) |
| Amount flexibility | Fixed per token | **Any amount** (invoice is arbitrary) |

#### L402 (HTTP 402) comparison

The [L402 protocol](https://github.com/lightninglabs/L402) uses `Authorization: L402 <macaroon>:<preimage>` over HTTP. The macaroon is an HMAC-chain bearer token that commits to the payment hash, enabling stateless distributed verification — any server with the root key can verify the credential without a database lookup.

For RADIUS, the macaroon is unnecessary — RADIUS already provides the authentication transport (the Access-Request/response cycle). The preimage alone is sufficient. This makes L402-over-RADIUS simpler than L402-over-HTTP.

#### Challenges

1. **Invoice delivery**: The user needs to see the BOLT11 invoice (typically as a QR code). Options:
   - **Captive portal**: Redirect to web page showing invoice QR — most practical
   - **RADIUS Reply-Message**: Return invoice in Access-Reject (phase 1 above) — works but client must parse it
   - **Out-of-band**: Pre-generated invoices at a known URL
2. **Payment confirmation latency**: Lightning payments settle in seconds, but the user must wait for settlement before getting the preimage. Not instant like Cashu (which is already in hand).
3. **Hold invoices**: The server must create a [hold invoice](https://github.com/lightningnetwork/lnd/blob/master/docs/hold_invoices.md) (HASH(M) type) so it controls the preimage. Requires an LND or CLN node with hold invoice support.
4. **No ecash privacy**: Lightning payments are visible to the routing nodes. Cashu provides Chaumian privacy (mint can't link payments to users).

#### Related: LNURLPoS / bitcoinVend offline vending

[Ben Arc's bitcoinVend](https://github.com/lnurlpi/bitcoinVend) and [LNURLPoS](https://github.com/lnbits/lnurlpos) use a related pattern for offline vending machines:

1. Machine generates a random PIN
2. PIN is XOR-encrypted with a shared secret, encoded into an LNURL
3. Customer pays Lightning invoice via LNURL-pay
4. LUD-10 `aes` success action: the payment preimage is used as AES key to decrypt the ciphertext containing the PIN
5. Customer enters PIN on the machine → access granted

This pattern — encrypt a secret in a Lightning invoice, reveal it on payment — could be adapted for RADIUS. The preimage decrypts the access credential rather than being the credential itself.

### Bearer instruments via BIP39 seed phrases

A BIP39 mnemonic seed phrase is a bearer instrument — whoever holds it controls the wallet. [BIP39](https://github.com/bitcoin/bips/blob/master/bip-0039.mediawiki) encodes 128–256 bits of entropy as 12–24 words from a 2048-word dictionary.

| Format | Entropy | Length | Fits RADIUS (253)? | Use case |
|--------|---------|--------|---------------------|----------|
| 12-word seed | 128 bits | ~160 chars | **Yes** | Standard wallet recovery |
| 24-word seed | 256 bits | ~320 chars | No (split needed) | High-security wallet |
| 128-bit hex | 128 bits | 32 chars | **Yes** | Raw entropy |
| 256-bit hex | 256 bits | 64 chars | **Yes** | Raw entropy |
| Compressed pubkey | — | 66 chars (hex) | **Yes** | Receive-only (not bearer) |

**As RADIUS payment**: A user could send a 12-word seed phrase (~160 chars) as a RADIUS password. The server imports the seed into a wallet, checks the balance, and grants access proportional to funds. But this has severe security problems:

1. **Key exposure**: The seed phrase is transmitted in cleartext inside the RADIUS packet (even EAP-TTLS only encrypts the transport — the RADIUS server sees the plaintext)
2. **Long-term secret**: Seed phrases control entire wallets, not individual payments. Sharing one is like giving someone your bank account
3. **No single-use**: Cashu tokens and Lightning preimages are consumed on use. A seed phrase works forever
4. **Verification requires wallet software**: The server would need to derive keys, scan the blockchain, and check balances — heavy infrastructure

**Practical only for**: proof-of-reserves (prove you hold N sats without transferring them) or pre-funded disposable wallets (generate a one-time wallet, fund it, hand over the seed). Neither is as clean as Cashu or Lightning for RADIUS auth.

### Comparison

| Approach | Size | Any payment amount? | Practical for phones? | Effort |
|----------|------|---------------------|-----------------------|--------|
| **No-DLEQ Cashu** | 230b | Single-proof (1-64 sat) | **Yes** — single paste | Done |
| **Lightning preimage** | **64 hex chars** | **Any** | **Yes** — needs invoice first | Medium (LND/CLN node) |
| LNURLw | ~60b | Any | **Yes** | Easy (claim Lightning) |
| Shorter mint URL | ~210b | Single-proof | **Yes** | Trivial (DNS) |
| Token reference | ~25b | Any | Yes (needs web first) | High |
| Captive portal | Unlimited | Any | **Yes** — web form | Medium |
| BIP39 seed phrase | ~160b | Any (wallet balance) | Security risk | High (needs wallet infra) |
| Ark private key | ~160b | Any VTXO | Theoretically | High (needs Ark client) |
| Split DLEQ token | 200+178b | Single-proof | No (two pastes) | Done |

## Bootstrap Token Spec (OpenTollGate)

This Cashu-over-RADIUS implementation is an instance of the **tollgate bootstrap token** defined in the [OpenTollGate specification](https://github.com/OpenTollGate/tollgate-rs/blob/master/docs/design/core/tollgate-bootstrap.md). The bootstrap token is the mechanism that gets a peer online using a regular Cashu ecash token before (or instead of) upgrading to a Spilman payment channel.

### Mapping: Bootstrap Token → RADIUS

| Bootstrap Spec | tollgate-auth RADIUS |
|---|---|
| Peer sends `BootstrapToken` | User sends Cashu token in RADIUS password field |
| Provider verifies with mint | Server calls mint `/v1/checkstate` |
| Provider redeems token | Server runs `cdk-cli receive` (NUT-03 swap) |
| Provider grants metered service | FreeRADIUS sends Access-Accept with Session-Timeout |
| Balance tracking (scaled units) | `amount × RateSecPerSat` seconds of access |
| `MeteringReport` (every 5s) | Session-Timeout + MAC-based reconnection |
| Top-up via additional `BootstrapToken` | Not yet implemented — requires HTTP API or captive portal |
| Upgrade to Spilman channel | Future: HTTP API for sustained micropayment |

### Current mode: Bootstrap-only

tollgate-auth currently operates in **bootstrap-only mode** — the entire session runs on a single Cashu token. There is no top-up or Spilman upgrade path yet. This matches the bootstrap spec's description:

> *Bootstrap-only is a special case of pay-only. The entire session runs on BootstrapToken messages.*

The bootstrap token is consumed immediately (redeemed via NUT-03 swap), and the session duration is fixed at `amount × RateSecPerSat` seconds. When time expires, the user must present a new token to reconnect — there is no in-session top-up.

### Future: Bootstrap → Spilman upgrade

The bootstrap spec defines an upgrade path:

1. **Bootstrap**: Peer uses a Cashu token to get connectivity (current implementation)
2. **Upgrade**: Once online, peer opens a Cashu [Spilman payment channel](https://github.com/cashubtc/nuts/pull/229) for sustained micropayment
3. **Streaming**: Channel enables per-second payment with no token size constraints

For RADIUS, this upgrade path means:

1. User connects via Cashu token in RADIUS password field (bootstrap)
2. Network access grants connectivity
3. An HTTP API or captive portal handles Spilman channel setup
4. Ongoing payment flows through the channel, not through RADIUS attributes
5. RADIUS handles only session management (MAC authorization, Session-Timeout)

This is the natural architecture: **RADIUS for bootstrap, HTTP for sustained payment**. The RADIUS attribute size limit (253 bytes) becomes irrelevant once the channel is established — all payment flows through the HTTP API.

## See Also

- [RFC 2865 Section 5.2](https://datatracker.ietf.org/doc/html/rfc2865#section-5.2) — User-Password attribute (max 253 bytes)
- [NUT-00](https://github.com/cashubtc/nuts/blob/main/00.md) — Cashu token encoding (V3/V4)
- [NUT-12](https://github.com/cashubtc/nuts/blob/main/12.md) — DLEQ proofs (optional, client-side verification)
- [Tollgate Bootstrap Token Spec](https://github.com/OpenTollGate/tollgate-rs/blob/master/docs/design/core/tollgate-bootstrap.md) — Cashu ecash bootstrap mechanism for connectivity
- [OpenTollGate/tollgate-rs](https://github.com/OpenTollGate/tollgate-rs) — Rust implementation with Spilman channel support
- [L402 Protocol](https://docs.lightning.engineering/the-lightning-network/l402) — Lightning HTTP 402 authentication (preimage as bearer token)
- [L402 Spec](https://github.com/lightninglabs/L402/blob/master/protocol-specification.md) — Macaroon + preimage stateless verification
- [LND Hold Invoices](https://github.com/lightningnetwork/lnd/blob/master/docs/hold_invoices.md) — Hash-locked invoices for preimage-based auth
- [BIP39](https://github.com/bitcoin/bips/blob/master/bip-0039.mediawiki) — Mnemonic code for deterministic wallets
- [OpenTollGate/tollgate](https://github.com/OpenTollGate/tollgate) — Captive portal approach with OpenNDS + BTCPayServer
- [Ark Protocol](https://ark-protocol.org/) — Bitcoin scaling with Virtual Transaction Outputs
- [Bark wallet](https://gitlab.com/ark-bitcoin/bark) — Ark on Bitcoin (Rust, by Second)
- [radius-testing.md](radius-testing.md) — Full testing guide with eapol_test examples
