# L402-over-RADIUS: Design Analysis

## What is L402

L402 (Lightning HTTP 402) is an authentication protocol that uses Lightning payment preimages as credentials. When adapted for RADIUS, the concept is straightforward: a Lightning payment preimage is 32 bytes (64 hex characters). The server can verify this with a single SHA-256 hash against the payment_hash. No external API calls, no mint, no CBOR parsing. Compare this to Cashu: a no-DLEQ token is 230 bytes, requires a mint checkstate HTTP call and a cdk-cli subprocess for redemption.

The elegance of L402 is in its simplicity. The preimage alone is sufficient proof of payment. Anyone who knows the preimage must have successfully settled the Lightning HTLC. Verification is a cryptographic hash, nothing more.

## The Two-Phase Flow

L402-over-RADIUS requires two separate authentication attempts. In the first phase, the client requests an invoice. The server creates a hold invoice via LND or CLN and returns the BOLT11 string. In the second phase, the client pays the invoice in any Lightning wallet, receives the preimage, and presents it as the credential.

The server verifies the preimage by checking if `sha256(preimage) == payment_hash`. If the hashes match, the server sends Access-Accept with Session-Timeout derived from the invoice amount. Then the server settles the hold invoice, which transfers funds to the gateway operator atomically.

Hold invoices (also called HODL invoices) are what make this work securely. The payer's funds are locked but not transferred until the receiver settles the invoice. If the server crashes before settling, the funds return to the payer after the invoice expires. If the preimage is correct, the server settles atomically and funds transfer immediately. This prevents "I paid but didn't get access" race conditions.

## The BOLT11 Delivery Problem (Why This Is Hard)

This is the core challenge. Delivering a BOLT11 invoice through RADIUS to a real phone does not work in practice.

### Phones don't display Reply-Message

When a RADIUS server returns Access-Reject with Reply-Message containing a BOLT11 invoice, the phone shows "incorrect password" and discards the message. The user never sees the invoice. This is true for iOS, Android, and most consumer OS WiFi UIs. The Reply-Message attribute is designed for authentication failures, not for delivering arbitrary data to the user.

### EAP-TTLS+PAP is single-shot

Inside the TLS tunnel, PAP sends the password and gets an accept or reject. There is no inner PAP challenge mechanism. You can't do "send invoice, wait, receive preimage" within a single EAP-TTLS+PAP exchange. The protocol is designed for a single round trip: present credentials, get response.

### EAP-GTC has spotty device support

EAP-GTC (Generic Token Card, RFC 3748) supports server-initiated challenge-response, which would allow the server to send the BOLT11 as a challenge and receive the preimage as the response. But Android support varies by manufacturer, iOS support is unreliable, and enterprise supplicants are inconsistent. You cannot build a consumer product on EAP-GTC in 2026. The device support is too fragmented.

### BOLT11 exceeds 253 bytes

A typical BOLT11 invoice with routing hints is 300 to 1500 characters. The RADIUS Reply-Message attribute holds a maximum of 253 bytes. You can chain multiple Reply-Message attributes, but most access points only read the first one. Minimal invoices (small amounts, no routing hints) can fit in about 180 characters, but this is fragile and limits real-world usability. Real invoices need routing hints to ensure payment success.

### No QR codes via RADIUS

A BOLT11 invoice is meant to be scanned as a QR code. Users should not type 300 characters into a WiFi password field. RADIUS has no mechanism to deliver a QR code. You can display text, but not images or machine-readable visual codes. This is a fundamental limitation of the protocol.

## The Workaround: Captive Portal Bridge

The practical architecture sidesteps these problems entirely. RADIUS grants a short walled-garden session (typically 5 minutes, DNS + payment server only). The phone auto-detects the captive portal (both iOS and Android do HTTP probe detection). The portal page displays a QR code with the BOLT11 invoice.

The user scans the QR code with any Lightning wallet, pays the invoice, and the portal backend confirms payment via the Lightning node. Once payment is confirmed, the portal sends a RADIUS CoA (Change of Authorization, RFC 5176) to extend Session-Timeout and lift restrictions. The client gets full internet access without reconnecting.

In this architecture, BOLT11 never touches RADIUS. RADIUS handles session lifecycle and authorization. Lightning handles payment. The captive portal bridges the two protocols. This is how OpenTollGate/tollgate handles L402 payments.

## Why We're Staying Cashu-Only for RADIUS

Cashu works within RADIUS constraints. A no-DLEQ Cashu token is 230 bytes, which fits in a single RADIUS attribute (under 253). The user pastes it as a WiFi password. One paste, one connection. No second phase, no captive portal, no QR scan, no wallet app needed. The token is the credential.

Cashu tokens are bearer instruments. They work offline because the client doesn't need to interact with the server to acquire one. A friend can hand you a token via any messaging app. A faucet can mint one on a web page. The token is self-contained. The mint URL, amount, and proofs are all encoded in the string. No round-trip to the server is needed before connecting.

Cashu doesn't require a Lightning node on the gateway. Cashu verification is an HTTP call to the mint's `/v1/checkstate` endpoint. Redemption is a cdk-cli subprocess call. No LND, no CLN, no channel management, no liquidity, no on-chain fees, no node sync. The gateway operator only needs cdk-cli installed.

Cashu is the simpler MVP. For a proof-of-concept that demonstrates "Bitcoin for network access," Cashu-over-RADIUS works end-to-end today. L402-over-RADIUS requires an LND or CLN node, hold invoice management, payment detection polling, and most importantly a captive portal for invoice delivery. That's four additional infrastructure components that we don't need right now.

L402's preimage verification is superior but the delivery is unsolved for RADIUS. The 64-character preimage and SHA-256 verification are elegant. But without a reliable way to deliver the BOLT11 to the client through RADIUS, you need a captive portal. At that point, you might as well handle all payment over HTTP and keep RADIUS for session management only. That's the OpenTollGate/tollgate approach, not the RADIUS-native approach we're building.

Cashu and L402 are complementary, not competing. Cashu is the bootstrap mechanism. It works in a single RADIUS attribute, requires no infrastructure, and gets users online. L402 is the sustained-payment mechanism. It requires a captive portal and Lightning node, but enables per-second streaming payments. The natural evolution is Cashu for bootstrap, then L402 for top-up. The client gets online with a Cashu token, then opens a Lightning channel for sustained access.

## When We Would Build This

Several triggers would make us implement L402-over-RADIUS:

### When we add a captive portal

If we deploy OpenNDS or CoovaChilli on access points, we get HTTP-based invoice delivery for free. At that point, adding L402 support is a small increment on top of the portal infrastructure. The portal already handles user interaction, so displaying a QR code is trivial.

### When we run a Lightning node

If the gateway operator already runs LND or CLN for settling Cashu tokens (melting to Lightning), adding hold invoice creation is trivial. The node infrastructure exists, so the incremental cost of L402 is minimal.

### When real-money support is needed

Cashu is test-only today. For real-value payments, Lightning is more mature, more widely supported by wallets, and doesn't require running a mint. When we move beyond test tokens, L402 becomes attractive because it uses the native Lightning network rather than a custom ecash system.

### When sustained sessions are needed

Bootstrap tokens grant fixed sessions. For continuous access lasting hours or days, per-second Lightning payments via Spilman channels are more appropriate than repeatedly entering Cashu tokens. L402 provides a path to those sustained sessions, though a full Spilman implementation requires HTTP-based channel management rather than RADIUS.

## Technical Reference: Preimage Verification

The verification logic is straightforward:

```
// Server-side: hold invoice creation
preimage = random(32 bytes)
payment_hash = sha256(preimage)
amount_sat = 15
invoice = LND.addHoldInvoice(payment_hash, amount_sat, expiry=3600)
// Returns BOLT11 string, stores preimage internally

// Client-side: after paying, wallet reveals preimage
client_preimage = hex_string_from_wallet  // 64 hex chars

// Server-side: verification
if sha256(hex_decode(client_preimage)) == stored_payment_hash:
    LND.settleInvoice(preimage)  // funds transfer
    return Access-Accept(Session-Timeout = amount_sat * 60)
else:
    return Access-Reject
```

The preimage is 64 hex characters. This is 3.5 times smaller than a no-DLEQ Cashu token (230 bytes). It fits in any RADIUS attribute, any EAP method, any protocol. The verification is a single hash. No mint, no cdk-cli, no subprocess, no HTTP call. This is the elegant part of L402 that we want to preserve for when the delivery problem is solved.

## Related

- [docs/radius-payment-models.md](radius-payment-models.md) for session lifecycle and CoA analysis
- [docs/radius-token-size.md](radius-token-size.md) for Cashu token size constraints
- [docs/operator-guide.md](operator-guide.md) for current deployment and upgrade path
- [https://docs.lightning.engineering/the-lightning-network/l402](https://docs.lightning.engineering/the-lightning-network/l402) for L402 specification