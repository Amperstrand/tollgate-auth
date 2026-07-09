# Security Audit — tollgate-auth

**Audit window:** June–July 2025  
**Scope:** Full codebase + production deployment (nodns.shop)  
**Auditors:** Sisyphus (OhMyOpenCode orchestration), automated shell-out + privilege scan

This document consolidates all findings from the security audit, including the original FreeRADIUS command-injection CVE, the defense-in-depth follow-up, and bonus issues discovered during the audit window. Each finding includes severity, root cause, fix, and verification.

---

## Executive summary

The audit found **one HIGH-severity vulnerability** (FreeRADIUS command injection — fixed before this audit began), **one MEDIUM-severity information disclosure** (world-readable Nostr private key), and **one MEDIUM-severity operational bug** (daemon graceful-shutdown loop). Five LOW-severity defense-in-depth gaps were also addressed.

**No CRITICAL issues remain.** All findings have been fixed, deployed, verified, and guarded against regression.

| # | Finding | Severity | Status |
|---|---------|----------|--------|
| 1 | FreeRADIUS `/bin/sh -c` with `%{...}` attribute expansion | HIGH | **Fixed** |
| 2 | `/etc/tollgate/settle.env` world-readable, contained `TOLLGATE_OPERATOR_NSEC` | MEDIUM | **Fixed** |
| 3 | `tollgate-daemon` graceful-shutdown loop (accept loop spin on closed socket) | MEDIUM | **Fixed** |
| 4 | `tollgate-settle` ran as root unnecessarily | MEDIUM | **Fixed** |
| 5 | 5 internal services bound to `0.0.0.0` (publicly reachable) | MEDIUM | **Fixed** |
| 6 | Repo systemd units lacked `User=` directives (drifted from prod) | LOW | **Fixed** |
| 7 | No systemd hardening directives (ProtectSystem, etc.) | LOW | **Fixed** |
| 8 | SSH jail paths constructed from username-derived strings | LOW | **Fixed** (defense-in-depth) |
| 9 | WireGuard pubkey passed to `wg` without format validation | LOW | **Fixed** (defense-in-depth) |
| 10 | No regression guard for FreeRADIUS shell-injection pattern | LOW | **Fixed** (3-layer) |

---

## Finding 1 — FreeRADIUS command injection (HIGH)

### Vulnerable pattern

Two FreeRADIUS exec module configs used `/bin/sh -c` while interpolating attacker-controlled RADIUS attributes:

- `config/freeradius/mods-available/cashu-exec-delegated`
- `config/freeradius/mods-available/tollgate-acct`

```radius
program = "/bin/sh -c '. /etc/tollgate/secrets.env && TOLLGATE_AUTH_MODE=delegated ... /usr/local/bin/tollgate-auth-radius \"%{User-Name}\" ...'"
```

### Root cause

FreeRADIUS expands `%{...}` references into the `program` string **before** anything executes. When the program is `/bin/sh -c '...'`, the shell re-parses the expansion. A crafted User-Name like `x'$(touch /tmp/pwned)'` escapes the surrounding quotes and runs arbitrary commands **before the Go binary starts**.

Crucially, Go-side input validation (`isValidCashuToken`, `isValidLNURLw`, etc.) cannot protect against this — the injection happens during shell parsing, before the Go binary receives any data.

### Fix

Removed `/bin/sh -c` entirely. FreeRADIUS now `execve()`'s a wrapper script directly, which means each `%{...}` expansion becomes its own argv element. `execve` does not re-parse argv — there is no shell in the chain.

**Wrapper** (`scripts/tollgate-auth-radius-delegated-wrapper.sh`, installed at `/usr/local/libexec/tollgate-auth-radius-delegated`):
- Uses `#!/bin/sh` + `set -eu`
- Sources `/etc/tollgate/secrets.env`
- Exports `TOLLGATE_AUTH_MODE=delegated` and `TOLLGATE_SESSIOND_URL=http://127.0.0.1:2121`
- Ends with `exec /usr/local/bin/tollgate-auth-radius "$@"`
- Does **not** use `eval`, does **not** concatenate args, does **not** filter input

**Updated configs:**
```radius
# cashu-exec-delegated
program = "/usr/local/libexec/tollgate-auth-radius-delegated %{User-Name} %{Calling-Station-Id} %{User-Password} %{Cleartext-Password}"

# tollgate-acct
program = "/usr/local/libexec/tollgate-auth-radius-delegated --accounting %{Acct-Status-Type} %{Acct-Session-Id} %{Calling-Station-Id} %{User-Name} %{Acct-Session-Time} %{Acct-Input-Octets} %{Acct-Output-Octets} %{Acct-Terminate-Cause} %{NAS-IP-Address}"
```

### Regression prevention (three layers)

1. **Pre-commit hook** (`scripts/git-hooks/pre-commit`): refuses to commit any change to `config/freeradius/*` if it combines `/bin/sh -c` with `%{` on a non-comment line.
2. **CI test** (`make test-freeradius-config`, `make test-all-available`): runs `scripts/check-freeradius-configs.sh`. Plug into `.github/workflows/unit-tests.yml`.
3. **Server-side guard** (in `Makefile` `deploy-radius-config`): the deploy target scp's the guard to the server and runs it against `/etc/freeradius/3.0` **before** `freeradius -XC`. If the check fails, the deploy aborts without restarting FreeRADIUS.

The check script (`scripts/check-freeradius-configs.sh`) skips comment lines so the security-warning comments in the configs (which legitimately mention both `/bin/sh -c` and `%{` to document the trap) don't trip it.

### Verification

End-to-end injection test against the live server: sent a RADIUS Access-Request with User-Name `x$(touch /tmp/pwned)` and an Accounting-Request with Acct-Session-Id `x'$(touch /tmp/pwned-acct)'`. Neither marker file was created — confirming the shell metacharacters were passed as literal argv elements and never reached a shell parser. The auth path correctly returned `Access-Reject` (the Go binary rejected the malformed token).

---

## Finding 2 — World-readable Nostr private key (MEDIUM)

### Vulnerable state

```
-rw-r--r-- 1 root root 256 Jun 22 13:51 /etc/tollgate/settle.env
```

The file contained `TOLLGATE_OPERATOR_NSEC=<bech32 private key>` — a Nostr operator key that can sign arbitrary Nostr events (settlement reports, DMs, zap receipts) on behalf of the operator. Mode 0644 meant **any local user** could read it.

The README's threat model explicitly states guest shells are adversarial ("Guests can run arbitrary code"). Any guest who ran `cat /etc/tollgate/settle.env` could exfiltrate the operator nsec.

### Fix

```
chown root:tollgate /etc/tollgate/settle.env
chmod 0640 /etc/tollgate/settle.env
```

Now only `root` and members of the `tollgate` group can read it. The `tollgate` user (which `tollgate-settle` runs as) needs read access to function.

### Verification

```bash
$ ls -la /etc/tollgate/settle.env
-rw-r----- 1 root tollgate 256 Jun 22 13:51 /etc/tollgate/settle.env
$ sudo -u tollgate test -r /etc/tollgate/settle.env && echo OK
OK
$ sudo -u nobody test -r /etc/tollgate/settle.env && echo BAD || echo "blocked"
blocked
```

---

## Finding 3 — Daemon graceful-shutdown loop (MEDIUM)

### Symptom

`systemctl stop tollgate-daemon` took 45 seconds (systemd's `TimeoutStopSec`) and ended with SIGKILL. During those 45 seconds the daemon logged millions of identical `"accept error","error":"accept unix /run/tollgate/tollgate.sock: use of closed network connection"` lines (journald suppressed 2M+ per 30s window).

### Root cause

The accept loop's "is this the listener-closed error?" check used a broken comparison:

```go
func isClosedErr(err error) bool {
    return err != nil && (err == net.ErrClosed || err.Error() == "use of closed network connection")
}
```

When `listener.Close()` is called, subsequent `Accept()` returns a `*net.OpError` **wrapping** `net.ErrClosed`. The `OpError.Error()` method formats as `"accept <addr>: use of closed network connection"` — with a prefix. So:

- `err == net.ErrClosed` → **false** (err is `*OpError`, not `net.ErrClosed`)
- `err.Error() == "use of closed network connection"` → **false** (prefix mismatch)

The check returned false, the loop logged the error and `continue`d — re-entering `Accept()` which immediately returned the same closed error. Infinite spin.

The shutdown goroutine correctly logged `"Shutdown complete"` and closed the `shutdownComplete` channel, but the main goroutine never reached the `<-shutdownComplete` branch — it was stuck in the error branch.

### Fix

Use `errors.Is` which unwraps `*OpError`:

```go
import "errors"

func isClosedErr(err error) bool {
    return err != nil && errors.Is(err, net.ErrClosed)
}
```

### Verification

```
Before: systemctl stop → 45s wall → Main PID killed, status=9/KILL, 27s CPU
After:  systemctl stop → 10ms wall → Main PID exited, status=0/SUCCESS, 73ms CPU

Last journal lines (clean shutdown):
  "Shutting down gracefully..." "signal":"terminated"
  "Shutdown complete"
  Deactivated successfully.
```

---

## Finding 4 — Unnecessary root: `tollgate-settle` (MEDIUM)

### Vulnerable state

`tollgate-settle` is a oneshot job that reads ledger JSONL files and publishes encrypted Nostr DMs (NIP-17) to operator relays. It performed **no privileged operations** — no `exec.Command`, no `chown`/`chmod`, no `systemctl`, no raw syscalls. Yet the systemd unit had no `User=` directive, so it ran as **root**.

### Fix

Migrated to `User=tollgate Group=tollgate SupplementaryGroups=cashu-wallet`. Verified by running a real settlement cycle as `tollgate` — successfully published a settlement DM for 1242 sats / 133 accepted / 21 rejected transactions.

### Defense-in-depth added

The unit now also has: `NoNewPrivileges`, `ProtectSystem=strict`, `ReadWritePaths=/opt/tollgate-auth /opt/cashu-tollgate`, `ProtectHome`, `ProtectKernel*`, `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6` (needs AF_INET for outbound wss:// to relays), `CapabilityBoundingSet=` (drop all caps), `SystemCallFilter=@system-service`.

Note: `tollgate-settle` cannot use `IPAddressDeny=any` because it must reach public Nostr relays. Defense comes from the unprivileged user, ProtectSystem, and SystemCallFilter.

### Window 3 — tollgate-auth-ssh SystemCallFilter + capability fix (2026-07-09)

The SSH service unit (`config/systemd/tollgate-auth-ssh.service`) had two
issues that prevented the auth pipeline from working under the hardened
systemd configuration:

1. **SystemCallFilter too restrictive**: `@system-service` alone blocks
   `chroot(2)`, `setgroups(2)`, `setgid(2)`, `setresuid(2)`, `setresgid(2)`
   — all needed by `chroot --userspec=nobody:nogroup`. The chroot process
   was silently killed by seccomp, causing the PTY master to receive EIO
   immediately with zero bytes of shell output. Fixed by adding
   `@file-system chroot setgroups setgid setresuid setresgid` to the filter.

2. **CapabilityBoundingSet drops CAP_DAC_OVERRIDE**: Root without
   `CAP_DAC_OVERRIDE` cannot bypass file permissions. The wallet dir
   (`/var/lib/cashu-wallet/`, mode 770, owned by `cashu-wallet:cashu-wallet`)
   and session dir (`/opt/cashu-tollgate/sessions/`, mode 775, owned by
   `tollgate:tollgate`) were inaccessible to the SSH service. Fixed by adding
   `SupplementaryGroups=cashu-wallet tollgate` (same pattern as FreeRADIUS).

3. **`cp -a` needs CAP_CHOWN**: The jail copy used `cp -a` (archive mode)
   which preserves ownership via `chown(2)`. This requires `CAP_CHOWN`,
   not in the bounding set. Changed to `cp -r --preserve=mode` which
   preserves file permissions but not ownership.

The SSH service unit now uses:
`SystemCallFilter=@system-service @file-system chroot setgroups setgid setresuid setresgid`
with `SupplementaryGroups=cashu-wallet tollgate`.

---

## Finding 5 — Internal services bound to 0.0.0.0 (MEDIUM)

### Vulnerable state

| Service | Port | Was bound | Should be |
|---|---|---|---|
| `tollgate-daemon` | 8091 | `:8091` (all interfaces) | `127.0.0.1:8091` |
| `tollgate-auth-ocpi` | 8093 | `:8093` | `127.0.0.1:8093` |
| `tollgate-webssh` | 8092 | `:8092` | `127.0.0.1:8092` |
| `tollgate-net` | 2121 | `0.0.0.0:2121` (binary has no `--bind` flag) | loopback-only (BPF) |
| `tollgate-csms` | 8887 | `*:8887` (ocpp-go lib binds `0.0.0.0`) | loopback-only (BPF) |

All five are reverse-proxied by Caddy via `reverse_proxy localhost:<port>` or are only consumed by sibling services over loopback. None need direct external exposure.

### Fix

Three mechanisms depending on whether the binary accepts a bind flag:

1. **Env var change** (`tollgate-webssh`, `tollgate-daemon`, `tollgate-auth-ocpi`): set `TOLLGATE_WEBSSH_ADDR=127.0.0.1:8092`, `TOLLGATE_HTTP_ADDR=127.0.0.1:8091`, `TOLLGATE_OCPI_ADDR=127.0.0.1:8093`. Now `ss` shows `127.0.0.1:<port>`.

2. **Systemd `IPAddressDeny`** (`tollgate-net`, `tollgate-csms`): the binaries lack a `--bind` flag, so they bind `0.0.0.0` regardless. But systemd installs a cgroup BPF program that filters packets at the kernel level. The drop-in override:
   ```ini
   [Service]
   IPAddressDeny=any
   IPAddressAllow=localhost
   ```
   drops all non-loopback packets for the unit's processes. `ss` still shows `0.0.0.0:<port>` (the bind succeeded) but external TCP SYN packets never reach the application.

### Verification

External probes from off-host:
```
$ nc -z -w 3 66.92.204.237 2121   → timeout (filtered)
$ nc -z -w 3 66.92.204.237 8887   → timeout (filtered)
$ nc -z -w 3 66.92.204.237 8092   → refused (loopback bind)
$ nc -z -w 3 66.92.204.237 8093   → refused (loopback bind)
$ nc -z -w 3 66.92.204.237 8091   → refused (loopback bind)
```

Local probes still succeed (services still work for caddy):
```
$ nc -z 127.0.0.1 2121   → succeeded
$ nc -z 127.0.0.1 8887   → succeeded
$ nc -z 127.0.0.1 8092   → succeeded
```

---

## Finding 6 — Repo/prod systemd drift (LOW)

### Vulnerable state

The repo's `config/systemd/*.service` files lacked `User=` directives entirely (defaulting to root), while the production server had already migrated most services to the `tollgate` user via out-of-band `systemctl edit`. The repo and prod were silently diverging — a fresh `make deploy` from the repo would have **re-rooted all services**.

### Fix

Synced repo units to prod reality with proper hardening. See Finding 7 for the directive list.

---

## Finding 7 — No systemd hardening directives (LOW)

### Vulnerable state

All repo systemd units had at most `NoNewPrivileges=true` and `ProtectSystem=strict`. None had capability bounding, syscall filters, or address-family restrictions.

### Fix

Applied the full hardening matrix to every unit:

| Directive | Effect |
|---|---|
| `NoNewPrivileges=true` | No setuid binaries can escalate |
| `ProtectSystem=strict` | `/usr`, `/boot`, `/etc` read-only except `ReadWritePaths` |
| `PrivateTmp=true` | Private `/tmp` namespace (no cross-service tmp races) |
| `ProtectHome=true` | `/home`, `/root`, `/run/user` inaccessible |
| `ProtectKernelTunables=true` | `/proc/sys` read-only |
| `ProtectKernelModules=true` | Cannot load kernel modules |
| `ProtectKernelLogs=true` | Cannot read kernel logs |
| `ProtectControlGroups=true` | `/sys/fs/cgroup` read-only |
| `ProtectClock=true` | Cannot change system clock |
| `ProtectHostname=true` | Cannot change hostname/domainname |
| `ProtectProc=invisible` | Cannot see other users' processes |
| `RestrictSUIDSGID=true` | Cannot create setuid binaries |
| `RemoveIPC=true` | IPC objects removed when service stops |
| `RestrictRealtime=true` | No realtime scheduling |
| `LockPersonality=true` | Cannot change execution domain |
| `RestrictNamespaces=true` | Cannot create any namespace type |
| `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6` | Only Unix + IPv4/IPv6 sockets |
| `CapabilityBoundingSet=` | Drop **all** capabilities (unprivileged services) |
| `AmbientCapabilities=` | No ambient caps either |
| `SystemCallFilter=@system-service` | Only syscalls in the `@system-service` group |
| `SystemCallArchitectures=native` | No 32-bit/foreign-arch syscall injection |

`tollgate-auth-ssh` keeps root but is bounded to **4 capabilities** only: `CAP_SYS_CHROOT`, `CAP_SETUID`, `CAP_SETGID`, `CAP_KILL`. Even if the binary is fully subverted, it cannot `iptables`, packet-capture, load modules, mount filesystems, or reboot the host. The SSH service also uses `SupplementaryGroups=cashu-wallet tollgate` for wallet and session directory access (since `CapabilityBoundingSet` drops `CAP_DAC_OVERRIDE`), and `SystemCallFilter=@system-service @file-system chroot setgroups setgid setresuid setresgid` (the additional syscalls are needed for `chroot --userspec`).

`sync-caddy-certs` keeps root (needs `systemctl restart freeradius`) but has all the other hardening.

---

## Finding 8 — SSH jail paths from hash-derived strings (LOW)

### Observation

The audit flagged `cmd/tollgate-auth-ssh/main.go` lines 52–71, 144, 182 as a path-traversal risk: `jailPath := SessionDir + "/" + guest`, where `guest` is used in `cp -r --preserve=mode`, `rm -rf`, and `chroot` invocations.

### Investigation

Traced `guest` to its source in `cmd/tollgate-auth-ssh/auth.go` line 176:
```go
func guestUsername(tokenStr string) string {
    return "g-" + cashu.TokenHash(tokenStr)[:8]
}
```

`cashu.TokenHash` returns a hex-encoded SHA256 hash. The guest name is therefore always `g-` + 8 chars of `[0-9a-f]` — **safe by construction**. The audit was overcautious.

### Fix (defense-in-depth)

Despite the construction being safe, added a regex validator (`safeGuestNamePattern = ^[a-z0-9-]{1,64}$`) called once at the top of the session handler, plus inside `createJail` and `cleanupJail`. If the construction logic ever changes (or someone introduces a new code path that bypasses `guestUsername`), the validator rejects anything that could traverse paths or break argv boundaries.

25 test cases lock the validator behavior:
- Accepts: `g-c3aa7bfb`, `radius-delegated-c3aa7bfb`, all current constructions
- Rejects: `../etc`, `../../etc/passwd`, `;rm -rf /`, `$(touch x)`, `` `id` ``, 70+ char overflow, bad hyphen placement

---

## Finding 9 — WireGuard pubkey passed to wg without validation (LOW)

### Observation

`cmd/tollgate-daemon/wg.go` lines 166, 171:
```go
exec.Command("wg", "set", wgIfName, "peer", pubkey, "allowed-ips", clientIP+"/32")
```

`pubkey` came from a JSON HTTP body (`req.Pubkey`) and reached the subprocess with only an empty-string check. `exec.Command` uses `execve` (no shell), so this was not exploitable. But it lacked explicit format validation.

### Fix (defense-in-depth)

Added `wgPubkeyPattern = ^[A-Za-z0-9+/]{43}=$` and a base64 decode-and-length check. `wgAddPeer` and `wgRemovePeer` now both refuse anything that doesn't look like a WireGuard Curve25519 public key (44 base64 chars, decodes to 32 bytes).

Test cases cover: real-shaped pubkeys pass, shell metacharacters and path-traversal strings fail, wrong-length strings fail, bad base64 fails.

---

## Finding 10 — No regression guard for FreeRADIUS pattern (LOW)

### Observation

The original FreeRADIUS fix had no automated check. A future contributor could revert the configs to `/bin/sh -c` and nothing would catch it until exploitation.

### Fix

Three-layer regression prevention (see Finding 1 above for details):
1. Pre-commit hook (local developer machine)
2. CI test target (`make test-freeradius-config`)
3. Server-side guard (in `deploy-radius-config` Makefile target — runs against `/etc/freeradius/3.0` before `freeradius -XC`)

The check script (`scripts/check-freeradius-configs.sh`) intentionally skips comment lines so the security-warning comments can mention both `/bin/sh -c` and `%{` to document the trap.

---

## Services still requiring root (justified)

| Service | Why root | Caps bounded? |
|---|---|---|
| `tollgate-auth-ssh` | Calls `useradd`/`userdel`, `chroot(2)`, `chown` of guest home dirs, PTY ioctl | Yes — only `CAP_SYS_CHROOT CAP_SETUID CAP_SETGID CAP_KILL` |
| `sync-caddy-certs` (oneshot timer) | `chown root:freerad` on certs + `systemctl restart freeradius` | All caps (trusted admin script, runs every 6h) |

All other services run as the unprivileged `tollgate` user.

---

## Things explicitly NOT fixed

These items were intentionally deferred (require architectural changes outside the scope of this audit window):

1. **tollgate-auth-ssh privilege split**: Splitting into a privileged account-creation helper (setuid binary) + unprivileged SSH handler would let the SSH listener itself drop root. Documented in the README as a known design tradeoff. See "Future Directions" in the Docker migration roadmap.

2. **No rate limiting** on RADIUS auth — intentional per README for frictionless test deployment. Should be added before production with real-value tokens.

3. **`clients.conf` accepts `0.0.0.0/0`** — intentional for open onboarding. Should be restricted to known NAS IPs in production.

4. **No HTTPS on `tollgate-daemon:8091`** — loopback only behind Caddy, which terminates TLS.

5. **Test token allowlist (`(?i)test` regex)** — only test mints accepted. Removing this constraint is a product decision, not a security fix.

---

## Audit methodology

The audit was conducted in three phases:

1. **Automated shell-out scan**: Two parallel `explore` agents swept the codebase for `exec.Command`, `os.StartProcess`, `syscall.Exec`, `eval`, `$(...)`, `ntlm_auth`, `program =`, `update { ... }` patterns. Each hit was triaged for shell-vs-execve and attacker-control.

2. **Privilege landscape mapping**: Every service's systemd unit was read for `User=`/`Group=` directives; production state was sampled via `systemctl show`. Each Go binary's source was scanned for `net.Listen`, `http.ListenAndServe`, `os.OpenFile`, and privileged syscalls.

3. **Deployment verification**: Every fix was deployed to the production server and verified by external probes, internal probes, and real service operations (settlement DM sent, RADIUS injection test run, SSH username validation confirmed).

This document will be updated as new findings emerge or as the Docker migration roadmap (see `docs/DOCKER_MIGRATION_ROADMAP.md`) is executed.
