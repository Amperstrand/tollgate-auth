# Enriched v1 Server Response Format

## Overview

The tollgate-rs v1 server returns a Nostr kind 1022 event for successful `POST /` (payment) requests. The response has been enriched with token metadata tags so that the Go RADIUS binary can display transparency information to users and record accurate ledger entries.

## Nostr 1022 Event Tags

### Existing tags (unchanged)

| Tag | Example | Description |
|---|---|---|
| `p` | `["p", "aa:bb:cc:dd:ee:ff"]` | Customer identifier (MAC address) |
| `allotment` | `["allotment", "180000"]` | Allotment in metric units (milliseconds or bytes) |
| `metric` | `["metric", "milliseconds"]` | Unit type: `"milliseconds"` or `"bytes"` |
| `device-identifier` | `["device-identifier", "mac", "aa:bb:cc:dd:ee:ff"]` | Device identification |
| `start-time` | `["start-time", "1718294400"]` | Unix timestamp of session start |

### New tags (enriched)

| Tag | Example | Description | Present |
|---|---|---|---|
| `amount_sat` | `["amount_sat", "4"]` | Raw token amount in sats | Always (for Cashu tokens) |
| `token_type` | `["token_type", "cashu"]` | Payment instrument type: `"cashu"` or `"lnurlw"` | Always |
| `effective_rate` | `["effective_rate", "45"]` | Seconds per sat (allotment_seconds / amount_sat) | When `amount_sat > 0` |

### Example enriched event

```json
{
  "kind": 1022,
  "tags": [
    ["p", "aa:bb:cc:dd:ee:ff"],
    ["allotment", "180000"],
    ["metric", "milliseconds"],
    ["device-identifier", "mac", "aa:bb:cc:dd:ee:ff"],
    ["start-time", "1718294400"],
    ["amount_sat", "4"],
    ["token_type", "cashu"],
    ["effective_rate", "45"]
  ]
}
```

## How Go uses the enriched fields

### Reply-Message transparency

When enriched fields are present (`amount_sat > 0`):

```
Reply-Message = "Valid Cashu token: 4 sat → 3m access (45s/sat)"
```

When enriched fields are absent (legacy server, backward compat):

```
Reply-Message = "Valid Cashu token: 3m access (delegated)"
```

### Ledger accounting

| Field | Before (buggy) | After (enriched) |
|---|---|---|
| `amount_sat` | Recorded minutes as sats (e.g., `3` for 3 minutes) | Records actual sats from token (e.g., `4`) |
| `duration_sec` | Correct (actual seconds from allotment) | Correct (unchanged) |

## How effective_rate is computed

```
effective_rate = (allotment_ms / 1000) / amount_sat
```

Example: 4-sat token, allotment = 180000 ms
- `allotment_seconds = 180000 / 1000 = 180`
- `effective_rate = 180 / 4 = 45` (seconds per sat)

This means the server grants 45 seconds of access per sat. The Go binary's `RateSecPerSat = 60` is irrelevant in delegated mode — the server's rate is authoritative.

## Backward compatibility

The enriched tags are purely additive. Old Go binaries that don't parse them will continue to work — they'll just show the less informative Reply-Message (`"3m access (delegated)"`) and record `0` in the ledger's `amount_sat` field.

Old tollgate-rs instances that don't emit the enriched tags will work with new Go binaries — the Go code detects `amount_sat == 0` and falls back to the legacy behavior.
