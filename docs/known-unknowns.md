# Known Unknowns — tollgate-auth

A catalog of unresolved questions, untested assumptions, and identified risks. Organized by severity and category.

**Last updated**: 2026-07-11
**Status**: Security audit Windows 1-3 complete. SSH and RADIUS auth pipelines verified end-to-end with real Cashu tokens. Core concept fully validated on production hardware. Firecracker microVM prototype fully verified — vsock bridge, NAT networking, boot time benchmarks all confirmed on SHC Dev VPS (see [FIRECRACKER_SSH_DESIGN.md](FIRECRACKER_SSH_DESIGN.md)).

---

## Critical (blocks production use)

### 1. No fresh-token end-to-end test on real hardware

**What we know**: CI validates the full token pipeline (16/16 tests pass). The phone successfully delivered a 230-byte no-DLEQ token through EAP-TTLS+PAP to FreeRADIUS, which forwarded it to the Go binary. The Go binary parsed it correctly.

**What we don't know**: Whether a truly fresh, unspent token completes the full cycle on the phone: mint → paste → WiFi connect → EAP auth → token validate → Access-Accept → DHCP → internet.

**Why it matters**: The one time we got close, the token was already spent by a CI run. Direct server validation confirmed the Go binary works, but we never saw the phone get Access-Accept AND internet from the same fresh token in one shot.

**Blocked by**: Reliable phone text entry (ADB `input text` corrupts 230-byte tokens in chunks).

**GitHub issue**: #2

---

### 2. ADB text input corruption — no reliable phone automation

**What we know**: `adb shell input text` on Android 10 (Motorola moto g(8) plus) truncates single calls to ~50 chars. Chunked calls (40 or 58 chars with 500ms delays) introduce extra characters — a 230-byte token becomes 471 bytes. The corruption pattern is non-deterministic.

**What we don't know**: Why chunks corrupt. Hypotheses: (a) IME auto-suggestion interference, (b) async InputConnection buffering, (c) ADB protocol framing issues. We also don't know if the Python phone automation script (`scripts/phone_connect.py`) solves this — it tries clipboard via `service call clipboard` as primary, with chunked input as fallback.

**Why it matters**: Without reliable phone automation, we can't reproducibly test Issue #2 or Issue #5.

**Remediation options**:
- Install a clipboard app (e.g., Clipper) — violates "no app install" constraint
- Use `service call clipboard` with hand-crafted Parcel — untested on this device
- Use a different Android device — unknown if the problem is device-specific
- Manual token entry during testing — slow but reliable

**GitHub issue**: Implicit in #2

---

### 3. Certificate validation disabled ("Do not validate CA")

**What we know**: The phone's EAP-TTLS config uses "Do not validate" for the CA certificate. This is standard for testing but vulnerable to rogue AP / MITM attacks. An attacker can set up a fake WiFi network with the same SSID, intercept the EAP-TTLS tunnel establishment, and capture the Cashu token inside.

**What we don't know**:
- Whether Android supports bundling a custom CA cert with the WiFi enterprise profile
- Whether a Let's Encrypt cert on the RADIUS server would be trusted by Android natively
- Whether QR-code WiFi provisioning can include CA cert + enterprise settings
- The practical exploitability: how hard is it to fake an EAP-TTLS server that accepts arbitrary credentials?

**Why it matters**: Without CA validation, the Cashu token (which is money) is transmitted to whoever runs the strongest nearby AP.

**Production path**: Must bundle a CA cert or use a publicly trusted one. Research needed.

**GitHub issue**: #4 (security review)

---

### 4. BlastRADIUS (CVE-2024-3596) — NOT VULNERABLE (defense-in-depth recommended)

**Finding**: EAP-TTLS is **NOT vulnerable** to BlastRADIUS. Per the BlastRADIUS researchers and FreeRADIUS documentation: "EAP authentication methods are protected against our attack because RFC 2869 mandates that a Message-Authenticator attribute must be present, and this attribute is an HMAC-MD5 over the entire packet that we cannot forge."

All EAP packets (including EAP-TTLS) automatically include Message-Authenticator. The attack only applies to non-EAP methods (raw PAP, CHAP, MS-CHAP) which our setup does not use.

**Recommendation**: Enable `require_message_authenticator = auto` and `limit_proxy_state = auto` in `radiusd.conf` for defense-in-depth. These will NOT break EAP-TTLS+PAP from Android/iOS.

**Severity**: INFO — not vulnerable, but hardening recommended.

---

## High (production quality gaps)

### 5. Token replay protection race condition — FIXED

**What it was**: Spent token hashes were checked and marked in separate operations (`IsSpent` + `MarkSpent`). Two concurrent requests with the same token could both pass the check before either marks it spent.

**Fix applied**: Replaced with `CheckAndMark()` method that uses `syscall.Flock(LOCK_EX)` for cross-process atomicity. A single call atomically checks AND marks. The method uses fail-safe defaults — if the file can't be opened or locked, it assumes the token is already spent (rejects). File permissions changed from 0644 to 0600.

---

### 6. Accounting (RFC 2866) — IMPLEMENTED

**Status**: FreeRADIUS accounting section now calls `tollgate-auth-radius --accounting` via the `tollgate-acct` exec module. The binary parses Start/Interim-Update/Stop packets and forwards usage data (Acct-Session-Time, Acct-Input-Octets, Acct-Output-Octets) to the tollgate-rs session daemon API at `POST /v1/sessions/{mac}/usage`.

**What works**:
- FreeRADIUS receives accounting packets on port 1813 and calls the exec module
- The Go binary parses Acct-Status-Type, session ID, MAC, usage counters, NAS-IP
- Usage reports are forwarded to the session daemon as JSON
- If the session daemon returns `access_level: "suspended"`, a RADIUS Disconnect-Request is sent to the NAS via `radclient`

**Remaining gaps**:
- Session daemon only logs usage data — not yet deducting from balance (valve/janitor still handles metering internally)
- CoA for Session-Timeout extension (mid-session top-up) not yet implemented — only Disconnect on suspend
- Real NAS (OpenWrt hostapd) accounting not yet verified — only tested via `radclient` from localhost

---

### 7. Session cleanup on timeout — server-side gap

**What we know**: The Go binary creates a JSON session file per MAC in `/opt/tollgate-auth/radius-sessions/`. It checks session validity on reconnection. But there's no background process cleaning up expired session files.

**What we don't know**: Whether expired sessions accumulate indefinitely. After weeks of operation, how many stale JSON files pile up?

**Why it matters**: Disk space leak, but also stale sessions could theoretically interfere with new connections if MAC addresses are reused (they are — phones reconnect).

**Fix**: The session check (`IsActive`) already handles expiry by comparing `started + duration` against `now`. But the files themselves are never cleaned up. Add a cron job or background goroutine to purge expired session files.

---

### 8. FreeRADIUS exec module — command injection surface — FIXED

**What it was**: FreeRADIUS passes `User-Name`, `User-Password`, and `Calling-Station-Id` as command-line arguments to the Go binary. The original `sanitizeInput()` only rejected `'`, `\`, `\n`, `\r`, `\0` — missing shell metacharacters like `;`, `` ` ``, `$`, `|`, `&`, `>`, `<`, `(`, `)`.

**Audit findings**:
1. **FreeRADIUS exec module uses `execve()` directly** — no shell interpretation (confirmed by reading `src/lib/server/exec.c` in FreeRADIUS source). The comment in `exec_child()` states: "execve(), unlike system(), treats all the space delimited arguments as literals, so there's no need to perform additional escaping."
2. **Go's `exec.Command` also uses `execve()`** — token→cdk-cli subprocess chain is secure. No shell injection possible.
3. **The legacy `users` file** used `Exec-Program-Wait` which DOES go through shell interpretation — this was a real injection vector.

**Fixes applied**:
- Added `isValidCashuToken()` — strict allowlist: `cashuA`/`cashuB` prefix + base64url chars only (`A-Za-z0-9_-`)
- Added `isValidLNURLw()` — strict allowlist: `lnurlw` prefix + alphanumeric only
- Updated `sanitizeInput()` to also reject shell metacharacters: `` ;`$()|&>< ``
- Updated `extractPayment()` to use strict validators instead of prefix-only checks
- Removed all `Exec-Program-Wait` entries from `config/freeradius/users` — replaced with reject-all fallback

---

## Medium (design questions, future work)

### 9. Token acquisition UX — chicken-and-egg problem

**What we know**: Users need internet to get Cashu tokens, but need tokens to get internet. Current test flow uses ADB to push tokens to the phone — not viable for real users.

**What we don't know**: The lowest-friction UX for non-technical users. Options researched but not validated:
- QR code on physical signage → scan with phone camera → open Cashu wallet via cellular → mint token → paste into WiFi dialog
- NFC tag → token copied to clipboard → paste into WiFi dialog
- SMS/Telegram bot → send message → receive token → paste
- Pre-purchased tokens in a wallet app
- Captive portal (bypasses RADIUS entirely for payment)

**Why it matters**: The entire concept is dead on arrival if users can't get tokens easily.

**GitHub issue**: #3

---

### 10. Multi-proof token sizes (>128 sat)

**What we know**: Single-proof no-DLEQ tokens are 230 bytes (fits RADIUS). Tokens for amounts >64 sat require multiple proofs, which increases token size significantly (~1800 bytes for 128 sat with DLEQ).

**What we don't know**: Exact size of multi-proof no-DLEQ tokens. Whether the no-DLEQ approach scales to arbitrary amounts or if there's a practical upper limit that still fits in 253 bytes.

**Why it matters**: If users want to buy 1 hour of access (60 sat), does the no-DLEQ token still fit? What about a full day (1440 sat)?

**Research needed**: Mint tokens at various amounts with `--no-dleq` and measure sizes. The `scripts/mint-testnut.js` script supports this.

---

### 11. RadSec enforcement — should UDP 1812 be disabled?

**What we know**: Both UDP 1812 (plaintext) and TCP 2083/RadSec (TLS) are active. The token travels inside EAP-TTLS (already encrypted), so the plaintext UDP path doesn't expose the token to network sniffers. RADIUS accounting packets are now forwarded to the session daemon — they travel over UDP 1813 in the clear (standard RADIUS accounting). In production, the NAS and FreeRADIUS are on the same LAN, so this is acceptable. For WAN deployments, RadSec should be used for accounting as well.

**What we don't know**: Whether hostapd on OpenWrt supports RadSec natively. Whether consumer-grade APs support it. Whether disabling UDP 1812 breaks any client compatibility.

**Why it matters**: Defense in depth. Even though the token is encrypted inside EAP-TTLS, plaintext RADIUS packets reveal metadata (MAC addresses, session timing, AP identity).

---

### 12. LNURL-withdraw payment not actually claimed (DEMO FEATURE)

**Status**: Documented as demo-only. LNURLw codes grant 1 hour access without claiming
the underlying Lightning payment. This is intentional for frictionless demo flow —
test tokens have no monetary value. Production deployments should use Cashu tokens only.
Replay-protected by hash.

---

### 13. Session-Timeout enforcement — NAS-dependent behavior

**What we know**: The Go binary sends `Session-Timeout = N` in Access-Accept. RFC 2865 says the NAS "should" terminate the session after this time. But enforcement is NAS-dependent.

**What we don't know**: Whether OpenWrt hostapd actually enforces Session-Timeout. If it doesn't, users get indefinite access after a single payment.

**Test needed**: Connect phone, authenticate, wait for Session-Timeout to expire, check if the NAS actually disconnects the client.

**Mitigation**: Implement CoA (Change of Authorization, RFC 5176) for server-side session termination. Or use accounting to detect overstays and block the MAC.

---

### 14. Concurrent token validation — cdk-cli subprocess limits

**What we know**: Each token validation calls `cdk-cli receive` as a subprocess. FreeRADIUS runs the Go binary for every Access-Request.

**What we don't know**: What happens under concurrent load. Can `cdk-cli` handle multiple simultaneous invocations against the same wallet directory? Does SQLite handle concurrent writes from multiple `cdk-cli` processes?

**Why it matters**: A coffee shop with 20 people connecting simultaneously would spawn 20 `cdk-cli` processes. If the wallet's SQLite database locks, tokens fail to redeem and users are rejected.

**Mitigation**: Add a mutex or queue around `cdk-cli` calls. Or migrate to a Rust library with native CDK integration (long-term plan).

---

## Low (nice-to-know, future improvements)

### 15. Phone-specific quirks — untested on other devices

**What we know**: All hardware testing was on a single Motorola moto g(8) plus (Android 10). Different Android versions, iOS, Windows, and macOS all have different EAP-TTLS+PAP dialog layouts and behaviors.

**What we don't know**: Whether the token-in-password approach works on:
- iOS (different EAP dialog, may have different field limits)
- Windows 11 (different 802.1X supplicant)
- macOS (different enterprise WiFi dialog)
- Android 13+ (may have changed EAP dialog behavior)

**Test needed**: Try connecting from at least iOS and Windows before claiming cross-platform compatibility.

---

### 16. DLEQ proof stripping — which wallets support it?

**What we know**: `cdk-cli` and our `scripts/mint-testnut.js` support `--no-dleq` to strip DLEQ proofs. This produces 230-byte tokens.

**What we don't know**: Whether mainstream Cashu wallets (Nutshell, Enuts, Minibits) produce no-DLEQ tokens, or whether they always include DLEQ. If all wallets produce 378-byte DLEQ tokens, users can't use the simple single-field approach.

**Impact**: If wallets always include DLEQ, the only options are:
- Split token approach (requires two pastes — bad UX)
- Token reference system (requires web service)
- Captive portal (bypasses RADIUS for payment)

---

### 17. Go binary error handling — crash after spend

**What we know**: The token flow is: (1) decode, (2) replay check, (3) mint verify, (4) redeem via `cdk-cli receive`, (5) output Accept.

**What we don't know**: What happens if step 4 succeeds (token is spent, money is in the wallet) but step 5 fails (Go binary crashes, timeout, or FreeRADIUS kills the subprocess). The user's token is consumed but they get rejected.

**Fix**: Implement a two-phase approach: verify first (checkstate), then accept, then redeem asynchronously. Or: checkstate + accept, with a separate reaper process that redeems verified tokens.

---

### 18. Router WiFi relay stability

**What we know**: The OpenWrt router uses `phy1-sta0` (WiFi relay client) as its WAN uplink, connecting to an upstream network. The `wwan` interface was moved from LAN to WAN firewall zone to fix NAT masquerading.

**What we don't know**: Whether the relay connection is stable over hours/days. Whether the router handles upstream AP changes, channel switches, or reconnection gracefully.

**Why it matters**: If the relay drops, all WiFi clients lose internet even though they're still authenticated to the RADIUS server.

---

### 19. OpenNDS / captive portal interference

**What we know**: The router may have OpenNDS installed (referenced in Issue #1). If active, it could intercept HTTP traffic from authenticated WiFi clients and redirect to a captive portal.

**What we don't know**: Whether OpenNDS is actually running on the router. Whether it interferes with post-authentication traffic.

**Test needed**: SSH to router, check `ndsctl status` or `ps | grep opennds`.

---

## Documentation Gaps

### 20. radius-testing.md references outdated EAP-TTLS approach

The testing guide (line 16) still describes the split-token approach (200b + 178b) as the primary EAP-TTLS method. It should be updated to reflect that no-DLEQ single-field (230b in password) is now the recommended approach, with split-token as fallback.

### 21. No deployment runbook for production

The README has install steps but no guidance for:
- Monitoring (how to check if FreeRADIUS is healthy, how many sessions are active)
- Logging (where to find auth logs, how to trace a specific connection)
- Troubleshooting (common failure modes and fixes)
- Backup/recovery (wallet seed, spent hashes, session files)

### 22. No capacity planning guidance

Unknowns:
- How many concurrent RADIUS requests can a single FreeRADIUS + Go binary handle?
- What's the latency budget? (EAP-TTLS handshake + FreeRADIUS processing + cdk-cli redemption)
- How much disk do spent hashes consume? (estimate: ~100 bytes per token, 10K tokens = 1MB)

---

## Resolved (documented for reference)

| # | Question | Resolution | Date |
|---|----------|------------|------|
| — | Can Cashu tokens fit in RADIUS attributes? | No-DLEQ = 230b fits. Full DLEQ = 378b exceeds 253b limit. | 2025-06-12 |
| — | Which field carries the token? | Password field (inside TLS tunnel, no 253b limit from EAP). Identity = "anonymous". | 2025-06-12 |
| — | Does the token arrive intact through EAP-TTLS+PAP? | Yes, confirmed with real phone. 230 bytes intact. | 2025-06-12 |
| — | Why does FreeRADIUS reject after exec module succeeds? | Missing `Auth-Type Accept { ok }` handler in authenticate{}. | 2025-06-12 |
| — | Why no internet after RADIUS auth? | `wwan` was in LAN zone instead of WAN zone — NAT masquerade not applied. | 2025-06-12 |
| — | Does Cleartext-Password need copying to User-Password? | Yes, inside EAP-TTLS PAP sends as Cleartext-Password. Inner-tunnel config copies it. | 2025-06-12 |
| 4 | BlastRADIUS (CVE-2024-3596) vulnerability? | **NOT vulnerable.** EAP-TTLS forces Message-Authenticator per RFC 2869. Enable `auto` for defense-in-depth. | 2025-06-12 |
| 5 | Token replay race condition? | **FIXED.** `CheckAndMark()` with `syscall.Flock(LOCK_EX)` for cross-process atomicity. | 2025-06-12 |
| 8 | FreeRADIUS exec module command injection? | **FIXED.** FreeRADIUS uses execve() (no shell). Added strict character allowlists. Removed vulnerable `users` file `Exec-Program-Wait` entries. | 2025-06-12 |
| — | SSRF via attacker-controlled mint URL? | **FIXED.** `isSafeMintURL()` blocks localhost, link-local, RFC1918 private IPs before HTTP POST. | 2025-06-12 |
| — | Loose input validation (prefix-only checks)? | **FIXED.** `isValidCashuToken()` enforces base64url-only after prefix. `isValidLNURLw()` enforces alphanumeric-only. | 2025-06-12 |
| — | File permissions too permissive (0644)? | **FIXED.** Changed to 0600 (owner-only) for spent hashes and wallet files. | 2025-06-12 |
| — | Legacy `users` file shell injection vector? | **FIXED.** Removed all `Exec-Program-Wait` entries. Replaced with reject-all fallback. | 2025-06-12 |
| 1 | Fresh-token end-to-end test on real hardware? | **VERIFIED.** Both SSH (port 2222) and RADIUS (port 1812) tested with fresh testnut tokens — Access-Accept, Session-Timeout correct, shell granted. | 2026-07-09 |
| — | SSH auth pipeline broken under hardened systemd? | **FIXED.** SystemCallFilter missing chroot/setgroups/setgid syscalls; CapabilityBoundingSet dropped CAP_DAC_OVERRIDE; cp -a needed CAP_CHOWN. See Window 3 audit. | 2026-07-09 |
| — | Daemon-path token redemption always fails as "already spent"? | **FIXED.** RedeemToken() output parser matched cdk-cli recovery-phase lines ("Recovered N ops, K skipped") as the receive result. Now skips "Recovered" lines. | 2026-07-09 |
| — | TLS broken on ssh.nodns.shop, dns.nodns.shop? | **FIXED.** Added `tls { on_demand }` to Caddy site blocks shadowed by `*.nodns.shop` wildcard. | 2026-07-09 |
| — | Can Firecracker VMs get networking via vsock? | **VERIFIED.** vsock bridge works end-to-end: host SSH connects to guest agent on AF_VSOCK port 52, shell commands execute and return output. | 2026-07-11 |
| — | Can Firecracker VMs access the internet (NAT)? | **VERIFIED.** With failover.ko included in initramfs, virtio_net loads, eth0 gets static IP, ping to 8.8.8.8 returns in 21ms, HTTP fetch succeeds. | 2026-07-11 |
| — | Firecracker boot time for per-SSH microVMs? | **MEASURED.** Cold boot: 2.52s avg (API to vsock ready). 3 concurrent VMs: 2.82s. vsock RTT: 0.248ms. Host memory overhead: 80MB/VM. | 2026-07-11 |

---

## Hypotheses for Open Questions

### H1: Why is host memory overhead only 80MB per VM (not 256MB+5MB)?

KVM uses lazy/on-demand memory allocation. Guest RAM is backed by host
memory only when the guest first touches each page. Since our initramfs
guest runs busybox + agent (minimal working set), most of the 256MB
configured RAM is never allocated. The 80MB overhead is: VMM thread
stacks (~5MB), EPT/page tables (~10MB for 256MB guest), virtio queues
(~2MB), and guest kernel metadata (~60MB for kernel structures, module
allocations, page cache). Production VMs that actually use their RAM
(e.g., running compilation, databases) will see the full 256MB consumed.

### H2: Why did boot time increase from 1.71s to 2.52s with networking?

Estimated breakdown of the 2.52s cold boot:
- ~0.3s: Firecracker process startup + VM config parsing
- ~0.4s: Kernel boot (decompression, ACPI, memory init)
- ~0.3s: Initramfs unpack + busybox applet installation
- ~0.8s: Module loading (7 modules: virtio_mmio, failover, net_failover, virtio_net, vsock, 2x transport)
- ~0.3s: Agent startup + vsock socket creation
- ~0.4s: Host-side polling for vsock socket availability

A Firecracker-optimized kernel (like Anvil) with networking/vsock built-in
(=y instead of =m) would eliminate the ~0.8s module loading, bringing cold
boot to ~1.7s. Snapshot restore would bring it to ~10ms.

### H3: Why do SHC Dev VPS instances disappear after 30-60 minutes?

The SHC billing system reports "CHARGE MISMATCH: charged $0.00, expected
$0.90" even after the payment confirmation flow completes. This suggests
the daily renewal billing doesn't properly deduct from account credit.
The VM is cleaned up when the next daily renewal cycle fails to charge.
Workaround: use a different KVM provider (Hetzner, AWS .metal) for
long-running Firecracker tests.
