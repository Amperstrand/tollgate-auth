#!/bin/bash
# setup-jail.sh — Build tollgate-shell and create minimal chroot template
# No busybox, no shell, no file tools. One static binary IS the guest experience.
set -euo pipefail

REMOTE_USER="root"
REMOTE_HOST="nodns.shop"
BASEDIR="/opt/cashu-tollgate"
JAIL="$BASEDIR/jail-template"

echo ">>> Building tollgate-shell for Linux (static, no CGO)..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o tollgate-shell ./cmd/tollgate-shell/
echo ">>> Built: $(file tollgate-shell | head -1)"

echo ">>> Copying tollgate-shell to $REMOTE_HOST..."
scp tollgate-shell "$REMOTE_USER@$REMOTE_HOST:/tmp/tollgate-shell"

echo ">>> Creating minimal jail template on $REMOTE_HOST..."
ssh "$REMOTE_USER@$REMOTE_HOST" bash -s << 'REMOTE_SCRIPT'
set -euo pipefail
BASEDIR="/opt/cashu-tollgate"
JAIL="$BASEDIR/jail-template"

cp /tmp/tollgate-shell "$BASEDIR/tollgate-shell"
chmod 755 "$BASEDIR/tollgate-shell"

rm -rf "$JAIL"
mkdir -p "$JAIL/bin" "$JAIL/tmp" "$JAIL/home"

cp "$BASEDIR/tollgate-shell" "$JAIL/bin/tollgate-shell"
chmod 755 "$JAIL/bin/tollgate-shell"
chmod 1777 "$JAIL/tmp"

echo ">>> Jail template ready at $JAIL"
echo ">>> Contents:"
ls -la "$JAIL/bin/"
echo ">>> Size: $(du -sh "$JAIL" | cut -f1)"
REMOTE_SCRIPT

echo ">>> Done. Jail contains only tollgate-shell — no busybox, no shell, no file tools."
