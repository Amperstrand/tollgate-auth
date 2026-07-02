# Host Hardening for Semi-Public SSH Access

Applied to the tollgate-ssh production server (nodns.shop / 46.224.104.12).

## Overview

The tollgate-ssh server accepts Cashu ecash tokens for SSH access. Users are chroot'd into a jail as `nobody:nogroup`. This document describes the host-level hardening that prevents guest users from:

- Seeing other users' processes
- Reading login history
- Making outbound network connections
- Exhausting system resources
- Reading system logs or secrets
- Enumerating system services

## Hardening Measures

### 1. Process Isolation (`hidepid=2` on `/proc`)

```fstab
proc /proc proc defaults,hidepid=2,gid=procview 0 0
```

Non-root users can only see their own processes in `/proc`. Admin users are added to the `procview` group to retain full visibility.

**Before**: `nobody` could see all 165 running processes, including tollgate-daemon, freeradius, cdk-cli, and admin SSH sessions.

**After**: `nobody` sees only 2 processes (its own `ps`/`sudo` invocation).

The `systemd-logind` service gets a `SupplementaryGroups=procview` override to function correctly with `hidepid=2`.

### 2. Login History Restriction

```bash
chmod 640 /var/log/wtmp /var/log/lastlog
chown root:utmp /var/log/wtmp /var/log/lastlog
```

**Before**: `nobody` could run `last` and see all login times, IPs, and durations — including admin root SSH sessions.

**After**: `last` returns 0 lines for `nobody`.

Also set `PrintLastLog no` in `/etc/ssh/sshd_config` for admin SSH.

### 3. Sysctl Hardening (`/etc/sysctl.d/99-tollgate-harden.conf`)

| Setting | Value | Purpose |
|---|---|---|
| `fs.suid_dumpable` | 0 | Disable SUID core dumps (was 2) |
| `fs.protected_symlinks` | 1 | Prevent symlink races in world-writable dirs |
| `fs.protected_hardlinks` | 1 | Prevent hardlink races |
| `fs.protected_fifos` | 2 | Restrict FIFO creation in sticky dirs |
| `fs.protected_regular` | 2 | Restrict file creation in sticky dirs |
| `kernel.kptr_restrict` | 1 | Hide kernel pointers from non-root |
| `kernel.dmesg_restrict` | 1 | Restrict dmesg to root (was already set) |
| `kernel.perf_event_paranoid` | 2 | Restrict perf events |
| `kernel.yama.ptrace_scope` | 1 | Restrict ptrace to parent-only |
| `net.ipv4.tcp_timestamps` | 0 | Disable TCP timestamps (prevent uptime guessing) |

### 4. PAM Resource Limits (`/etc/security/limits.d/tollgate-guests.conf`)

| Resource | Soft | Hard | Purpose |
|---|---|---|---|
| `nproc` | 32 | 64 | Prevent fork bombs |
| `maxlogins` | — | 20 | Cap concurrent guest sessions |
| `fsize` | — | 1 GB | Prevent disk fill |
| `cpu` | — | 600s | Prevent crypto mining |
| `nofile` | 128 | 256 | Limit file descriptors |
| `as` | — | 2 GB | Limit virtual memory |
| `core` | — | 0 | Disable core dumps |

### 5. Network Restriction (iptables)

Outbound network access is blocked for UID 65534 (`nobody`):

```bash
iptables -A OUTPUT -m owner --uid-owner 65534 -j tollgate-guests
# Chain allows: loopback, established connections, DNS to 127.0.0.1
# Everything else: REJECT
```

**Before**: `nobody` could `curl http://1.1.1.1` — full outbound network access.

**After**: Outbound connections from `nobody` are rejected. DNS to the local resolver is still allowed.

### 6. SSH Config Hardening (`/etc/ssh/sshd_config`)

| Setting | Value |
|---|---|
| `X11Forwarding` | no |
| `AllowTcpForwarding` | no |
| `AllowAgentForwarding` | no |
| `PermitTunnel` | no |
| `MaxAuthTries` | 3 |
| `ClientAliveInterval` | 300 |
| `ClientAliveCountMax` | 2 |
| `PrintLastLog` | no |
| `LoginGraceTime` | 30 |

### 7. Shared Memory Hardening

```fstab
tmpfs /dev/shm tmpfs rw,nosuid,nodev,noexec 0 0
```

### 8. Polkit Restriction

`systemctl` access denied for `nobody` via polkit rule (`/etc/polkit-1/rules.d/10-tollgate-guests.rules`).

## Verification Results

After applying all measures:

| Check | Result |
|---|---|
| `nobody` process visibility | 2 processes (own only) |
| `nobody` `last` output | 0 lines |
| `nobody` outbound network | BLOCKED |
| `nobody` `/var/log/syslog` | NOT readable |
| `nobody` `/var/log/auth.log` | NOT readable |
| `nobody` `/etc/tollgate/secrets.env` | NOT readable (mode 600 root:root) |
| `nobody` `/proc/1/environ` | NOT readable (mode 400 root:root) |
| `nobody` `dmesg` | NOT readable (dmesg_restrict=1) |
| `nobody` `/root/` | NOT readable |
| `fs.suid_dumpable` | 0 |
| `/dev/shm` | noexec,nosuid,nodev |

## Remaining Considerations

### `ss`/netlink access
`nobody` can still see listening ports via `ss` (reads from netlink, not `/proc`). This is not exploitable from inside the chroot jail (no `ss` binary available). For full-shell access, consider network namespaces.

### SUID binaries
24 SUID binaries exist on the system (standard Debian set: sudo, su, passwd, mount, etc.). These are well-audited. `nobody` is not in sudoers.

### `/tmp` sharing
The chroot jail provides a private `/tmp` per session. For full-shell users, consider per-session tmpfs or `PrivateTmp` in a systemd wrapper.

## Applying the Hardening

```bash
# On the target server as root:
bash scripts/harden-host.sh -v
systemctl restart systemd-logind
```

The script is idempotent and safe to run multiple times.

## Script

See [`scripts/harden-host.sh`](../scripts/harden-host.sh) for the full implementation.
