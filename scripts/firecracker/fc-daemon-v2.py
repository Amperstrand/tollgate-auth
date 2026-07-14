#!/usr/bin/env python3
"""Mini Firecracker daemon v2 with pre-warmed pool (snapshot restore).

API:
  POST   /vms            Create VM from snapshot (~10ms) or cold boot
  POST   /pool/warm      Create snapshot for pre-warming
  GET    /pool/status    Pool status (snapshot ready, VMs running)
  DELETE /vms/{id}       Destroy VM
  GET    /health         Health check
  GET    /vms            List VMs

Pre-warming flow:
  1. POST /pool/warm  → boots a cold VM, waits for agent, pauses, creates snapshot
  2. POST /vms        → if snapshot exists, restores from snapshot (~10ms)
                        if not, cold boots (~2.5s)
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
SNAPSHOT_DIR = "/tmp/fc-snapshot"
DEFAULT_MEM = 256
DEFAULT_VCPUS = 1

vms = {}
snapshot = {"ready": False, "state_path": "", "mem_path": "", "cid_base": 100}


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
            self._json(200, {"status": "ok", "vms": len(vms), "snapshot_ready": snapshot["ready"]})
        elif self.path == "/vms":
            self._json(200, [{"id": k, "alive": v["proc"].poll() is None} for k, v in vms.items()])
        elif self.path == "/pool/status":
            self._json(200, {
                "snapshot_ready": snapshot["ready"],
                "state_path": snapshot["state_path"],
                "mem_path": snapshot["mem_path"],
                "active_vms": len(vms),
            })
        else:
            self._json(404, {"error": "not found"})

    def do_POST(self):
        if self.path == "/pool/warm":
            self._warm_pool()
            return

        if self.path != "/vms":
            self._json(404, {"error": "not found"})
            return

        length = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(length)) if length else {}
        mem_mb = body.get("mem_mb", DEFAULT_MEM)
        vcpus = body.get("cpus", DEFAULT_VCPUS)

        if snapshot["ready"]:
            vm_id, vm_ip = self._create_from_snapshot(mem_mb, vcpus)
        else:
            vm_id, vm_ip = self._create_cold(mem_mb, vcpus)

        if vm_id:
            self._json(201, {"id": vm_id, "ip": vm_ip, "public_port": 0,
                             "cpus": vcpus, "mem_mb": mem_mb, "disk_mb": 0})
        else:
            self._json(500, {"error": "VM creation failed"})

    def _warm_pool(self):
        """Boot a cold VM via API, wait for agent, pause, create snapshot."""
        global snapshot
        warm_id = "warm" + uuid.uuid4().hex[:8]
        warm_dir = Path(VM_BASE) / warm_id
        warm_dir.mkdir(parents=True, exist_ok=True)

        api_sock = str(warm_dir / "api.sock")
        vsock_path = str(Path(SNAPSHOT_DIR) / "warm.sock")  # Stable path — snapshot bakes this in
        os.makedirs(SNAPSHOT_DIR, exist_ok=True)  # Ensure snapshot dir exists for vsock + later
        tap_name = f"fc{warm_id[:8]}"
        warm_cid = 99

        # Create TAP
        try:
            subprocess.run(["ip", "tuntap", "add", tap_name, "mode", "tap"], check=True, capture_output=True)
            subprocess.run(["ip", "link", "set", tap_name, "up"], check=True, capture_output=True)
            subprocess.run(["ip", "link", "set", tap_name, "master", "br-fc"], check=True, capture_output=True)
        except subprocess.CalledProcessError:
            self._json(500, {"error": "TAP setup failed"})
            return

        log_file = open(warm_dir / "serial.log", "w")

        # Start Firecracker with API socket only (no --config-file)
        # We'll configure everything via the API
        proc = subprocess.Popen(
            [FIRECRACKER, "--api-sock", api_sock],
            stdout=log_file, stderr=subprocess.STDOUT,
        )

        # Wait for API socket to appear
        for _ in range(30):
            if os.path.exists(api_sock): break
            if proc.poll() is not None:
                self._json(500, {"error": "Firecracker exited before API ready"})
                return
            time.sleep(0.1)
        else:
            proc.kill()
            self._json(500, {"error": "API socket timeout"})
            return

        def api_put(path, data):
            r = subprocess.run(
                ["curl", "-s", "-w", "\n%{http_code}", "--unix-socket", api_sock,
                 "-X", "PUT", f"http://localhost{path}",
                 "-H", "Content-Type: application/json",
                 "-d", json.dumps(data)],
                capture_output=True
            )
            output = r.stdout.decode()
            lines = output.rsplit("\n", 1)
            http_code = int(lines[-1].strip()) if lines[-1].strip().isdigit() else 0
            if http_code != 204 and http_code != 200:
                body = lines[0].strip() if len(lines) > 1 else output.strip()
                print(f"[fc-daemon] API PUT {path} failed: HTTP {http_code} body={body[:200]}")
                return False
            return True

        # Configure VM via API
        if not api_put("/boot-source", {
            "kernel_image_path": VMLINUX,
            "initrd_path": INITRAMFS,
            "boot_args": "console=ttyS0 reboot=k panic=1 fcip=172.16.0.99",
        }):
            proc.kill(); self._json(500, {"error": "boot-source API failed"}); return

        if not api_put("/machine-config", {
            "vcpu_count": DEFAULT_VCPUS, "mem_size_mib": DEFAULT_MEM,
        }):
            proc.kill(); self._json(500, {"error": "machine-config API failed"}); return

        if not api_put("/vsock", {
            "guest_cid": warm_cid, "uds_path": vsock_path,
        }):
            proc.kill(); self._json(500, {"error": "vsock API failed"}); return

        # Network interface needs iface_id in the URL path
        if not api_put("/network-interfaces/net0", {
            "iface_id": "net0", "host_dev_name": tap_name,
        }):
            proc.kill(); self._json(500, {"error": "network API failed"}); return

        # Start the VM
        if not api_put("/actions", {"action_type": "InstanceStart"}):
            proc.kill(); self._json(500, {"error": "InstanceStart API failed"}); return

        # Wait for vsock socket (file appears immediately but VM hasn't booted)
        for _ in range(50):
            if os.path.exists(vsock_path): break
            if proc.poll() is not None:
                self._json(500, {"error": "VM exited during warm-up boot"})
                return
            time.sleep(0.1)

        # Poll for agent to be reachable (VM needs to boot + load modules + start agent)
        import socket
        agent_ready = False
        for attempt in range(60):
            time.sleep(0.5)
            if proc.poll() is not None:
                self._json(500, {"error": "VM exited during agent wait"})
                return
            try:
                test_sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
                test_sock.settimeout(2)
                test_sock.connect(vsock_path)
                test_sock.sendall(b"CONNECT 52\n")
                resp = test_sock.recv(1024)
                test_sock.close()
                if resp.startswith(b"OK"):
                    agent_ready = True
                    print(f"[fc-daemon] Agent ready after {attempt+1} attempts ({(attempt+1)*0.5:.1f}s)")
                    break
            except Exception:
                pass

        if not agent_ready:
            self._json(500, {"error": "Agent not reachable after 30s of polling"})
            proc.kill()
            return

        # Pause the VM
        r = subprocess.run(
            ["curl", "-s", "--unix-socket", api_sock,
             "-X", "PATCH", "http://localhost/vm",
             "-H", "Content-Type: application/json",
             "-d", '{"state": "Paused"}'],
            capture_output=True
        )
        if r.returncode != 0:
            self._json(500, {"error": "Pause failed"})
            proc.kill()
            return

        # Create snapshot
        snap_state = str(Path(SNAPSHOT_DIR) / "snap.bin")
        snap_mem = str(Path(SNAPSHOT_DIR) / "mem.bin")
        os.makedirs(SNAPSHOT_DIR, exist_ok=True)

        r = subprocess.run(
            ["curl", "-s", "--unix-socket", api_sock,
             "-X", "PUT", "http://localhost/snapshot/create",
             "-H", "Content-Type: application/json",
             "-d", json.dumps({"snapshot_type": "Full",
                               "snapshot_path": snap_state,
                               "mem_file_path": snap_mem})],
            capture_output=True
        )
        if r.returncode != 0:
            self._json(500, {"error": "Snapshot create failed"})
            proc.kill()
            return

        # Kill the warm-up VM (snapshot is saved)
        proc.kill()
        log_file.close()
        subprocess.run(["ip", "link", "delete", tap_name], capture_output=True)

        snapshot["ready"] = True
        snapshot["state_path"] = snap_state
        snapshot["mem_path"] = snap_mem
        print(f"[fc-daemon] Snapshot created: state={snap_state}, mem={snap_mem}")

        self._json(200, {"status": "snapshot_ready", "state": snap_state, "mem": snap_mem})

    def _create_from_snapshot(self, mem_mb, vcpus):
        """Restore VM from snapshot. TAP must exist; no device config needed."""
        vm_id = uuid.uuid4().hex[:12]
        vm_dir = Path(VM_BASE) / vm_id
        vm_dir.mkdir(parents=True, exist_ok=True)

        ip_suffix = 2 + len(vms)
        vm_ip = f"172.16.0.{ip_suffix}"
        tap_name = f"fc{vm_id[:8]}"
        vsock_path = str(vm_dir / "v.sock")
        api_sock = str(vm_dir / "api.sock")

        try:
            subprocess.run(["ip", "tuntap", "add", tap_name, "mode", "tap"], check=True, capture_output=True)
            subprocess.run(["ip", "link", "set", tap_name, "up"], check=True, capture_output=True)
            subprocess.run(["ip", "link", "set", tap_name, "master", "br-fc"], check=True, capture_output=True)
        except subprocess.CalledProcessError:
            return None, None

        log_file = open(vm_dir / "serial.log", "w")

        proc = subprocess.Popen(
            [FIRECRACKER, "--api-sock", api_sock],
            stdout=log_file, stderr=subprocess.STDOUT,
        )

        for _ in range(30):
            if os.path.exists(api_sock): break
            if proc.poll() is not None:
                return None, None
            time.sleep(0.1)

        # Load snapshot directly — no device config allowed before snapshot load.
        # The snapshot contains vsock UDS path from warm-up. Must unlink before restore.
        warm_vsock = str(Path(SNAPSHOT_DIR) / "warm.sock")
        if os.path.exists(warm_vsock):
            os.unlink(warm_vsock)

        load_result = subprocess.run(
            ["curl", "-s", "-w", "\n%{http_code}", "--unix-socket", api_sock,
             "-X", "PUT", "http://localhost/snapshot/load",
             "-H", "Content-Type: application/json",
             "-d", json.dumps({
                 "snapshot_path": snapshot["state_path"],
                 "mem_backend": {"backend_path": snapshot["mem_path"], "backend_type": "File"},
                 "resume_vm": True,
             })],
            capture_output=True
        )
        load_output = load_result.stdout.decode()
        http_code = load_output.rsplit("\n", 1)[-1].strip()
        if http_code != "204" and http_code != "200":
            print(f"[fc-daemon] Snapshot load failed: HTTP {http_code} body={load_output[:300]}")
            proc.kill()
            return None, None

        # Wait for vsock socket at the warm-up path
        for _ in range(50):
            if os.path.exists(warm_vsock) or os.path.exists(vsock_path): break
            if proc.poll() is not None:
                return None, None
            time.sleep(0.1)

        vms[vm_id] = {"proc": proc, "dir": str(vm_dir), "log": log_file, "tap": tap_name}
        # Symlink warm.sock into VM dir so callers find it at the standard path
        try:
            os.symlink(warm_vsock, str(vm_dir / "v.sock"))
        except OSError:
            pass
        print(f"[fc-daemon] VM {vm_id} restored from snapshot (ip={vm_ip})")
        return vm_id, vm_ip

    def _create_cold(self, mem_mb, vcpus):
        """Cold boot a new VM (~2.5s)."""
        vm_id = uuid.uuid4().hex[:12]
        vm_dir = Path(VM_BASE) / vm_id
        vm_dir.mkdir(parents=True, exist_ok=True)

        cid = 3 + len(vms)
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

        config = {
            "boot-source": {
                "kernel_image_path": VMLINUX,
                "initrd_path": INITRAMFS,
                "boot_args": f"console=ttyS0 reboot=k panic=1 fcip={vm_ip}",
            },
            "drives": [],
            "machine-config": {"vcpu_count": vcpus, "mem_size_mib": mem_mb},
            "vsock": {"guest_cid": cid, "uds_path": vsock_path},
            "network-interfaces": [{"iface_id": "net0", "host_dev_name": tap_name}],
        }
        config_path = vm_dir / "config.json"
        config_path.write_text(json.dumps(config))

        log_file = open(vm_dir / "serial.log", "w")
        proc = subprocess.Popen(
            [FIRECRACKER, "--no-api", "--config-file", str(config_path)],
            stdout=log_file, stderr=subprocess.STDOUT,
        )

        for _ in range(50):
            if os.path.exists(vsock_path): break
            if proc.poll() is not None:
                return None, None
            time.sleep(0.1)

        vms[vm_id] = {"proc": proc, "dir": str(vm_dir), "cid": cid, "log": log_file, "tap": tap_name}
        print(f"[fc-daemon] VM {vm_id} cold-booted (cid={cid}, ip={vm_ip})")
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
    print("[fc-daemon] All VMs cleaned up")


if __name__ == "__main__":
    os.makedirs(VM_BASE, exist_ok=True)
    signal.signal(signal.SIGTERM, cleanup_all)
    signal.signal(signal.SIGINT, cleanup_all)
    server = HTTPServer(("127.0.0.1", 8081), Handler)
    print(f"[fc-daemon v2] listening on :8081 ({len(vms)} VMs, snapshot={'ready' if snapshot['ready'] else 'cold'})")
    server.serve_forever()
