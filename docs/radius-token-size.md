# RADIUS Attribute Length vs Cashu Token Size

## The Problem

RADIUS attributes (User-Name, User-Password) are limited to **253 bytes**. Cashu V4 tokens (`cashuB...`) are always **378 bytes** regardless of the token amount. This means Cashu tokens **cannot be sent via raw RADIUS** (radclient, simple PAP auth).

## Token Size Measurements

All tokens minted from `testnut.cashu.exchange`, single proof:

| Amount | Proofs | Token Length | Fits RADIUS (253 bytes)? |
|--------|--------|-------------|--------------------------|
| 1 sat  | 1      | **378 bytes** | No |
| 2 sat  | 1      | **378 bytes** | No |
| 4 sat  | 1      | **378 bytes** | No |
| 8 sat  | 1      | **378 bytes** | No |
| 128 sat| 7      | ~1800 bytes   | No |

Token size is **fixed at 378 bytes** for any single-proof token. The size comes from the proof structure, not the amount:

- Mint URL: ~31 bytes (`https://testnut.cashu.exchange`)
- Proof secret: 32 bytes (random)
- Proof public key (C): 33 bytes (secp256k1 compressed point)
- Proof ID: 16 bytes
- CBOR + base64url encoding overhead: ~44 bytes

Even a 1-sat token (the smallest possible) exceeds 253 bytes. Power-of-2 amounts don't help — the amount field is just a small integer in the encoding.

## The Solution: EAP-TTLS+PAP

The password inside an EAP-TTLS+PAP TLS tunnel has **no length limit**. The RADIUS attribute limit only applies to outer attributes. Inside the TLS tunnel, the PAP password can be arbitrarily long.

```
┌─────────────────────────────────────────────┐
│ RADIUS Access-Request (outer)               │
│   EAP-Message = <TLS tunnel data>           │
│                                              │
│   ┌──────────────────────────────────────┐  │
│   │ TLS Tunnel (encrypted)                │  │
│   │   User-Name = "ci-user"               │  │
│   │   User-Password = "cashuBo2Fte..."   │  │ ← 378 bytes, no limit
│   │            (378 bytes — no limit!)     │  │
│   └──────────────────────────────────────┘  │
└─────────────────────────────────────────────┘
```

This is the standard path for WiFi authentication. Every phone/laptop that connects to WPA2-Enterprise WiFi uses EAP-TTLS+PAP (or PEAP). The Cashu token goes in the password field inside the TLS tunnel.

## What Works and What Doesn't

| Transport | Cashu Token (378 bytes) | LNURLw (~50 bytes) |
|-----------|------------------------|--------------------|
| **Raw RADIUS (radclient)** | No — exceeds 253-byte limit | Yes — fits in attribute |
| **EAP-TTLS+PAP** | Yes — no limit in TLS tunnel | Yes — no limit in TLS tunnel |
| **PEAP+MSCHAPv2** | No — token must fit in User-Name | Yes — fits in User-Name |
| **RadSec (TLS transport)** | No — RADIUS attribute limit still applies to inner attributes | Yes |

**RadSec encrypts the RADIUS transport but does NOT remove the 253-byte attribute limit.** RadSec = TLS for the RADIUS packet itself, not for the inner auth data.

## Testing with eapol_test

`eapol_test` (from `wpasupplicant` package) simulates a real WiFi supplicant doing EAP-TTLS+PAP. This is the correct tool for testing Cashu tokens over RADIUS.

```bash
# Install
sudo apt install wpasupplicant

# Create config
cat > /tmp/eapol.conf << 'EOF'
network={
    ssid="cashu-test"
    key_mgmt=IEEE8021X
    eap=TTLS
    identity="user"
    password="cashuBo2FteB5odHRwczovL3Rlc3RudXQuY2FzaHUuZXhjaGFuZ2VhdWNzYXRhdIGiYWlIAI6Ai4mswUFhcIGkYWEEYXN4QDY5MDBkMjk2OGE5ZDE2ZjNiZmZiMDI1MmI4OWZlN2UyN2Y1MDFiMDYzNDI1ZDdjODdmMWYxNTE5ZmIwNjAxYzhhY1ghAoyrFXYAyD5LHRRi-RKU7YUonX6OUnwAavPTNmuDELymYWSjYWVYIAt_3BbqxcWOP3etn6JFLO39xbFsYJSaK1YCwOTCRx5SYXNYIGBgzmXwXpY4Aok35PhBa4n1gVbaCUuXpqsOg6TNCGR1YXJYIGh3YvPl_ugdQo_WYdFnUqvnkf1qvbFHx5j6LS6ZCD9e"
    phase2="auth=PAP"
    anonymous_identity="anonymous"
}
EOF

# Run against RADIUS server
eapol_test -c /tmp/eapol.conf -a nodns.shop -s tollgate -M aa:bb:cc:dd:ee:ff -r 1 -t 30
```

Expected output on success:
```
eapol_test -> RADIUS returned: Access-Accept
```

## Why the Username+Password Split Doesn't Work

A tempting hack: split the token across both User-Name and User-Password fields and concatenate on the server side. This is a bad idea because:

1. **Non-standard** — no RADIUS client would do this automatically
2. **Fragile** — depends on exact splitting logic, breaks if either field is truncated
3. **Breaks real clients** — phones, VPN clients, APs don't let you control both fields independently
4. **Already solved** — EAP-TTLS+PAP handles long passwords correctly, it's the standard protocol

## See Also

- [RFC 2865 Section 5.2](https://datatracker.ietf.org/doc/html/rfc2865#section-5.2) — User-Password attribute (max 253 bytes)
- [NUT-00](https://github.com/cashubtc/nuts/blob/main/00.md) — Cashu token encoding (V3/V4)
- [radius-testing.md](radius-testing.md) — Full testing guide with eapol_test examples
