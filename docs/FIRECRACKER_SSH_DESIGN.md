# Firecracker microVM per SSH Session — Architecture Design

**Status:** Prototype verified (vsock bridge tested end-to-end)
**Date:** July 2025
**Depends on:** [vps-on-demand](https://github.com/Amperstrand/vps-on-demand) Firecracker infrastructure

## Verification (July 10 2025)

Tested on SHC Dev VPS Professional (4C/16GB, nested KVM, Debian 13):

- Firecracker v1.16.0 boots a microVM with vsock device in ~1 second
- `tollgate-vm-agent` (1.9MB Go binary) listens on AF_VSOCK port 52
- Host-side vsock handshake (CONNECT 52 / OK) succeeds
- Interactive shell session works: host sends commands, guest busybox executes and returns output
- Kernel modules required in initramfs: `virtio_mmio`, `vsock`, `vmw_vsock_virtio_transport_common`, `vmw_vsock_virtio_transport`

## Overview

Replace the current chroot-jail SSH session provisioning with Firecracker
microVMs. Each paying user gets a full Linux VM — hardware-level isolation,
real root, real networking — instead of a shared-kernel chroot.

**Current flow:**
```
ssh cashuBtoken@host:2222
  → verify token → redeem → cp jail template → chroot → tollgate-shell
```

**Proposed flow:**
```
ssh cashuBtoken@host:2222
  → verify token → redeem → launch Firecracker VM → vsock bridge → guest shell
```

The auth pipeline (token → verify → redeem) stays identical. Only the
session-provisioning tail changes.

## Why

The current chroot jail has fundamental limitations documented in the
[Security Disclaimer](../README.md#security-disclaimer):

- Shared kernel — guests can exploit kernel vulnerabilities
- No real isolation — fork bombs, resource exhaustion affect the host
- Root required for the SSH service itself
- No per-user network isolation

Firecracker provides **hardware-level VM isolation** with a **125ms boot
time** and **5MB VMM overhead** per VM. Users get a real Linux environment
that cannot escape to the host.

## Performance Targets

| Metric | Chroot (current) | Firecracker (proposed) |
|---|---|---|
| Session start latency | ~5ms (cp + chroot) | ~300ms cold, ~10ms pre-warmed |
| Isolation | Weak (shared kernel) | Strong (hardware VM) |
| Memory per session | ~50MB (busybox jail) | 128MB guest + 5MB VMM |
| Capabilities | User can compile, fork-bomb, scan | User gets real root in isolated VM |

The 300ms cold-start is well within the user's 2-5 second target. With a
pre-warmed pool, it drops to ~10ms.

## Architecture

```
User SSH client
    │
    │ ssh -p 2222 cashuBtoken@host
    │
    ▼
┌─────────────────────────────────────────────────────┐
│  Host (KVM-capable, e.g. Hetzner AX41)              │
│                                                     │
│  tollgate-auth-ssh (:2222)                          │
│    1. Verify Cashu token (same pipeline)            │
│    2. Create Firecracker VM via daemon API          │
│    3. Bridge SSH PTY ↔ vsock ↔ guest agent          │
│    4. Timer: kill VM on session timeout              │
│                                                     │
│  firecracker-daemon (:8081)                         │
│    (from vps-on-demand — unchanged)                 │
│    POST /vms → creates rootfs, boots VM, returns ID │
│                                                     │
│  ┌─────────────────────────────────────────┐        │
│  │ Firecracker microVM (per session)       │        │
│  │                                         │        │
│  │  Kernel: Anvil (~33MB, stripped)        │        │
│  │  Rootfs: Alpine ext4 (overlay)          │        │
│  │  Guest agent: tollgate-vm-agent         │        │
│  │    ├─ Listens on AF_VSOCK port 52       │        │
│  │    ├─ Creates PTY on connection         │        │
│  │    └─ Spawns /bin/sh as root            │        │
│  │                                         │        │
│  │  Networking: NAT via host iptables      │        │
│  │  Disk: copy-on-write overlay            │        │
│  │  Memory: 128-256MB                      │        │
│  │  CPU: 1 vCPU                            │        │
│  └─────────────────────────────────────────┘        │
│                                                     │
│  vsock bridge: Unix socket ↔ AF_VSOCK port 52      │
└─────────────────────────────────────────────────────┘
```

## Component Specification

### 1. tollgate-auth-ssh (modified)

**Current code path** (`cmd/tollgate-auth-ssh/main.go`):
```go
// After token verified + redeemed:
jailPath := SessionDir + "/" + guest
cmd := exec.Command("chroot", "--userspec=nobody:nogroup", jailPath, "/bin/tollgate-shell")
ptmx, err := pty.Start(cmd)
// ... PTY bridge between SSH session and chroot process
```

**New code path:**
```go
// After token verified + redeemed:

// 1. Create VM via firecracker-daemon API
vmID, vsockPath, err := createFirecrackerVM(seconds, tokenData.Amount)
if err != nil { /* fallback to chroot or reject */ }

// 2. Connect to VM's vsock via Firecracker's Unix socket proxy
vsockConn, err := vsockDial(vsockPath, 52) // port 52 = guest agent
if err != nil { /* cleanup VM, reject */ }

// 3. Bridge SSH PTY ↔ vsock connection
// (same io.Copy pattern as current PTY bridge, but vsockConn instead of ptmx)
go io.Copy(vsockConn, sshSession)    // SSH → VM
io.Copy(sshSession, vsockConn)       // VM → SSH

// 4. Timer: destroy VM on session timeout
time.AfterFunc(duration, func() {
    destroyFirecrackerVM(vmID)
})
```

**Key change**: `exec.Command("chroot", ...)` + `pty.Start()` becomes
`createFirecrackerVM()` + `vsockDial()`. The SSH ↔ session bridge is the
same pattern (bidirectional copy), just different file descriptors.

**Fallback**: If Firecracker creation fails (no KVM, out of memory),
fall back to chroot jail. Log the fallback for monitoring.

### 2. firecracker-daemon (from vps-on-demand — minimal changes)

The existing daemon at `vps-on-demand/firecracker/firecracker-daemon.py`
already handles:
- Rootfs creation from Alpine template
- Firecracker process management
- NAT/port forwarding setup
- VM lifecycle (create, list, destroy)

**Required addition**: expose a `vsock_path` field in the create-VM response.
The daemon already configures Firecracker with a vsock device — we just
need to return the Unix socket path so tollgate-auth-ssh can connect.

Current API response (`POST /vms`):
```json
{
  "vm_id": "abc123",
  "ssh_port": 24001,
  "ssh_command": "ssh root@host -p 24001"
}
```

Extended response:
```json
{
  "vm_id": "abc123",
  "ssh_port": 24001,
  "vsock_path": "/var/lib/vms/abc123/v.sock",
  "ssh_command": "ssh root@host -p 24001"
}
```

### 3. tollgate-vm-agent (new — guest-side)

A minimal Go binary that runs inside each VM as PID 1 (or launched by
init). It listens on AF_VSOCK and provides a shell.

**Responsibilities:**
- Listen on vsock port 52
- On connection: create a PTY, spawn `/bin/sh` as root
- Bridge vsock ↔ PTY (bidirectional copy)
- Handle window resize requests (vsock message → TIOCSWINSZ ioctl)
- Exit when vsock connection closes

**Implementation** (~100 lines of Go):
```go
package main

import (
    "os"
    "os/exec"
    "syscall"
    "github.com/creack/pty"
    "golang.org/x/sys/unix"
)

func main() {
    // Listen on vsock port 52
    fd, _ := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
    unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: 52})
    unix.Listen(fd, 1)
    
    for {
        connFd, _, _ := unix.Accept(fd)
        go handleSession(connFd)
    }
}

func handleSession(connFd int) {
    conn := os.NewFile(uintptr(connFd), "vsock")
    cmd := exec.Command("/bin/sh")
    cmd.Env = []string{"TERM=xterm-256color", "PS1=# "}
    ptmx, _ := pty.Start(cmd)
    
    go io.Copy(ptmx, conn)    // vsock → PTY (user input)
    io.Copy(conn, ptmx)       // PTY → vsock (shell output)
    
    cmd.Wait()
    conn.Close()
}
```

**Build**: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o tollgate-vm-agent`

**Packaging**: Statically compiled (~2MB), included in the Alpine rootfs
template at `/usr/local/bin/tollgate-vm-agent`. The rootfs init script
starts it as a background process.

### 4. vsock Bridge Protocol

**Host → Guest connection** (Firecracker vsock handshake):

```
1. Host connects to Unix socket: /var/lib/vms/<vm_id>/v.sock
2. Host sends: "CONNECT 52\n"
3. Firecracker forwards to guest's vsock port 52
4. Guest agent accepts
5. Host receives: "OK <host_port>\n"
6. Bidirectional stream established
```

**Message types** (after handshake):

The vsock connection is a raw byte stream. For basic shell access, no
framing protocol is needed — bytes flow bidirectionally just like a PTY.

For window resize support (optional, phase 2):
```
\x1b[8;<rows>;<cols>t   — DCS sequence for resize (parsed by guest agent)
```

Or a simple framed protocol:
```
[1 byte type][2 bytes length][payload]
type 0x01 = stdin/stdout data
type 0x02 = resize (rows=u16, cols=u16)
type 0x03 = signal (signal_number=u8)
```

**Phase 1**: Raw byte stream (no framing). Window resize not supported.
Phase 2: Add framed protocol for resize + signals.

### 5. Rootfs Strategy: Overlay

**Base image**: Read-only Alpine ext4 image (~100MB) containing:
- busybox + standard Alpine packages
- OpenSSH (for users who prefer SSH-in-SSH)
- tollgate-vm-agent (vsock listener)
- Standard /dev nodes

**Per-session overlay**: Each VM gets a copy-on-write overlay:
```
/var/lib/vms/<vm_id>/rootfs.overlay  (sparse, grows as needed)
```

Firecracker supports this via its drive configuration:
```json
{
  "drive_id": "rootfs",
  "path_on_host": "/var/lib/vms/<vm_id>/rootfs.overlay",
  "is_root_device": true,
  "is_read_only": false
}
```

The overlay is created by copying the base image with `cp --reflink=auto`
(on btrfs) or `qemu-img create -b base.ext4 -f qcow2 overlay.qcow2` (on
ext4/xfs). On cleanup, the overlay file is deleted.

**Disk usage**: Base image stored once (~100MB). Each overlay starts at 0
and grows with user activity. Typical session: 10-50MB. Max configurable.

## Lifecycle Management

### Session start
```
1. SSH connection received → token verified → redeemed
2. POST /vms to firecracker-daemon (amount=8 → 256MB RAM, 80s timeout)
3. Daemon creates overlay, boots VM (~300ms)
4. tollgate-auth-ssh dials vsock → guest agent
5. SSH PTY bridged to vsock → user gets shell
```

### Session timeout
```
1. Timer fires after `amount * rate_per_sat` seconds
2. Send "Session ending" message to user via vsock
3. DELETE /vms/<vm_id> to firecracker-daemon
4. Daemon sends CtrlAltDel to Firecracker → VM shuts down
5. Overlay file deleted
6. SSH session closed
```

### Session disconnect (user exits)
```
1. vsock connection closes (EOF from SSH side)
2. Guest agent's shell exits
3. tollgate-auth-ssh detects disconnect → DELETE /vms/<vm_id>
4. Same cleanup as timeout
```

### Pre-warmed pool (phase 2)
```
1. Daemon maintains N pre-boototed VMs in "paused" state
2. On session start: resume a paused VM (~10ms)
3. On session end: destroy (don't re-pause — fresh VM per session for security)
4. Background goroutine replenishes pool
```

## Resource Limits

| Parameter | Default | Configurable via |
|---|---|---|
| VM memory | 256MB | `TOLLGATE_VM_MEM_MB` |
| VM vCPUs | 1 | `TOLLGATE_VM_CPUS` |
| VM disk overlay max | 512MB | `TOLLGATE_VM_DISK_MB` |
| Session max duration | amount × 10s/sat | Rate constant |
| Concurrent VMs | host_mem / (vm_mem + 5MB) | Host hardware |

**Host sizing** (for concurrent sessions):
- 8GB RAM host → ~25 concurrent VMs (256MB each + 5MB VMM overhead)
- 16GB RAM host → ~55 concurrent VMs
- 32GB RAM host → ~120 concurrent VMs

## Integration with vps-on-demand

The vps-on-demand project provides the Firecracker daemon, rootfs builder,
and networking setup. tollgate-auth-ssh calls it as a local API:

```
tollgate-auth-ssh (:2222)
    │
    │ POST http://127.0.0.1:8081/vms
    │ {"cpus":1, "mem_mb":256, "disk_mb":512, "duration_sec":80}
    │
    ▼
firecracker-daemon (:8081)
    │
    ├── create_rootfs() → overlay from Alpine template
    ├── start_firecracker() → boot VM with Anvil kernel
    ├── setup_nat() → iptables rules for VM outbound
    └── return {"vm_id":"...", "vsock_path":"..."}
```

**No changes to firecracker-daemon core logic** — only add `vsock_path`
to the API response. The daemon already creates the vsock device for
each VM.

## Deployment Plan

### Phase 1: Prototype on KVM host
1. Provision bare-metal host with KVM (Hetzner AX41 or existing hardware)
2. Install vps-on-demand (`bash public/install-reseller.sh`)
3. Build `tollgate-vm-agent` and add to Alpine rootfs template
4. Modify `tollgate-auth-ssh` to call firecracker-daemon API
5. Test: SSH with token → VM launches → shell works → VM destroys on exit

### Phase 2: Pre-warmed pool
1. Add VM pool manager to firecracker-daemon
2. Keep N VMs pre-booted, resume on session start
3. Measure: cold start vs warm start latency

### Phase 3: Production migration
1. Deploy tollgate-auth-ssh + firecracker-daemon on production host
2. Keep chroot as fallback (if VM creation fails, use chroot)
3. Monitor: VM boot success rate, session latency, resource usage
4. Gradual rollout: 10% of sessions use VM, rest use chroot

## Security Analysis

### Attack surface comparison

| Threat | Chroot jail | Firecracker VM |
|---|---|---|
| Kernel exploit | Guest escapes to host | Blocked by hardware VT-x |
| Fork bomb | Affects host + all sessions | Contained in VM (cgroup limits) |
| Network scan | Host network visible | VM has NAT'd network, isolated |
| Resource exhaustion | Shared with host | Bounded by VM config |
| Side-channel | Possible (shared caches) | Reduced (separate vCPU) |

### VM → Host escape
Firecracker's minimal device model (virtio-net, virtio-block, serial,
vsock — no USB, no SCSI, no frame buffer) means a very small attack
surface. The Firecracker VMM itself is 50K lines of Rust (vs QEMU's
1.4M lines of C).

### vsock security
The vsock connection is host-initiated. The guest agent only accepts
connections from the host's Unix socket. A VM user cannot initiate
connections to the host or other VMs via vsock.

### Per-session isolation
Each session gets a fresh VM with a fresh rootfs overlay. No data
persistence between sessions. No shared state. When the VM is destroyed,
all data is gone.

## Open Questions

1. **Shell choice**: Should the VM provide `busybox sh` (minimal) or
   `bash` + development tools (heavier rootfs, slower boot)?

2. **Networking policy**: Should VMs have outbound internet access?
   (Current vps-on-demand gives NAT'd outbound. For a tollgate demo,
   restricted or no networking might be safer.)

3. **Tollgate-shell integration**: The current chroot runs `tollgate-shell`
   (arcade games). Should the VM agent run the same shell, or give users
   a real shell with `timeleft` as a command?

4. **Token → VM spec mapping**: How many sats = how much RAM/CPU/disk/time?
   Current: 1 sat = 10 seconds. Proposed: 1 sat = 10s + baseline VM specs.
   Or: higher-value tokens get more resources?

5. **Fallback behavior**: When Firecracker is unavailable (no KVM, OOM),
   should tollgate-auth-ssh fall back to chroot or reject the session?

## References

- [Firecracker SPECIFICATION.md](https://github.com/firecracker-microvm/firecracker/blob/main/SPECIFICATION.md) — boot time, memory overhead specs
- [Firecracker vsock docs](https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md) — host↔guest communication
- [Fly.io Machines](https://fly.io/blog/fly-machines/) — production pre-warming strategy
- [mdlayher/vsock](https://github.com/mdlayher/vsock) — Go AF_VSOCK library
- [sahil-shubham/bhatti](https://github.com/sahil-shubham/bhatti) — SSH-over-vsock reference implementation
- [vps-on-demand](https://github.com/Amperstrand/vps-on-demand) — existing Firecracker daemon + rootfs builder

## Bugs Found During Testing

### B1: virtio_net fails to load — missing failover module dependency

**Root cause**: Debian 13 cloud kernel has `CONFIG_VIRTIO_NET=m`, `CONFIG_NET_FAILOVER=m`, `CONFIG_FAILOVER=m` (all modules). The dependency chain is `virtio_net → net_failover → failover`. If `failover.ko` is not loaded first, `net_failover` fails with `Unknown symbol failover_register`, which cascades to `virtio_net` failing with `Unknown symbol net_failover_create`.

**Fix**: Include all three modules in the initramfs and load in order: `failover → net_failover → virtio_net`.

**Status**: Fix written (`/tmp/rebuild-v3.sh`), root cause confirmed from serial log. Not yet tested on live VM (VPS lost).

### B2: SSH-piped commands don't complete

**Root cause**: The agent uses direct stdin/stdout piping (no PTY). When SSH pipes input (e.g., `echo "cmd" | ssh ...`), the stdin EOF propagates through the vsock to the shell, causing it to exit before processing. Interactive SSH sessions (typing commands) work fine.

**Fix**: Restore PTY support in the agent (confirmed `/dev/ptmx exists: YES` with proper devpts mount).

### B3: fc-daemon TAP name exceeds IFNAMSIZ

**Root cause**: Linux limits interface names to 15 characters. TAP names like `tap-2d278af41e0a` (17 chars) are rejected by `ip tuntap add`.

**Fix**: Use shorter names (e.g., `fc{N}` where N is a counter).

### B4: fc-daemon config-file argument format

**Root cause**: Firecracker v1.16.0 doesn't accept `--config-file=/path` (single argument with `=`). It requires `--config-file /path` (two separate arguments).

**Fix**: Use `subprocess.Popen([FIRECRACKER, "--no-api", "--config-file", str(config_path)], ...)`.

## Unknown Unknowns Discovered

### U1: ACPI device discovery works without CONFIG_VIRTIO_MMIO_CMDLINE_DEVICES

Firecracker adds `virtio_mmio.device=` to kernel boot args, but Debian cloud kernels have `CONFIG_VIRTIO_MMIO_CMDLINE_DEVICES is not set`. Despite this, Firecracker devices are discovered via ACPI tables (DSDT). The vsock device works without the command-line option.

### U2: All networking/vsock modules are modules, not built-in

On Debian 13 cloud kernels: `virtio_mmio`, `vsock`, `vmw_vsock_virtio_transport_*`, `virtio_net`, `net_failover`, `failover` are ALL compiled as modules (=m), not built-in (=y). This means they must ALL be included in the initramfs and loaded with `insmod` in dependency order. A Firecracker-optimized kernel (like Anvil) with these built-in would eliminate this complexity.

### U3: vsock RTT is sub-millisecond

Measured at **0.285ms average** (min 0.181ms, max 0.803ms). This is faster than any network-based approach (SSH-in-SSH, serial console). Users will perceive zero latency from the vsock bridge.

### U4: devtmpfs + devpts work in initramfs

With proper PATH setup (`export PATH=/bin; busybox --install -s /bin` before mount calls), both devtmpfs and devpts mount successfully inside the Firecracker initramfs. `/dev/ptmx` exists, enabling PTY support.

## Improvement Areas

### I1: Build proper Alpine ext4 rootfs

The initramfs approach works for proving the concept but is limited:
- No package installation (no apk)
- No persistence within session
- Module loading is manual and fragile
- No standard init system

An Alpine ext4 rootfs (512MB) with OpenRC, full networking stack, and package support would be production-ready. The vps-on-demand project has rootfs builder scripts for this.

### I2: Pre-warmed VM pool

Current cold start: ~1.7 seconds. With a pre-warmed pool (paused VMs resumed on demand): ~10ms. This would make the experience indistinguishable from a local shell.

### I3: PTY-based agent with window resize

Now that `/dev/ptmx` is confirmed available, the agent should use `creack/pty` for proper terminal semantics: window resize (SIGWINCH), signal forwarding (Ctrl+C), and proper line editing.

### I4: Snapshot-based boot

Firecracker supports snapshot restore — save a pre-booted VM's memory state and restore it for instant boot. This eliminates kernel boot time entirely (~10ms restore vs ~1s cold boot).

### I5: Token-to-VM-spec mapping

Design needed: how many sats maps to how much RAM/CPU/disk/time?
- Minimum viable: 1 sat = 10 seconds + 256MB RAM + 1 vCPU
- Tiered: higher-value tokens get more resources
- Configurable via operator settings

### I6: Integration with vps-on-demand daemon

The mini fc-daemon (`/tmp/fc-daemon.py`) is a prototype. Production should use the full vps-on-demand daemon which handles: payment verification, Nostr-based VM requests, proper rootfs management, port forwarding, TTL-based reaping, and health monitoring.
