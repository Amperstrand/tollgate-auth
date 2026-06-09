# tollgate-ssh

**Ecash for SSH.** Pay-per-minute shell access with [Cashu](https://cashu.space) tokens.

Users paste a Cashu ecash token as their SSH username. The server redeems it, creates a throwaway guest account, and gives them an interactive bash shell for as many minutes as the token is worth (1 sat = 1 minute). When time runs out or they disconnect, the account is destroyed.

```
$ ssh -t cashuBo2FteB5odH...@tollgate.example.com

  +======================================+
  |        CASHU TOLLGATE                |
  +======================================+
  |  Mint:   https://testnut.cashu.exchange |
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

## Quick Start

### Try the live demo

1. Visit the **[faucet](https://amperstrand.github.io/tollgate-ssh/)** to mint a free test token
2. Copy the SSH command
3. Paste in your terminal — you get 8 minutes of shell time

> The faucet mints tokens from [testnut](https://testnut.cashu.exchange), a test mint with fake Bitcoin. All Lightning invoices auto-pay. No real money involved.

### Deploy to your own server

```bash
git clone https://github.com/Amperstrand/tollgate-ssh.git
cd tollgate-ssh
make build-linux
make deploy
```

See [Install](#install) for the full setup guide.

## Architecture

```
User
  └── ssh -t <cashu_token>@<host>
        │
        ▼
  tollgate-ssh (Go, port 22)
    ├── 1. Decode Cashu token (V3 JSON / V4 CBOR)
    ├── 2. Check replay (spent hash list)
    ├── 3. Verify unspent with mint API
    ├── 4. Redeem to CDK wallet (cdk-cli receive)
    ├── 5. Create JIT guest user (useradd)
    ├── 6. Spawn bash -i inside PTY (creack/pty)
    ├── 7. Timer kills session after N minutes
    └── 8. Cleanup (userdel -r -f) on disconnect/timeout

  Wallet (isolated cashu-wallet user)
    └── /var/lib/cashu-wallet/
          └── Accumulates redeemed tokens, melt to Lightning later
```

## What users get

- Interactive bash shell as a guest user
- `timeleft` command shows remaining time with a progress bar
- Compilers, interpreters, network tools — whatever's on the server
- Their own home directory (`chmod 700`)
- Automatic cleanup when time expires or they disconnect

## What users don't get

- Root or sudo
- Access to other users' home directories
- Access to the Cashu wallet or server logs
- Persistence — the account is deleted on disconnect

## Components

| File | Purpose |
|---|---|
| `main.go` | Go SSH server — token decoding, verification, guest management, PTY shell |
| `faucet/index.html` | Static page — mints free test tokens, shows copy-paste SSH command |
| `timeleft` | Shell script — shows remaining session time |
| `Makefile` | Build, deploy, and faucet deployment targets |

## Requirements

- Debian 12 (or any Linux with `useradd`/`userdel`)
- [Go 1.22+](https://go.dev/) (for building)
- [cdk-cli](https://github.com/cashubtc/cdk/releases) v0.16+ (for token redemption)
- SSH host keys (`/etc/ssh/ssh_host_ed25519_key`)

## Install

### 1. Build

```bash
make build-linux
```

Produces a static `cashu-tollgate` binary for linux/amd64.

### 2. Deploy to your VPS

```bash
make deploy
```

Or manually:

```bash
scp cashu-tollgate your-vps:/opt/cashu-tollgate/
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

This creates an isolated system user. The wallet seed and database live in `/var/lib/cashu-wallet/`, readable only by `cashu-wallet` (and root).

### 5. Deploy timeleft

```bash
cp timeleft /usr/local/bin/timeleft
chmod +x /usr/local/bin/timeleft
```

### 6. Move admin SSH to port 2222

```bash
# /etc/ssh/sshd_config
Port 2222
systemctl restart sshd
```

### 7. Create systemd service

```ini
# /etc/systemd/system/cashu-tollgate.service
[Unit]
Description=Cashu Tollgate SSH Server
After=network.target

[Service]
Type=simple
ExecStart=/opt/cashu-tollgate/cashu-tollgate
Restart=on-failure
RestartSec=5
WorkingDirectory=/opt/cashu-tollgate

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now cashu-tollgate
```

### 8. Deploy the faucet (optional)

Host `faucet/index.html` anywhere that serves static files — GitHub Pages, Netlify, Caddy, nginx.

Update the `TOLLGATE_HOST` constant in the HTML to point to your server.

For GitHub Pages: push to `main` and enable Pages in repo settings. The faucet will be at `https://<username>.github.io/tollgate-ssh/`.

## Configuration

All config is at the top of `main.go`:

| Constant | Default | Description |
|---|---|---|
| `Port` | `22` | SSH listener port |
| `RateSecPerSat` | `60` | Seconds of shell time per sat (1 sat = 1 min) |
| `BaseDir` | `/opt/cashu-tollgate` | Directory for logs and spent hashes |
| `SpentHashesFile` | `spent.txt` | SHA256 hashes of used tokens (replay protection) |
| `TokensLogFile` | `tokens.log` | JSONL log of all token attempts |

## Wallet Management

```bash
# Check balance
sudo -u cashu-wallet cdk-cli --work-dir /var/lib/cashu-wallet balance

# Cash out to Lightning
sudo -u cashu-wallet cdk-cli --work-dir /var/lib/cashu-wallet melt

# Transfer to another mint
sudo -u cashu-wallet cdk-cli --work-dir /var/lib/cashu-wallet transfer \
  --source-mint https://testnut.cashu.exchange \
  --target-mint <your-mint-url> \
  --full-balance

# Backup the seed (keep this safe!)
sudo cat /var/lib/cashu-wallet/seed > ~/cashu-wallet-seed-backup.txt
```

## Security Disclaimer

**This software creates ephemeral user accounts with shell access on your server.**

- **Guests can run arbitrary code.** While they can't sudo, they can compile and execute programs, make network connections, and consume resources.
- **The Cashu token is the only authentication.** No password, no public key check. Anyone with a valid token gets a shell.
- **The server runs as root** because it needs to create/delete system users.
- **Resource exhaustion is trivial** — a user could fork-bomb, fill disk, or consume all memory.
- **Network access is unrestricted** — guests can use your server as a jump host, run port scans, or attack other systems.
- **No rate limiting** — no protection against token brute-forcing or connection flooding.

**Do not run this on a production server, a server with sensitive data, or any system you care about without understanding the risks.** If you're thinking about running this at work, **consult your IT/security team first.** This is a proof of concept for educational and experimental purposes.

## Roadmap

- [ ] **SCP/SFTP support** — handle `exec` and `sftp` subsystem requests for file transfer
- [ ] **Single command exec** — `ssh token@host ls -la` runs one command and exits
- [ ] **Configurable payload** — swap `bash` for any TUI app (OpenCode, ratatui, etc.)
- [ ] **Rate limiting** — connection throttling, max concurrent sessions, token mint allowlist
- [ ] **Real Bitcoin support** — accept tokens from trusted mints with proper fee handling
- [ ] **Rust rewrite** — native [CDK](https://github.com/cashubtc/cdk) integration + [russh](https://github.com/Eugeny/russh) in a single binary
- [ ] **Monitoring** — Prometheus metrics, structured logging, session history dashboard
- [ ] **Docker image** — one-command deploy without touching the host system

## How It Works

### Token flow

1. **Connect:** `ssh -t cashuB...@host` — the Cashu token is the entire SSH username (378+ chars)
2. **Decode:** Server parses V3 (`cashuA`, JSON/base64) or V4 (`cashuB`, CBOR), extracts mint URL, amount, proofs
3. **Replay check:** SHA256 of the token checked against `spent.txt`
4. **Mint verify:** `POST /v1/checkstate` confirms proofs are unspent
5. **Redeem:** `cdk-cli receive --allow-untrusted <token>` — NUT-03 swap invalidates user's proofs, mints new ones to the wallet. If this fails, no shell.
6. **Create user:** `useradd -m -s /bin/bash g-<hash>` with locked-down home dir
7. **Shell:** `sudo -u guest bash -i` inside a PTY via `creack/pty`. I/O bridged with `io.Copy`.
8. **Timer:** Goroutine sleeps for `amount * 60` seconds, then SIGTERM → close PTY → close SSH → cleanup
9. **Cleanup:** `pkill -u <guest>` + `userdel -r -f <guest>` — user ceases to exist

### Why Go

Started with Python asyncssh. PTY handling was broken — `asyncio.create_subprocess_exec` creates pipes, not PTYs, so bash gets EOF and dies. The `pty.openpty()` manual bridge also failed (master_fd returned EOF). Go's `gliderlabs/ssh` + `creack/pty` handles it in three lines:

```go
cmd := exec.Command("sudo", "-u", guest, "-H", "bash", "-i")
ptmx, _ := pty.Start(cmd)
go io.Copy(ptmx, s)   // stdin: SSH → PTY
io.Copy(s, ptmx)       // stdout: PTY → SSH
```

### Why shell out to cdk-cli

[CDK](https://github.com/cashubtc/cdk) (Cashu Development Kit) is the reference Rust library for Cashu wallet operations. It has no Go bindings (only Python, Swift, Kotlin via UniFFI). Rather than reimplement the DHKE blinding math in Go, we call `cdk-cli receive` as a subprocess. The long-term plan is a Rust rewrite with native CDK integration.

## Related

- [OpenTollGate](https://github.com/OpenTollGate) — ecash for internet access
- [Cashu](https://github.com/cashubtc/cashu) — Chaumian ecash for Bitcoin
- [CDK](https://github.com/cashubtc/cdk) — Cashu Development Kit (Rust)
- [cashu-ts](https://github.com/cashubtc/cashu-ts) — Cashu wallet library (TypeScript, used by the faucet)

## License

[MIT](LICENSE)
