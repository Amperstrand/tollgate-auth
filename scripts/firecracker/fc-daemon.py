#!/usr/bin/env python3
"""Mini Firecracker daemon for tollgate-auth-ssh prototype.

Wraps Firecracker VM lifecycle behind the same HTTP API as vps-on-demand:
  POST   /vms       Create VM (returns id, vsock path)
  DELETE /vms/{id}  Destroy VM
  GET    /health    Health check

Each VM gets:
- initramfs with busybox + tollgate-vm-agent + vsock modules
- vsock device (uds_path = /tmp/fc-vm-{id}/v.sock)
- 1 vCPU, 256MB RAM (configurable)
"""
import json
import os
import signal
import subprocess
import time
import uuid
from http.server import HTTPServer, BaseHTTPRequestHandler
from pathlib import Path

FIRECRACKER = "/usr/local/bin/firecracker"
VMLINUX = "/tmp/vmlinux"
INITRAMFS = "/tmp/initramfs.cpio.gz"
VM_BASE = "/tmp/fc-vms"
DEFAULT_MEM = 256
DEFAULT_VCPUS = 1

vms = {}  # id -> {proc, dir, cid}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass

    def _json(self, code, data):
        body = json.dumps(data).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", len(body))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/health":
            self._json(200, {"status": "ok", "vms": len(vms)})
        elif self.path == "/vms":
            self._json(200, [{"id": k, "alive": v["proc"].poll() is None} for k, v in vms.items()])
        else:
            self._json(404, {"error": "not found"})

    def do_POST(self):
        if self.path != "/vms":
            self._json(404, {"error": "not found"})
            return

        length = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(length)) if length else {}

        mem_mb = body.get("mem_mb", DEFAULT_MEM)
        vcpus = body.get("cpus", DEFAULT_VCPUS)
        vm_id = uuid.uuid4().hex[:12]
        vm_dir = Path(VM_BASE) / vm_id
        vm_dir.mkdir(parents=True, exist_ok=True)

        cid = 3 + len(vms)
        ip_suffix = 2 + len(vms)
        vm_ip = f"172.16.0.{ip_suffix}"
        tap_name = f"fc{len(vms)+1}"
        vsock_path = str(vm_dir / "v.sock")

        # Create TAP device and attach to bridge
        try:
            subprocess.run(["ip", "tuntap", "add", tap_name, "mode", "tap"], check=True, capture_output=True)
            subprocess.run(["ip", "link", "set", tap_name, "up"], check=True, capture_output=True)
            subprocess.run(["ip", "link", "set", tap_name, "master", "br-fc"], check=True, capture_output=True)
        except subprocess.CalledProcessError as e:
            self._json(500, {"error": f"TAP setup failed: {e.stderr.decode()}"})
            return

        config = {
            "boot-source": {
                "kernel_image_path": VMLINUX,
                "initrd_path": INITRAMFS,
                "boot_args": f"console=ttyS0 reboot=k panic=1 fcip={vm_ip}",
            },
            "drives": [],
            "machine-config": {
                "vcpu_count": vcpus,
                "mem_size_mib": mem_mb,
            },
            "vsock": {
                "guest_cid": cid,
                "uds_path": vsock_path,
            },
            "network-interfaces": [{
                "iface_id": "net0",
                "host_dev_name": tap_name,
            }],
        }
        config_path = vm_dir / "config.json"
        config_path.write_text(json.dumps(config))

        log_path = vm_dir / "serial.log"
        log_file = open(log_path, "w")

        proc = subprocess.Popen(
            [FIRECRACKER, "--no-api", "--config-file", str(config_path)],
            stdout=log_file, stderr=subprocess.STDOUT,
        )

        # Wait for vsock socket to appear (max 5 seconds)
        for _ in range(50):
            if os.path.exists(vsock_path):
                break
            if proc.poll() is not None:
                self._json(500, {"error": "VM exited during boot"})
                return
            time.sleep(0.1)
        else:
            proc.kill()
            self._json(500, {"error": "vsock socket timeout"})
            return

        vms[vm_id] = {"proc": proc, "dir": str(vm_dir), "cid": cid, "log": log_file, "tap": tap_name}
        print(f"[fc-daemon] VM {vm_id} started (cid={cid}, ip={vm_ip}, vsock={vsock_path})")

        self._json(201, {
            "id": vm_id,
            "ip": vm_ip,
            "public_port": 0,
            "cpus": vcpus,
            "mem_mb": mem_mb,
            "disk_mb": 0,
        })

    def do_DELETE(self):
        parts = self.path.strip("/").split("/")
        if len(parts) != 2 or parts[0] != "vms":
            self._json(404, {"error": "not found"})
            return

        vm_id = parts[1]
        vm = vms.pop(vm_id, None)
        if vm is None:
            self._json(404, {"error": "not found"})
            return

        proc = vm["proc"]
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=3)
            except subprocess.TimeoutExpired:
                proc.kill()

        vm["log"].close()
        subprocess.run(["ip", "link", "delete", vm.get("tap", "")], capture_output=True)
        print(f"[fc-daemon] VM {vm_id} destroyed")
        self._json(200, {"status": "destroyed", "id": vm_id})


def cleanup_all(signum=None, frame=None):
    for vm_id, vm in list(vms.items()):
        proc = vm["proc"]
        if proc.poll() is None:
            proc.kill()
        vm["log"].close()
    vms.clear()
    print("[fc-daemon] All VMs cleaned up")


if __name__ == "__main__":
    os.makedirs(VM_BASE, exist_ok=True)
    signal.signal(signal.SIGTERM, cleanup_all)
    signal.signal(signal.SIGINT, cleanup_all)
    server = HTTPServer(("127.0.0.1", 8081), Handler)
    print(f"[fc-daemon] listening on :8081 ({len(vms)} VMs)")
    server.serve_forever()
