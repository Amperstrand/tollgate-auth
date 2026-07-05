#!/bin/sh
# check-freeradius-configs.sh — regression guard for CVE-style command injection.
#
# Fails (exit 1) if any file under config/freeradius/ combines:
#   1. `/bin/sh -c`   (shell invocation in the exec `program` line)
#   2. `%{`           (FreeRADIUS attribute expansion)
#
# on the SAME line. That combination is the exact pattern that allowed the
# original command-injection vulnerability: FreeRADIUS expands %{User-Name}
# etc. into the program string, then `/bin/sh -c` re-parses it, so a crafted
# attribute like `"; rm -rf / #` escapes its quoted slot and runs BEFORE the
# Go binary starts. Go-side input validation cannot help because the injection
# happens during shell parsing.
#
# Correct pattern: call a wrapper directly (no shell), letting execve() keep
# each attribute in its own argv slot:
#   program = "/usr/local/libexec/tollgate-auth-radius-delegated %{User-Name} ..."
#
# This script is wired into `make test-freeradius-config`, `make test-all-available`,
# and scripts/git-hooks/pre-commit.
set -eu

CONF_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)/config/freeradius}"

if [ ! -d "$CONF_DIR" ]; then
    echo "SKIP: $CONF_DIR does not exist (not a tollgate-auth checkout?)"
    exit 0
fi

# Find every config file. -type f so we don't follow symlinks.
# Skip comment lines (leading optional whitespace then `#`) — FreeRADIUS
# config uses `#` for comments, and the security-warning comments in these
# files legitimately mention both `/bin/sh -c` and `%{` to document the trap.
# We only care about ACTIVE config that would actually be parsed.
violations=$(
    find "$CONF_DIR" -type f \
        | while read -r f; do
            # Strip full-line comments (lines whose first non-space char is `#`),
            # then look for lines containing BOTH `/bin/sh -c` and `%{`.
            grep -nE '/bin/sh[[:space:]]+-c' "$f" 2>/dev/null \
                | grep -E '%\{' \
                | grep -vE '^[0-9]+:[[:space:]]*#' \
                | while IFS= read -r line; do
                    lineno=${line%%:*}
                    rest=${line#*:}
                    printf '%s:%s: %s\n' "$f" "$lineno" "$rest"
                done
        done
)

if [ -n "$violations" ]; then
    cat >&2 <<'EOF'
FAIL: command-injection pattern detected in FreeRADIUS configs.

The following lines combine `/bin/sh -c` with `%{...}` FreeRADIUS attribute
expansion. RADIUS attributes are ATTACKER-CONTROLLED — a crafted value
escapes its quotes during shell parsing and runs arbitrary commands before
the Go binary starts. Go-side input validation cannot protect against this.

Fix: call the wrapper directly so execve() preserves argv boundaries:
    program = "/usr/local/libexec/tollgate-auth-radius-delegated %{User-Name} ..."

See:
  scripts/tollgate-auth-radius-delegated-wrapper.sh
  config/freeradius/mods-available/cashu-exec-delegated (comment block)
  config/freeradius/mods-available/tollgate-acct         (comment block)

Violations:
EOF
    printf '%s\n' "$violations" | sed 's/^/  /' >&2
    exit 1
fi

echo "OK: no /bin/sh -c with %{...} expansion in $CONF_DIR"
