#!/bin/bash
# build-ubuntu-rootfs.sh — Build a Firecracker-compatible Ubuntu 24.04 ext4 rootfs
# with systemd, apt, curl, git, and tollgate-vm-agent.
#
# Must run as root on a Linux host with debootstrap.
#
# Output: /tmp/tollgate-ubuntu-rootfs.ext4 (512MB ext4 image)

set -ex

ROOTFS_IMG="/tmp/tollgate-ubuntu-rootfs.ext4"
ROOTFS_SIZE_MB=512
MNT="/tmp/ubuntu-rootfs-mnt"
UBUNTU_RELEASE="noble"
AGENT_BIN="${1:-/tmp/tollgate-vm-agent}"

echo "=== Install debootstrap ==="
apt-get install -y -qq debootstrap 2>/dev/null

echo "=== Create ext4 image ==="
dd if=/dev/zero of="$ROOTFS_IMG" bs=1M count=$ROOTFS_SIZE_MB 2>/dev/null
mkfs.ext4 -q "$ROOTFS_IMG"
mkdir -p "$MNT"
mount -o loop "$ROOTFS_IMG" "$MNT"

echo "=== Debootstrap Ubuntu $UBUNTU_RELEASE ==="
debootstrap --variant=minbase --arch=amd64 "$UBUNTU_RELEASE" "$MNT" http://archive.ubuntu.com/ubuntu/

echo "=== Configure apt sources ==="
cat > "$MNT/etc/apt/sources.list" << 'EOF'
deb http://archive.ubuntu.com/ubuntu noble main universe
deb http://archive.ubuntu.com/ubuntu noble-updates main universe
deb http://archive.ubuntu.com/ubuntu noble-security main universe
EOF

echo "=== Mount + chroot + install packages ==="
mount -t proc proc "$MNT/proc"
mount -t sysfs sysfs "$MNT/sys"
mount --bind /dev "$MNT/dev"
mount --bind /dev/pts "$MNT/dev/pts" 2>/dev/null || true

# DNS for apt
cp /etc/resolv.conf "$MNT/etc/resolv.conf"

chroot "$MNT" apt-get update -qq
chroot "$MNT" apt-get install -y -qq \
    systemd systemd-sysv \
    curl wget git vim-tiny \
    iproute2 iputils-ping dnsutils \
    ca-certificates \
    openssh-server \
    python3 \
    2>&1 | tail -3

echo "=== Add tollgate-vm-agent ==="
if [ -f "$AGENT_BIN" ]; then
    cp "$AGENT_BIN" "$MNT/usr/local/bin/tollgate-vm-agent"
    chmod +x "$MNT/usr/local/bin/tollgate-vm-agent"
else
    echo "WARNING: agent binary not found at $AGENT_BIN"
fi

echo "=== Add kernel modules ==="
KVER=$(uname -r)
mkdir -p "$MNT/lib/modules/$KVER"
MODDIR=/lib/modules/$KVER
for mod in virtio_mmio failover net_failover virtio_net vsock vmw_vsock_virtio_transport_common vmw_vsock_virtio_transport; do
  SRC=$(find "$MODDIR" -name "${mod}.ko.xz" 2>/dev/null | head -1)
  if [ -n "$SRC" ]; then
    cp "$SRC" "$MNT/lib/modules/$KVER/"
  fi
done
depmod -b "$MNT" "$KVER" 2>/dev/null || true

echo "=== Configure systemd service for agent ==="
cat > "$MNT/etc/systemd/system/tollgate-vm-agent.service" << 'EOF'
[Unit]
Description=Tollgate VM Agent (vsock shell)
After=network.target
Wants=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/tollgate-vm-agent
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
EOF

chroot "$MNT" systemctl enable tollgate-vm-agent.service 2>/dev/null || \
    ln -sf /etc/systemd/system/tollgate-vm-agent.service "$MNT/etc/systemd/system/multi-user.target.wants/tollgate-vm-agent.service"

echo "=== Module loading ==="
cat > "$MNT/etc/modules-load.d/tollgate.conf" << 'EOF'
virtio_mmio
failover
net_failover
virtio_net
vsock
vmw_vsock_virtio_transport_common
vmw_vsock_virtio_transport
EOF

echo "=== Network config ==="
cat > "$MNT/etc/systemd/network/10-eth0.network" << 'EOF'
[Match]
Name=eth0

[Link]
RequiredForOnline=yes

[Network]
DHCP=no
Address=172.16.0.100/24
Gateway=172.16.0.1
DNS=8.8.8.8
DNS=1.1.1.1
EOF

# Also add a script to set IP from kernel cmdline
cat > "$MNT/etc/systemd/system/tollgate-net-setup.service" << 'EOF'
[Unit]
Description=Tollgate Network Setup from kernel cmdline
After=systemd-networkd.service
Before=network.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/tollgate-net-setup
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOF

cat > "$MNT/usr/local/bin/tollgate-net-setup" << 'EOF'
#!/bin/bash
FCIP=$(cat /proc/cmdline | sed -n 's/.*fcip=\([0-9.]*\).*/\1/p')
if [ -n "$FCIP" ]; then
    ip link set eth0 up
    ip addr add $FCIP/24 dev eth0
    ip route add default via 172.16.0.1
    echo "nameserver 8.8.8.8" > /etc/resolv.conf
fi
EOF
chmod +x "$MNT/usr/local/bin/tollgate-net-setup"
chroot "$MNT" systemctl enable tollgate-net-setup.service 2>/dev/null || \
    ln -sf /etc/systemd/system/tollgate-net-setup.service "$MNT/etc/systemd/system/multi-user.target.wants/tollgate-net-setup.service"

# Enable systemd-networkd
chroot "$MNT" systemctl enable systemd-networkd 2>/dev/null || true

echo "=== System config ==="
echo "tollgate-ubuntu" > "$MNT/etc/hostname"

cat > "$MNT/etc/passwd" << 'EOF'
root:x:0:0:root:/root:/bin/bash
EOF

cat > "$MNT/etc/fstab" << 'EOF'
/dev/vda / ext4 rw,relatime 0 1
EOF

# Set root password to empty (login without password)
cat > "$MNT/etc/shadow" << 'EOF'
root::19000:0:99999:7:::
EOF

# Configure SSH for root login
mkdir -p "$MNT/etc/ssh"
cat > "$MNT/etc/ssh/sshd_config" << 'EOF'
Port 22
PermitRootLogin yes
PasswordAuthentication yes
PermitEmptyPasswords yes
UsePAM no
Subsystem sftp /usr/lib/openssh/sftp-server
EOF

echo "=== Unmount ==="
umount "$MNT/dev/pts" 2>/dev/null || true
umount "$MNT/dev" 2>/dev/null || true
umount "$MNT/proc" 2>/dev/null || true
umount "$MNT/sys" 2>/dev/null || true
umount "$MNT"

echo "Ubuntu rootfs: $(ls -la "$ROOTFS_IMG" | awk '{print $5}') bytes"
echo "=== UBUNTU ROOTFS BUILD COMPLETE ==="
