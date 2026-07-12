#!/usr/bin/env python3
"""fc-daemon-v3.py — Multi-rootfs Firecracker daemon.

Supports Alpine, Ubuntu, and initramfs rootfs types.
API:
  POST /vms            Create VM (body: {rootfs: "alpine"|"ubuntu"|"initramfs"})
  POST /pool/warm      Create snapshot for pre-warming
  GET  /health         Health check
  GET  /vms            List VMs
  DELETE /vms/{id}     Destroy VM
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
ALPINE_ROOTFS = "/tmp/tollgate-rootfs.ext4"
UBUNTU_ROOTFS = "/tmp/tollgate-ubuntu-rootfs.ext4"
VM_BASE = "/tmp/fc-vms"
DEFAULT_MEM = 256
DEFAULT_VCPUS = 1

vms = {}


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
            rootfs_available = []
            if os.path.exists(ALPINE_ROOTFS): rootfs_available.append("alpine")
            if os.path.exists(UBUNTU_ROOTFS): rootfs_available.append("ubuntu")
            if os.path.exists(INITRAMFS): rootfs_available.append("initramfs")
            self._json(200, {"status": "ok", "vms": len(vms), "rootfs": rootfs_available})
        elif self.path == "/vms":
            self._json(200, [{"id": k, "alive": v["proc"].poll() is None, "rootfs": v.get("rootfs", "?")} for k, v in vms.items()])
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
        rootfs_type = body.get("rootfs", "initramfs")

        vm_id, vm_ip = self._create_vm(rootfs_type, mem_mb, vcpus)
        if vm_id:
            self._json(201, {"id": vm_id, "ip": vm_ip, "public_port": 0,
                             "cpus": vcpus, "mem_mb": mem_mb, "disk_mb": 0,
                             "rootfs": rootfs_type})
        else:
            self._json(500, {"error": f"VM creation failed for rootfs={rootfs_type}"})

    def _create_vm(self, rootfs_type, mem_mb, vcpus):
        vm_id = uuid.uuid4().hex[:12]
        vm_dir = Path(VM_BASE) / vm_id
        vm_dir.mkdir(parents=True, exist_ok=True)

        ip_suffix = 2 + len(vms)
        vm_ip = f"172.16.0.{ip_suffix}"
        tap_name = f"fc{vm_id[:8]}"
        vsock_path = str(vm_dir / "v.sock")

        try:
            subprocess.run(["ip", "tuntap", "add", tap_name, "mode", "tap"], check=True, capture_output=True)
            subprocess.run(["ip", "link", "set", tap_name, "up"], check=True, capture_output=True)
            subprocess.run(["ip", "link", "set", tap_name, "master", "br-fc"], check=True, capture_output=True)
        except subprocess.CalledProcessError:
            return None, None

        boot_args = f"console=ttyS0 reboot=k panic=1 fcip={vm_ip}"

        if rootfs_type == "initramfs":
            config = {
                "boot-source": {
                    "kernel_image_path": VMLINUX,
                    "initrd_path": INITRAMFS,
                    "boot_args": boot_args,
                },
                "drives": [],
            }
        elif rootfs_type in ("alpine", "ubuntu"):
            rootfs_path = ALPINE_ROOTFS if rootfs_type == "alpine" else UBUNTU_ROOTFS
            if not os.path.exists(rootfs_path):
                return None, None
            config = {
                "boot-source": {
                    "kernel_image_path": VMLINUX,
                    "initrd_path": INITRAMFS,
                    "boot_args": boot_args + " root=/dev/vda",
                },
                "drives": [{
                    "drive_id": "rootfs",
                    "path_on_host": rootfs_path,
                    "is_root_device": True,
                    "is_read_only": False,
                }],
            }
        else:
            return None, None

        config["machine-config"] = {"vcpu_count": vcpus, "mem_size_mib": mem_mb}
        config["vsock"] = {"guest_cid": 3 + len(vms), "uds_path": vsock_path}
        config["network-interfaces"] = [{"iface_id": "net0", "host_dev_name": tap_name}]

        config_path = vm_dir / "config.json"
        config_path.write_text(json.dumps(config))

        log_file = open(vm_dir / "serial.log", "w")
        proc = subprocess.Popen(
            [FIRECRACKER, "--no-api", "--config-file", str(config_path)],
            stdout=log_file, stderr=subprocess.STDOUT,
        )

        # For ext4 rootfs: wait longer (systemd/OpenRC takes more time to boot)
        max_wait = 30 if rootfs_type != "initramfs" else 5
        for _ in range(max_wait * 10):
            if os.path.exists(vsock_path):
                break
            if proc.poll() is not None:
                return None, None
            time.sleep(0.1)

        vms[vm_id] = {"proc": proc, "dir": str(vm_dir), "log": log_file, "tap": tap_name, "rootfs": rootfs_type}
        print(f"[fc-daemon] VM {vm_id} ({rootfs_type}) started (ip={vm_ip})")
        return vm_id, vm_ip

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


if __name__ == "__main__":
    os.makedirs(VM_BASE, exist_ok=True)
    signal.signal(signal.SIGTERM, cleanup_all)
    signal.signal(signal.SIGINT, cleanup_all)
    server = HTTPServer(("127.0.0.1", 8081), Handler)
    rootfs_list = []
    if os.path.exists(ALPINE_ROOTFS): rootfs_list.append("alpine")
    if os.path.exists(UBUNTU_ROOTFS): rootfs_list.append("ubuntu")
    if os.path.exists(INITRAMFS): rootfs_list.append("initramfs")
    print(f"[fc-daemon v3] listening on :8081 ({len(vms)} VMs, rootfs={rootfs_list})")
    server.serve_forever()
