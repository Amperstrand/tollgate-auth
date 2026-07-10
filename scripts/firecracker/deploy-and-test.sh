#!/bin/bash
set -ex
export PATH=$PATH:/usr/local/go/bin

# Kill any old processes
pkill -f fc-daemon 2>/dev/null || true
pkill -f firecracker 2>/dev/null || true
sleep 1

# Verify prerequisites
ls /dev/kvm || exit 1
ls /tmp/vmlinux || exit 1
ls /tmp/initramfs.cpio.gz || exit 1
ls /tmp/tollgate-auth-ssh || exit 1
ls /tmp/fc-daemon.py || exit 1

# NAT setup
echo 1 > /proc/sys/net/ipv4/ip_forward
ip link show br-fc 2>/dev/null || {
  ip link add br-fc type bridge
  ip addr add 172.16.0.1/24 dev br-fc
  ip link set br-fc up
}
iptables -t nat -C POSTROUTING -s 172.16.0.0/24 -o eth0 -j MASQUERADE 2>/dev/null || \
  iptables -t nat -A POSTROUTING -s 172.16.0.0/24 -o eth0 -j MASQUERADE
iptables -C FORWARD -i br-fc -o eth0 -j ACCEPT 2>/dev/null || \
  iptables -A FORWARD -i br-fc -o eth0 -j ACCEPT
iptables -C FORWARD -i eth0 -o br-fc -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || \
  iptables -A FORWARD -i eth0 -o br-fc -m state --state RELATED,ESTABLISHED -j ACCEPT

# Start daemon
rm -f /tmp/fc-daemon.log /tmp/tollgate-ssh.log
setsid python3 -u /tmp/fc-daemon.py >/tmp/fc-daemon.log 2>&1 &
sleep 2
curl -s http://127.0.0.1:8081/health || { echo "DAEMON FAILED"; cat /tmp/fc-daemon.log; exit 1; }
echo ""

# Start SSH service
TOLLGATE_VM_MODE=firecracker TOLLGATE_VM_TEST=true \
TOLLGATE_FC_DAEMON=http://127.0.0.1:8081 TOLLGATE_FC_VSOCK_DIR=/tmp/fc-vms \
TOLLGATE_VM_FALLBACK=false setsid /tmp/tollgate-auth-ssh >/tmp/tollgate-ssh.log 2>&1 &
sleep 2
ss -tlnp | grep 2222 || { echo "SSH FAILED"; cat /tmp/tollgate-ssh.log; exit 1; }

# Run all tests
python3 -u /tmp/fc-full-test.py

echo "=== ALL TESTS COMPLETE ==="
