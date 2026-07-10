#!/bin/bash
set -ex
KVER=$(uname -r)
MODDIR=/lib/modules/$KVER

rm -rf /tmp/initramfs
mkdir -p /tmp/initramfs/{bin,dev,proc,sys,lib/modules,etc,tmp}
cp /tmp/busybox /tmp/initramfs/bin/busybox
cp /tmp/tollgate-vm-agent /tmp/initramfs/bin/tollgate-vm-agent
ln -sf busybox /tmp/initramfs/bin/sh

for mod in virtio_mmio failover net_failover virtio_net vsock vmw_vsock_virtio_transport_common vmw_vsock_virtio_transport; do
  SRC=$(find $MODDIR -name "${mod}.ko.xz" | head -1)
  [ -n "$SRC" ] && xz -dc "$SRC" > "/tmp/initramfs/lib/modules/${mod}.ko" && echo "  ${mod}.ko OK"
done

cat > /tmp/initramfs/init << 'EOF'
#!/bin/sh
export PATH=/bin
/bin/busybox --install -s /bin 2>/dev/null
mount -t proc proc /proc
mount -t sysfs sysfs /sys
mount -t devtmpfs devtmpfs /dev 2>/dev/null || mount -t tmpfs tmpfs /dev
mkdir -p /dev/pts /dev/shm /tmp
mount -t devpts devpts /dev/pts
mount -t tmpfs tmpfs /tmp

echo "INIT: /dev/ptmx=$(test -c /dev/ptmx && echo OK || echo MISSING)"

insmod /lib/modules/virtio_mmio.ko
insmod /lib/modules/failover.ko 2>/dev/null && echo "INIT: failover OK" || echo "INIT: failover FAIL"
insmod /lib/modules/net_failover.ko 2>/dev/null && echo "INIT: net_failover OK" || echo "INIT: net_failover FAIL"
insmod /lib/modules/virtio_net.ko 2>/dev/null && echo "INIT: virtio_net OK" || echo "INIT: virtio_net FAIL"
insmod /lib/modules/vsock.ko
insmod /lib/modules/vmw_vsock_virtio_transport_common.ko
insmod /lib/modules/vmw_vsock_virtio_transport.ko

sleep 1
echo "INIT: eth0=$(ip link show eth0 2>/dev/null | head -1 || echo MISSING)"

FCIP=$(cat /proc/cmdline | sed -n 's/.*fcip=\([0-9.]*\).*/\1/p')
if [ -n "$FCIP" ]; then
  ip link set eth0 up 2>/dev/null
  ip addr add $FCIP/24 dev eth0 2>/dev/null
  ip route add default via 172.16.0.1 2>/dev/null
  echo "nameserver 8.8.8.8" > /etc/resolv.conf
  echo "INIT: net up ip=$FCIP"
fi

/bin/tollgate-vm-agent &
echo "INIT: agent PID=$!"
while true; do sleep 60; done
EOF
chmod +x /tmp/initramfs/init

rm -f /tmp/initramfs.cpio.gz
cd /tmp/initramfs && find . | cpio -H newc -o 2>/dev/null | gzip > /tmp/initramfs.cpio.gz
echo "initramfs: $(wc -c < /tmp/initramfs.cpio.gz) bytes"
