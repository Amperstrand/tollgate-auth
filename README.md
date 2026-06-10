# tollgate-auth

**Ecash for infrastructure access.** Pay-per-minute SSH and WiFi with [Cashu](https://cashu.space) tokens.

Built as a hackathon project to explore what it looks like when internet infrastructure accepts ecash natively. Part of the [OpenTollGate](https://github.com/OpenTollGate) concept — "ecash for internet access."

Two components, one repo:

| Component | Protocol | Port | What users get |
|---|---|---|---|
| **tollgate-auth-ssh** | SSH | 2222 | Interactive bash shell |
| **tollgate-auth-radius** | RADIUS (WiFi) | 1812 | Network access via WPA2-Enterprise |

Both accept Cashu ecash tokens (`cashuA...`/`cashuB...`) and LNURL-withdraw codes (`lnurlw...`) as payment. Tokens from [testnut.cashu.space](https://testnut.cashu.space) only (test mint, zero monetary value).

> **Status:** Not currently running a public instance. The code works — you can spin it up on a fresh VPS in about 10 minutes using the install guide below. See [Deploy to your own server](#deploy-to-your-own-server).

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
$ radtest "lnurlw1dp68gurn8ghj7ampd3kx2ar0veekzar0wd5xjtnrdakj7" "anything" nodns.shop 0 tollgate

Received Access-Accept
    Reply-Message = "Valid LNURLw code: 60m access (TODO: claim Lightning payment)"
    Session-Timeout = 3600
```

### RADIUS features

- **Dual EAP**: EAP-TTLS+PAP (token in password, no length limit) + PEAP+MSCHAPv2 (token in username, <253 bytes)
- **Payment from either field**: username or password — whichever has the `cashu`/`lnurlw` prefix
- **Reply-Message**: Decoded payment info in Access-Accept (amount, duration, mint)
- **Session tracking**: MAC-based reconnection — active sessions skip payment check
- **Replay protection**: SHA256 hash of used tokens/codes
- **Mint allowlist**: Only test mints accepted (regex `(?i)test`)

See [docs/radius-testing.md](docs/radius-testing.md) for the full testing guide with real AP, phone, and CI examples.

## Quick Start

### Try it yourself

1. Spin up a VPS (any Debian 12 machine works)
2. Follow the [install guide](#install) — about 10 minutes
3. Visit the **[faucet](https://amperstrand.github.io/tollgate-auth/)** (hosted on GitHub Pages) to mint a free test token
4. Copy the SSH command, paste in your terminal — you get 8 minutes of shell time

> The faucet mints tokens from [testnut](https://testnut.cashu.space), a test mint with fake Bitcoin. All Lightning invoices auto-pay. No real money involved.

### Deploy to your own server

```bash
git clone https://github.com/Amperstrand/tollgate-auth.git
cd tollgate-auth
make build-linux
make deploy
```

See [Install](#install) for the full setup guide.

## Architecture

```
                    tollgate-auth
                    ┌─────────────────────────────────────────┐
                    │                                         │
  SSH client ──────► tollgate-auth-ssh (Go, port 2222)       │
  (cashu token      │  Decode → Verify → Redeem → Chroot     │
   as username)     │  Jail → Timer → Cleanup                 │
                    │                                         │
  WiFi client ─────► AP ──► FreeRADIUS (port 1812)            │
  (cashu token              │                                 │
   as password)             ▼                                 │
                    │  tollgate-auth-radius (Go binary)       │
                    │  Decode → Verify → Redeem → Session     │
                    │                                         │
                    │  Shared: internal/cashu/                │
                    │  Token decode, mint verify, replay      │
                    │  guard, wallet redemption (cdk-cli)     │
                    └─────────────────────────────────────────┘
```

## Components

| File | Purpose |
|---|---|
| `cmd/tollgate-auth-ssh/main.go` | SSH server — token decode, guest management, chroot jail, PTY shell |
| `cmd/tollgate-auth-radius/main.go` | RADIUS validator — called by FreeRADIUS exec module |
| `internal/cashu/` | Shared Cashu library — V3/V4 decode, mint verify, replay guard, wallet |
| `config/freeradius/` | FreeRADIUS configs — exec module, EAP, inner-tunnel, clients |
| `scripts/` | Setup scripts — FreeRADIUS, jail, e2e tests |
| `docs/index.html` | Faucet — static page that mints free test tokens |
| `docs/radius-testing.md` | Live demo guide with copy-paste examples |

## Requirements

- Debian 12 (or any Linux with `useradd`/`userdel`)
- [Go 1.22+](https://go.dev/) (for building)
- [cdk-cli](https://github.com/cashubtc/cdk/releases) v0.16+ (for token redemption)
- FreeRADIUS 3.x (for RADIUS/WiFi auth)

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

### 8. Set up FreeRADIUS (for WiFi auth)

```bash
scripts/setup-freeradius.sh
```

### 9. Deploy the faucet (optional)

Host `docs/index.html` anywhere that serves static files — GitHub Pages, Netlify, Caddy, nginx.

Update the `TOLLGATE_HOST` constant in the HTML to point to your server.

For GitHub Pages: push to `main` and enable Pages in repo settings. The faucet will be at `https://<username>.github.io/tollgate-auth/`.

## Configuration

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

The [E2E workflow](../../actions/workflows/e2e-demo.yml) runs on every push to `main`. It:

1. Compiles both binaries (`go vet` + cross-compile)
2. Tests RADIUS against the live server using `radclient` with fake MAC addresses:
   - Fresh `lnurlw` → Accept + Reply-Message
   - Same code again → Reject (replay protection)
   - Same MAC, different code → Accept (session reconnection)
   - Invalid credentials → Reject
3. Checks SSH tollgate responds with SSH banner on port 2222

## Security Disclaimer

**This software creates ephemeral user accounts with shell access on your server.**

- **Guests can run arbitrary code.** While they can't sudo, they can compile and execute programs, make network connections, and consume resources.
- **The Cashu token is the only authentication.** No password, no public key check. Anyone with a valid token gets a shell.
- **The server runs as root** because it needs to create/delete system users.
- **Resource exhaustion is trivial** — a user could fork-bomb, fill disk, or consume all memory.
- **Network access is unrestricted** — guests can use your server as a jump host, run port scans, or attack other systems.
- **No rate limiting** — no protection against token brute-forcing or connection flooding.

**Do not run this on a production server, a server with sensitive data, or any system you care about without understanding the risks.** If you're thinking about running this at work, **consult your IT/security team first.** This is a proof of concept for educational and experimental purposes.

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
7. **Reconnection:** Same MAC + active session → accept without payment
8. **Reply-Message:** Binary prints `Reply-Message = "..."` to stdout, FreeRADIUS parses it into Access-Accept
9. **Session-Timeout:** Set by FreeRADIUS policy (3600s default)

### Why shell out to cdk-cli

[CDK](https://github.com/cashubtc/cdk) (Cashu Development Kit) is the reference Rust library for Cashu wallet operations. It has no Go bindings (only Python, Swift, Kotlin via UniFFI). Rather than reimplement the DHKE blinding math in Go, we call `cdk-cli receive` as a subprocess. The long-term plan is a Rust rewrite with native CDK integration.

## Related

- [OpenTollGate](https://github.com/OpenTollGate) — ecash for internet access
- [Cashu](https://github.com/cashubtc/cashu) — Chaumian ecash for Bitcoin
- [CDK](https://github.com/cashubtc/cdk) — Cashu Development Kit (Rust)
- [cashu-ts](https://github.com/cashubtc/cashu-ts) — Cashu wallet library (TypeScript, used by the faucet)

## License

[MIT](LICENSE)
