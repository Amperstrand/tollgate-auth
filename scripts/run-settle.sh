#!/usr/bin/env bash
# run-settle.sh — wrapper for tollgate-settle that computes the settlement
# window (last 7 days) as RFC3339 timestamps before invoking the binary.
#
# Works on both Linux (GNU date) and macOS (BSD date).
set -euo pipefail

EXEC=/opt/cashu-tollgate/tollgate-settle
LEDGER=/opt/cashu-tollgate/ledger.jsonl
OPERATOR=${TOLLGATE_OPERATOR_ID:-default}

# GNU date (Linux) uses -d, BSD date (macOS) uses -v. Try GNU first, fall back.
SINCE=$(date -u -d '7 days ago' '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null \
	|| date -u -v-7d '+%Y-%m-%dT%H:%M:%SZ')
UNTIL=$(date -u '+%Y-%m-%dT%H:%M:%SZ')

exec "$EXEC" \
	--ledger "$LEDGER" \
	--operator "$OPERATOR" \
	--since "$SINCE" \
	--until "$UNTIL"
