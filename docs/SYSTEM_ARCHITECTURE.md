# System Architecture — tollgate-ssh + vps-on-demand + Shared Infrastructure

**Date:** July 2025
**Scope:** Full system map across all repos and hosts

## Overview

Three repos form an ecash-paywalled infrastructure access platform:

```
tollgate-ssh (Go)        vps-on-demand (Python/TS)    tollgate-rs (Rust)
Cashu auth + SSH/RADIUS  Firecracker VPS vending      Payment protocol
vsock microVM sessions   Web UI + Nostr + SHC reseller  Spilman channels
         \                    |                    /
          \                   |                   /
           →  Shared Firecracker Daemon (Python)  ←
                      Shared rootfs images
                      Shared kernel (vmlinux)
                      Shared NAT/bridge config
                              |
                    ┌─────────┴─────────┐
                    |                   |
              ai-legion (20c/32GB)  nodns.shop (2c/4GB)
              KVM host               Production server
              Dev/testing            SSH/RADIUS/OCPI/WireGuard
```

## Components

### tollgate-ssh (Go)

**Repo:** github.com/Amperstrand/tollgate-auth

**Purpose:** Pay-per-minute infrastructure access via Cashu ecash tokens.

**Binaries:**

| Binary | Protocol | Port | Purpose |
|---|---|---|---|
| `tollgate-auth-ssh` | SSH | 2222 | Interactive shell (chroot or Firecracker microVM) |
| `tollgate-auth-radius` | RADIUS | 1812/2083 | WiFi auth (EAP-TTLS+PAP) |
| `tollgate-auth-ocpi` | OCPI 2.2.1 | 8093 | EV charging |
| `tollgate-daemon` | HTTP | 8091 | Persistent auth server |
| `tollgate-shim` | Exec | — | FreeRADIUS exec bridge to daemon |
| `tollgate-settle` | Timer | — | Operator wallet settlement |
| `tollgate-wg` | CLI | — | WireGuard peer management |
| `tollgate-vm-agent` | vsock | 52 | Guest-side agent for Firecracker VMs |

**Two VM modes:**
- **Chroot mode** (default): `cp -r jail-template → chroot → busybox shell`
- **Firecracker mode** (`TOLLGATE_VM_MODE=firecracker`): create microVM → vsock bridge → Alpine/Ubuntu shell

**Auth pipeline:** decode token → mint checkstate → cdk-cli redeem → grant access

### vps-on-demand (Python + TypeScript)

**Repo:** github.com/Amperstrand/vps-on-demand

**Purpose:** Self-hosted Firecracker microVM vending with Lightning/Cashu payment.

**Components:**

| Component | Language | Port | Purpose |
|---|---|---|---|
| `firecracker-daemon.py` | Python | 8080/8081 | VM lifecycle (create/list/destroy) |
| `contextvm/server.py` | Python | — | Nostr ContextVM (kind 25910) MCP server |
| `src/index.ts` | TypeScript | — | Cloudflare Worker (web UI + API proxy) |
| `install-reseller.sh` | Bash | — | One-command installer for operators |

**VM access:** SSH port forwarding (DNAT 24000-24999) or web terminal (xterm.js)

**Payment:** Lightning BOLT11 via SHC BTCPay server, or Cashu token via ContextVM

### tollgate-rs (Rust, design phase)

**Repo:** github.com/OpenTollGate/tollgate-rs

**Purpose:** Autonomous device-to-device payment protocol (Spilman channels).

**Status:** Protocol design finalized. Crates: tollgate-protocol, tollgate-core, tollgate-net.

**Future role:** Replace bootstrap tokens with streaming micropayments. Enable
mid-session top-up via RADIUS CoA. Support hop-by-hop payment across
infrastructure meshes.

## Hosts

### ai-legion (192.168.13.208)

**Specs:** 20 cores, 32GB RAM, 937GB NVMe, KVM, Ubuntu 24.04.4 LTS

**Role:** Primary KVM host for Firecracker microVMs.

**Kernel advantage:** Ubuntu 6.17 has virtio_blk/virtio_net/failover/net_failover
ALL built-in (=y). Only vsock modules need loading. Cold boot: 1.49s.

**Runs:**
- `fc-daemon` (Python) — VM lifecycle API on :8081
- Firecracker v1.16.0
- NAT bridge (br-fc, 172.16.0.0/24)
- Alpine/Ubuntu rootfs images

**Verified capacity:** 20 concurrent VMs at 85MB overhead each.
Theoretical: ~250 VMs (limited by guest RAM allocation).

### nodns.shop (66.92.204.237)

**Specs:** 2 cores, 3.7GB RAM, 38GB disk. No KVM.

**Role:** Production tollgate-auth server.

**Runs:**
- `tollgate-auth-ssh` on :2222 (chroot mode, verified working)
- `tollgate-daemon` in Docker container on :8091
- `tollgate-auth-radius` via FreeRADIUS Docker on :1812/:2083
- `tollgate-auth-ocpi` in Docker on :8093
- `caddy` reverse proxy (:80/:443) with on-demand TLS
- `tollgate-webssh` in Docker on :8092
- OpenCPO CSMS stack (7 containers)
- nodns-checkout (mint, relay, lease, sync)

**Not suitable for Firecracker** (no /dev/kvm).

## User Flows

### Flow 1: SSH Session (tollgate-ssh)

```
1. User mints token at faucet (amperstrand.github.io/tollgate-auth)
2. ssh -p 2222 cashuBtoken@nodns.shop
3. tollgate-auth-ssh:
   a. Decodes Cashu token
   b. POST to testnut.cashu.exchange/v1/checkstate → UNSPENT
   c. cdk-cli receive → token redeemed to operator wallet
   d. Creates session (1 sat = 10 seconds)
4. If VM_MODE=firecracker:
   a. POST /vms to fc-daemon → Firecracker VM created
   b. vsock bridge established (CONNECT 52)
   c. MOTD displayed (VM ID, session time)
   d. Shell prompt — user can interactively run commands
5. On timeout or disconnect:
   a. VM destroyed (DELETE /vms/{id})
   b. TAP device removed
   c. Session cleaned up
```

**Cost:** 1 sat = 10 seconds (base rate). Tiered: higher specs = more sats/minute.

### Flow 2: Persistent VPS (vps-on-demand)

```
1. User visits compute.cashu.email
2. Selects package (Standard/Professional/Business) or custom spec
3. Worker creates SHC order → BOLT11 invoice
4. User pays via Lightning wallet
5. Worker polls SHC → payment confirmed
6. Worker orders VM:
   a. If SHC: order via SHC API (10% operator markup)
   b. If Firecracker: POST /vms to fc-daemon (self-hosted)
7. VM provisioned → SSH credentials returned
8. User SSHes to host:24000-24999 (port forwarded to VM)
9. VM auto-expires after TTL (default 24h)
```

**Cost:** SHC rate + 10% markup, or sats/minute * (RAM/256) * duration.

### Flow 3: ContextVM (Nostr MCP)

```
1. AI agent (Claude, Cursor) calls create_vps via Nostr kind 25910
2. Request NIP-44 v2 encrypted, sent to provider's Nostr pubkey
3. Daemon:
   a. Decrypts request
   b. Validates Cashu token (if required)
   c. Creates Firecracker VM
   d. Returns encrypted SSH credentials
4. Agent uses credentials to access VM programmatically
```

**Cost:** Same as Flow 2 (Firecracker path).

## Pricing Model

### tollgate-ssh (per-session, Cashu token)

| Spec | Rate | Example |
|---|---|---|
| Alpine 256MB | 1 sat = 10s (base) | 8 sats = 80 seconds |
| Alpine 512MB | 1 sat = 7s (1.5x) | 8 sats = 56 seconds |
| Ubuntu 512MB | 1 sat = 5s (2x) | 8 sats = 40 seconds |
| Ubuntu 1GB | 1 sat = 3s (3x) | 8 sats = 24 seconds |

Formula: `seconds_per_sat = 10 / (mem_mb / 256) / rootfs_multiplier`

Where `rootfs_multiplier` = 1.0 for Alpine, 1.3 for Ubuntu.

### vps-on-demand (persistent, Lightning/Cashu)

| Spec | Rate | 1 hour cost |
|---|---|---|
| 256MB / 1 vCPU | 1 sat/min (base) | 60 sats |
| 512MB / 1 vCPU | 2 sat/min | 120 sats |
| 1GB / 2 vCPU | 4 sat/min | 240 sats |
| 2GB / 4 vCPU | 8 sat/min | 480 sats |

Formula: `sats_per_min = base_rate * (mem_mb / 256)`

### SHC Reseller (persistent, Lightning)

| Tier | SHC Rate | With 10% Markup |
|---|---|---|
| Standard (2c/8GB) | $0.49/day | $0.54/day |
| Professional (4c/16GB) | $0.96/day | $1.06/day |
| Business (8c/32GB) | $1.91/day | $2.10/day |

## Shared Infrastructure

### Single Daemon Architecture

Both tollgate-ssh and vps-on-demand call the same `fc-daemon` process:

```
tollgate-auth-ssh (Go)          vps-on-demand Worker (TS)
       ↓ POST /vms                      ↓ POST /vms
       ↓ DELETE /vms/{id}               ↓ DELETE /vms/{id}
       ↓                                ↓
    ┌──────────────────────────────────────┐
    │     fc-daemon-v3.py (Python)         │
    │     POST /vms {rootfs, mem_mb, cpus} │
    │     Firecracker process management   │
    │     TAP device + bridge management   │
    │     NAT iptables rules               │
    └──────────────────────────────────────┘
       ↓                                ↓
    vsock bridge (port 52)         SSH port forwarding (24000+)
    (tollgate-ssh sessions)        (vps-on-demand persistent VPS)
```

### Shared Resources (on KVM host)

| Resource | Path | Shared by |
|---|---|---|
| Kernel | `/var/lib/tollgate/vmlinux` | Both |
| Alpine rootfs | `/var/lib/tollgate/rootfs/alpine.ext4` | Both |
| Ubuntu rootfs | `/var/lib/tollgate/rootfs/ubuntu.ext4` | Both |
| Initramfs | `/var/lib/tollgate/initramfs.cpio.gz` | tollgate-ssh |
| Bridge | `br-fc` (172.16.0.0/24) | Both |
| Firecracker | `/usr/local/bin/firecracker` | Both |

### VM Differentiation

| Property | tollgate-ssh | vps-on-demand |
|---|---|---|
| Access method | vsock bridge (no SSH port) | SSH port forwarding |
| Duration | Minutes (token-funded) | Hours-days (Lightning-funded) |
| User interaction | Interactive shell via SSH | SSH or web terminal |
| Rootfs | Alpine/Ubuntu/initramfs | Alpine/Ubuntu |
| Agent | tollgate-vm-agent (vsock:52) | openssh-server (port 22) |
| Cleanup | On session timeout/disconnect | On TTL expiry (reaper thread) |

## Roadmap

### Phase 1: Shared Infrastructure (current)
- [x] Multi-rootfs daemon (fc-daemon-v3.py)
- [x] Alpine + Ubuntu rootfs builders
- [x] tollgate-ssh firecracker mode with rootfs selection
- [x] Real testnut E2E verified (Alpine)
- [x] ai-legion load test (20 concurrent VMs)
- [ ] Web UI spec selector on vps-on-demand
- [ ] ContextVM create_vps with rootfs parameter
- [ ] Tiered pricing implementation

### Phase 2: Production Deployment
- [ ] Deploy fc-daemon + rootfs to ai-legion as systemd service
- [ ] Wire tollgate-ssh (on ai-legion) to use firecracker mode
- [ ] HTTPS endpoint for web terminal access
- [ ] Monitoring (Prometheus metrics from fc-daemon)
- [ ] Snapshot restore for pre-warmed pool

### Phase 3: Federation
- [ ] tollgate-rs session daemon replaces bootstrap tokens
- [ ] Spilman payment channels for sustained sessions
- [ ] Multi-operator discovery via CEP-6 Nostr announcements
- [ ] ContextVM tools for all operations (create/destroy/list/top-up)

### Phase 4: Consolidation
- [ ] Evaluate merging tollgate-ssh + vps-on-demand into monorepo
- [ ] Rust daemon (tollgate-rs) replaces Python fc-daemon
- [ ] Single binary deployment
- [ ] Unified pricing engine
