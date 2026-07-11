#!/bin/bash
# build-alpine-rootfs.sh — Build a Firecracker-compatible Alpine ext4 rootfs
# with tollgate-vm-agent, OpenRC init, networking, and devpts support.
#
# Must run as root on a Linux host with mkfs.ext4, mount, curl.
#
# Output: /tmp/tollgate-rootfs.ext4 (256MB ext4 image)
#
# Usage:
#   sudo bash build-alpine-rootfs.sh
#
# Then copy to KVM host:
#   scp /tmp/tollgate-rootfs.ext4 kvm-host:/tmp/rootfs.ext4

set -ex

ALPINE_VERSION="3.21"
ALPINE_URL="https://dl-cdn.alpinelinux.org/alpine/v${ALPINE_VERSION}/releases/x86_64/alpine-minirootfs-${ALPINE_VERSION}.3-x86_64.tar.gz"
ROOTFS_IMG="/tmp/tollgate-rootfs.ext4"
ROOTFS_SIZE_MB=256
MNT="/tmp/rootfs-mnt"
AGENT_BIN="${1:-/tmp/tollgate-vm-agent}"

echo "=== 1. Download Alpine mini rootfs ==="
if [ ! -f /tmp/alpine-rootfs.tar.gz ]; then
    curl -sL -o /tmp/alpine-rootfs.tar.gz "$ALPINE_URL"
fi
ls -la /tmp/alpine-rootfs.tar.gz

echo "=== 2. Create ext4 image ==="
dd if=/dev/zero of="$ROOTFS_IMG" bs=1M count=$ROOTFS_SIZE_MB 2>/dev/null
mkfs.ext4 -q "$ROOTFS_IMG"
mkdir -p "$MNT"
mount -o loop "$ROOTFS_IMG" "$MNT"

echo "=== 3. Extract Alpine ==="
tar xzf /tmp/alpine-rootfs.tar.gz -C "$MNT"

echo "=== 4. Fix DNS for chroot apk ==="
cp /etc/resolv.conf "$MNT/etc/resolv.conf"

cat > "$MNT/etc/apk/repositories" << 'EOF'
https://dl-cdn.alpinelinux.org/alpine/v3.21/main
https://dl-cdn.alpinelinux.org/alpine/v3.21/community
EOF

echo "=== 5. Install packages ==="
mount -t proc proc "$MNT/proc"
mount -t sysfs sysfs "$MNT/sys"
mount --bind /dev "$MNT/dev"

chroot "$MNT" /sbin/apk update
chroot "$MNT" /sbin/apk add openrc iproute2 iputils bind-tools

echo "=== 6. Add tollgate-vm-agent ==="
if [ -f "$AGENT_BIN" ]; then
    cp "$AGENT_BIN" "$MNT/usr/local/bin/tollgate-vm-agent"
    chmod +x "$MNT/usr/local/bin/tollgate-vm-agent"
else
    echo "WARNING: agent binary not found at $AGENT_BIN"
    echo "Build it first: make build-vm-agent"
fi

echo "=== 7. Configure OpenRC services ==="
cat > "$MNT/etc/init.d/tollgate-vm-agent" << 'EOF'
#!/sbin/openrc-run
name="tollgate-vm-agent"
description="Cashu tollgate vsock shell agent"
command="/usr/local/bin/tollgate-vm-agent"
command_background=true
pidfile="/run/tollgate-vm-agent.pid"
depend() {
    need net
}
EOF
chmod +x "$MNT/etc/init.d/tollgate-vm-agent"

cat > "$MNT/etc/network/interfaces" << 'EOF'
auto lo
iface lo inet loopback

auto eth0
iface eth0 inet manual
EOF

mkdir -p "$MNT/etc/local.d"
cat > "$MNT/etc/local.d/net-setup.start" << 'EOF'
#!/bin/sh
FCIP=$(cat /proc/cmdline | sed -n 's/.*fcip=\([0-9.]*\).*/\1/p')
if [ -n "$FCIP" ]; then
    ip link set eth0 up
    ip addr add $FCIP/24 dev eth0
    ip route add default via 172.16.0.1
    echo "nameserver 8.8.8.8" > /etc/resolv.conf
fi
EOF
chmod +x "$MNT/etc/local.d/net-setup.start"

chroot "$MNT" rc-update add devfs sysinit 2>/dev/null || true
chroot "$MNT" rc-update add dmesg sysinit 2>/dev/null || true
chroot "$MNT" rc-update add mdev sysinit 2>/dev/null || true
chroot "$MNT" rc-update add hwclock boot 2>/dev/null || true
chroot "$MNT" rc-update add modules boot 2>/dev/null || true
chroot "$MNT" rc-update add sysctl boot 2>/dev/null || true
chroot "$MNT" rc-update add hostname boot 2>/dev/null || true
chroot "$MNT" rc-update add bootmisc boot 2>/dev/null || true
chroot "$MNT" rc-update add networking boot 2>/dev/null || true
chroot "$MNT" rc-update add local default 2>/dev/null || true
chroot "$MNT" rc-update add tollgate-vm-agent default 2>/dev/null || true

echo "=== 8. Configure system ==="
echo "tollgate-vm" > "$MNT/etc/hostname"

cat > "$MNT/etc/fstab" << 'EOF'
/dev/vda / ext4 rw,relatime 0 1
proc /proc proc defaults 0 0
sysfs /sys sysfs defaults 0 0
devtmpfs /dev devtmpfs defaults 0 0
devpts /dev/pts devpts defaults 0 0
tmpfs /tmp tmpfs defaults 0 0
EOF

cat > "$MNT/etc/passwd" << 'EOF'
root:x:0:0:root:/root:/bin/sh
EOF
cat > "$MNT/etc/group" << 'EOF'
root:x:0:
EOF
cat > "$MNT/etc/shadow" << 'EOF'
root::19000:0:::::
EOF

echo "=== 9. Unmount ==="
umount "$MNT/dev" 2>/dev/null || true
umount "$MNT/proc" 2>/dev/null || true
umount "$MNT/sys" 2>/dev/null || true
umount "$MNT"

echo "Rootfs: $(ls -la "$ROOTFS_IMG" | awk '{print $5}') bytes"
echo "=== ROOTFS BUILD COMPLETE ==="
