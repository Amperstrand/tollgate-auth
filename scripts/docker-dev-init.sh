#!/bin/bash
# docker-dev-init.sh — one-time setup for local Docker dev environment.
#
# Creates:
#   - Stub state directories (/opt/tollgate-auth, /opt/cashu-tollgate, /var/lib/cashu-wallet)
#     owned by the current user (so bind-mounts work without permission issues)
#   - Stub /etc/tollgate/secrets.env with dev-only test values
#   - Stub /etc/tollgate/settle.env for the settlement job
#
# Does NOT:
#   - Touch any production files
#   - Start any containers (use `make docker-up-dev` after running this)
#   - Install Docker (install separately: https://docs.docker.com/get-docker/)
#
# Idempotent: safe to run multiple times. Existing files are preserved.
set -euo pipefail

if [[ "$(id -u)" -ne 0 ]]; then
    echo "ERROR: Run with sudo (creates files in /opt and /etc)." >&2
    echo "  sudo scripts/docker-dev-init.sh" >&2
    exit 1
fi

DEV_USER="${SUDO_USER:-$USER}"
echo ">>> Setting up Docker dev environment for user: $DEV_USER"

# State directories — bind-mounted into containers.
# Owned by $DEV_USER so the user can inspect/modify them without sudo.
echo ">>> Creating state directories..."
for dir in /opt/tollgate-auth /opt/cashu-tollgate /var/lib/cashu-wallet; do
    if [[ ! -d "$dir" ]]; then
        mkdir -p "$dir"
        chown "$DEV_USER":"$DEV_USER" "$dir"
        chmod 755 "$dir"
        echo "  created: $dir (owner=$DEV_USER)"
    else
        echo "  exists:  $dir (skipped)"
    fi
done

# Stub sessions subdirectory the daemon expects
mkdir -p /opt/tollgate-auth/radius-sessions
chown "$DEV_USER":"$DEV_USER" /opt/tollgate-auth/radius-sessions

# Secrets — stub nsec with the correct bech32 format but all-zero payload.
# Will pass validation but cannot sign anything meaningful.
echo ""
echo ">>> Creating stub secrets..."
mkdir -p /etc/tollgate

if [[ ! -f /etc/tollgate/secrets.env ]]; then
    cat > /etc/tollgate/secrets.env <<'EOF'
# DEV-ONLY STUB. Do not use these values in production.
# Generate a real nsec: see docs/known-unknowns.md
TOLLGATE_OPERATOR_NSEC=nsec10000000000000000000000000000000000000000000000000000000000000
TOLLGATE_API_KEY=dev-only-stub-not-real-73ecee3aa1ee08c6cf97c388f29081bc
EOF
    chmod 600 /etc/tollgate/secrets.env
    chown root:root /etc/tollgate/secrets.env
    echo "  created: /etc/tollgate/secrets.env (mode 0600, root:root)"
else
    echo "  exists:  /etc/tollgate/secrets.env (skipped)"
fi

if [[ ! -f /etc/tollgate/settle.env ]]; then
    cat > /etc/tollgate/settle.env <<'EOF'
# DEV-ONLY STUB. Used by the settlement job.
TOLLGATE_OPERATOR_ID=dev-operator
TOLLGATE_OPERATOR_NSEC=nsec10000000000000000000000000000000000000000000000000000000000000
TOLLGATE_OPERATOR_NPUB=npub10000000000000000000000000000000000000000000000000000000000000
TOLLGATE_RELAYS=wss://relay.damus.io,wss://nos.lol
EOF
    chmod 640 /etc/tollgate/settle.env
    chown root:tollgate /etc/tollgate/settle.env 2>/dev/null || chown root:root /etc/tollgate/settle.env
    echo "  created: /etc/tollgate/settle.env (mode 0640)"
else
    echo "  exists:  /etc/tollgate/settle.env (skipped)"
fi

echo ""
echo ">>> Done. Next steps:"
echo "  1. make docker-build-all"
echo "  2. make docker-up-dev"
echo "  3. make docker-logs-follow"
echo ""
echo ">>> Smoke test:"
echo "  curl http://127.0.0.1:8091/healthz"
