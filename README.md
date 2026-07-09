# tollgate-auth

**Ecash for infrastructure access.** Pay-per-minute SSH, WiFi, and EV charging with [Cashu](https://cashu.space) tokens.

Built as a hackathon project to explore what it looks like when internet infrastructure accepts ecash natively. Part of the [OpenTollGate](https://github.com/OpenTollGate) concept — "ecash for internet access."

Three components, one repo:

| Component | Protocol | Port | What users get |
|---|---|---|---|
| **tollgate-auth-ssh** | SSH | 2222 | Interactive bash shell |
| **tollgate-auth-radius** | RADIUS (WiFi) | 1812 | Network access via WPA2-Enterprise |
| **tollgate-auth-ocpi** | OCPI 2.2.1 (EV) | 8093 | EV charging session authorization |

All three accept Cashu ecash tokens (`cashuA...`/`cashuB...`) as payment and share the same `internal/auth.ProcessAuth` verification pipeline.

**Live OCPI demo:** [ocpi.nodns.shop](https://ocpi.nodns.shop/) — paste a test Cashu token to start the virtual charger.

**Two transport modes:**
- **UDP 1812** — plain RADIUS with shared secret `tollgate` (standard, all devices)
- **TCP 2083** — RadSec (RADIUS over TLS) with valid Let's Encrypt cert (encrypted, enterprise-grade)

> **Status:** Live at nodns.shop — test tokens only (testnut.cashu.space). UDP 1812, TCP 2083/RadSec, SSH port 2222, daemon HTTP :8091, WireGuard :51820.

## tollgate-auth-ssh — Ecash for SSH

Users paste a Cashu ecash token as their SSH username. The server redeems it, creates a throwaway guest account in a busybox chroot jail, and gives them an interactive shell for as many minutes as the token is worth (1 sat = 1 minute). When time runs out or they disconnect, the account is destroyed.

```
$ ssh -t cashuBo2FteB5odH...@tollgate.example.com -p 2222

  +======================================+
  |        CASHU TOLLGATE                |
  +======================================+
  |  Mint:   https://testnut.cashu.space |
  |  Amount:    8 sat                     |
  |  Time:      8 min (  480 sec)       |
  |  User:   g-c3aa7bfb                   |
  +======================================+
  |  Run 'timeleft' to see remaining time|
  |  Session self-destructs on timeout.  |
  +======================================+

g-c3aa7bfb@tollgate:~$ whoami
g-c3aa7bfb
g-c3aa7bfb@tollgate:~$ timeleft
  Time remaining: 7m 39s (459s)
  Paid for:       8 minutes
  [############################--]
```

### What SSH users get

- Interactive shell inside a busybox chroot jail (limited applets)
- `timeleft` command shows remaining time with a progress bar
- Own home directory (`chmod 700`)
- Automatic cleanup when time expires or they disconnect

### What SSH users don't get

- Root, sudo, or access to the host system
- Access to other users' home directories
- Access to the Cashu wallet or server logs
- Persistence — the account is deleted on disconnect

## tollgate-auth-radius — Ecash for WiFi

WiFi access points send RADIUS Access-Request to FreeRADIUS, which calls `tollgate-auth-radius` to validate payment. Supports EAP-TTLS+PAP (recommended) and PEAP+MSCHAPv2 (legacy). Payment goes in the username or password field — whichever starts with `cashu` or `lnurlw`.

```
$ radtest "cashuBo2FteB5odHRwczovL3Rlc3RudXQuY2FzaHUuZXhjaGFuZ2VhdWN..." "anything" nodns.shop 0 tollgate

Received Access-Accept
    Reply-Message = "Valid Cashu token: 8 sat = 8m access from https://testnut.cashu.exchange"
    Session-Timeout = 480
    Acct-Interim-Interval = 60
```

### RADIUS features

- **Dual transport**: UDP 1812 (shared secret `tollgate`) + TCP 2083/RadSec (TLS with Let's Encrypt cert)
- **Dual EAP**: EAP-TTLS+PAP (no-DLEQ 230b token in single password field, or split 378b across password+identity) + PEAP+MSCHAPv2 (token in username, <253 bytes)
- **Payment from either field**: username or password — whichever has the `cashu`/`lnurlw` prefix
- **Reply-Message**: Decoded payment info in Access-Accept (amount, duration, mint)
- **Session-Timeout**: Derived from payment amount (1 sat = 60 seconds), sent in Access-Accept for NAS enforcement
- **Acct-Interim-Interval**: NAS reports usage every 60 seconds for real-time metering
- **Session tracking**: MAC-based reconnection — active sessions skip payment check, remaining time returned in Session-Timeout
- **Replay protection**: SHA256 hash of used tokens/codes
- **Mint allowlist**: Only test mints accepted (regex `(?i)test` in hostname). Test tokens are validated and redeemed (NUT-03 swap). Non-test mints are rejected before any network call.

See [docs/radius-testing.md](docs/radius-testing.md) for the full testing guide with real AP, phone, and CI examples, plus config examples for VPN, wired 802.1X, and captive portals. See [docs/radius-payment-models.md](docs/radius-payment-models.md) for session management, accounting, operator credit collection, and infrastructure use cases (5G, PAM-RADIUS, EV charging, IoT).

## Quick Start

### Try it yourself

1. Spin up a VPS (any Debian 12 machine works)
2. Follow the [install guide](#install) — about 10 minutes
3. Visit the **[faucet](https://amperstrand.github.io/tollgate-auth/)** (hosted on GitHub Pages) to mint a free test token
4. Copy the SSH command, paste in your terminal — you get 8 minutes of shell time

> The faucet mints tokens from [testnut](https://testnut.cashu.space), a test mint with fake Bitcoin. All Lightning invoices auto-pay. No real money involved.

### Deploy to your own server

**Two supported deployment modes — pick one:**

#### Option A — Docker (recommended for new deployments)

Isolated containers, atomic rollback via image tags, easier local dev.

```bash
git clone https://github.com/Amperstrand/tollgate-auth.git
cd tollgate-auth

# 1. Build all 6 service images (5 min first time)
make docker-build-all

# 2. Set up host directories and stub secrets
sudo scripts/docker-dev-init.sh

# 3. Bring up the full stack
make docker-up-dev

# 4. Verify
make docker-logs-follow     # Ctrl-C to detach
curl http://127.0.0.1:8091/healthz
```

See [docs/DEV_WITH_DOCKER.md](docs/DEV_WITH_DOCKER.md) for the local-dev workflow and [docs/MIGRATION_RUNBOOK.md](docs/MIGRATION_RUNBOOK.md) for production-grade deployment steps.

**What's NOT in containers** (host systemd):
- `tollgate-auth-ssh` (needs `useradd`/`chroot`)
- `caddy` (ACME integration)
- `tollgate-net`, `tollgate-eur-mint` (separate repos, separate deployment story)

#### Option B — systemd (the original deployment, still fully supported)

Binaries run on the host under hardened systemd units with `ProtectSystem=strict`, `CapabilityBoundingSet`, `IPAddressDeny`, unprivileged `tollgate` user.

```bash
git clone https://github.com/Amperstrand/tollgate-auth.git
cd tollgate-auth
make build-linux
make deploy
```

See [Install](#install) for the full setup guide, or the [Operator Deployment Guide](docs/operator-guide.md) for end-to-end instructions including AP flashing with Nostr identity, payment flow, revenue attribution, and upgrade paths.

#### Which should you pick?

| Want... | Pick |
|---|---|
| Strongest isolation between services | Docker (filesystem overlay per container) |
| Easiest local dev (full stack on a laptop) | Docker (`make docker-up-dev`) |
| Atomic rollback | Docker (`docker run <prev-image>`) |
| Simplest ops story (one host, one process model) | systemd |
| No new dependencies on the server | systemd |
| Compatibility with the audit-hardened config from June 2025 | either (both apply the same hardening) |

The Docker deployment was added in July 2025. The systemd deployment was hardened in the June–July 2025 security audit. Both modes apply the same defense-in-depth (unprivileged users, capability bounding, network filtering, syscall filtering). Docker adds filesystem isolation; systemd adds mature operations tooling. They're comparable in security posture.

## Architecture

```
WiFi client → AP → FreeRADIUS (1812) → tollgate-shim → tollgate-daemon (socket + HTTP :8091)
                                                             ↓
                                                   internal/auth.ProcessAuth
                                                   (decode → verify → redeem)

VPN client → WireGuard (:51820) → daemon /v1/wg/connect → peer allocation

SSH client → tollgate-auth-ssh (2222) → chroot jail + timer

Operator → cdk-cli wallet → tollgate-settle → Lightning melt
```

## Daemon Mode

The daemon (`tollgate-daemon`) is the primary auth path for WiFi (FreeRADIUS) and WireGuard. It avoids the per-request startup cost of spawning a new process for every authentication request.

### Why daemon mode exists

- **Performance**: Startup latency drops from ~800ms (exec binary) to ~5-20ms (daemon request)
- **Persistent state**: Wallet stays warm, sessions tracked in memory, replay guard cached
- **Concurrent-safe**: Handles multiple simultaneous auth requests via goroutines and sync primitives
- **Secrets isolation**: Operator private key (`TOLLGATE_OPERATOR_NSEC`) lives in the daemon, not in the shim

### How it works

**WiFi (FreeRADIUS path):**

```
FreeRADIUS (1812) → exec(tollgate-shim) → Unix socket → tollgate-daemon → auth.ProcessAuth
```

1. FreeRADIUS receives auth request from AP
2. FreeRADIUS calls `tollgate-shim` (lightweight exec binary, ~5KB)
3. Shim connects to daemon Unix socket (`/run/tollgate/tollgate.sock`)
4. Shim sends JSON `AuthRequest` (username, MAC, password, cleartext-password)
5. Daemon runs `auth.ProcessAuth` — decode, verify, redeem
6. Daemon returns JSON `AuthResponse` (accept/reject, reply-message, session-timeout, class)
7. Shim translates response to RADIUS attribute format and exits
8. FreeRADIUS parses stdout, sends Access-Accept or Access-Reject to AP

**WireGuard path:**

```
VPN client → tollgate-wg CLI → daemon /v1/wg/connect → peer allocation → wg0
```

1. User runs `tollgate-wg <token>` locally
2. CLI generates or loads WireGuard keypair
3. CLI POSTs to daemon `/v1/wg/connect` with token and public key
4. Daemon validates token via `auth.ProcessAuth`
5. Daemon assigns client IP (10.200.0.2-254)
6. Daemon calls `wg set wg0 peer <pubkey> allowed-ips <ip>/32`
7. Daemon tracks peer in state file, auto-cleanup after session timeout
8. CLI receives server pubkey, client IP, DNS and prints wg-quick config

**Dual mode support:**

- **Local mode** (default): Daemon processes auth directly with local wallet
- **Delegated mode** (`TOLLGATE_AUTH_MODE=delegated`): Daemon delegates to external sessiond API (`TOLLGATE_SESSIOND_URL`) for token verification and session management

Both modes use the same `auth.ProcessAuth` pipeline — only the verification and redemption steps differ.

## The Bigger Picture: Bitcoin for Any RADIUS Infrastructure

RADIUS is the backbone of network authentication worldwide. Every time you connect to corporate WiFi, log into a VPN, or plug into an enterprise network — RADIUS is what checks your credentials. FreeRADIUS alone authenticates [~100 million people daily](https://freeradius.org/).

tollgate-auth replaces "username + password" with "ecash token." Any device that speaks RADIUS can accept Bitcoin payments for access:

| Use Case | What speaks RADIUS | User experience | Token goes in |
|---|---|---|---|
| **WiFi (WPA2-Enterprise)** | Access point (UniFi, OpenWRT, MikroTik, Cisco, Aruba) | Phone prompts for credentials on connect | Password field (EAP-TTLS+PAP) |
| **VPN** | OpenVPN, WireGuard+plugin, IPsec/StrongSwan, pfSense | User pastes token in VPN client | Username or password field |
| **Wired networks (802.1X)** | Network switch (Cisco, HP/Aruba, Huawei) | OS prompts when plugging in Ethernet | Username or password field |
| **Captive portals** | Hotspot controller (MikroTik, OpenWRT, CoovaChilli) | Web page asks for credentials | Web form → RADIUS backend |
| **eduroam / academic** | Federated RADIUS (100M+ users globally) | Student pastes token at any participating institution | Password field |
| **PPPoE / ISP** | NAS / broadband concentrator | User pastes token in PPPoE client | Username or password field |

### Why this matters

Any operator of a RADIUS infrastructure — ISP, hotel, café, university, co-working space, conference venue — can drop in tollgate-auth and start accepting Bitcoin payments for network access. No payment processor, no merchant account, no KYC. Just ecash tokens.

The Cashu token does triple duty:

- **Authentication** — valid ecash proves the user paid
- **Authorization** — token amount determines access duration (1 sat = 1 minute)
- **Accounting** — the token itself is the payment, redeemed to the operator's wallet

### Payment model

| Mint hostname contains "test" | Full pipeline: verify + redeem to wallet |
|---|---|
| **Validate only** | Mint `checkstate` confirms unspent, but token is NOT redeemed — user keeps their ecash |
| **Validate + redeem** | Full NUT-03 swap: operator gets new tokens, user's originals are invalidated |

Currently configured for test mints only (`(?i)test` in hostname). To accept real-value tokens, change the mint allowlist regex and remove the test-only constraint.

> **Demo mode:** LNURL-withdraw codes (`lnurlw...`) are accepted without claiming the underlying Lightning payment. They grant 1 hour of access, replay-protected by hash. This keeps the demo frictionless.

### Configuration examples for non-WiFi use cases

See [docs/radius-testing.md](docs/radius-testing.md) for practical config examples covering VPN (OpenVPN, WireGuard), wired 802.1X switch authentication, and captive portal setup. See [docs/radius-payment-models.md](docs/radius-payment-models.md) for the full analysis of infrastructure use cases including 5G cellular (Diameter Credit Control), PAM-RADIUS for SSH login, PPPoE/ISP access, satellite, EV charging, and IoT — with RFC references and payment settlement models.

## Components

| Binary | Protocol | Port | Purpose |
|---|---|---|---|
| `tollgate-daemon` | HTTP + Unix socket | 8091 + /run/tollgate/tollgate.sock | Persistent auth server — primary path for FreeRADIUS and WG |
| `tollgate-auth-ocpi` | OCPI 2.2.1 (HTTP) | 8093 | EV charging eMSP — authorize, sessions, CDRs, virtual charger |
| `tollgate-shim` | Exec (called by FreeRADIUS) | - | Bridge: FreeRADIUS exec → daemon socket |
| `tollgate-auth-radius` | Exec (called by FreeRADIUS) | - | Delegated mode: direct Cashu processing with ledger/CoA |
| `tollgate-auth-ssh` | SSH | 2222 | Interactive shell — chroot jail, timer, auto-cleanup |
| `tollgate-wg` | CLI | - | WireGuard peer connect/disconnect via daemon |
| `tollgate-settle` | Timer (systemd) | - | Operator wallet settlement — melt tokens to Lightning |

### Directories

| Path | Purpose |
|---|---|
| `docs/ocpi-testing.md` | OCPI + Cashu testnut testing guide, OCPPLab onboarding |
| `docs/ARCHITECTURE.md` | Full system architecture — components, data flows, protocol coverage |
| `docs/PRODUCTION_READINESS.md` | Production gap analysis, CPO integration paths, prioritized roadmap |
| `docs/radius-testing.md` | RADIUS/WiFi testing guide with real AP, phone, and CI examples |
| `docs/radius-payment-models.md` | Session management, accounting, infrastructure use cases |
| `docs/radius-token-size.md` | Token size analysis, payment approaches, bootstrap spec |
| `docs/tollgate-rs-integration.md` | tollgate-auth + tollgate-rs integration design — shared session API, top-up, CoA |
| `docs/tollgate-rs-deprecation-and-migration.md` | Go payment stack deprecation plan — file inventory, deprecation map, phased migration |
| `docs/SECURITY_AUDIT.md` | Full security audit report — Window 1 (FreeRADIUS + Go) and Window 2 (defense in depth + privilege reduction) findings with root cause, fix, and verification per item |
| `docs/DOCKER_MIGRATION_ROADMAP.md` | Phased plan to migrate from systemd-managed host binaries to Docker containers — what's easy, what's hard, network/persistence/secret models |

### Supported transports

| Transport | Port | Encryption | Notes |
|---|---|---|---|
| UDP RADIUS | 1812 | Shared secret `tollgate` | Standard, all devices |
| TCP RadSec | 2083 | TLS (Let's Encrypt) | Encrypted, enterprise-grade |
| SSH | 2222 | SSH keys/host key | Interactive shell |
| WireGuard | 51820 | WireGuard crypto | VPN, peer allocation via daemon |
| HTTP | 8091 | None (local only) | Daemon API, WireGuard endpoint, metrics |

## Requirements

- Debian 12 (or any Linux with `useradd`/`userdel`)
- [Go 1.25+](https://go.dev/) (for building)
- [cdk-cli](https://github.com/cashubtc/cdk/releases) v0.16+ (for token redemption)
- FreeRADIUS 3.x (for RADIUS/WiFi auth)
- wireguard-tools (for WireGuard, optional)

## Install

### 1. Build

```bash
# Build both binaries
make build-linux           # tollgate-auth-ssh
make build-radius          # tollgate-auth-radius

# Or both at once
make build-linux && make build-radius
```

### 2. Deploy

```bash
make deploy                # SSH binary + service
make deploy-radius         # RADIUS binary + FreeRADIUS restart
make deploy-radius-config  # FreeRADIUS configs only
make deploy-jail           # Busybox chroot template
```

### 3. Install cdk-cli

```bash
curl -sL -o /usr/local/bin/cdk-cli \
  https://github.com/cashubtc/cdk/releases/latest/download/cdk-cli-$(uname -m)
chmod +x /usr/local/bin/cdk-cli
```

### 4. Create the wallet user

```bash
useradd -r -m -d /var/lib/cashu-wallet -s /usr/sbin/nologin cashu-wallet
chmod 700 /var/lib/cashu-wallet
sudo -u cashu-wallet cdk-cli --work-dir /var/lib/cashu-wallet balance
```

### 5. Deploy timeleft

```bash
cp timeleft /usr/local/bin/timeleft
chmod +x /usr/local/bin/timeleft
```

### 6. Move admin SSH to port 2222

```bash
# /etc/ssh/sshd_config
Port 22    # admin SSH stays on 22, tollgate-auth-ssh uses 2222
systemctl restart sshd
```

### 7. Create systemd service for SSH tollgate

```ini
# /etc/systemd/system/tollgate-auth-ssh.service
[Unit]
Description=Tollgate Auth SSH Server
After=network.target

[Service]
Type=simple
ExecStart=/opt/tollgate-auth/tollgate-auth-ssh
Restart=on-failure
RestartSec=5
WorkingDirectory=/opt/tollgate-auth

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now tollgate-auth-ssh
```

### 8. Deploy the daemon (recommended for WiFi auth)

```bash
# Build daemon and shim
make build-daemon
make build-shim

# Deploy to server
make deploy-daemon

# Configure FreeRADIUS exec module to use shim
# Edit /etc/freeradius/3.0/mods-available/cashu-exec:
#   program = "/usr/local/bin/tollgate-shim ..."
```

### 9. Set up WireGuard (optional)

```bash
# Install wireguard-tools
apt-get install wireguard-tools

# Create wg0 interface
cat > /etc/wireguard/wg0.conf << 'EOF'
[Interface]
PrivateKey = <server-private-key>
Address = 10.200.0.1/24
ListenPort = 51820
EOF

# Enable IP forwarding
echo "net.ipv4.ip_forward=1" >> /etc/sysctl.conf
sysctl -p

# Start WireGuard
systemctl enable wg-quick@wg0
systemctl start wg-quick@wg0
```

### 10. Deploy the settle service (optional)

```bash
make deploy-settle
# This sets up a systemd timer that runs tollgate-settle periodically
# to melt accumulated tokens to Lightning
```

### 11. Set up FreeRADIUS (for WiFi auth)

```bash
scripts/setup-freeradius.sh
```

### 12. Move admin SSH to port 2222

```bash
# /etc/ssh/sshd_config
Port 22    # admin SSH stays on 22, tollgate-auth-ssh uses 2222
systemctl restart sshd
```

### 13. Create systemd service for SSH tollgate

```ini
# /etc/systemd/system/tollgate-auth-ssh.service
[Unit]
Description=Tollgate Auth SSH Server
After=network.target

[Service]
Type=simple
ExecStart=/opt/cashu-tollgate/tollgate-auth-ssh
Restart=on-failure
RestartSec=5
WorkingDirectory=/opt/cashu-tollgate

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now tollgate-auth-ssh
```

### 14. Deploy the faucet (optional)

Host `docs/index.html` anywhere that serves static files — GitHub Pages, Netlify, Caddy, nginx.

Update the `TOLLGATE_HOST` constant in the HTML to point to your server.

For GitHub Pages: push to `main` and enable Pages in repo settings. The faucet will be at `https://<username>.github.io/tollgate-auth/`.

## Configuration

### tollgate-daemon (`cmd/tollgate-daemon/main.go`)

| Environment Variable | Default | Description |
|---|---|---|
| `TOLLGATE_SOCKET` | `/run/tollgate/tollgate.sock` | Unix socket path for shim communication |
| `TOLLGATE_HTTP_ADDR` | `:8091` | HTTP listen address (metrics, WireGuard endpoint) |
| `TOLLGATE_BASE_DIR` | `/opt/cashu-tollgate` | Directory for logs, sessions, spent hashes |
| `TOLLGATE_WALLET_DIR` | `/var/lib/cashu-wallet` | cdk-cli wallet directory |
| `TOLLGATE_AUTH_MODE` | `local` | Auth mode: `local` or `delegated` |
| `TOLLGATE_SESSIOND_URL` | `http://127.0.0.1:2121` | Session daemon URL (delegated mode only) |
| `TOLLGATE_OPERATOR_NSEC` | - | Operator Nostr private key (from `/etc/tollgate/secrets.env`) |

### tollgate-auth-ssh (`cmd/tollgate-auth-ssh/main.go`)

| Constant | Default | Description |
|---|---|---|
| `Port` | `2222` | SSH listener port |
| `RateSecPerSat` | `60` | Seconds of shell time per sat (1 sat = 1 min) |
| `BaseDir` | `/opt/tollgate-auth` | Directory for logs and spent hashes |

### tollgate-auth-radius (`cmd/tollgate-auth-radius/main.go`)

| Constant | Default | Description |
|---|---|---|
| `RateSecPerSat` | `60` | Seconds of access per sat (1 sat = 1 min) |
| `LNURLWDefaultSec` | `3600` | Default session for lnurlw (1 hour) |
| `MaxInputLen` | `8192` | Maximum input length for username/password |
| `BaseDir` | `/opt/tollgate-auth` | Directory for logs, sessions, spent hashes |
| `testMintPattern` | `(?i)test` | Regex for allowed mints (test mints only) |

## Wallet Management

```bash
# Check balance
sudo -u cashu-wallet cdk-cli --work-dir /var/lib/cashu-wallet balance

# Cash out to Lightning
sudo -u cashu-wallet cdk-cli --work-dir /var/lib/cashu-wallet melt

# Transfer to another mint
sudo -u cashu-wallet cdk-cli --work-dir /var/lib/cashu-wallet transfer \
  --source-mint https://testnut.cashu.space \
  --target-mint <your-mint-url> \
  --full-balance

# Backup the seed (keep this safe!)
sudo cat /var/lib/cashu-wallet/seed > ~/cashu-wallet-seed-backup.txt
```

## CI

The CI runs on every push to `main`. All tests are strict — a single failure stops the pipeline.

**Unit tests:**

- `go test ./...` — Full test suite with race detector (`-race`)
- `go test -v ./internal/auth/...` — Auth pipeline tests (local and delegated modes)
- `go test -v ./internal/cashu/...` — Cashu decode, verify, replay guard tests
- `go test -v ./internal/radius/...` — RADIUS attribute and session state tests

**E2E tests (against live server):**

1. Fresh `lnurlw` → Accept + Reply-Message
2. Same code again → Reject (replay protection)
3. Same MAC, different code → Accept (session reconnection)
4. `lnurlw` in password field → Accept
5. Uppercase `LNURLW` → Accept
6. Invalid credentials → Reject
7. **Cashu no-DLEQ token in password** (230 bytes, minted fresh, single field) → Access-Accept
8. **Cashu no-DLEQ token in username** (230 bytes, single field) → Access-Accept
9. **Cashu split token with DLEQ** (378 bytes, split 200b+178b) → Access-Accept
10. **Cashu no-DLEQ token replay** (same token, different MAC) → Access-Reject
11. **RadSec** (TLS on port 2083) → Accept via encrypted transport
12. **WireGuard connect** → peer allocation, IP assigned, config returned
13. **WireGuard disconnect** → peer removed, state updated
14. **Session timeout** → expired session rejected, remaining time returned
15. **Concurrent auth** (3 simultaneous requests) → all succeed, race detector passes
16. **Concurrent auth failure** (3 simultaneous invalid tokens) → all reject, no data races

**Total:** 16 E2E tests + 16 parity tests + 3 concurrent auth tests with `-race` detector

Cashu V4 tokens with DLEQ proofs are 378 bytes, exceeding FreeRADIUS's `diameter2vp` 253-byte limit inside EAP-TTLS tunnels. Two solutions work: (1) strip the optional DLEQ proof to produce 230-byte tokens that fit in a single RADIUS attribute, or (2) split the 378-byte token across password (200b) and identity (178b) fields. DLEQ is a client-side verification feature (NUT-12) — not required for mint checkstate or token redemption. See [docs/radius-token-size.md](docs/radius-token-size.md) for details.

## Security Audit

Two audit windows have been completed. The full consolidated report is at [docs/SECURITY_AUDIT.md](docs/SECURITY_AUDIT.md).

### Window 1 — FreeRADIUS + Go binary (June 2025)

6 findings, all fixed:

| Finding | Severity | Status |
|---------|----------|--------|
| Token replay race condition (non-atomic check+mark) | HIGH | **Fixed** — `CheckAndMark()` with `flock(LOCK_EX)` |
| Command injection surface (loose input validation) | HIGH | **Fixed** — strict allowlist validators (`isValidCashuToken`, `isValidLNURLw`) |
| SSRF via attacker-controlled mint URL | HIGH | **Fixed** — `isSafeMintURL()` blocks private/local IPs |
| Legacy `users` file `Exec-Program-Wait` shell injection | HIGH | **Fixed** — removed, replaced with reject-all fallback |
| File permissions too permissive (0644) | MEDIUM | **Fixed** — changed to 0600 (owner-only) |
| BlastRADIUS (CVE-2024-3596) mitigations disabled | INFO | **Not vulnerable** — EAP-TTLS forces Message-Authenticator per RFC 2869 |

**Key finding**: Both FreeRADIUS's exec module and Go's `exec.Command` use `execve()` directly (no shell). Arguments cannot escape their argv slot. The original `users` file `Exec-Program-Wait` entries DID go through shell interpretation — those have been removed.

### Window 2 — Defense in depth + privilege reduction (July 2025)

10 findings (1 HIGH regression-introduced, 4 MEDIUM, 5 LOW), all fixed:

| Finding | Severity | Status |
|---------|----------|--------|
| FreeRADIUS `/bin/sh -c` with `%{...}` attribute expansion (regression of W1) | HIGH | **Fixed** — wrapper script + 3-layer regression guard |
| `/etc/tollgate/settle.env` world-readable, contained `TOLLGATE_OPERATOR_NSEC` | MEDIUM | **Fixed** — `0640 root:tollgate` |
| `tollgate-daemon` graceful-shutdown loop (accept spin on closed socket) | MEDIUM | **Fixed** — `errors.Is(err, net.ErrClosed)` |
| `tollgate-settle` ran as root unnecessarily | MEDIUM | **Fixed** — migrated to `tollgate` user |
| 5 internal services bound to `0.0.0.0` (publicly reachable) | MEDIUM | **Fixed** — env var changes + systemd `IPAddressDeny` BPF filter |
| Repo systemd units lacked `User=` (drifted from prod) | LOW | **Fixed** — synced, deploy target added |
| No systemd hardening directives (ProtectSystem, caps, syscalls) | LOW | **Fixed** — full hardening matrix applied |
| SSH jail paths from hash-derived strings (no validation) | LOW | **Fixed** — regex validator + 25 tests |
| WireGuard pubkey passed to `wg` without format validation | LOW | **Fixed** — base64+length validator + tests |
| No regression guard for FreeRADIUS shell-injection pattern | LOW | **Fixed** — pre-commit + CI + server-side guard |

See [docs/SECURITY_AUDIT.md](docs/SECURITY_AUDIT.md) for root cause, fix detail, and verification per finding. See [docs/DOCKER_MIGRATION_ROADMAP.md](docs/DOCKER_MIGRATION_ROADMAP.md) for the longer-term plan to containerize the deployment for stronger isolation.

### Window 3 — SSH service SystemCallFilter + capability fix (July 9 2025)

3 findings, all fixed. The SSH auth pipeline was broken under the hardened
systemd configuration from Window 2. Each issue was discovered and resolved
during end-to-end testing with real Cashu tokens:

| Finding | Severity | Status |
|---------|----------|--------|
| `SystemCallFilter=@system-service` blocked `chroot`, `setgroups`, `setgid`, `setresuid`, `setresgid` | HIGH | **Fixed** — added `@file-system chroot setgroups setgid setresuid setresgid` |
| `CapabilityBoundingSet` dropped `CAP_DAC_OVERRIDE`, root couldn't access wallet/session dirs | HIGH | **Fixed** — `SupplementaryGroups=cashu-wallet tollgate` |
| `cp -a` (archive copy) required `CAP_CHOWN` for ownership preservation | MEDIUM | **Fixed** — changed to `cp -r --preserve=mode` |

Additionally fixed: `RedeemToken()` output parser falsely matched cdk-cli's
recovery-phase lines ("Recovered N operations, K skipped") as the receive
result, causing all daemon-path redemptions (RADIUS, WireGuard) to fail as
"token already spent" when the wallet had any skipped recovery operations.

**Verification**: SSH auth (port 2222) and RADIUS auth (port 1812) both
tested end-to-end with real testnut tokens — Access-Accept with correct
Session-Timeout. See [docs/SECURITY_AUDIT.md](docs/SECURITY_AUDIT.md)
Window 3 section for details.

## Intentional Design Decisions

The following configuration choices are intentional for the test deployment:

**Open RADIUS clients (`clients.conf` accepts `0.0.0.0/0`)**

This is **intentional** — the deployment is designed for open onboarding. Any access point or network device can authenticate against the server without pre-registration. This is appropriate for a test deployment with free tokens. For production, restrict to known NAS IP addresses.

**No rate limiting**

This is **intentional** — the deployment prioritizes frictionless access over brute-force protection. Test tokens are free (zero monetary value), and the replay guard prevents reuse. For production with real-value tokens, implement rate limiting at the daemon or FreeRADIUS layer.

## Deployment Security

**Read [hackathon-tooling/docs/SECURITY_BEST_PRACTICES.md](https://github.com/Amperstrand/hackathon-tooling/blob/main/docs/SECURITY_BEST_PRACTICES.md) before deploying any tollgate service.**

### Current deployment status on nodns.shop (post-July 2025 audit)

All services hardened. Privilege reduced where possible, network locked to loopback, capabilities bounded, syscall filter applied. As of July 5 2025, 5 services run in Docker containers (for stronger isolation); the remaining services stay on host systemd because they need host integration (`useradd`, `chroot`, ACME, kernel modules).

| Service | Mode | Runs as | Port binding | Status |
|---|---|---|---|---|
| `tollgate-auth-ocpi` | **Docker container** | UID 994 (tollgate) | `127.0.0.1:8093` | **Secured** |
| `tollgate-csms` | **Docker container** | nonroot (65534) | `127.0.0.1:8887` | **Secured** |
| `tollgate-auth-ssh` | systemd | `root` (justified — needs `chroot`+`useradd`) | `:2222` (public) | **Secured** (caps bounded to `CAP_SYS_CHROOT CAP_SETUID CAP_SETGID CAP_KILL`) |
| `tollgate-daemon` | **Docker container** | UID 994 (tollgate) | `127.0.0.1:8091` HTTP, `127.0.0.1:18094` TCP socket | **Secured** |
| `tollgate-net` | systemd | `tollgate` | `0.0.0.0:2121` (filtered to loopback via BPF) | **Secured** |
| `tollgate-webssh` | **Docker container** | nonroot (65534) | `127.0.0.1:8092` | **Secured** |
| `tollgate-settle` | systemd timer → **Docker container** | UID 994 (tollgate) | n/a (oneshot) | **Secured** |
| `freeradius` | **Docker container** (host networking) | freerad (UID 101) | `0.0.0.0:1812/udp`, `0.0.0.0:2083` (intentional) | **Secured** |
| `caddy` | systemd | `caddy` | `:80`, `:443`, `127.0.0.1:2019` | **Secured** |
| `sync-caddy-certs` (timer) | systemd | `root` (justified — restarts containers) | n/a | **Secured** |
| OpenCPO (7 containers) | Docker (separate stack) | Docker-isolated | `127.0.0.1` all | **Secured** |
| `tollgate-eur-mint` | systemd | tollgate | loopback | **Secured** |

**Migration history**: June 2025 audit hardened systemd units; July 5 2025 migrated 5 services to Docker containers while preserving all hardening. Both deployment modes (Docker + systemd) are documented in [Quick Start](#quick-start) above. See [docs/SECURITY_AUDIT.md](docs/SECURITY_AUDIT.md) for the full audit report and [docs/DOCKER_MIGRATION_POSTMORTEM.md](docs/DOCKER_MIGRATION_POSTMORTEM.md) for what we learned during the container migration.

### Hardening matrix applied to every unit

`NoNewPrivileges`, `ProtectSystem=strict`, `PrivateTmp`, `ProtectHome`, `ProtectKernelTunables`, `ProtectKernelModules`, `ProtectKernelLogs`, `ProtectControlGroups`, `ProtectClock`, `ProtectHostname`, `ProtectProc=invisible`, `RestrictSUIDSGID`, `RemoveIPC`, `RestrictRealtime`, `LockPersonality`, `RestrictNamespaces`, `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6`, `CapabilityBoundingSet=` (empty for unprivileged, 4 caps for SSH), `SystemCallFilter=@system-service` (SSH service also includes `@file-system chroot setgroups setgid setresuid setresgid`), `SystemCallArchitectures=native`. SSH service uses `SupplementaryGroups=cashu-wallet tollgate` for wallet/session directory access. For Docker containers: `--cap-drop=ALL`, `read_only: true` where possible, `--restart unless-stopped`, bind-mounted state dirs owned by matching UIDs.

### Why tollgate-auth-ssh runs as root (and stays on host systemd)

`tollgate-auth-ssh` creates and deletes system user accounts (`useradd`/`userdel`) for guest shell sessions, calls `chroot(2)`, and manages PTYs. This requires root AND host integration that doesn't containerize cleanly. The `CapabilityBoundingSet` restricts it to exactly the 4 capabilities it needs:
- `CAP_SYS_CHROOT` — chroot into the jail
- `CAP_SETUID` / `CAP_SETGID` — drop to `nobody:nogroup` inside the jail
- `CAP_KILL` — SIGTERM the chrooted shell on session timeout

A future improvement would split this into a privileged account-creation helper (setuid binary) + unprivileged SSH handler. See [docs/DOCKER_MIGRATION_ROADMAP.md](docs/DOCKER_MIGRATION_ROADMAP.md) Phase 4 for the longer-term plan.

## Security Disclaimer

**This software creates ephemeral user accounts with shell access on your server.**

- **Guests can run arbitrary code.** While they can't sudo, they can compile and execute programs, make network connections, and consume resources.
- **The Cashu token is the only authentication.** No password, no public key check. Anyone with a valid token gets a shell.
- **The server runs as root** because it needs to create/delete system users.
- **Resource exhaustion is trivial** — a user could fork-bomb, fill disk, or consume all memory.
- **Network access is unrestricted** — guests can use your server as a jump host, run port scans, or attack other systems.
- **No rate limiting** — no protection against token brute-forcing or connection flooding.

**Do not run this on a production server, a server with sensitive data, or any system you care about without understanding the risks.** If you're thinking about running this at work, **consult your IT/security team first.** This is a proof of concept for educational and experimental purposes.

## Challenges

### FreeRADIUS diameter2vp 253-byte limit inside EAP-TTLS tunnels

We initially assumed EAP-TTLS+PAP had no password length limit inside the TLS tunnel — the tunnel encrypts everything, so why would there be a limit? In practice, FreeRADIUS's `diameter2vp` function (which converts Diameter AVPs to RADIUS VPs inside the TTLS tunnel) enforces a **253-byte attribute limit** even inside the encrypted tunnel.

**Discovery**: Cashu tokens are exactly 378 bytes (fixed, regardless of amount). Sending a 378-byte token as the PAP password inside EAP-TTLS resulted in this silent failure:
```
eap_ttls: WARNING: diameter2vp skipping long attribute 2
```
The password was silently dropped. The inner tunnel received `User-Name` but NO `User-Password` at all — the request reached our Go binary with an empty password field, causing it to reject with "no payment credential found."

This was NOT documented anywhere we could find. The RADIUS RFC 2865 specifies the 253-byte limit for RADIUS attributes, but the assumption was that inside a TLS tunnel this limit wouldn't apply. FreeRADIUS enforces it anyway.

**Solution (primary)**: Strip the optional DLEQ proof (NUT-12) from the token. This produces a 230-byte token that fits entirely in a single RADIUS attribute — no split needed. DLEQ is a client-side verification feature that proves the mint didn't cheat during blind signing. It's NOT required for mint `checkstate` or NUT-03 swap (token redemption). Users paste one string into the password field — the same UX as typing a WiFi password.

**Solution (fallback)**: For full DLEQ tokens (378 bytes), split across both EAP-TTLS+PAP inner fields:
- Password: first 200 bytes (starts with `cashuB` prefix) — under 253 ✓
- Username: remaining 178 bytes (raw base64url) — under 253 ✓

The Go binary detects the split (password starts with `cashuB` + username is base64url-only, no `cashu`/`lnurlw` prefix) and concatenates them back into the full token.

**Trade-offs**: The no-DLEQ approach makes Cashu tokens practical for real WiFi clients (single paste in password field). The split approach works for automated testing (eapol_test, scripts) but is impractical for real clients — users would need to paste two separate strings. Future improvement: token reference system (minter stores token, sends short hash).

### eapol_test requires IP address, not hostname

`eapol_test`'s `-a` flag uses `hostapd_parse_ip_addr()` which only accepts IP address literals, not DNS hostnames. The CI workflow resolves `nodns.shop` via `dig +short` at runtime into `$RADIUS_IP`.

### FreeRADIUS exec module runs as `freerad` user, not root

FreeRADIUS exec module runs external programs as the `freerad` system user. This caused a chain of issues:

1. **NO_NEW_PRIVS**: FreeRADIUS sets the `NO_NEW_PRIVS` flag on exec modules, which blocks `sudo` and `runuser` from switching users. Both failed silently — `sudo` returned exit code 1, `runuser` reported "may not be used by non-root users."
2. **PATH**: `cdk-cli` wasn't in the default PATH. Fixed by using absolute path `/usr/local/bin/cdk-cli`.
3. **Group permissions**: Final fix — `freerad` added to `cashu-wallet` group, wallet directory set to mode 775 (group-writable), SQLite files set to mode 664. `cdk-cli` runs directly as `freerad` without privilege escalation.

### Cleartext-Password vs User-Password in inner tunnel

Inside EAP-TTLS, the PAP password arrives as `Cleartext-Password` (not `User-Password`). The FreeRADIUS inner-tunnel config needed explicit handling to copy `Cleartext-Password` to `User-Password` for the exec module to receive it as a command-line argument.

### Auth-Type := Accept is required in inner-tunnel

The default inner-tunnel config uses `Response-Packet-Type := Access-Accept` after a successful exec module call. This does **NOT** work — FreeRADIUS logs `No Auth-Type found: rejecting the user via Post-Auth-Type = Reject` even after the exec module returns success.

Two changes are required in `sites-available/inner-tunnel`:

1. In `authorize{}`: Use `update control { Auth-Type := Accept }` instead of `update reply { Response-Packet-Type := Access-Accept }`
2. In `authenticate{}`: Add an explicit handler: `Auth-Type Accept { ok }`

Without the handler, FreeRADIUS has no module to process the `Accept` auth type and falls through to rejection. The `ok` module simply returns `RLM_MODULE_OK` which maps to Access-Accept.

See `config/freeradius/sites-available/inner-tunnel` for the full annotated config with inline comments.

## How It Works

### Token flow (shared by both components)

1. **Decode:** Server parses V3 (`cashuA`, JSON/base64) or V4 (`cashuB`, CBOR), extracts mint URL, amount, proofs
2. **Replay check:** SHA256 of the token checked against spent hash list
3. **Mint allowlist:** Only mints matching `(?i)test` accepted
4. **Mint verify:** `POST /v1/checkstate` confirms proofs are unspent
5. **Redeem:** `cdk-cli receive --allow-untrusted <token>` — NUT-03 swap invalidates user's proofs, mints new ones to the wallet

### SSH-specific

6. **Create user:** `useradd -m -s /bin/bash g-<hash>` with locked-down home dir
7. **Shell:** `sudo -u guest bash -i` inside a PTY via `creack/pty`. I/O bridged with `io.Copy`.
8. **Timer:** Goroutine sleeps for `amount * 60` seconds, then SIGTERM → close PTY → close SSH → cleanup
9. **Cleanup:** `pkill -u <guest>` + `userdel -r -f <guest>` — user ceases to exist

### RADIUS-specific

6. **Session:** JSON file per MAC in `/opt/tollgate-auth/radius-sessions/`
7. **Split token detection**: If password starts with `cashuB` but is NOT a complete token (too short), and username is base64url-only, concatenate password+username to reassemble the full 378-byte token
8. **Reconnection:** Same MAC + active session → accept without payment, Session-Timeout set to remaining time (min 1 second)
9. **Reply-Message:** Binary prints `Reply-Message = "..."` to stdout, FreeRADIUS parses it into Access-Accept
10. **Session-Timeout:** Derived from payment amount (`amount × 60` seconds), output by Go binary to stdout
11. **Acct-Interim-Interval:** Set to 60s — NAS sends periodic usage reports for real-time metering

See [docs/radius-payment-models.md](docs/radius-payment-models.md) for the full analysis of RADIUS session lifecycle, accounting (RFC 2866), dynamic authorization/CoA (RFC 5176), operator credit collection, and infrastructure use cases beyond WiFi.

### Why shell out to cdk-cli

[CDK](https://github.com/cashubtc/cdk) (Cashu Development Kit) is the reference Rust library for Cashu wallet operations. It has no Go bindings (only Python, Swift, Kotlin via UniFFI). Rather than reimplement the DHKE blinding math in Go, we call `cdk-cli receive` as a subprocess. The long-term plan is a Rust rewrite with native CDK integration.

## Future Directions

### Bootstrap token → Spilman channel upgrade

tollgate-auth is an implementation of the **tollgate bootstrap token** spec — a Cashu ecash token used to get connectivity before (or instead of) upgrading to a [Spilman payment channel](https://github.com/cashubtc/nuts/pull/229). The [OpenTollGate bootstrap spec](https://github.com/OpenTollGate/tollgate-rs/blob/master/docs/design/core/tollgate-bootstrap.md) defines the flow:

1. **Bootstrap**: Peer sends Cashu token → provider verifies with mint → grants metered access (current implementation via RADIUS)
2. **Upgrade**: Once online, peer opens a Spilman channel for sustained micropayment
3. **Streaming**: Channel enables per-second payment, no token size constraints

Our current implementation is **bootstrap-only** — single token, fixed session duration, no in-session top-up. The natural upgrade path: **RADIUS for bootstrap, HTTP for sustained payment**. Once the user has connectivity (via the RADIUS bootstrap token), an HTTP API or captive portal handles Spilman channel setup. RADIUS then handles only session management (MAC authorization). The 253-byte RADIUS attribute limit becomes irrelevant. Mid-session top-up uses RADIUS CoA (Change of Authorization, [RFC 5176](https://datatracker.ietf.org/doc/html/rfc5176)) to extend `Session-Timeout` without disconnecting the user. See [docs/radius-token-size.md](docs/radius-token-size.md) for the full analysis and [docs/radius-payment-models.md](docs/radius-payment-models.md) for session lifecycle, accounting, and top-up flows.

### Lightning HTLC preimage as RADIUS credential (L402-over-RADIUS)

A Lightning payment preimage is **64 hex characters** (32 bytes) — 6x smaller than a no-DLEQ Cashu token, and verifiable with a single SHA-256 hash. This enables a two-phase RADIUS flow:

```
Phase 1: Request invoice
→ Access-Request  User-Password = "request-invoice"
← Access-Reject   Reply-Message = "lnbc1500n1pw5kjhmpp..."  (BOLT11 invoice)

Phase 2: Present preimage
→ Access-Request  User-Password = "a1b2c3d4e5f6...64hexchars"
← Access-Accept   Reply-Message = "Lightning payment verified: 15 sat, 15 min"
```

The server creates a hold invoice (`H = sha256(preimage)`), returns the BOLT11 string in Reply-Message, and the user pays from any Lightning wallet. Verification is purely local — `sha256(preimage) == payment_hash` — no external API calls, no mint checkstate, no CBOR parsing. Works with any EAP method (64 chars << 253-byte limit).

This is [L402](https://docs.lightning.engineering/the-lightning-network/l402) (Lightning HTTP 402) adapted for RADIUS instead of HTTP. L402 uses `macaroon:preimage` pairs over HTTP; for RADIUS, the preimage alone suffices since RADIUS provides the authentication transport. Requires an LND or CLN node with hold invoice support. See [docs/radius-token-size.md](docs/radius-token-size.md) for the full analysis including comparison with Cashu, LNURLPoS offline vending, and BIP39 seed phrase bearer instruments.

### Captive portal for real-world deployment

The Cashu-over-RADIUS approach works for single-proof tokens (230 bytes, no DLEQ). For multi-proof tokens (128+ sat, ~1800 bytes) or real-world phone UX, a captive portal sidesteps RADIUS attribute limits entirely — the token goes in an HTTP POST body (no size limit), and RADIUS handles only session management afterward. [OpenTollGate/tollgate](https://github.com/OpenTollGate/tollgate) uses this approach with OpenNDS on OpenWRT and BTCPayServer for payment processing, including sustained session management. A captive portal also solves the invoice delivery problem for Lightning HTLC payments — show the BOLT11 as a QR code.

### Ark / Bark — Bitcoin over RADIUS?

[Ark](https://ark-protocol.org/) is a Bitcoin scaling protocol using Virtual Transaction Outputs (VTXOs) — pre-signed transaction trees anchored on-chain. [Bark](https://gitlab.com/ark-bitcoin/bark) (by [Second](https://second.tech/)) is the reference wallet.

Ark wallets use BIP39 12-word seed phrases (~160 chars) — these fit in a RADIUS attribute. But Ark doesn't have portable "tokens" like Cashu. Ark payments require cooperative rounds with an Ark Service Provider. A VTXO "proof" (V-PACK) with tree path + signatures exceeds 253 bytes for any non-trivial tree.

A practical path: use Ark for backend settlement (fast Bitcoin transactions) but present Cashu-like UX at the RADIUS layer. The Ark wallet holds funds, mints Cashu tokens on demand, and those tokens flow through RADIUS as we've built here. See [docs/radius-token-size.md](docs/radius-token-size.md) for the full analysis.

## Known Unknowns

The core concept is validated — Cashu tokens work as RADIUS credentials. Security audit completed with 6 fixes applied. Remaining production gaps. See [docs/known-unknowns.md](docs/known-unknowns.md) for the full catalog, including:

- **Fresh token e2e on real hardware** — CI passes (16/16), but no complete phone test with an unspent token + internet
- **Certificate validation** — "Do not validate CA" is vulnerable to rogue AP / MITM
- **RADIUS accounting** — implemented: FreeRADIUS forwards Start/Interim-Update/Stop to tollgate-rs session daemon API (`/v1/sessions/`)
- **Token acquisition UX** — chicken-and-egg problem: need internet to get tokens, need tokens to get internet
- **Multi-proof token sizes** — unknown whether no-DLEQ scales past 64 sat
- **clients.conf accepts 0.0.0.0/0** — should restrict to AP's IP address

**Resolved in security audit Window 1** (2025-06-12):
- ~~BlastRADIUS mitigations disabled~~ — **NOT vulnerable** (EAP-TTLS forces Message-Authenticator)
- ~~Token replay race condition~~ — **FIXED** (`CheckAndMark()` with flock)
- ~~Command injection surface~~ — **FIXED** (strict allowlist validators, execve confirmed safe)
- ~~SSRF via mint URL~~ — **FIXED** (`isSafeMintURL()` blocks private IPs)
- ~~Legacy users file shell injection~~ — **FIXED** (removed Exec-Program-Wait)
- ~~File permissions 0644~~ — **FIXED** (changed to 0600)

**Resolved in security audit Window 2** (2025-07-05):
- ~~FreeRADIUS `/bin/sh -c` with `%{...}` attribute expansion (regression)~~ — **FIXED** (wrapper script + 3-layer regression guard)
- ~~`/etc/tollgate/settle.env` world-readable, contained `TOLLGATE_OPERATOR_NSEC`~~ — **FIXED** (`0640 root:tollgate`)
- ~~`tollgate-daemon` graceful-shutdown loop~~ — **FIXED** (`errors.Is(err, net.ErrClosed)`)
- ~~`tollgate-settle` ran as root unnecessarily~~ — **FIXED** (migrated to `tollgate` user)
- ~~5 internal services bound to `0.0.0.0`~~ — **FIXED** (env vars + systemd `IPAddressDeny`)
- ~~SSH jail paths lacked input validation~~ — **FIXED** (regex validator + 25 tests)
- ~~WireGuard pubkey passed to `wg` without validation~~ — **FIXED** (base64+length validator)
- ~~No systemd hardening directives~~ — **FIXED** (full matrix applied to every unit)
- ~~Repo systemd units drifted from prod~~ — **FIXED** (synced, `make deploy-systemd-units` target added)

See [docs/SECURITY_AUDIT.md](docs/SECURITY_AUDIT.md) for the full audit report.

## Related

- [OpenTollGate](https://github.com/OpenTollGate) — ecash for internet access
- [Cashu](https://github.com/cashubtc/cashu) — Chaumian ecash for Bitcoin
- [CDK](https://github.com/cashubtc/cdk) — Cashu Development Kit (Rust)
- [cashu-ts](https://github.com/cashubtc/cashu-ts) — Cashu wallet library (TypeScript, used by the faucet)

## License

[MIT](LICENSE)
