#!/usr/bin/env python3
"""fc-loadtest-clean.py — All-in-one load test for SHC Starter VPS.
Run as root: sudo python3 fc-loadtest-clean.py
"""
import socket, time, json, subprocess, os, sys, signal, threading

DAEMON = "http://127.0.0.1:8081"
VSOCK_BASE = "/tmp/fc-vms"
LOG = "/tmp/fc-loadtest-clean.log"
RESULTS_FILE = "/tmp/loadtest-results.json"

def log(msg):
    ts = time.strftime('%H:%M:%S')
    print(f"[{ts}] {msg}", flush=True)
    with open(LOG, "a") as f:
        f.write(f"[{ts}] {msg}\n")

def run(cmd, check=True):
    r = subprocess.run(cmd, shell=True, capture_output=True, text=True)
    if check and r.returncode != 0:
        log(f"FAILED: {cmd}\n  stderr: {r.stderr[:200]}")
    return r

def host_mem_used():
    return int(subprocess.check_output("free -m | awk '/Mem:/{print $3}'", shell=True).strip())

def host_mem_avail():
    return int(subprocess.check_output("free -m | awk '/Mem:/{print $7}'", shell=True).strip())

# ============================================================
# SETUP
# ============================================================
os.chdir("/tmp")
os.environ["PATH"] = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/local/go/bin"

_host_cores = subprocess.check_output("nproc", shell=True).decode().strip()
_host_ram = subprocess.check_output("free -m | awk '/Mem:/{print $2}'", shell=True).decode().strip()
_host_kernel = subprocess.check_output("uname -r", shell=True).decode().strip()
log("=" * 60)
log(f"SHC STARTER VPS LOAD TEST — {time.strftime('%Y-%m-%dT%H:%M:%SZ')}")
log(f"Host: {_host_cores} cores, {_host_ram}MB RAM")
log(f"Kernel: {_host_kernel}")
log("=" * 60)

# Verify artifacts
for f in ["/tmp/tollgate-vm-agent", "/tmp/vmlinux", "/tmp/initramfs.cpio.gz", "/tmp/fc-daemon.py"]:
    if not os.path.exists(f):
        log(f"MISSING: {f}")
        sys.exit(1)
log("All artifacts present.")

# Kill old processes
log("Killing old processes...")
run("pkill -9 -f fc-daemon", check=False)
run("pkill -9 -f firecracker", check=False)
time.sleep(2)

# Clean taps
log("Cleaning taps...")
r = run("ip link show | grep -oP 'fc[a-f0-9]{8}' | sort -u", check=False)
for tap in r.stdout.strip().split("\n"):
    if tap:
        run(f"ip link delete {tap}", check=False)

# Clean vms dir
run("rm -rf /tmp/fc-vms && mkdir -p /tmp/fc-vms")

# NAT setup
log("Setting up NAT...")
run("echo 1 > /proc/sys/net/ipv4/ip_forward")
r = run("ip link show br-fc 2>/dev/null", check=False)
if r.returncode != 0:
    run("ip link add br-fc type bridge")
    run("ip addr add 172.16.0.1/24 dev br-fc")
    run("ip link set br-fc up")
run("iptables -t nat -C POSTROUTING -s 172.16.0.0/24 -o eth0 -j MASQUERADE 2>/dev/null || "
    "iptables -t nat -A POSTROUTING -s 172.16.0.0/24 -o eth0 -j MASQUERADE", check=False)
run("iptables -C FORWARD -i br-fc -o eth0 -j ACCEPT 2>/dev/null || "
    "iptables -A FORWARD -i br-fc -o eth0 -j ACCEPT", check=False)
run("iptables -C FORWARD -i eth0 -o br-fc -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || "
    "iptables -A FORWARD -i eth0 -o br-fc -m state --state RELATED,ESTABLISHED -j ACCEPT", check=False)

# Start daemon
log("Starting daemon...")
# Remove old log to avoid permission issues
run("rm -f /tmp/fc-daemon.log")
daemon_proc = subprocess.Popen(
    ["python3", "-u", "/tmp/fc-daemon.py"],
    stdout=open("/tmp/fc-daemon.log", "w"),
    stderr=subprocess.STDOUT
)
time.sleep(3)

# Health check
import urllib.request
try:
    resp = urllib.request.urlopen(f"{DAEMON}/health", timeout=5)
    health = json.loads(resp.read())
    log(f"Daemon healthy: {health}")
except Exception as e:
    log(f"Daemon FAILED: {e}")
    log(f"Daemon log:\n{open('/tmp/fc-daemon.log').read()[-500:]}")
    sys.exit(1)

# ============================================================
# LOAD TEST FUNCTIONS
# ============================================================
def create_vm(rootfs="initramfs", mem=256):
    """Create a VM and connect via vsock. Returns (vid, socket_or_None)."""
    try:
        data = json.dumps({"cpus": 1, "mem_mb": mem, "rootfs": rootfs}).encode()
        req = urllib.request.Request(f"{DAEMON}/vms", data=data,
                                     headers={"Content-Type": "application/json"})
        resp = urllib.request.urlopen(req, timeout=60)
        vm = json.loads(resp.read())
        vid = vm["id"]
        vp = f"{VSOCK_BASE}/{vid}/v.sock"
        for _ in range(300):
            try:
                s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
                s.settimeout(2)
                s.connect(vp)
                s.sendall(b"CONNECT 52\n")
                resp_data = s.recv(1024)
                if resp_data.startswith(b"OK"):
                    return vid, s
                s.close()
            except:
                pass
            time.sleep(0.1)
        return vid, None
    except Exception as e:
        log(f"  create_vm error: {e}")
        return None, None

def destroy(vid):
    if vid:
        try:
            req = urllib.request.Request(f"{DAEMON}/vms/{vid}", method="DELETE")
            urllib.request.urlopen(req, timeout=10)
        except:
            pass

baseline_mem = host_mem_used()
HOST_TOTAL = int(subprocess.check_output("free -m | awk '/Mem:/{print $2}'", shell=True).strip())
log(f"Baseline: {baseline_mem}MB used, {host_mem_avail()}MB available, {HOST_TOTAL}MB total")

results = {
    "date": time.strftime('%Y-%m-%dT%H:%M:%SZ'),
    "host_cores": int(subprocess.check_output("nproc", shell=True).strip()),
    "host_total_mb": HOST_TOTAL,
    "baseline_mem_mb": baseline_mem,
    "kernel": subprocess.check_output("uname -r", shell=True).decode().strip(),
}

# ============================================================
# 1. COLD BOOT BASELINE (3 runs)
# ============================================================
log("\n" + "=" * 60)
log("1. COLD BOOT BASELINE (3 runs)")
log("=" * 60)
times = []
for i in range(3):
    t0 = time.time(); vid, vs = create_vm(); t1 = time.time()
    if vs:
        times.append(t1 - t0)
        log(f"  Run {i+1}: {t1-t0:.3f}s")
        vs.close()
    else:
        log(f"  Run {i+1}: FAILED")
    destroy(vid)
    time.sleep(1)

if times:
    avg = sum(times) / len(times)
    log(f"  AVG: {avg:.3f}s")
    results["cold_boot_avg_s"] = round(avg, 3)
    results["cold_boot_times"] = [round(t, 3) for t in times]

# ============================================================
# 2. MAX CONCURRENT VMs (push until failure or 35)
# ============================================================
log("\n" + "=" * 60)
log("2. MAX CONCURRENT VMs (push until failure or 35)")
log("=" * 60)
all_vids = []
all_socks = []
mem_readings = []

for i in range(35):
    vid, vs = create_vm()
    if vs:
        all_vids.append(vid)
        all_socks.append(vs)
        mem = host_mem_used()
        avail = host_mem_avail()
        overhead = mem - baseline_mem
        mem_readings.append({"vms": i+1, "used_mb": mem, "avail_mb": avail, "overhead_mb": overhead})
        log(f"  VM {i+1:2d}: OK  mem={mem:4d}MB  avail={avail:4d}MB  overhead={overhead:4d}MB")
    else:
        log(f"  VM {i+1:2d}: FAILED — stopping at {len(all_vids)} concurrent")
        if vid:
            destroy(vid)  # clean up leaked VM
        break
    time.sleep(0.3)

max_concurrent = len(all_vids)
results["max_concurrent"] = max_concurrent
results["mem_readings"] = mem_readings
if mem_readings:
    avg_overhead = mem_readings[-1]["overhead_mb"] / max_concurrent if max_concurrent else 0
    results["avg_overhead_per_vm_mb"] = round(avg_overhead, 1)
    log(f"  Max concurrent: {max_concurrent}")
    log(f"  Total overhead: {mem_readings[-1]['overhead_mb']}MB")
    log(f"  Avg per VM: {avg_overhead:.1f}MB")

for s in all_socks:
    try: s.close()
    except: pass
for v in all_vids:
    destroy(v)
time.sleep(3)

# ============================================================
# 3. MEMORY SCALING CURVE
# ============================================================
log("\n" + "=" * 60)
log("3. MEMORY SCALING CURVE")
log("=" * 60)
scaling = []
for n in [1, 2, 3, 5, 8, 10, 12, 15, 18, 20]:
    if n > max_concurrent:
        log(f"  n={n:2d}: skipped (exceeds max {max_concurrent})")
        continue
    vids = []; socks = []
    for i in range(n):
        vid, vs = create_vm()
        if vs:
            vids.append(vid); socks.append(vs)
        else:
            log(f"  n={n:2d}: VM {i+1} FAILED")
            if vid: destroy(vid)
            break
    if len(vids) == n:
        time.sleep(1)
        mem = host_mem_used()
        overhead = mem - baseline_mem
        per_vm = overhead / n if n else 0
        scaling.append({"vms": n, "used_mb": mem, "overhead_mb": overhead, "per_vm_mb": round(per_vm, 1)})
        log(f"  n={n:2d}: {mem:4d}MB used, {overhead:4d}MB overhead, {per_vm:.1f}MB/VM")
    for s in socks:
        try: s.close()
        except: pass
    for v in vids:
        destroy(v)
    time.sleep(2)

results["scaling_curve"] = scaling

# ============================================================
# 4. VSOCK RTT (20 rounds)
# ============================================================
log("\n" + "=" * 60)
log("4. VSOCK RTT (20 rounds)")
log("=" * 60)
vid, vs = create_vm()
if vs:
    time.sleep(1); lats = []
    for i in range(20):
        t0 = time.perf_counter()
        vs.sendall(b"echo x\n")
        try:
            data = vs.recv(4096)
            t1 = time.perf_counter()
            if b"x" in data:
                lats.append((t1 - t0) * 1000)
        except:
            pass
    if lats:
        ls = sorted(lats)
        log(f"  Rounds: {len(lats)}")
        log(f"  MIN: {min(lats):.3f}ms  P50: {ls[len(ls)//2]:.3f}ms  MAX: {max(lats):.3f}ms")
        results["vsock_rtt_ms"] = {"min": round(min(lats),3), "p50": round(ls[len(ls)//2],3), "max": round(max(lats),3)}
    vs.close()
destroy(vid)

# ============================================================
# 5. VM LIFECYCLE STRESS (20 cycles)
# ============================================================
log("\n" + "=" * 60)
log("5. VM LIFECYCLE (20 cycles)")
log("=" * 60)
ok = 0
for i in range(20):
    vid, vs = create_vm()
    if vs:
        ok += 1; vs.close()
    destroy(vid)
    if i % 5 == 4:
        log(f"  {i+1}/20: {ok} OK")
log(f"  Total: {ok}/20")
results["lifecycle_20"] = ok

# ============================================================
# 6. PARALLEL BOOT (5 simultaneous)
# ============================================================
log("\n" + "=" * 60)
log("6. PARALLEL BOOT (5 simultaneous)")
log("=" * 60)
import concurrent.futures
t0 = time.time()
with concurrent.futures.ThreadPoolExecutor(5) as pool:
    futs = [pool.submit(create_vm) for _ in range(5)]
    results_parallel = [f.result() for f in futs]
total = time.time() - t0
ok = sum(1 for _, v in results_parallel if v)
log(f"  Success: {ok}/5 in {total:.3f}s ({total/max(ok,1):.3f}s/VM)")
results["parallel_boot_5"] = {"success": ok, "total_s": round(total, 3)}
for vid, vs in results_parallel:
    if vs: vs.close()
    destroy(vid)

# ============================================================
# SUMMARY
# ============================================================
log("\n" + "=" * 60)
log("SUMMARY")
log("=" * 60)
log(json.dumps(results, indent=2))

with open(RESULTS_FILE, "w") as f:
    json.dump(results, f, indent=2)
log(f"Results saved to {RESULTS_FILE}")

# Cleanup
try:
    resp = urllib.request.urlopen(f"{DAEMON}/vms", timeout=5)
    vms = json.loads(resp.read())
    for v in vms:
        destroy(v["id"])
except:
    pass

# Kill daemon
daemon_proc.terminate()
log("DONE")
