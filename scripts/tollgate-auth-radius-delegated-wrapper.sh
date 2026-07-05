#!/bin/sh
# tollgate-auth-radius-delegated — safe wrapper for FreeRADIUS exec module.
#
# Installed at /usr/local/libexec/tollgate-auth-radius-delegated (root:root, 0755).
#
# WHY THIS WRAPPER EXISTS:
#   FreeRADIUS expands %{User-Name}, %{User-Password}, %{Acct-Session-Id}, etc.
#   directly into the `program` line of an exec module. Those values are
#   ATTACKER-CONTROLLED — a malicious RADIUS client can send anything. If the
#   program line is `/bin/sh -c '... "%{User-Name}" ...'`, the expansion is
#   re-parsed by the shell and a crafted User-Name like `"; rm -rf / #` escapes
#   its quoted slot → arbitrary command execution BEFORE the Go binary starts.
#
#   This wrapper is invoked DIRECTLY by FreeRADIUS (no `/bin/sh -c`), so each
#   RADIUS attribute arrives as its own argv element. The shell never re-parses
#   them. We only use this wrapper to load secrets from a root-only file and
#   set the delegated-mode env vars, then `exec` the Go binary with "$@" intact.
#
# DO NOT:
#   - wrap this in `/bin/sh -c` from the FreeRADIUS config
#   - use `eval` anywhere in this file
#   - concatenate "$@" into a single string and pass it through a shell
#   - add input filtering here — argv boundaries are the security boundary
#
# Regression guard: scripts/check-freeradius-configs.sh fails the build if any
# file under config/freeradius/ combines `/bin/sh -c` with `%{`.
set -eu

# Load operator secrets (nsec, API key) from a root-owned 0600 file.
# Fail loudly if missing — do not silently fall back to empty credentials.
if [ ! -f /etc/tollgate/secrets.env ]; then
    echo "ERROR: /etc/tollgate/secrets.env missing — create it before starting FreeRADIUS" >&2
    exit 1
fi
# shellcheck source=/dev/null
. /etc/tollgate/secrets.env

# Delegated mode: tollgate-auth-radius forwards verification/accounting to the
# local session daemon (tollgate-net / tollgate-rs) instead of using an
# in-process wallet.
export TOLLGATE_AUTH_MODE=delegated
export TOLLGATE_SESSIOND_URL=http://127.0.0.1:2121

# Replace this shell with the Go binary, preserving argv exactly as FreeRADIUS
# passed it. Each "%{...}" expansion from the program line is a distinct argv
# slot; execve does not re-parse them.
exec /usr/local/bin/tollgate-auth-radius "$@"
