#!/bin/bash
# sync-caddy-certs.sh — Copy Let's Encrypt certs from Caddy to FreeRADIUS
#
# Copies certs from Caddy's ACME store to /etc/freeradius/3.0/certs/letsencrypt/
# Sets restrictive permissions (0400 on certs, 0700 on dir, freerad:freerad).
# Only restarts FreeRADIUS if the certificate actually changed (checksum comparison).
# If Caddy certs don't exist yet (fresh install), exits cleanly — FreeRADIUS
# continues using its self-signed fallback certs.
set -euo pipefail

TAG="sync-caddy-certs"

# --- Source paths (Caddy's ACME store) ---
CADDY_CERT_DIR="/var/lib/caddy/.local/share/caddy/certificates/acme-v02.api.letsencrypt.org-directory/nodns.shop"
SRC_CERT="$CADDY_CERT_DIR/nodns.shop.crt"
SRC_KEY="$CADDY_CERT_DIR/nodns.shop.key"

# --- Destination paths (FreeRADIUS) ---
DEST_DIR="/etc/freeradius/3.0/certs/letsencrypt"
DEST_CERT="$DEST_DIR/nodns.shop.crt"
DEST_KEY="$DEST_DIR/nodns.shop.key"

log() {
    logger -t "$TAG" "$@"
    echo "[$TAG] $*"
}

# --- Bail out if Caddy hasn't provisioned certs yet (fresh install) ---
if [ ! -f "$SRC_CERT" ] || [ ! -f "$SRC_KEY" ]; then
    log "Caddy LE certs not found at $CADDY_CERT_DIR — skipping sync (self-signed fallback active)"
    exit 0
fi

# --- Create destination directory if needed ---
if [ ! -d "$DEST_DIR" ]; then
    log "Creating $DEST_DIR"
    mkdir -p "$DEST_DIR"
    chown freerad:freerad "$DEST_DIR"
    chmod 0700 "$DEST_DIR"
fi

# --- Compare checksums — only act if something changed ---
CERT_CHANGED=false

if [ -f "$DEST_CERT" ]; then
    SRC_CSUM=$(sha256sum "$SRC_CERT" | awk '{print $1}')
    DEST_CSUM=$(sha256sum "$DEST_CERT" | awk '{print $1}')
    if [ "$SRC_CSUM" != "$DEST_CSUM" ]; then
        CERT_CHANGED=true
    fi
else
    # No existing cert — need to copy
    CERT_CHANGED=true
fi

if [ -f "$DEST_KEY" ]; then
    SRC_KSUM=$(sha256sum "$SRC_KEY" | awk '{print $1}')
    DEST_KSUM=$(sha256sum "$DEST_KEY" | awk '{print $1}')
    if [ "$SRC_KSUM" != "$DEST_KSUM" ]; then
        CERT_CHANGED=true
    fi
else
    CERT_CHANGED=true
fi

# --- Nothing changed — done ---
if [ "$CERT_CHANGED" = false ]; then
    log "Certs unchanged — no restart needed"
    exit 0
fi

# --- Copy and lock down permissions ---
log "Copying updated certs to $DEST_DIR"

cp "$SRC_CERT" "$DEST_CERT"
cp "$SRC_KEY" "$DEST_KEY"

chown freerad:freerad "$DEST_CERT" "$DEST_KEY"
chmod 0400 "$DEST_CERT" "$DEST_KEY"

# --- Restart FreeRADIUS to pick up new certs ---
log "Restarting FreeRADIUS to load new certs"
if systemctl restart freeradius; then
    log "FreeRADIUS restarted successfully"
else
    log "ERROR: FreeRADIUS restart failed"
    exit 1
fi
