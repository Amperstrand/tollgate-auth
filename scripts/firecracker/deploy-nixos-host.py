#!/usr/bin/env python3
"""deploy-nixos-host.py — Deploy a Firecracker-ready NixOS host on SHC.

Orders a Dev VPS Starter with nixos-cloud template, installs Firecracker,
configures the fc-daemon as a systemd service, and verifies KVM works.

Usage:
    python3 deploy-nixos-host.py --hostname fc-prod-01 --ssh-key ~/.ssh/id_ed25519.pub

Prerequisites:
    - shc-toolkit installed and configured (SHC_API_KEY env var)
    - SSH key registered with SHC
    - Credit balance for VPS ordering ($0.24/day for Starter)
"""
import argparse
import json
import os
import subprocess
import sys
import time
import urllib.request

SHC_API_KEY = os.environ.get("SHC_API_KEY", "")
SSH_KEY_PATH = os.path.expanduser("~/.ssh/id_ed25519")
SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))


def shc_api(method, path, data=None, confirm=False):
    import ssl
    BASE = "https://blesta.sovereignhybridcompute.com/user-api/v2"
    ctx = ssl.create_default_context()
    headers = {"Authorization": f"Bearer {SHC_API_KEY}", "Content-Type": "application/json"}
    if confirm:
        headers["X-User-Api-Confirm"] = "true"
    url = f"{BASE}{path}"
    req_data = json.dumps(data).encode() if data else None
    req = urllib.request.Request(url, data=req_data, headers=headers, method=method)
    try:
        resp = urllib.request.urlopen(req, timeout=120, context=ctx)
        return json.loads(resp.read())
    except urllib.error.HTTPError as e:
        body = e.read().decode()[:300]
        raise RuntimeError(f"SHC API {method} {path}: HTTP {e.code} {body}")


def ssh_run(host, command, user="root", timeout=120):
    cmd = ["ssh", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=no",
           "-o", "ConnectTimeout=10", "-i", SSH_KEY_PATH, f"{user}@{host}", command]
    r = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
    return r.returncode, r.stdout.strip(), r.stderr.strip()


def scp_upload(host, local, remote, user="root"):
    cmd = ["scp", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=no",
           "-i", SSH_KEY_PATH, local, f"{user}@{host}:{remote}"]
    r = subprocess.run(cmd, capture_output=True, text=True, timeout=60)
    return r.returncode == 0


def wait_for_provisioning(service_id, timeout=300):
    for i in range(timeout // 10):
        vm = shc_api("GET", f"/vm/{service_id}")
        state = vm.get("provisioning_state", "?")
        ips = vm.get("ips", [])
        ip = ips[0]["ip"] if ips else "no-ip"
        print(f"  {(i+1)*10}s: {state} ({ip})")
        if state == "ready" and ips:
            return vm
        time.sleep(10)
    raise TimeoutError(f"VPS {service_id} not ready after {timeout}s")


def deploy(hostname, ssh_key_file, channel="nixos-cloud"):
    pub_key = open(ssh_key_file).read().strip()

    print("=" * 60)
    print(f"  DEPLOYING FIRECRACKER NIXOS HOST: {hostname}")
    print("=" * 60)

    # Step 1: Order VPS
    print("\n[1/7] Ordering VPS with nixos-cloud template...")
    order = shc_api("POST", "/order", {
        "package_id": 80,
        "pricing_id": 241,
        "image": "nixos-cloud",
        "hostname": hostname,
        "ssh_key": pub_key,
        "term": 1,
    }, confirm=True)
    service_id = order.get("service_ids", [None])[0]
    invoice_id = order.get("invoice", {}).get("invoice_id")
    print(f"  Ordered: service_id={service_id}, invoice={invoice_id}")

    if invoice_id:
        print("  Paying invoice...")
        import uuid
        shc_api("POST", f"/payment/{invoice_id}/checkout", {
            "gateway": "btcpay_server",
            "idempotency_key": f"deploy-{uuid.uuid4().hex[:24]}",
        }, confirm=True)
        print("  Paid.")

    # Step 2: Wait for provisioning
    print(f"\n[2/7] Waiting for VPS {service_id} to provision...")
    vm = wait_for_provisioning(service_id)
    host = vm["ips"][0]["ip"]
    print(f"  Ready: {host}")

    # Step 3: Wait for SSH
    print(f"\n[3/7] Waiting for SSH on {host}...")
    for i in range(12):
        time.sleep(10)
        rc, out, _ = ssh_run(host, "echo OK", timeout=15)
        if rc == 0 and "OK" in out:
            print(f"  SSH ready after {(i+1)*10}s")
            break
    else:
        print("  SSH not responding — trying key injection...")
        shc_api("POST", f"/vm/{service_id}/ssh-keys/apply-live",
                {"ssh_key": pub_key}, confirm=True)
        time.sleep(10)

    # Step 4: Verify KVM
    print(f"\n[4/7] Verifying KVM...")
    rc, out, _ = ssh_run(host, "grep -c vmx /proc/cpuinfo; ls /dev/kvm 2>/dev/null && echo KVM_OK || echo NO_KVM")
    vmx_count = out.split("\n")[0].strip() if out else "0"
    has_kvm = "KVM_OK" in out
    print(f"  VMX flags: {vmx_count}")
    print(f"  /dev/kvm: {'YES' if has_kvm else 'NO'}")
    if not has_kvm:
        print("  WARNING: No KVM on this node! Firecracker VMs will not boot.")
        print("  Consider cancelling this VPS and ordering again (different node).")
    else:
        print("  KVM available — Firecracker ready.")

    # Step 5: Install Firecracker via nix profile
    print(f"\n[5/7] Installing Firecracker...")
    rc, out, _ = ssh_run(host, "nix profile install nixpkgs#firecracker 2>&1 | tail -3", timeout=300)
    rc, out, _ = ssh_run(host, "firecracker --version 2>&1 | head -1")
    print(f"  {out}")

    # Step 6: Upload + configure daemon
    print(f"\n[6/7] Deploying fc-daemon...")
    ssh_run(host, "mkdir -p /var/lib/firecracker/{rootfs,vms}")
    scp_upload(host, f"{SCRIPT_DIR}/fc-daemon-v3.py", "/var/lib/firecracker/fc-daemon.py")

    # Set up NAT bridge
    nat_setup = """
    echo 1 > /proc/sys/net/ipv4/ip_forward
    ip link show br-fc 2>/dev/null || (ip link add br-fc type bridge && ip addr add 172.16.0.1/24 dev br-fc && ip link set br-fc up)
    iptables -t nat -C POSTROUTING -s 172.16.0.0/24 -o eth0 -j MASQUERADE 2>/dev/null || iptables -t nat -A POSTROUTING -s 172.16.0.0/24 -o eth0 -j MASQUERADE
    iptables -C FORWARD -i br-fc -o eth0 -j ACCEPT 2>/dev/null || iptables -A FORWARD -i br-fc -o eth0 -j ACCEPT
    iptables -C FORWARD -i eth0 -o br-fc -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || iptables -A FORWARD -i eth0 -o br-fc -m state --state RELATED,ESTABLISHED -j ACCEPT
    echo "NAT setup done"
    """
    rc, out, _ = ssh_run(host, nat_setup)
    print(f"  NAT: {out}")

    # Start daemon
    ssh_run(host, "pkill -f fc-daemon 2>/dev/null; sleep 1; "
             "nohup python3 -u /var/lib/firecracker/fc-daemon.py > /var/lib/firecracker/daemon.log 2>&1 &"
             "sleep 2; curl -s http://127.0.0.1:8081/health")
    rc, out, _ = ssh_run(host, "curl -s http://127.0.0.1:8081/health 2>/dev/null")
    print(f"  Daemon: {out}")

    # Step 7: Verify with VM boot test
    print(f"\n[7/7] Verifying with VM boot test...")
    if has_kvm:
        test_script = """
        nix profile install nixpkgs#busybox 2>/dev/null
        curl -sL -o /tmp/busybox-static "https://busybox.net/downloads/binaries/1.35.0-x86_64-linux-musl/busybox"
        mkdir -p /tmp/fc-test/{bin,dev,proc,sys}
        cp /tmp/busybox-static /tmp/fc-test/bin/busybox
        ln -sf busybox /tmp/fc-test/bin/sh
        cat > /tmp/fc-test/init << 'EOF'
#!/bin/sh
/bin/busybox --install -s /bin 2>/dev/null
mount -t proc proc /proc; mount -t sysfs sysfs /sys
echo "FIRECRACKER VM OK"
poweroff -f
EOF
        chmod +x /tmp/fc-test/init
        cd /tmp/fc-test && find . | cpio -H newc -o 2>/dev/null | gzip > /tmp/fc-test.cpio.gz
        # Extract kernel
        python3 -c "
import struct,zstandard
d=open('/run/current-system/kernel','rb').read()
ss=d[0x1f1];po=struct.unpack('<I',d[0x248:0x24c])[0]
open('/tmp/vmlinux','wb').write(zstandard.ZstdDecompressor().decompress(d[(ss+1)*512+po:],max_output_size=200*1024*1024))
" 2>/dev/null
        timeout 8 firecracker --no-api --config-file <(echo '{"boot-source":{"kernel_image_path":"/tmp/vmlinux","initrd_path":"/tmp/fc-test.cpio.gz","boot_args":"console=ttyS0 reboot=k panic=1"},"drives":[],"machine-config":{"vcpu_count":1,"mem_size_mib":256}}') 2>&1 | grep -E "FIRECRACKER VM OK|panic|Error" | head -3
        """
        rc, out, _ = ssh_run(host, test_script, timeout=120)
        if "FIRECRACKER VM OK" in out:
            print("  VM BOOT TEST: PASS")
        else:
            print(f"  VM BOOT TEST: CHECK OUTPUT\n  {out}")
    else:
        print("  SKIPPED (no KVM on this node)")

    # Summary
    print("\n" + "=" * 60)
    print(f"  DEPLOYMENT COMPLETE: {hostname}")
    print(f"  Service ID: {service_id}")
    print(f"  IP: {host}")
    print(f"  KVM: {'available' if has_kvm else 'NOT available'}")
    print(f"  Daemon: http://{host}:8081/health")
    print(f"  SSH: ssh root@{host}")
    print("=" * 60)
    return {"service_id": service_id, "ip": host, "kvm": has_kvm}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Deploy Firecracker NixOS host on SHC")
    parser.add_argument("--hostname", default="fc-host", help="VPS hostname")
    parser.add_argument("--ssh-key", default="~/.ssh/id_ed25519.pub",
                        help="SSH public key file")
    parser.add_argument("--channel", default="nixos-cloud",
                        choices=["nixos-cloud", "debian13-cloud"],
                        help="SHC template to use")
    args = parser.parse_args()

    ssh_key = os.path.expanduser(args.ssh_key)
    if not os.path.exists(ssh_key):
        print(f"ERROR: SSH key not found: {ssh_key}")
        sys.exit(1)

    if not SHC_API_KEY:
        print("ERROR: SHC_API_KEY environment variable not set")
        sys.exit(1)

    result = deploy(args.hostname, ssh_key, args.channel)
    print(f"\nResult: {json.dumps(result, indent=2)}")
