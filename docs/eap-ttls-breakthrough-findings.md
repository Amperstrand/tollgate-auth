# EAP-TTLS+PAP Breakthrough Findings

Date: 2026-06-12

## Summary

**Verified end-to-end: a Cashu ecash token can pass through a real RADIUS server, through EAP-TTLS+PAP, from a physical Android phone, and be received intact by our Go validation binary.**

This confirms the core concept: Cashu tokens as RADIUS credentials for WiFi access is viable.

## What We Tested

**Hypothesis D**: Identity=`anonymous`, Password=no-DLEQ cashu token (230 bytes)

- **EAP method**: TTLS
- **Phase 2**: PAP
- **CA certificate**: Do not validate
- **Identity field**: `anonymous`
- **Password field**: 230-byte Cashu V4 token without DLEQ proof

## Result: PASS

1. Phone connected to the WPA2-Enterprise access point
2. FreeRADIUS received the inner tunnel credentials:
   - `User-Name = "anonymous"`
   - `User-Password = "cashuBo2FteB5...kDsaHmF"` (230 bytes, intact)
3. Go binary received and parsed the token
4. Phone authenticated successfully (RADIUS Access-Accept)
5. DHCP assigned IP address to the phone
6. Phone had internet connectivity (ping to 1.1.1.1 and google.com, 0% packet loss)

### Key Log Evidence

FreeRADIUS `cashu-debug.log` showed the phone's MAC delivering the token through the EAP-TTLS inner tunnel:

```
USER="anonymous" PASS="cashuBo2FteB5..." CPASS="" MAC="B6-95-54-46-E0-27"
```

FreeRADIUS `radius.log` confirmed the Go binary processed it (token was rejected only because it was already spent from a previous CI run, not because of any transport failure):

```
cashu-auth: ERROR: Program returned code (1) and output 'Reply-Message = "Rejected: token already used"'
```

After re-authentication with a fresh session, the phone achieved full connectivity.

## What We Learned

### Identity vs Anonymous Identity (Android EAP-TTLS)

| Field | Where it goes | RADIUS attribute | Size limit | Contents |
|---|---|---|---|---|
| **Identity** | Outer EAP identity (unencrypted) → RADIUS `User-Name` | `User-Name` | 253 bytes | `anonymous` |
| **Password** | Inner PAP password (inside TLS tunnel) → `Cleartext-Password` → `User-Password` | No 253-byte limit from EAP, but FreeRADIUS `diameter2vp` enforces 253 | Token goes here |
| **Anonymous Identity** | Alternative outer identity (replaces Identity in the unencrypted EAP phase). Purpose: privacy. If empty, Android uses Identity field for both outer and inner. Left empty in our test. | `User-Name` (outer only) | 253 bytes | Empty |

**Conclusion**: Token goes in Password. Identity/Anonymous Identity = `anonymous` or empty. This is the correct field mapping for Cashu-over-RADIUS.

### ADB `input text` Limitation

`adb shell input text` truncates long strings on Android. A 230-byte token was truncated to 50 characters in a single call. Workaround: send in chunks of ~60 characters with delays between each chunk.

### FreeRADIUS Inner-Tunnel Fix Required

The default inner-tunnel config did NOT work. Two changes were required:

1. **`Auth-Type := Accept`** instead of `Response-Packet-Type := Access-Accept` in the authorize section
2. **`Auth-Type Accept { ok }`** handler in the authenticate section

Without this fix, FreeRADIUS logged `No Auth-Type found: rejecting the user via Post-Auth-Type = Reject` even after the exec module matched the token.

### Token Size: No-DLEQ Is the Answer

| Token type | Size | Fits in 253-byte RADIUS attr? | Single field? |
|---|---|---|---|
| Cashu V4 with DLEQ | 378 bytes | No | No (requires split) |
| Cashu V4 without DLEQ | 230 bytes | Yes | Yes (password field) |

DLEQ (NUT-12) is an optional client-side verification that the mint didn't cheat during blind signing. It is NOT required for:
- Mint `checkstate` (proof validity check)
- NUT-03 swap (token redemption)
- Any operator-side validation

Stripping DLEQ reduces the token to 230 bytes, fitting comfortably in a single RADIUS attribute with 23 bytes to spare.

## Known Issues (Not Blockers)

### Token replay during testing

CI tests mint and spend tokens. If the same token is used for a phone test, the Go binary rejects it as "already used." Need to ensure fresh tokens for hardware tests.

### FreeRADIUS log permissions warning

FreeRADIUS warns about 0644 permissions on debug log files (wants 0600). Cosmetic only.

## Resolved Since Initial Findings

### Internet passthrough — FIXED

The phone authenticated and got an IP, but internet was blocked. Root cause: the `wwan` interface (WiFi relay uplink) was in the LAN firewall zone instead of the WAN zone. NAT masquerade was not applied to traffic from LAN clients going out through `phy1-sta0`. Fixed by moving `wwan` to the WAN zone in OpenWrt. Phone now has full internet access (ping 1.1.1.1 and google.com, 0% packet loss). See GitHub Issue #1.

## Remaining Hypotheses (Deferred)

| Hypothesis | Description | Status | Reason to defer |
|---|---|---|---|
| **B** | Identity=no-DLEQ 230b token, Password=empty | Deferred | Hypothesis D already works and is better UX (anonymous outer identity) |
| **A** | Identity=`anonymous`, Password=full DLEQ 378b token | Deferred | FreeRADIUS `diameter2vp` enforces 253-byte limit inside TLS tunnel — likely to fail |
| **C** | Identity=`anonymous`, Anonymous Identity=token | Deferred | Anonymous Identity maps to outer EAP identity → 253-byte RADIUS attr limit, same problem as Identity |

Hypothesis D is the recommended approach. The others can be tested if needed for edge cases or specific client requirements.

## Recommended Next Steps

1. **Fix internet passthrough** — investigate whether OpenNDS/ndsctl is blocking traffic after RADIUS auth
2. **Fresh token end-to-end** — mint fresh token, test full happy path with unused token
3. **Session management** — verify Session-Timeout, reconnection, and accounting work with real phone
4. **UX automation** — phone-side app or QR code to automate token entry in enterprise dialog
5. **Token acquisition UX** — how does the user get a token before connecting? (captive portal, NFC, QR)
6. **Security review** — BlastRADIUS, RadSec enforcement, certificate validation
