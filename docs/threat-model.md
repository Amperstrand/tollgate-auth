# Threat Model

Security analysis for tollgate-auth RADIUS payment gateway.

## Trust Boundaries

```
┌─────────────────────────────────────────────────┐
│                  Public Internet                  │
│  (attacker controls network, can inject packets) │
└─────────┬──────────────────────┬────────────────┘
          │                      │
    UDP 1812               TCP 2083 (TLS)
    Shared secret           Let's Encrypt cert
          │                      │
┌─────────▼──────────────────────▼────────────────┐
│              FreeRADIUS (RADIUS server)           │
│  - Receives Access-Request, Accounting-Request    │
│  - Validates shared secret / TLS                 │
│  - Calls tollgate-auth-radius via exec module     │
│  - execve() — NO shell interpretation             │
└─────────┬───────────────────────────────────────┘
          │ execve (argv, no shell)
┌─────────▼───────────────────────────────────────┐
│         tollgate-auth-radius (Go binary)          │
│  - Validates Cashu/LNURLw payment                 │
│  - Generates Session-Timeout, Class               │
│  - Records to ledger                              │
└──────────────────────────────────────────────────┘
```

## Threats

### T1: Token Replay
**Severity**: HIGH  
**Description**: User spends a Cashu token once, then reuses it at a different AP or after session expires.  
**Mitigation**: `CheckAndMark()` with `flock(LOCK_EX)` for cross-process atomic check-and-mark. SHA256 hash of full token string stored in append-only file.  
**Residual risk**: Race between check and mark across two processes — mitigated by file locking.  
**Status**: FIXED (atomic flock-based CheckAndMark)

### T2: Command Injection via RADIUS Attributes
**Severity**: HIGH  
**Description**: Attacker crafts username/password containing shell metacharacters (`;`, `$()`, backticks) that could escape exec arguments.  
**Mitigation**: FreeRADIUS exec module uses `execve()` directly (no shell). Go's `exec.Command` also uses `execve()`. Arguments cannot escape their argv slot. Additionally, `sanitizeInput()` rejects `'`, `\`, `;`, `` ` ``, `$`, `(`, `)`, `|`, `&`, `>`, `<`, `\n`, `\r`, `\0`.  
**Residual risk**: The FreeRADIUS config uses `/bin/sh -c` wrapper to inject env vars. The `%{...}` expansions are quoted but defense-in-depth via sanitizeInput() is critical.  
**Status**: FIXED (execve confirmed + strict input validation)

### T3: SSRF via Attacker-Controlled Mint URL
**Severity**: HIGH  
**Description**: Cashu tokens contain a mint URL. An attacker crafts a token with mint URL pointing at internal services (localhost, 169.254.169.254, etc.) to probe the internal network.  
**Mitigation**: `isSafeMintURL()` blocks RFC 1918 ranges (10.x, 172.16-31.x, 192.168.x), link-local (169.254.x), and localhost. Only HTTP/HTTPS schemes allowed.  
**Status**: FIXED

### T4: BlastRADIUS (CVE-2024-3596)
**Severity**: INFO  
**Description**: Attacker modifies RADIUS packets in transit by manipulating the Response-Authenticator.  
**Mitigation**: Not applicable — EAP-TTLS forces `Message-Authenticator` attribute per RFC 2869, which is HMAC-MD5 of the entire packet using the shared secret. Any modification invalidates the HMAC.  
**Status**: NOT VULNERABLE (EAP forces Message-Authenticator)

### T5: Token Leakage in Logs
**Severity**: MEDIUM  
**Description**: Full Cashu tokens or LNURLw codes logged to files accessible to system administrators.  
**Mitigation**: `safeLog()` strips newlines. `truncate()` limits logged strings. Token logs use SHA256 hashes, not full tokens.  
**Residual risk**: `log.Printf()` in some paths may log truncated but still-usable portions of tokens.  
**Status**: PARTIAL — ongoing improvement via `internal/redact` package.

### T6: Shared Secret Compromise (0.0.0.0/0)
**Severity**: MEDIUM  
**Description**: `clients.conf` accepts RADIUS from any IP with shared secret `tollgate`. Anyone who knows the secret can send forged Access-Requests.  
**Mitigation**: This is **intentional open demo mode** — designed so any AP can point at the gateway without configuration. The secret `tollgate` is public.  
**Production guidance**: Restrict to known AP IPs. Use RadSec (TLS) instead of shared secret.  
**Status**: BY DESIGN (open mode) — production operators must restrict

### T7: Forged Accounting Packets
**Severity**: MEDIUM  
**Description**: Attacker sends forged Accounting-Stop with inflated `Acct-Session-Time` to consume another user's quota, or sends fake Start to register phantom sessions.  
**Mitigation**: Accounting packets require the shared secret (HMAC in `Authenticator` field). Forged packets from off-network will fail authenticator check.  
**Residual risk**: Anyone with the shared secret (which is `tollgate` in demo mode) can forge accounting.  
**Status**: Acceptable for demo. Production requires RadSec + IP restrictions.

### T8: Operator Impersonation
**Severity**: MEDIUM  
**Description**: Attacker configures their AP to send `NAS-Identifier` matching a registered operator, collecting payments meant for the real operator.  
**Mitigation**: Operator identity is resolved from multiple sources (client IP, NAS-ID, config). In demo mode, all operators are self-asserted. Production requires RadSec client certificates for verified operator identity.  
**Status**: Demo mode — settlement requires verified operator

### T9: Session Hijacking (MAC Spoofing)
**Severity**: LOW  
**Description**: Attacker spoofs another user's MAC address to reuse their active session without payment.  
**Mitigation**: Session-Timeout is bound to the original MAC. The spoofed device would need to be on the same network segment and complete EAP-TTLS handshake (requires the TLS tunnel).  
**Residual risk**: On open networks or with PAP-only, MAC spoofing is trivial.  
**Status**: Acceptable for WiFi (physical proximity required)

### T10: DoS via Token Exhaustion
**Severity**: LOW  
**Description**: Attacker floods with valid-looking but invalid tokens, consuming CPU in mint verification HTTP calls.  
**Mitigation**: Strict format validation (isValidCashuToken) rejects non-base64url before any network call. Test-mint-only restriction limits scope.  
**Residual risk**: No rate limiting currently.  
**Status**: TODO — rate limit by source IP/MAC

### T11: RadSec TLS Downgrade
**Severity**: LOW  
**Description**: Attacker attempts MITM to downgrade TLS or present fake certificate.  
**Mitigation**: Let's Encrypt certificate with full chain. Clients verify the certificate chain.  
**Residual risk**: Self-signed cert path exists but is not default.  
**Status**: FIXED (LE certs in production)

### T12: Reply-Message Injection
**Severity**: LOW  
**Description**: Attacker crafts input that causes `replyMessage()` to output malformed RADIUS attribute pairs, potentially injecting additional attributes.  
**Mitigation**: `replyMessage()` sanitizes newlines, quotes, and commas before output. FreeRADIUS exec module parses stdout as attribute pairs — commas separate attributes.  
**Status**: FIXED (comma/quote/newline sanitization)

## Security Configuration Checklist

### Demo / Test Mode (current default)
- [x] Test-mint-only restriction (`(?i)test` regex)
- [x] Replay protection (flock-based atomic check-and-mark)
- [x] Input sanitization (shell metacharacters, length limits)
- [x] SSRF protection (private IP blocking)
- [x] BlastRADIUS not applicable (EAP forces Message-Authenticator)
- [x] File permissions 0600 (owner-only)
- [ ] Token redaction in all log paths
- [ ] Rate limiting by source IP/MAC
- [ ] Accounting packet validation

### Production Mode (required for real money)
- [ ] Restrict `clients.conf` to known AP IPs
- [ ] Use RadSec (TLS) instead of plain UDP
- [ ] Use verified Let's Encrypt or CA-signed certificates
- [ ] Enable operator registry with verified identities
- [ ] Enable HMAC-signed Class attribute for accounting correlation
- [ ] Enable local ledger for audit trail
- [ ] Change shared secret from default `tollgate`
- [ ] Remove or restrict LNURLw pass-through accept
- [ ] Add rate limiting (per IP, per MAC, per token hash)
- [ ] Enable `Message-Authenticator` requirement
- [ ] Log rotation and retention policy
- [ ] Monitor for anomalous patterns (rapid token attempts, many failed auths)
