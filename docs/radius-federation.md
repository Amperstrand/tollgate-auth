# RADIUS Federation: WISP Cashu Settlement

**How a Wireless Internet Service Provider (WISP), café, university, or any network operator can federate to tollgate-auth and accept Cashu ecash tokens for internet access — with automated tallying and payout settlement.**

## Overview

tollgate-auth is an open access service: anyone with a Cashu token can connect. No pre-registration, no shared secret configuration, no RADIUS client setup. The Cashu token IS the authentication, authorization, and accounting — all in one.

This document covers:

1. **Federation models** — how operators connect their infrastructure
2. **Settlement architecture** — how tokens are tallied and paid out
3. **Real-world deployment plan** — step-by-step for a WISP
4. **Proxy/realm configuration** — FreeRADIUS realm proxying
5. **Operator identity and payout** — npub-based settlement

---

## Federation Models

### Model 1: Direct RADIUS (Simplest)

The operator points their Access Point (AP) or RADIUS infrastructure at the tollgate-auth server.

```
User phone ──WiFi──► WISP AP ──RADIUS──► tollgate-auth server
                                         (1812/UDP or 2083/TCP RadSec)
```

**Setup**: 5 minutes. Configure the AP's RADIUS server IP, shared secret (`tollgate`), and port. Done.

**Best for**: Small operators, cafés, single-site deployments, testing.

**Limitation**: All tokens are redeemed by the tollgate-auth server's wallet. The operator doesn't directly receive the ecash.

### Model 2: RADIUS Proxy with Settlement (WISP-Grade)

The operator runs their own FreeRADIUS proxy that forwards to tollgate-auth for authentication, then independently handles accounting and settlement.

```
User phone ──WiFi──► WISP AP ──► WISP FreeRADIUS ──proxy──► tollgate-auth
                          │
                          ├──Access-Accept──► User gets online
                          │
                          └──Accounting──────► WISP settlement ledger
                                               (operator tally + payout)
```

**Setup**: 1-2 hours. The WISP configures their FreeRADIUS to proxy auth requests to tollgate-auth, while keeping local accounting records for settlement.

**Best for**: WISPs, universities, multi-site operators who need revenue tracking.

**Key insight**: RADIUS accounting (RFC 2866) runs independently from authentication. The WISP's FreeRADIUS can record Start/Interim-Update/Stop events locally AND forward them to tollgate-auth's session daemon.

### Model 3: Realm-Based Federation (eduroam-style)

Multiple operators share a single tollgate-auth instance via RADIUS realm routing. Users from any participating operator can authenticate at any other operator's infrastructure.

```
User from Operator A ──WiFi──► Operator B AP
                                    │
                              Operator B FreeRADIUS
                                    │
                              Realm: @tollgate ──► tollgate-auth
                              Realm: @local   ──► Operator B local auth
```

**Setup**: Each operator configures their FreeRADIUS with a realm proxy rule: `@tollgate` → tollgate-auth server, everything else → local auth.

**Best for**: Federations of operators (like eduroam for universities, or a cooperative of independent ISPs).

---

## Settlement Architecture

### The Core Problem

Cashu tokens are bearer instruments — whoever redeems them gets the value. If the tollgate-auth server redeems a token, the WISP operator doesn't automatically get paid. We need a settlement layer.

### Solution: Operator Identity + NUT-03 Swap

The settlement flow uses Cashu's native NUT-03 token swap to route value to the correct operator:

```
1. User pays:    cashuB... (8 sat token from testnut.cashu.space)
2. Auth:         tollgate-auth verifies + redeems token to wallet
3. Accounting:   Session tracked by operator ID (from Class attribute)
4. Settlement:   Periodic payout to operator's npub via NUT-04 (mint quote)
```

### Operator Identity (How We Know Who to Pay)

Each RADIUS Access-Request carries attributes that identify the originating operator:

| Source | Attribute | Example |
|--------|-----------|---------|
| NAS-Identifier (RFC 2865) | `NAS-Identifier = "wisp-seattle-01"` | Set by AP/controller |
| NAS-IP-Address (RFC 2865) | `NAS-IP-Address = 203.0.113.5` | AP's public IP |
| Operator registry | Config file mapping NAS-ID → operator | Static config |
| Class attribute | HMAC-signed session class | Returned in Access-Accept |

tollgate-auth resolves the operator using this priority:

1. **Operator registry** (`operators.json`) — explicit NAS-ID → operator mapping with payout npub
2. **RADIUS attributes** — NAS-Identifier, NAS-IP-Address matched against known patterns
3. **Environment variables** — `TOLLGATE_OPERATOR_NPUB`, `TOLLGATE_OPERATOR_ID`
4. **Default: anonymous** — tokens tallied under "anonymous", no payout

See [docs/operator-identity.md](operator-identity.md) for the full operator resolution design.

### Operator Registry Format

```json
{
  "operators": [
    {
      "id": "wisp-seattle",
      "payout_npub": "npub1a2b3c4d5e6f...operator's-nostr-public-key",
      "match": {
        "nas_id": "seattle-ap-*",
        "nas_ip": "203.0.113.0/24"
      }
    },
    {
      "id": "cafe-downtown",
      "payout_npub": "npub1x7y8z9w0...cafe-owner-nostr-key",
      "match": {
        "nas_id": "downtown-cafe"
      }
    }
  ]
}
```

### Settlement Ledger

tollgate-auth records every session in a JSONL append-only ledger with entries like:

```json
{"timestamp":"2025-06-13T10:30:00Z","event_type":"auth_accept","operator_id":"wisp-seattle","mac":"B6:95:54:46:E0:27","payment_type":"cashu","amount_sat":8,"duration_sec":480,"mint_url":"https://testnut.cashu.space","token_hash":"a1b2c3..."}
```

Each line is a complete event record. The ledger is append-only — entries are never modified or deleted, making it auditable and crash-safe.

The settlement process:

1. **Aggregate**: Sum `amount_sat` grouped by `operator_id` over a settlement period (daily/weekly)
2. **Deduct fees**: Subtract any tollgate-auth service fee (configurable, default 0%)
3. **Mint payout**: Create a new Cashu token via NUT-04 mint quote for the net amount, sent to the operator's mint
4. **Notify**: Send payout notification via Nostr (encrypted DM to operator's npub) with the settlement token

### Why Nostr for Settlement Notification

The operator's payout identity is a Nostr public key (`npub`). Nostr provides:

- **Encrypted DMs** (NIP-17) — settlement details are private
- **No infrastructure** — relay-based, no server to run
- **Cryptographic proof** — operator can verify the settlement is from tollgate-auth

---

## Real-World Deployment Plan: WISP Federation

### Phase 1: Bootstrap (Day 1)

**Goal**: Get the WISP's APs authenticating against tollgate-auth.

1. **Deploy tollgate-auth** on a VPS:
   ```bash
   git clone https://github.com/Amperstrand/tollgate-auth.git
   cd tollgate-auth
   make build-linux && make deploy && make deploy-radius
   scripts/setup-freeradius.sh
   ```

2. **Configure the WISP's AP** (UniFi, MikroTik, OpenWRT, etc.):
   - RADIUS server 1: `<tollgate-vps-ip>:1812`
   - RADIUS shared secret: `tollgate`
   - EAP method: TTLS + PAP (recommended for phones)
   - Accounting: enabled, same server/port

3. **Test with a real token**:
   ```bash
   radtest "cashuB..." "anything" <tollgate-vps-ip> 0 tollgate
   ```

4. **Verify the user gets internet access** on their phone.

### Phase 2: Operator Registration (Day 2)

**Goal**: The WISP registers for settlement.

1. **Generate a Nostr keypair** (this is the operator's payout identity):
   ```bash
   nostril generate  # or any Nostr wallet
   # npub1... (public, share freely — this is your payout address)
   # nsec1... (private, NEVER share — controls payout claims)
   ```

2. **Register in the operator registry**:
   ```json
   {
     "operators": [{
       "id": "wisp-seattle",
       "payout_npub": "npub1...",
       "match": { "nas_id": "wisp-seattle-*" }
     }]
   }
   ```

3. **Configure the WISP's AP** with a NAS-Identifier that matches the registry pattern:
   - `NAS-Identifier = "wisp-seattle-01"` for AP #1
   - `NAS-Identifier = "wisp-seattle-02"` for AP #2

### Phase 3: Settlement Accounting (Week 1+)

**Goal**: Track revenue and automate payouts.

1. **Enable the ledger**:
   ```bash
   export TOLLGATE_LEDGER_PATH=/var/lib/tollgate/ledger.db
   systemctl restart tollgate-auth-radius
   ```

2. **Periodic settlement script** (cron daily/weekly):
   ```bash
   # Query the JSONL ledger for operator revenue
   cat /var/lib/tollgate/ledger.jsonl | \
     python3 -c "
   import sys, json
   from collections import defaultdict
   totals = defaultdict(int)
   for line in sys.stdin:
       e = json.loads(line)
       if e['event_type'] == 'auth_accept':
           totals[e.get('operator_id', 'anonymous')] += e.get('amount_sat', 0)
   for op, sats in sorted(totals.items()):
       print(f'{op}: {sats} sats')
   "
   ```

3. **Payout** (manual for now, automated later):
   - Mint a Cashu token for the settlement amount
   - Send to the operator's npub via Nostr DM
   - Operator redeems with their wallet

### Phase 4: Scale (Month 1+)

**Goal**: Multi-site federation, automated payout, monitoring.

1. **RadSec**: Switch from UDP 1812 to TCP 2083 (RadSec/TLS) for encrypted transport across untrusted networks.

2. **Multi-realm routing**: Configure the WISP's FreeRADIUS as a proxy:
   ```
   # /etc/freeradius/proxy.conf
   realm TOLLGATE {
       type = radius
       authhost = tcp:tollgate.example.com:2083
       accthost = tcp:tollgate.example.com:2083
       secret = tollgate
       proto = tcp
       tls {
           # RadSec TLS configuration
       }
   }
   ```

3. **Monitoring**: Aggregate ledger data into Grafana dashboards (revenue per AP, session duration distributions, token rejection rates).

---

## FreeRADIUS Proxy Configuration

For a WISP running their own FreeRADIUS that proxies to tollgate-auth:

### Auth Proxy (`proxy.conf`)

```
realm NULL {
    type = radius
    authhost = <tollgate-server-ip>:1812
    accthost = <tollgate-server-ip>:1813
    secret = tollgate
    nostrip
}
```

### Accounting Forwarding (`sites-available/default`)

```
accounting {
    detail              # Log locally for settlement
    sql                 # If using SQL accounting

    # Forward to tollgate-auth session daemon
    exec tollgate-acct {
        program = "/usr/local/bin/tollgate-auth-radius --accounting '%{Acct-Status-Type}' '%{Calling-Station-Id}' '%{Acct-Session-Id}' '%{Acct-Session-Time}' '%{Acct-Input-Octets}' '%{Acct-Output-Octets}'"
        output = none
        wait = no
    }
}
```

### Realm-Based Routing (Multiple Backends)

```
# proxy.conf
realm TOLLGATE {
    authhost = <tollgate-ip>:1812
    accthost = <tollgate-ip>:1813
    secret = tollgate
}

realm LOCAL {
    # Local authentication for WISP's own subscribers
    authhost = localhost:1812
    accthost = localhost:1813
}

realm DEFAULT {
    # Unknown realms → tollgate-auth (open access)
    authhost = <tollgate-ip>:1812
    accthost = <tollgate-ip>:1813
    secret = tollgate
}
```

Users connect with their Cashu token as the username. FreeRADIUS strips the realm and proxies to the appropriate backend. For tollgate-auth, no realm stripping is needed — the entire token goes through.

---

## Settlement Flow Diagram

```
┌─────────┐      ┌──────────┐      ┌──────────────┐      ┌────────────┐
│  User    │      │ WISP AP  │      │ WISP         │      │ tollgate-  │
│  (phone) │      │          │      │ FreeRADIUS   │      │ auth       │
└────┬─────┘      └────┬─────┘      └──────┬───────┘      └─────┬──────┘
     │ WiFi           │ RADIUS            │ Proxy              │
     │ (EAP-TTLS)     │ Access-Req        │                    │
     ├───────────────►├──────────────────►├───────────────────►│
     │                │                   │                    │
     │                │                   │   1. Decode token  │
     │                │                   │   2. Verify mint   │
     │                │                   │   3. Redeem token  │
     │                │                   │   4. Resolve op    │
     │                │                   │   5. Record ledger │
     │                │                   │                    │
     │                │ Access-Accept     │                    │
     │                │  + Class attr     │                    │
     │◄───────────────┤◄──────────────────┤◄───────────────────┤
     │                │                   │                    │
     │ ONLINE         │                   │                    │
     │                │ Accounting-Start  │                    │
     │                ├──────────────────►├───────────────────►│
     │                │                   │  Record session    │
     │                │ Acct-Interim (60s)│                    │
     │                ├──────────────────►├───────────────────►│
     │                │                   │  Update usage      │
     │                │ Accounting-Stop   │                    │
     │                ├──────────────────►├───────────────────►│
     │                │                   │  Close session     │
     │                │                   │                    │
     │                │          ┌────────┴────────────────────┘
     │                │          │ SETTLEMENT (periodic)
     │                │          │
     │                │          │  Aggregate by operator_id
     │                │          │  Mint payout token (NUT-04)
     │                │          │  Send via Nostr DM to npub
     │                │          ▼
     │                │    ┌──────────┐
     │                │    │ Operator │
     │                │    │ wallet   │
     │                │    └──────────┘
```

---

## Security Considerations

### Open Access by Design

tollgate-auth accepts RADIUS from `0.0.0.0/0`. This is intentional:

- **Cashu tokens are money** — the anti-spam mechanism is economic. Every connection costs a real ecash token.
- **Zero configuration** — operators don't need to register IP addresses, shared secrets, or certificates to start accepting tokens.
- **Open service** — anyone can bring their AP and immediately accept Cashu for WiFi.

For operators who want stricter control, RadSec (TLS) provides transport authentication without restricting the open access model.

### Double-Spend Protection

Cashu's anti-double-spend is enforced by the **mint**, not by tollgate-auth:

1. tollgate-auth calls `POST /v1/checkstate` to verify proofs are unspent
2. tollgate-auth calls `cdk-cli receive` (NUT-03 swap) to redeem the token
3. The mint invalidates the user's proofs and mints new ones to the operator wallet
4. If the user tries to spend the same token again, `checkstate` returns "SPENT"

tollgate-auth adds a **local replay guard** (SHA256 hash + file lock) as a fast first check, but the mint's checkstate is the authoritative anti-double-spend.

**For test tokens** (`?i)test` in hostname): tokens are fully verified and redeemed via NUT-03 swap. The test mint auto-pays all Lightning invoices, so there's no monetary cost, but the anti-double-spend mechanics are identical to production.

### Token Privacy

Cashu tokens are Chaumian ecash — the mint cannot link a specific token to a specific user. However:

- tollgate-auth logs the token hash (SHA256) for replay detection
- The RADIUS Calling-Station-Id (MAC address) is logged with the session
- The mint URL is visible in the decoded token

For maximum user privacy, users should use a different token for each session (the faucet mints fresh tokens on demand).

---

## Current Limitations

| Limitation | Impact | Mitigation |
|-----------|--------|------------|
| Settlement is manual | Operator payouts require a script run | Automate via cron + NUT-04 minting |
| No automated Nostr DM | Payout notification not sent yet | Implement NIP-17 encrypted DM in settlement script |
| Single wallet | All tokens redeemed to one wallet | Future: per-operator wallet swap during redemption |
| Test mints only | Only tokens from mints with "test" in hostname accepted | Change `testMintPattern` regex for production |
| No fee mechanism | tollgate-auth doesn't take a cut | Add `service_fee_pct` to operator registry |

---

## API Reference: Operator Settlement

### Query Revenue (from JSONL ledger)

```bash
# Total revenue by operator (last 7 days)
cat $TOLLGATE_LEDGER_PATH | \
  python3 -c "
import sys, json
from datetime import datetime, timedelta
cutoff = datetime.utcnow() - timedelta(days=7)
totals = {}
for line in sys.stdin:
    e = json.loads(line)
    if e['event_type'] != 'auth_accept': continue
    ts = datetime.fromisoformat(e['timestamp'].replace('Z','+00:00'))
    if ts.replace(tzinfo=None) < cutoff: continue
    op = e.get('operator_id', 'anonymous')
    totals[op] = totals.get(op, 0) + e.get('amount_sat', 0)
for op, sats in sorted(totals.items(), key=lambda x: -x[1]):
    print(f'{op}: {sats} sats')
"

# Per-AP breakdown (unique MAC addresses per operator)
cat $TOLLGATE_LEDGER_PATH | \
  python3 -c "
import sys, json
from collections import defaultdict
sessions = defaultdict(lambda: {'connects': 0, 'sats': 0})
for line in sys.stdin:
    e = json.loads(line)
    if e['event_type'] != 'auth_accept': continue
    key = (e.get('operator_id','anonymous'), e.get('mac','?'))
    sessions[key]['connects'] += 1
    sessions[key]['sats'] += e.get('amount_sat', 0)
for (op, mac), v in sorted(sessions.items(), key=lambda x: -x[1]['sats']):
    print(f'{op} {mac}: {v[\"connects\"]} sessions, {v[\"sats\"]} sats')
"
```

The Go ledger package also provides programmatic query methods:

```go
ledger, _ := ledger.OpenLedger(path)
entries, _ := ledger.QueryByOperator("wisp-seattle", time.Now().AddDate(0, 0, -7))
report, _ := ledger.RevenueSummary("wisp-seattle", startTime, endTime)
fmt.Printf("Sessions: %d, Revenue: %d sat\n", report.AcceptedSessions, report.TotalSat)
```

---

## Related Documentation

- [operator-identity.md](operator-identity.md) — Operator resolution, npub payout design
- [radius-payment-models.md](radius-payment-models.md) — Session lifecycle, accounting, CoA
- [radius-compatibility-matrix.md](radius-compatibility-matrix.md) — EAP method support by device
- [threat-model.md](threat-model.md) — Security threats and mitigations
- [radius-testing.md](radius-testing.md) — Live testing with real AP and phone
