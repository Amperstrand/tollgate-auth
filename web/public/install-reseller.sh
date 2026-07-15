#!/bin/bash
set -euo pipefail

# install-reseller.sh — One-command Firecracker + ContextVM node setup.
#
# Usage:
#   git clone https://github.com/Amperstrand/vps-on-demand.git
#   cd vps-on-demand
#   sudo bash public/install-reseller.sh
#
# Requirements:
#   - Debian 13 (trixie) or later
#   - /dev/kvm (nested KVM)
#   - 4GB+ RAM
#   - Root access
#
# What this installs:
#   1. Firecracker v1.16.0 + Anvil kernel
#   2. Alpine rootfs with OpenSSH
#   3. Initramfs with standalone busybox + dropbear + seed_rand + NAT
#   4. ContextVM server (MCP over Nostr, kind 25910, CEP-4 encrypted)
#   5. Firecracker daemon (HTTP API, --no-dvm)
#   6. Health monitor (systemd timer, auto-restart)
#   7. NAT for VM outbound internet

INSTALL_DIR="/opt/contextvm"
DATA_DIR="/var/lib/vps-on-demand"
FC_VERSION="v1.16.0"
ANVIL_VERSION="v7.1.2"
DAEMON_PORT="${DAEMON_PORT:-8081}"
RELAYS="${RELAYS:-wss://relay.cashu.email,wss://nos.lol,wss://offchain.pub}"

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"

echo "======================================================="
echo "  Firecracker + ContextVM Node Installer"
echo "======================================================="
echo ""

# ─── Preflight ───
if [ "$(id -u)" -ne 0 ]; then echo "ERROR: Run as root"; exit 1; fi
if [ ! -e /dev/kvm ]; then echo "ERROR: /dev/kvm not found — need nested KVM"; exit 1; fi
RAM_MB=$(free -m | awk '/^Mem:/{print $2}')
if [ "$RAM_MB" -lt 3000 ]; then echo "WARNING: ${RAM_MB}MB RAM — recommend 4GB+"; fi
echo "[0] Preflight: OK ($(free -m | awk '/^Mem:/{print $2}')MB, $(nproc) CPUs)"

# ─── Step 1: System packages ───
echo "[1/9] System packages..."
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq gcc make autoconf zlib1g-dev libssl-dev \
    python3 python3-pip iptables iproute2 kmod procps \
    wget curl xz-utils cpio e2fsprogs openssh-client \
    >/dev/null 2>&1
echo "  done"

# ─── Step 2: Standalone busybox (NOT Debian's broken busybox-static) ───
echo "[2/9] Standalone busybox..."
BUSYBOX_BIN="${DATA_DIR}/busybox"
mkdir -p "$DATA_DIR/images" "$DATA_DIR/run"
if [ ! -f "$BUSYBOX_BIN" ] || [ "$(stat -c%s "$BUSYBOX_BIN" 2>/dev/null || echo 0)" -lt 1000000 ]; then
    wget -q -O "$BUSYBOX_BIN" \
      'https://busybox.net/downloads/binaries/1.35.0-x86_64-linux-musl/busybox'
    chmod +x "$BUSYBOX_BIN"
fi
echo "  $(stat -c%s "$BUSYBOX_BIN") bytes"

# ─── Step 3: Firecracker ───
echo "[3/9] Firecracker ${FC_VERSION}..."
if ! command -v firecracker >/dev/null 2>&1; then
    cd /tmp
    wget -q "https://github.com/firecracker-microvm/firecracker/releases/download/${FC_VERSION}/firecracker-${FC_VERSION}-x86_64.tgz"
    tar xzf "firecracker-${FC_VERSION}-x86_64.tgz"
    cp "release-${FC_VERSION}-x86_64/firecracker-${FC_VERSION}-x86_64" /usr/local/bin/firecracker
    chmod +x /usr/local/bin/firecracker
    rm -rf "firecracker-${FC_VERSION}-x86_64.tgz" "release-${FC_VERSION}-x86_64"
fi
echo "  $(firecracker --version 2>&1 | head -1)"

# ─── Step 4: Anvil kernel ───
echo "[4/9] Anvil kernel ${ANVIL_VERSION}..."
if [ ! -f "${DATA_DIR}/vmlinux" ] || [ "$(stat -c%s "${DATA_DIR}/vmlinux" 2>/dev/null || echo 0)" -lt 10000000 ]; then
    wget -q -O /tmp/vmlinux-anvil.xz \
      "https://github.com/Work-Fort/Anvil/releases/download/${ANVIL_VERSION}/vmlinux-7.1.2-x86_64.xz"
    xz -d /tmp/vmlinux-anvil.xz
    mv /tmp/vmlinux-anvil "${DATA_DIR}/vmlinux"
fi
echo "  $(du -h "${DATA_DIR}/vmlinux" | cut -f1)"

# ─── Step 5: Alpine rootfs ───
echo "[5/9] Alpine rootfs..."
ROOTFS="${DATA_DIR}/images/alpine-base.ext4"
if [ ! -f "$ROOTFS" ] || [ "$(stat -c%s "$ROOTFS" 2>/dev/null || echo 0)" -lt 100000000 ]; then
    cd /tmp
    wget -q 'https://dl-cdn.alpinelinux.org/alpine/v3.22/releases/x86_64/alpine-minirootfs-3.22.1-x86_64.tar.gz'
    dd if=/dev/zero of="$ROOTFS" bs=1M count=512 status=none
    mkfs.ext4 -q "$ROOTFS"
    mkdir -p /tmp/rootfs-mnt
    mount -o loop "$ROOTFS" /tmp/rootfs-mnt
    tar xzf alpine-minirootfs-3.22.1-x86_64.tar.gz -C /tmp/rootfs-mnt/
    mkdir -p /tmp/rootfs-mnt/root/.ssh
    chmod 700 /tmp/rootfs-mnt/root/.ssh
    printf '#!/bin/sh\nexit 0\n' > /tmp/rootfs-mnt/sbin/init
    chmod +x /tmp/rootfs-mnt/sbin/init
    umount /tmp/rootfs-mnt
    rm -rf /tmp/rootfs-mnt alpine-minirootfs-*.tar.gz
fi
echo "  $(du -h "$ROOTFS" | cut -f1)"

# ─── Step 6: Static dropbear ───
echo "[6/9] Static dropbear..."
DROPBEAR_BIN="${DATA_DIR}/dropbear"
if [ ! -f "$DROPBEAR_BIN" ]; then
    cd /tmp
    wget -q 'https://matt.ucc.asn.au/dropbear/releases/dropbear-2024.86.tar.bz2'
    tar xjf dropbear-2024.86.tar.bz2
    cd dropbear-2024.86
    ./configure --enable-static --disable-shared --disable-zlib \
        --disable-lastlog --disable-utmp --disable-utmpx \
        --disable-wtmp --disable-wtmpx >/dev/null 2>&1
    make -j"$(nproc)" PROGRAMS=dropbear >/dev/null 2>&1
    cp dropbear "$DROPBEAR_BIN"
    cd /
    rm -rf /tmp/dropbear-2024.86*
fi
echo "  $(du -h "$DROPBEAR_BIN" | cut -f1)"

# ─── Step 7: seed_rand + initramfs ───
echo "[7/9] seed_rand + initramfs..."
SEED_BIN="${DATA_DIR}/seed_rand"
if [ ! -f "$SEED_BIN" ]; then
    cat > /tmp/seed_rand.c << 'SRCEOF'
#include <sys/ioctl.h>
#include <fcntl.h>
#include <linux/random.h>
#include <unistd.h>
#include <stdlib.h>
int main() {
    int fd = open("/dev/urandom", O_RDWR);
    if (fd < 0) return 1;
    struct rand_pool_info *info = malloc(sizeof(struct rand_pool_info) + 128);
    info->entropy_count = 512; info->buf_size = 64;
    read(fd, info->buf, 64);
    ioctl(fd, RNDADDENTROPY, info);
    read(fd, info->buf, 64);
    ioctl(fd, RNDADDENTROPY, info);
    free(info);
    close(fd); return 0;
}
SRCEOF
    gcc -static -o "$SEED_BIN" /tmp/seed_rand.c
    rm /tmp/seed_rand.c
fi

KVER=$(uname -r)
rm -rf /tmp/irfs
mkdir -p /tmp/irfs/{bin,sbin,dev,proc,sys,mnt,etc/dropbear,root/.ssh,var/log,tmp,etc}
mkdir -p "/tmp/irfs/lib/modules/$KVER/kernel"
mkdir -p /tmp/irfs/lib/x86_64-linux-gnu /tmp/irfs/lib64

cp "$BUSYBOX_BIN" /tmp/irfs/bin/busybox
for cmd in sh mount insmod sleep ls mkdir ip ifconfig route hostname \
           cat echo cp chmod grep cut awk sed uname; do
    ln -sf busybox "/tmp/irfs/bin/$cmd"
done
cp "$DROPBEAR_BIN" /tmp/irfs/sbin/dropbear
cp "$SEED_BIN" /tmp/irfs/sbin/seed_rand

# NSS libraries for glibc static dropbear
for lib in /lib/x86_64-linux-gnu/libnss_files.so.2 \
           /lib/x86_64-linux-gnu/libc.so.6 \
           /lib64/ld-linux-x86-64.so.2; do
    [ -f "$lib" ] && cp "$lib" "/tmp/irfs${lib}" 2>/dev/null || true
done
printf 'passwd: files\nshadow: files\ngroup: files\n' > /tmp/irfs/etc/nsswitch.conf
echo "root:x:0:0:root:/root:/bin/sh" > /tmp/irfs/etc/passwd
echo "root::18000:0:99999:7:::" > /tmp/irfs/etc/shadow

# All virtio modules
for mod in virtio_mmio virtio virtio_ring virtio_blk virtio_net; do
    src_dir=$(find "/lib/modules/$KVER/kernel" -name "${mod}.ko.xz" 2>/dev/null | head -1)
    if [ -n "$src_dir" ]; then
        rel="${src_dir#/lib/modules/$KVER/kernel/}"
        dest_dir="/tmp/irfs/lib/modules/$KVER/kernel/$(dirname "$rel")"
        mkdir -p "$dest_dir"
        xz -dkc "$src_dir" > "$dest_dir/${mod}.ko"
    fi
done

cat > /tmp/irfs/init << 'INITEOF'
#!/bin/sh
mount -t proc proc /proc
mount -t sysfs sysfs /sys
mount -t devtmpfs devtmpfs /dev
KVER=$(uname -r)
M=/lib/modules/$KVER/kernel
insmod $M/drivers/virtio/virtio.ko 2>/dev/null
insmod $M/drivers/virtio/virtio_ring.ko 2>/dev/null
insmod $M/drivers/virtio/virtio_mmio.ko 2>/dev/null
insmod $M/drivers/block/virtio_blk.ko 2>/dev/null
insmod $M/drivers/net/virtio_net.ko 2>/dev/null
sleep 3
mkdir -p /mnt
mount -t ext4 /dev/vda /mnt
mount --bind /dev /mnt/dev
mount --bind /proc /mnt/proc
mount --bind /sys /mnt/sys
/sbin/seed_rand
ip link set lo up
VM_IP=$(grep 'ip addr add' /mnt/sbin/init 2>/dev/null | head -1 | sed 's/.*ip addr add //' | cut -d/ -f1 | tr -d ' ')
HOST_IP=$(grep 'ip route add' /mnt/sbin/init 2>/dev/null | head -1 | sed 's/.*via //' | tr -d ' ')
[ -z "$VM_IP" ] && VM_IP="172.16.1.2"
[ -z "$HOST_IP" ] && HOST_IP="172.16.1.1"
ip addr add $VM_IP/24 dev eth0
ip link set eth0 up
ip route add default via $HOST_IP
if [ -f /mnt/root/.ssh/authorized_keys ]; then
    cp /mnt/root/.ssh/authorized_keys /root/.ssh/authorized_keys
    chmod 600 /root/.ssh/authorized_keys
fi
/sbin/dropbear -E -R -p 22 &
sleep 2
echo MICROVM_READY
while true; do sleep 60; done
INITEOF
chmod +x /tmp/irfs/init

cd /tmp/irfs
find . | cpio -H newc -o 2>/dev/null | gzip > "${DATA_DIR}/initramfs.cpio.gz"
cd / && rm -rf /tmp/irfs
echo "  initramfs: $(du -h "${DATA_DIR}/initramfs.cpio.gz" | cut -f1)"

# ─── Step 8: Install code from repo ───
echo "[8/9] Installing code from repo..."
mkdir -p "$INSTALL_DIR/handlers"

# Copy firecracker daemon code from local repo
if [ -d "$REPO_DIR/firecracker" ]; then
    cp "$REPO_DIR"/firecracker/*.py "$INSTALL_DIR/" 2>/dev/null || true
    cp -r "$REPO_DIR/firecracker/handlers" "$INSTALL_DIR/" 2>/dev/null || true
    cp -r "$REPO_DIR/firecracker/nostr" "$INSTALL_DIR/" 2>/dev/null || true
fi

# Copy ContextVM server from local repo
if [ -d "$REPO_DIR/contextvm" ]; then
    cp "$REPO_DIR/contextvm/server.py" "$INSTALL_DIR/contextvm-server.py"
    cp "$REPO_DIR/contextvm/nip44_gw.py" "$INSTALL_DIR/"
fi

# If repo files not available, download from GitHub
if [ ! -f "$INSTALL_DIR/firecracker-daemon.py" ]; then
    echo "  Repo not found locally — downloading from GitHub..."
    REPO_URL="https://raw.githubusercontent.com/Amperstrand/vps-on-demand/main"
    for f in firecracker-daemon.py config.py log.py persistence.py allocator.py \
             port_forward.py launcher.py validator.py credentials.py mmds_init.py \
             reaper.py payment.py dvm_provider.py; do
        wget -q "${REPO_URL}/firecracker/${f}" -O "$INSTALL_DIR/${f}" 2>/dev/null || true
    done
    for f in "handlers/__init__.py" "handlers/firecracker_vm.py"; do
        mkdir -p "$INSTALL_DIR/$(dirname $f)"
        wget -q "${REPO_URL}/firecracker/${f}" -O "$INSTALL_DIR/${f}" 2>/dev/null || true
    done
    wget -q "${REPO_URL}/contextvm/server.py" -O "$INSTALL_DIR/contextvm-server.py" 2>/dev/null || true
    wget -q "${REPO_URL}/contextvm/nip44_gw.py" -O "$INSTALL_DIR/nip44_gw.py" 2>/dev/null || true
fi

# Python deps
pip3 install --break-system-packages -q pynostr websockets coincurve cryptography 2>/dev/null

# Generate provider nsec if missing
PROVIDER_NSEC="${DATA_DIR}/provider.nsec"
if [ ! -f "$PROVIDER_NSEC" ]; then
    python3 -c "from pynostr.key import PrivateKey; pk=PrivateKey(); open('${PROVIDER_NSEC}','w').write(pk.hex())" 2>/dev/null
    chmod 600 "$PROVIDER_NSEC"
fi

# Get public IP and provider pubkey
PUBLIC_IP=$(hostname -I | awk '{print $1}')
PROVIDER_PUBKEY=$(python3 -c "
import sys; sys.path.insert(0, '$INSTALL_DIR')
from pynostr.key import PrivateKey
sk = PrivateKey.from_hex(open('$PROVIDER_NSEC').read().strip())
print(sk.public_key.hex())
" 2>/dev/null || echo "unknown")

echo "  Provider pubkey: ${PROVIDER_PUBKEY:0:20}..."

# ─── Step 9: NAT + systemd services ───
echo "[9/9] NAT + systemd services..."

# Enable IP forwarding
echo 'net.ipv4.ip_forward=1' > /etc/sysctl.d/99-vps.conf
sysctl -p /etc/sysctl.d/99-vps.conf >/dev/null 2>&1

# NAT for VM subnet
UPSTREAM=$(ip route show default | awk '{print $5}' | head -1)
iptables -t nat -C POSTROUTING -s 172.16.0.0/16 -o "$UPSTREAM" -j MASQUERADE 2>/dev/null || \
    iptables -t nat -A POSTROUTING -s 172.16.0.0/16 -o "$UPSTREAM" -j MASQUERADE
iptables -C FORWARD -s 172.16.0.0/16 -o "$UPSTREAM" -j ACCEPT 2>/dev/null || \
    iptables -A FORWARD -s 172.16.0.0/16 -o "$UPSTREAM" -j ACCEPT
iptables -C FORWARD -d 172.16.0.0/16 -i "$UPSTREAM" -j ACCEPT 2>/dev/null || \
    iptables -A FORWARD -d 172.16.0.0/16 -i "$UPSTREAM" -j ACCEPT

# Firecracker daemon service
cat > /etc/systemd/system/firecracker-daemon.service << EOF
[Unit]
Description=Firecracker Daemon
After=network-online.target

[Service]
Type=simple
WorkingDirectory=$INSTALL_DIR
ExecStart=/usr/bin/python3 $INSTALL_DIR/firecracker-daemon.py --port $DAEMON_PORT --no-dvm
Restart=always
RestartSec=3
User=root
Environment=VPS_PUBLIC_HOST=$PUBLIC_IP
Environment=VPS_DEV_MODE=true
Environment=VPS_MOCK_LAUNCHER=false
Environment=VPS_AUTO_GENERATE_NSEC=false
Environment=VPS_PROVIDER_NSEC_FILE=$PROVIDER_NSEC
Environment=VPS_KERNEL_PATH=$DATA_DIR/vmlinux
Environment=VPS_ROOTFS_BASE_DIR=$DATA_DIR/images
Environment=VPS_INITRAMFS_PATH=$DATA_DIR/initramfs.cpio.gz
Environment=VPS_FIRECRACKER_BINARY=/usr/local/bin/firecracker

[Install]
WantedBy=multi-user.target
EOF

# ContextVM service
cat > /etc/systemd/system/contextvm.service << EOF
[Unit]
Description=ContextVM MCP Server
After=network-online.target firecracker-daemon.service

[Service]
Type=simple
WorkingDirectory=$INSTALL_DIR
ExecStart=/usr/bin/python3 $INSTALL_DIR/contextvm-server.py
Restart=always
RestartSec=10
User=root
Environment=PYTHONUNBUFFERED=1

[Install]
WantedBy=multi-user.target
EOF

# Health monitor
cat > $INSTALL_DIR/health-check.sh << 'HCEOF'
#!/bin/bash
FC=$(curl -s --connect-timeout 3 http://localhost:8081/health 2>/dev/null)
CV=$(systemctl is-active contextvm 2>/dev/null)
[ "$CV" != "active" ] && systemctl restart contextvm
echo "$FC" | grep -q '"ok' || systemctl restart firecracker-daemon
HCEOF
chmod +x $INSTALL_DIR/health-check.sh

cat > /etc/systemd/system/health-monitor.service << 'HCEOF'
[Unit]
Description=Health monitor
[Service]
Type=oneshot
ExecStart=/opt/contextvm/health-check.sh
HCEOF

cat > /etc/systemd/system/health-monitor.timer << 'HTEOF'
[Unit]
Description=Run health monitor every 2 min
[Timer]
OnBootSec=30
OnUnitActiveSec=120
Unit=health-monitor.service
[Install]
WantedBy=timers.target
HTEOF

# Start everything
systemctl daemon-reload
systemctl enable firecracker-daemon contextvm health-monitor.timer
systemctl restart firecracker-daemon
sleep 2
systemctl restart contextvm
systemctl start health-monitor.timer

sleep 4
echo ""
echo "======================================================="
echo "  Firecracker + ContextVM Node READY"
echo "======================================================="
echo "  HTTP API:     http://${PUBLIC_IP}:${DAEMON_PORT}"
echo "  Health:       curl http://localhost:${DAEMON_PORT}/health"
echo "  Provider key: ${PROVIDER_PUBKEY}"
echo "  Relays:       ${RELAYS}"
echo "  Tools:        create_vps, connect_vpn, list_vms, destroy_vm, faucet, health"
echo ""
echo "  Services:"
echo "    firecracker-daemon: $(systemctl is-active firecracker-daemon)"
echo "    contextvm:          $(systemctl is-active contextvm)"
echo "    health-monitor:     $(systemctl is-active health-monitor.timer)"
echo ""
echo "  Test VM:"
echo "    curl -X POST http://localhost:${DAEMON_PORT}/vms \\"
echo "      -H 'Content-Type: application/json' \\"
echo "      -d '{\"cpus\":1,\"mem_mb\":256,\"disk_mb\":512,\"ssh_key\":\"$(cat ~/.ssh/id_ed25519.pub 2>/dev/null || echo 'YOUR_SSH_KEY')\"}'"
echo ""
echo "  Logs: journalctl -u contextvm -f"
echo "======================================================="
