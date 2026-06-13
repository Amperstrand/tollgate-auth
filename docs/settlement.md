# Settlement: Weekly Revenue Notification

**How `tollgate-settle` aggregates ledger revenue and sends an encrypted, aggregate-only Nostr DM to the operator — with zero PII.**

## Overview

`tollgate-settle` is a CLI binary that runs periodically (default: weekly) to summarize tollgate-auth revenue and notify the operator via a NIP-17 encrypted Nostr direct message.

### What it does

1. Reads the JSONL ledger (written by `tollgate-auth-radius` and `tollgate-auth-ssh`)
2. Filters entries by operator ID and time window (default: last 7 days)
3. Aggregates revenue: total sats, accepted/rejected session counts, average amount
4. Publishes the aggregate report as a NIP-17 gift-wrapped DM to the operator's npub

### Privacy guarantees

The settlement report is **aggregate-only by construction**. The `SettlementReport` struct contains:

| Field | Description |
|---|---|
| `type` | Always `"settlement"` |
| `operator_id` | Operator identifier (e.g. `"default"`) |
| `period_start` / `period_end` | Settlement window (RFC3339) |
| `total_sat` | Sum of all accepted token amounts |
| `accepted_sessions` | Count of successful auths |
| `rejected_sessions` | Count of rejected auths |
| `average_amount_sat` | Mean sat value per accepted session |
| `generated_at` | Report generation timestamp |

**What is NOT in the report:** MAC addresses, IP addresses, token hashes, individual session records, mint URLs, usernames. Even if the NIP-17 gift-wrap were unwrapped by a third party, it leaks no personally identifiable information.

See [radius-federation.md](radius-federation.md) for the full settlement architecture and operator identity model.

---

## Prerequisites

### 1. Nostr keypair

The operator needs a Nostr keypair for sending and receiving the settlement DM:

```bash
# Generate a keypair (using nostril, nos chloride, or any Nostr client)
# You get:
#   nsec1...  (secret key — controls the sender identity, NEVER share)
#   npub1...  (public key — your payout/notification address, share freely)
```

The `nsec` signs and sends the DM. The `npub` is the recipient — both belong to the operator. In a multi-operator federation, each operator has their own keypair and the sender's `nsec` is the tollgate-auth service key.

### 2. Cashu wallet

The ledger tracks revenue from redeemed Cashu tokens. Ensure `tollgate-auth-radius` or `tollgate-auth-ssh` has a working wallet (cdk-cli) and the ledger is enabled:

```bash
# The ledger path is configured via TOLLGATE_LEDGER_PATH
# Verify the ledger exists and has entries:
wc -l /opt/cashu-tollgate/ledger.jsonl
```

### 3. Nostr relay access

The settlement DM is published to Nostr relays. The defaults (`wss://relay.damus.io`, `wss://nos.lol`) work out of the box. Override with `TOLLGATE_RELAYS` if you prefer different relays.

---

## Configuration

Create `/etc/tollgate/settle.env` on the server:

```bash
sudo mkdir -p /etc/tollgate
sudo tee /etc/tollgate/settle.env <<'EOF'
# Sender identity (signs the NIP-17 DM)
TOLLGATE_OPERATOR_NSEC=nsec1...

# Recipient identity (receives the DM — usually the same operator)
TOLLGATE_OPERATOR_NPUB=npub1...

# Optional: override default relays
# TOLLGATE_RELAYS=wss://relay.damus.io,wss://nos.lol

# Optional: override operator ID (defaults to "default")
# TOLLGATE_OPERATOR_ID=default
EOF
sudo chmod 600 /etc/tollgate/settle.env
```

This file is read by the systemd service via `EnvironmentFile`. The `-` prefix in `EnvironmentFile=-/etc/tollgate/settle.env` means the service starts even if the file is missing (dry-run mode doesn't need credentials).

---

## Deployment

```bash
# From your local machine (repo root):
make deploy-settle
```

This target:

1. Cross-compiles `tollgate-settle` for Linux amd64
2. Copies the binary to `/opt/cashu-tollgate/tollgate-settle`
3. Installs `scripts/run-settle.sh` to `/usr/local/sbin/run-settle.sh`
4. Installs the systemd service + timer
5. Enables and starts the timer

After deployment, verify the timer is scheduled:

```bash
ssh root@nodns.shop 'systemctl list-timers tollgate-settle.timer --no-pager'
```

You should see the next run scheduled for Monday 03:00.

---

## Dry-Run Testing

Before enabling live DM sending, test the aggregation locally:

```bash
# On the server — prints the settlement report JSON to stdout, sends nothing
/opt/cashu-tollgate/tollgate-settle \
  --dry-run \
  --ledger /opt/cashu-tollgate/ledger.jsonl \
  --operator default \
  --since $(date -u -d '7 days ago' '+%Y-%m-%dT%H:%M:%SZ') \
  --until $(date -u '+%Y-%m-%dT%H:%M:%SZ')
```

Example output:

```json
{
  "type": "settlement",
  "operator_id": "default",
  "period_start": "2025-06-06T03:00:00Z",
  "period_end": "2025-06-13T03:00:00Z",
  "total_sat": 142,
  "accepted_sessions": 18,
  "rejected_sessions": 2,
  "average_amount_sat": 7.888888888888889,
  "generated_at": "2025-06-13T03:00:01.234567Z"
}
```

Dry-run mode requires no Nostr credentials — it's safe to run anytime.

---

## Scheduling

The systemd timer runs **weekly on Monday at 03:00** server time:

```ini
# config/systemd/tollgate-settle.timer
[Timer]
OnCalendar=Mon *-*-* 03:00:00
Persistent=true
```

`Persistent=true` means if the server was down during the scheduled time, the timer fires immediately on next boot (catch-up).

### Changing the schedule

Edit the timer on the server:

```bash
# Example: run daily at 02:30 instead
ssh root@nodns.shop \
  'sed -i "s/OnCalendar=.*/OnCalendar=*-*-* 02:30:00/" /etc/systemd/system/tollgate-settle.timer && \
   systemctl daemon-reload && \
   systemctl restart tollgate-settle.timer'
```

Or edit `config/systemd/tollgate-settle.timer` in the repo and re-run `make deploy-settle`.

---

## Manual Run

Trigger a settlement immediately without waiting for the timer:

```bash
ssh root@nodns.shop 'systemctl start tollgate-settle.service'
```

This runs `/usr/local/sbin/run-settle.sh`, which computes the last-7-days window and invokes the binary with the credentials from `/etc/tollgate/settle.env`.

---

## Verification

### Check the service ran successfully

```bash
ssh root@nodns.shop 'journalctl -u tollgate-settle.service --no-pager -n 20'
```

Look for:

```
tollgate-settle: operator=default ledger=/opt/cashu-tollgate/ledger.jsonl ... dry-run=false
sending settlement DM: total_sat=142 accepted=18 rejected=2
settlement DM sent to operator default
```

### Check the timer status

```bash
ssh root@nodns.shop 'systemctl status tollgate-settle.timer'
```

### Verify the DM was received

Open your Nostr client (Damus, Amethyst, nos chloride, etc.) logged in with the `npub` from `settle.env`. Look for a NIP-17 direct message containing the settlement JSON.

---

## Troubleshooting

### `TOLLGATE_OPERATOR_NSEC is required`

The service is running in live mode but `/etc/tollgate/settle.env` is missing or doesn't contain the key. Create the file (see [Configuration](#configuration)) and restart:

```bash
systemctl restart tollgate-settle.service
```

### `decode sender nsec: ...` or `decode recipient npub: ...`

The nsec/npub values are malformed. Verify they start with `nsec1` / `npub1` and are complete (not truncated). Regenerate if needed.

### `open ledger: ... no such file or directory`

The ledger path doesn't exist. Either:
- The ledger isn't enabled in `tollgate-auth-radius` (set `TOLLGATE_LEDGER_PATH`)
- The path in `run-settle.sh` doesn't match where the ledger is written

Check:
```bash
ls -la /opt/cashu-tollgate/ledger.jsonl
grep TOLLGATE_LEDGER_PATH /etc/default/tollgate-auth-radius 2>/dev/null || true
```

### `send settlement DM: ... timeout` / `connection refused`

The Nostr relays are unreachable. Check network connectivity and relay status:

```bash
# Test relay connectivity from the server
curl -s -o /dev/null -w '%{http_code}' https://relay.damus.io 2>/dev/null || echo "unreachable"
```

Try alternative relays by setting `TOLLGATE_RELAYS` in `/etc/tollgate/settle.env`.

### DM not appearing in client

NIP-17 gift-wrapped DMs require the recipient's client to support NIP-17 (not all clients do). Ensure you're using a NIP-17-compatible client. Also verify the recipient `npub` in `settle.env` matches the key you're logged in with.

### Timer didn't fire

Check if the timer is enabled and the service isn't in a failed state:

```bash
systemctl is-enabled tollgate-settle.timer
systemctl is-active tollgate-settle.service
systemctl list-timers tollgate-settle.timer --no-pager
```

If the service failed, check `journalctl -u tollgate-settle.service` for the error, fix it, then:

```bash
systemctl reset-failed tollgate-settle.service
systemctl start tollgate-settle.service
```

---

## Files

| File | Purpose |
|---|---|
| `cmd/tollgate-settle/main.go` | Binary entry point — flags, ledger open, report build, DM send |
| `cmd/tollgate-settle/settlement.go` | `SettlementReport` struct + `BuildSettlementReport` aggregation |
| `config/systemd/tollgate-settle.service` | systemd oneshot service (calls `run-settle.sh`) |
| `config/systemd/tollgate-settle.timer` | Weekly Monday 03:00 timer |
| `scripts/run-settle.sh` | Wrapper computing 7-day window (Linux/macOS compatible) |
| `/etc/tollgate/settle.env` | Credentials (nsec, npub, optional relays) — created on server |

## Flags

| Flag | Default | Description |
|---|---|---|
| `--ledger` | `/opt/tollgate-auth/ledger.jsonl` | Path to the JSONL ledger |
| `--operator` | `TOLLGATE_OPERATOR_ID` or `"default"` | Operator ID to summarize |
| `--since` | 7 days ago (RFC3339) | Period start |
| `--until` | now (RFC3339) | Period end |
| `--relays` | `TOLLGATE_RELAYS` or defaults | Comma-separated Nostr relay URLs |
| `--dry-run` | `false` | Print report to stdout, don't send DM |

## Related

- [radius-federation.md](radius-federation.md) — Full settlement architecture, operator identity, NUT-03 swap flow
- [operator-identity.md](operator-identity.md) — Operator resolution design (NAS-ID, registry, npub payout)
- [radius-payment-models.md](radius-payment-models.md) — Session lifecycle, accounting (RFC 2866), CoA
