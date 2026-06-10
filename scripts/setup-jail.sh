#!/bin/bash
# setup-jail.sh — Create the busybox chroot template for tollgate-auth-ssh
# Run once on the target server as root.
set -euo pipefail

BASEDIR="/opt/cashu-tollgate"
JAIL="$BASEDIR/jail-template"

echo ">>> Setting up jail template at $JAIL"

# Install static busybox
if ! command -v busybox &>/dev/null || ! file "$(which busybox)" | grep -q "statically linked"; then
    echo ">>> Installing busybox-static..."
    apt-get update -qq && apt-get install -y -qq busybox-static
fi

BBOX="$(which busybox)"
echo ">>> Using busybox: $BBOX ($(file "$BBOX" | head -1))"

# Create directory structure
rm -rf "$JAIL"
mkdir -p "$JAIL"/{bin,dev,etc,home/nobody,tmp,proc,sys}

# Copy busybox as the sole binary
cp "$BBOX" "$JAIL/bin/busybox"
chmod 755 "$JAIL/bin/busybox"

# Create applet symlinks (only safe ones)
APPLETS="sh ash ls cat echo grep head tail wc date pwd clear
         printf tr cut sort uniq sed awk sleep true false test expr
         mkdir rmdir rm mv cp ln chmod chown touch du df free
         hostname whoami id env printenv set tty
         more less yes tee"

for applet in $APPLETS; do
    ln -sf busybox "$JAIL/bin/$applet"
done

# Create the timeleft command (pure shell, busybox-compatible)
cat > "$JAIL/bin/timeleft" << 'TIMELEFT'
#!/bin/sh
# timeleft - show remaining session time
META="$HOME/.tollgate"
if [ ! -f "$META" ]; then
    echo "  Not in a tollgate session."
    exit 1
fi
STARTED=$(sed 's/.*"started":\([0-9]*\).*/\1/' "$META")
DURATION=$(sed 's/.*"duration":\([0-9]*\).*/\1/' "$META")
AMOUNT=$(sed 's/.*"amount":\([0-9]*\).*/\1/' "$META")
NOW=$(date +%s)
REMAINING=$((STARTED + DURATION - NOW))
if [ "$REMAINING" -le 0 ]; then
    echo "  Time's up!"
    exit 0
fi
MINS=$((REMAINING / 60))
SECS=$((REMAINING % 60))
TOTAL_MINS=$((DURATION / 60))
FILLED=$((REMAINING * 30 / DURATION))
BAR=""
i=0
while [ "$i" -lt 30 ]; do
    if [ "$i" -lt "$FILLED" ]; then
        BAR="${BAR}#"
    else
        BAR="${BAR}-"
    fi
    i=$((i + 1))
done
echo "  Time remaining: ${MINS}m ${SECS}s (${REMAINING}s)"
echo "  Paid for:       ${TOTAL_MINS} minutes"
echo "  [${BAR}]"
TIMELEFT
chmod 755 "$JAIL/bin/timeleft"

# Minimal /etc/passwd and /etc/group
echo "root:x:0:0:root:/root:/bin/sh" > "$JAIL/etc/passwd"
echo "nobody:x:65534:65534:nobody:/home/nobody:/bin/sh" >> "$JAIL/etc/passwd"
echo "root:x:0:" > "$JAIL/etc/group"
echo "nogroup:x:65534:" >> "$JAIL/etc/group"

# Login profile: shows welcome + sets simple prompt
cat > "$JAIL/etc/profile" << 'PROFILE'
# /etc/profile - Cashu Tollgate session
export HOME=/home/nobody
export PATH=/bin
export PS1='tollgate $ '
clear
if [ -f "$HOME/.tollgate" ]; then
    echo ""
    echo "  Welcome to the Cashu Tollgate."
    echo "  Type 'timeleft' to see your remaining time."
    echo "  Your session ends automatically when time runs out."
    echo ""
fi
PROFILE

# Set permissions
chmod 1777 "$JAIL/tmp"
chown -R 65534:65534 "$JAIL/home/nobody"
chmod 700 "$JAIL/home/nobody"
chown root:root "$JAIL/dev" "$JAIL/etc" "$JAIL/proc" "$JAIL/sys"

# Create device nodes (requires root)
mknod -m 666 "$JAIL/dev/null" c 1 3 2>/dev/null || true
mknod -m 666 "$JAIL/dev/zero" c 1 5 2>/dev/null || true
mknod -m 666 "$JAIL/dev/urandom" c 1 9 2>/dev/null || true
mknod -m 666 "$JAIL/dev/tty" c 5 0 2>/dev/null || true

echo ">>> Jail template ready at $JAIL"
echo ">>> Applets available: $(ls "$JAIL/bin/" | wc -l)"
echo ">>> Size: $(du -sh "$JAIL" | cut -f1)"
