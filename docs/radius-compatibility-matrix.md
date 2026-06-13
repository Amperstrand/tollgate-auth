# RADIUS Payment Compatibility Matrix

Which RADIUS authentication methods work with Cashu/LNURL payment tokens, and why.

## Token Formats

| Format | Prefix | Size | Example Use |
|--------|--------|------|-------------|
| Cashu V3 | `cashuA` | Variable | Legacy tokens |
| Cashu V4 no-DLEQ | `cashuB` | ~230 bytes | Single-field WiFi auth |
| Cashu V4 with DLEQ | `cashuB` | ~378 bytes | Split across identity+password |
| LNURL-withdraw | `lnurlw`/`LNURLW` | ~60 bytes | Lightning-based demo |

## RADIUS Attribute Limits

| Context | Max attribute size | Implication |
|---------|-------------------|-------------|
| Raw RADIUS (UDP) | 253 bytes | No-DLEQ tokens (230b) fit. DLEQ tokens (378b) don't. |
| EAP-TTLS inner tunnel | 253 bytes | FreeRADIUS `diameter2vp` enforces this limit even inside TLS |
| RadSec (TCP 2083) | 253 bytes | Same RADIUS attribute limit — TLS only encrypts transport |
| HTTP POST body | Unlimited | Captive portal path has no size constraints |

## Authentication Method Matrix

| Method | Inner credential visible? | Token in username | Token in password | Split token | Notes |
|--------|--------------------------|-------------------|-------------------|-------------|-------|
| **PAP** | Yes (cleartext) | ✅ All formats | ✅ All formats | ✅ Supported | Simplest path. Works with `radclient` and `eapol_test`. |
| **EAP-TTLS/PAP** | Yes (cleartext inside TLS) | ✅ All formats | ✅ All formats | ✅ Supported | **Recommended for WiFi.** TLS tunnel protects credentials. FreeRADIUS `diameter2vp` enforces 253-byte limit. |
| **EAP-TTLS/CHAP** | No (challenge-response) | ✅ LNURLw only | ❌ Password unrecoverable | ❌ | CHAP hashes the password — Cashu token cannot be extracted. Only works if token is in username. |
| **EAP-TTLS/MSCHAPv2** | No (challenge-response) | ✅ LNURLw only | ❌ Password unrecoverable | ❌ | Same as CHAP — password is hashed, cannot extract Cashu token. |
| **PEAP/MSCHAPv2** | No (challenge-response) | ✅ LNURLw only | ❌ Password unrecoverable | ❌ | Most common Android EAP method. Only works with LNURLw or short tokens in username. |
| **PEAP/GTC** | Yes (cleartext) | ✅ All formats | ✅ All formats | ✅ | If FreeRADIUS supports it. GTC sends password in cleartext. |
| **EAP-TLS** | N/A (certificate-based) | ❌ No password field | ❌ No password field | ❌ | Client certificate auth. No password field for token. Certificate CN could serve as operator identity. |
| **CHAP** (raw RADIUS) | No (challenge-response) | ✅ LNURLw only | ❌ Password unrecoverable | ❌ | VPN/PAM-RADIUS scenarios. Only username works for payment. |
| **MS-CHAP** (raw RADIUS) | No (challenge-response) | ✅ LNURLw only | ❌ Password unrecoverable | ❌ | Same limitation as CHAP. |

## Payment Credential Placement

### Detection Order (in `extractPayment()`)

1. **Full Cashu token in username** — `cashuA...` or `cashuB...` in User-Name
2. **LNURLw in any field** — username, password, or cleartext-password
3. **Split Cashu token** — password is exactly 200 bytes starting with `cashuB`, username is base64url tail
4. **Full Cashu token in password** — `cashuA...` or `cashuB...` in User-Password
5. **Full Cashu token in cleartext-password** — `cashuA...` or `cashuB...` in Cleartext-Password

### Split Token Details

Cashu V4 tokens with DLEQ proofs are ~378 bytes, exceeding the 253-byte RADIUS attribute limit. The split approach:

| Field | Content | Size |
|-------|---------|------|
| Password | First 200 bytes (starts with `cashuB` prefix) | ≤253 ✓ |
| Identity/Username | Remaining 178 bytes (base64url tail) | ≤253 ✓ |

The Go binary detects splits by checking: password starts with `cashuB` AND is exactly 200 bytes AND username is base64url-only (no `cashu`/`lnurlw` prefix).

**Practical note**: Split tokens require pasting two separate strings into a phone's WiFi dialog — impractical for real users. The recommended path is no-DLEQ tokens (230 bytes, single field).

## Transport Matrix

| Transport | Port | Encryption | Token protection | Token size limit |
|-----------|------|------------|-----------------|-----------------|
| UDP RADIUS | 1812 | Shared secret | Attribute only (253b) | 253 bytes |
| RadSec (TLS) | 2083 | TLS 1.2/1.3 | Full tunnel encryption | 253 bytes |
| RADIUS/TCP | 1812 | Shared secret | Attribute only | 253 bytes |
| HTTP (captive portal) | 80/443 | TLS optional | POST body (unlimited) | Unlimited |

## Android EAP Method Results

| EAP Method | Phase 2 | Token placement | Result | Notes |
|------------|---------|-----------------|--------|-------|
| TTLS | PAP | Password (no-DLEQ 230b) | ✅ Works | **Recommended** — single paste in password field |
| TTLS | PAP | Split (200b pw + 178b identity) | ✅ Works | Requires two pastes — impractical for users |
| TTLS | PAP | Identity (no-DLEQ 230b) | ✅ Works | Token in username field |
| TTLS | MSCHAPv2 | Password | ❌ Fails | Password hashed, token unrecoverable |
| TTLS | CHAP | Password | ❌ Fails | Same as MSCHAPv2 |
| PEAP | MSCHAPv2 | Password | ❌ Fails | Password hashed |
| PEAP | MSCHAPv2 | Username (LNURLw ~60b) | ✅ Works | Only short tokens in username |
| PEAP | GTC | Password | ✅ Works (if available) | Cleartext password — GTC not widely supported |
| TLS | N/A | N/A | ❌ No token path | Certificate-based, no password field |

## Recommended Configurations

### WiFi (Android/iOS)
- **EAP-TTLS + PAP** with no-DLEQ Cashu token in password field
- CA certificate: "Do not validate" (demo) or install CA for production
- Anonymous identity: empty or `anonymous`

### VPN (OpenVPN PAM-RADIUS)
- **PAP** with token in password field
- OpenVPN sends PAP by default

### Captive Portal
- **HTTP POST** with token in form body (no size limit)
- Bypasses RADIUS attribute limits entirely

### Enterprise (RadSec)
- **RadSec (TCP 2083)** for encrypted transport
- Same EAP-TTLS+PAP inner auth
- Let's Encrypt certificate for TLS

## Key RFC References

| RFC | Title | Relevance |
|-----|-------|-----------|
| RFC 2865 | RADIUS Authentication | 253-byte attribute limit |
| RFC 2866 | RADIUS Accounting | Start/Interim/Stop events |
| RFC 2869 | RADIUS Extensions | EAP, Message-Authenticator |
| RFC 5176 | Dynamic Authorization (CoA) | Session-Timeout updates |
| RFC 3580 | IEEE 802.1X RADIUS | EAP-TTLS/PEAP guidelines |
| RFC 6614 | RadSec (RADIUS over TLS) | Encrypted RADIUS transport |
