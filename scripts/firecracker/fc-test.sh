#!/bin/bash
exec 2>&1
set -ex

echo "STEP 1: Kill old processes"
pkill -9 -f fc-daemon 2>/dev/null || true
pkill -9 -f firecracker 2>/dev/null || true
rm -rf /tmp/fc-vms
mkdir -p /tmp/fc-vms
sleep 1

echo "STEP 2: Start daemon"
rm -f /tmp/fc-daemon.log
python3 -u /tmp/fc-daemon.py >/tmp/fc-daemon.log 2>&1 &
sleep 3
curl -s http://127.0.0.1:8081/health || { echo "DAEMON FAILED"; cat /tmp/fc-daemon.log; exit 1; }
echo ""

echo "STEP 3: Create VM and check serial log"
RESULT=$(curl -s -X POST http://127.0.0.1:8081/vms \
  -H "Content-Type: application/json" \
  -d '{"cpus":1,"mem_mb":256}')
echo "Create response: $RESULT"

VM_ID=$(echo "$RESULT" | python3 -c "import json,sys;print(json.load(sys.stdin)['id'])")
echo "VM ID: $VM_ID"

sleep 5

echo ""
echo "STEP 4: Check init messages"
SERIAL=/tmp/fc-vms/$VM_ID/serial.log
echo "=== Serial (relevant lines) ==="
grep -iE "INIT|eth0|virtio|net_failover|devpts|ptmx|network|agent|error|fail" $SERIAL 2>/dev/null | head -20
echo ""

echo "STEP 5: Test vsock shell + networking"
python3 -c "
import socket, time

vsock_path = '/tmp/fc-vms/$VM_ID/v.sock'
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(5)
try:
    s.connect(vsock_path)
    s.sendall(b'CONNECT 52\n')
    resp = s.recv(1024)
    print(f'vsock handshake: {resp}')
    if not resp.startswith(b'OK'):
        print('HANDSHAKE FAILED')
        exit(1)

    time.sleep(1)

    def cmd(c, t=3):
        s.sendall((c+'\n').encode())
        time.sleep(0.5)
        s.settimeout(t)
        try: return s.recv(4096).decode(errors='replace').strip()
        except: return '(timeout)'

    print('--- ip addr ---')
    print(cmd('ip addr show eth0'))
    print()
    print('--- ping 8.8.8.8 ---')
    print(cmd('ping -c 2 8.8.8.8', 6))
    print()
    print('--- wget example.com ---')
    print(cmd('wget -q -O- http://example.com 2>&1 | head -3', 8))
    print()
    print('--- echo test ---')
    print(cmd('echo SHELL_WORKS'))

except Exception as e:
    print(f'Error: {e}')
finally:
    s.close()
"

echo ""
echo "STEP 6: Cleanup"
curl -s -X DELETE http://127.0.0.1:8081/vms/$VM_ID
pkill -f fc-daemon 2>/dev/null
echo "DONE"
