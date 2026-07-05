#!/bin/bash
# sync-caddy-certs.sh — Copy Let's Encrypt certs from Caddy to FreeRADIUS.
#
# In a containerized deployment, also refreshes the freeradius-certs Docker
# volume and restarts the FreeRADIUS container. Without this, the container
# serves stale certs after Let's Encrypt rotates (~every 60 days), breaking
# RadSec silently.
#
# Both modes run when invoked — host path is preserved for any systemd
# fallback; Docker volume is refreshed for the live container. Either can
# be disabled by commenting the relevant section below.
set -euo pipefail

TAG="sync-caddy-certs"

# --- Source paths (Caddy's ACME store) ---
CADDY_CERT_DIR="/var/lib/caddy/.local/share/caddy/certificates/acme-v02.api.letsencrypt.org-directory/nodns.shop"
SRC_CERT="$CADDY_CERT_DIR/nodns.shop.crt"
SRC_KEY="$CADDY_CERT_DIR/nodns.shop.key"

# --- Destination A: host path (used by systemd-managed FreeRADIUS if rolled back) ---
HOST_DEST_DIR="/etc/freeradius/3.0/certs/letsencrypt"
HOST_DEST_CERT="$HOST_DEST_DIR/nodns.shop.crt"
HOST_DEST_KEY="$HOST_DEST_DIR/nodns.shop.key"

# --- Destination B: Docker volume (used by containerized FreeRADIUS) ---
DOCKER_VOLUME="freeradius-certs"
DOCKER_CONTAINER="tollgate-freeradius-docker"

log() {
    logger -t "$TAG" "$@"
    echo "[$TAG] $*"
}

# --- Bail out if Caddy hasn't provisioned certs yet (fresh install) ---
if [ ! -f "$SRC_CERT" ] || [ ! -f "$SRC_KEY" ]; then
    log "Caddy LE certs not found at $CADDY_CERT_DIR — skipping sync"
    exit 0
fi

# ===========================================================================
# Helper: do a checksum-based copy. Returns 0 if changed, 1 if unchanged.
# ===========================================================================
sync_to_dest() {
    local dest_cert="$1" dest_key="$2" dest_dir="$3"

    if [ ! -d "$dest_dir" ]; then
        log "Creating $dest_dir"
        mkdir -p "$dest_dir"
    fi

    local changed=false
    if [ -f "$dest_cert" ]; then
        if [ "$(sha256sum "$SRC_CERT" | awk '{print $1}')" != "$(sha256sum "$dest_cert" | awk '{print $1}')" ]; then
            changed=true
        fi
    else
        changed=true
    fi
    if [ -f "$dest_key" ]; then
        if [ "$(sha256sum "$SRC_KEY" | awk '{print $1}')" != "$(sha256sum "$dest_key" | awk '{print $1}')" ]; then
            changed=true
        fi
    else
        changed=true
    fi

    if [ "$changed" = false ]; then
        return 1
    fi

    log "Certs changed — copying to $dest_dir"
    cp "$SRC_CERT" "$dest_cert"
    cp "$SRC_KEY"  "$dest_key"
    return 0
}

CHANGED_HOST=false
CHANGED_DOCKER=false

# --- Sync A: host path (always done — cheap, supports rollback) ---
if sync_to_dest "$HOST_DEST_CERT" "$HOST_DEST_KEY" "$HOST_DEST_DIR"; then
    CHANGED_HOST=true
    chown freerad:freerad "$HOST_DEST_CERT" "$HOST_DEST_KEY" 2>/dev/null || true
    chmod 0400 "$HOST_DEST_CERT" "$HOST_DEST_KEY"
fi

# --- Sync B: Docker volume (only if container is running) ---
if docker ps --format '{{.Names}}' | grep -q "^${DOCKER_CONTAINER}\$"; then
    # Copy into the volume via a one-shot alpine container.
    # Sets ownership to GID 101 (freerad inside the FreeRADIUS container)
    # and mode 0640 so the non-root freerad user can read them.
    if docker run --rm \
        -v "${DOCKER_VOLUME}":/certs \
        -v "$CADDY_CERT_DIR":/src:ro \
        alpine sh -c "
            mkdir -p /certs
            changed=false
            for f in nodns.shop.crt nodns.shop.key; do
                if [ ! -f /certs/\$f ] || [ \"\$(sha256sum /src/\$f | awk '{print \$1}')\" != \"\$(sha256sum /certs/\$f | awk '{print \$1}')\" ]; then
                    cp /src/\$f /certs/\$f
                    changed=true
                fi
            done
            if [ \"\$changed\" = true ]; then
                chgrp 101 /certs/nodns.shop.crt /certs/nodns.shop.key
                chmod 640 /certs/nodns.shop.crt /certs/nodns.shop.key
                echo CHANGED
            else
                echo UNCHANGED
            fi
        " 2>/dev/null | grep -q CHANGED; then
        CHANGED_DOCKER=true
    fi
fi

# --- Restart the appropriate FreeRADIUS if anything changed ---
if [ "$CHANGED_HOST" = true ] || [ "$CHANGED_DOCKER" = true ]; then
    if [ "$CHANGED_DOCKER" = true ] && docker ps --format '{{.Names}}' | grep -q "^${DOCKER_CONTAINER}\$"; then
        log "Restarting FreeRADIUS container to load new certs"
        docker restart "$DOCKER_CONTAINER"
    elif systemctl list-unit-files | grep -q freeradius.service; then
        log "Restarting systemd FreeRADIUS to load new certs"
        systemctl restart freeradius
    fi
    log "Sync complete (host=${CHANGED_HOST}, docker=${CHANGED_DOCKER})"
else
    log "Certs unchanged — no restart needed"
fi
