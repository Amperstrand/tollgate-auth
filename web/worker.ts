/**
 * VPS-on-Demand Cloudflare Worker.
 *
 * Lightning-paywalled proxy to SHC. Users pick a VPS spec, pay via
 * Lightning through SHC's BTCPay, and get SSH credentials for a
 * freshly-provisioned VM with a 10% markup retained as operator margin.
 */

import { SHCClient } from "./shc";
import indexHtml from "./compute.html";
import llmsTxt from "./public/llms.txt";

// ── Config ─────────────────────────────────────────────────

const PACKAGES = {
  standard:     { package_id: 81, pricing_id: 245, daily_rate: 0.49, name: "Standard",     cpu: 2, ram_gb: 8,  disk_gb: 16, size: "dev-2c-8gb" },
  professional: { package_id: 82, pricing_id: 249, daily_rate: 0.96, name: "Professional", cpu: 4, ram_gb: 16, disk_gb: 32, size: "dev-4c-16gb" },
  business:     { package_id: 83, pricing_id: 253, daily_rate: 1.91, name: "Business",     cpu: 8, ram_mb: 32, disk_gb: 64, size: "dev-8c-32gb" },
};
type PackageTier = keyof typeof PACKAGES;

const MARKUP = 1.10;

/** VM lifetime in milliseconds (24h). */
const VM_LIFETIME_MS = 24 * 60 * 60 * 1000;

// ── Rate limiting ──────────────────────────────────────────

interface RateLimitConfig {
  max: number;
  windowSec: number;
  label: string;
}

const RATE_LIMITS: Record<string, RateLimitConfig> = {
  "vm-create": { max: 10, windowSec: 3600, label: "VM creations per hour" },
  "faucet":    { max: 100, windowSec: 86400, label: "faucet mints per day" },
  "order":     { max: 5, windowSec: 3600, label: "orders per hour" },
};

/**
 * KV-based rate limiter. Uses a fixed-window counter per IP+action.
 * KV eventual consistency means brief over-counting is possible under
 * extreme concurrency — acceptable for abuse prevention, not billing.
 */
async function checkRateLimit(
  env: Env,
  action: keyof typeof RATE_LIMITS,
  request: Request,
): Promise<Response | null> {
  const cfg = RATE_LIMITS[action];
  if (!cfg) return null;

  const ip = request.headers.get("CF-Connecting-IP") || "unknown";
  const now = Math.floor(Date.now() / 1000);
  const windowStart = Math.floor(now / cfg.windowSec) * cfg.windowSec;
  const key = `rl:${action}:${ip}:${windowStart}`;

  const raw = await env.VPS_ORDERS.get(key);
  const count = raw ? parseInt(raw, 10) : 0;

  if (count >= cfg.max) {
    const retryAfter = windowStart + cfg.windowSec - now;
    return jsonResponse(
      {
        status: "error",
        message: `Rate limit exceeded: ${cfg.label}`,
        retry_after: retryAfter,
        limit: cfg.max,
        window_seconds: cfg.windowSec,
      },
      429,
    );
  }

  await env.VPS_ORDERS.put(key, String(count + 1), {
    expirationTtl: cfg.windowSec + 60,
  });

  return null;
}

// ── Types ──────────────────────────────────────────────────

interface Env {
  SHC_API_KEY: string;
  VPS_ORDERS: KVNamespace;
  FC_HOST_IP?: string;
  ENCRYPTION_KEY?: string;
  WEBSSH_PROXY_URL?: string;
  VPS_PROXY_URL?: string;
  FC_HOSTS?: string;  // JSON array of host URLs: ["http://192.168.13.208:8081","http://66.92.204.237:8081"]
  FC_API_KEY?: string; // Bearer token for fc-daemon auth
}

type VMStatus =
  | "awaiting_payment"
  | "ordering"
  | "provisioning"
  | "configuring_ssh"
  | "ready"
  | "error";

interface OrderState {
  checkout_url: string;
  bolt11: string;
  credit_before: number;
  service_id: number | null;
  hostname: string | null;
  ip: string | null;
  os_user: string | null;
  password: string | null;
  credentials_checked_at: number | null;
  tier: PackageTier;
  ssh_key: string | null;
  vm_status: VMStatus;
  error_message: string | null;
  created_at: number;
}

// ── Helpers ────────────────────────────────────────────────

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function htmlResponse(content: string): Response {
  return new Response(content, {
    headers: { "Content-Type": "text/html;charset=UTF-8" },
  });
}

function hexToBytes(hex: string): Uint8Array {
  const bytes = new Uint8Array(hex.length / 2);
  for (let i = 0; i < hex.length; i += 2) {
    bytes[i / 2] = parseInt(hex.slice(i, i + 2), 16);
  }
  return bytes;
}

const ENC_PREFIX = "enc:";

async function encryptField(plaintext: string, keyHex: string): Promise<string> {
  const key = await crypto.subtle.importKey(
    "raw", hexToBytes(keyHex), { name: "AES-GCM" }, false, ["encrypt"],
  );
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const encoded = new TextEncoder().encode(plaintext);
  const ciphertext = new Uint8Array(
    await crypto.subtle.encrypt({ name: "AES-GCM", iv }, key, encoded),
  );
  const combined = new Uint8Array(iv.length + ciphertext.length);
  combined.set(iv, 0);
  combined.set(ciphertext, iv.length);
  return ENC_PREFIX + btoa(String.fromCharCode(...combined));
}

async function decryptField(stored: string, keyHex: string): Promise<string> {
  if (!stored.startsWith(ENC_PREFIX)) return stored;
  const combined = Uint8Array.from(
    atob(stored.slice(ENC_PREFIX.length)), c => c.charCodeAt(0),
  );
  const iv = combined.slice(0, 12);
  const ciphertext = combined.slice(12);
  const key = await crypto.subtle.importKey(
    "raw", hexToBytes(keyHex), { name: "AES-GCM" }, false, ["decrypt"],
  );
  const decrypted = await crypto.subtle.decrypt(
    { name: "AES-GCM", iv }, key, ciphertext,
  );
  return new TextDecoder().decode(decrypted);
}

async function getOrderState(env: Env, orderId: string): Promise<OrderState | null> {
  const raw = await env.VPS_ORDERS.get(orderId);
  if (!raw) return null;
  const state = JSON.parse(raw) as OrderState;
  if (state.password && env.ENCRYPTION_KEY) {
    try {
      state.password = await decryptField(state.password, env.ENCRYPTION_KEY);
    } catch {
      // Decryption failed — treat as no password available.
      state.password = null;
    }
  }
  return state;
}

const ORDER_TTL = 48 * 60 * 60;

async function saveOrderState(env: Env, orderId: string, state: OrderState): Promise<void> {
  const toStore: OrderState = { ...state };
  if (toStore.password && env.ENCRYPTION_KEY) {
    toStore.password = await encryptField(toStore.password, env.ENCRYPTION_KEY);
  }
  await env.VPS_ORDERS.put(orderId, JSON.stringify(toStore), { expirationTtl: ORDER_TTL });
}

function getHostname(orderId: string): string {
  return `vps-${orderId.slice(0, 8)}`;
}

const CREDENTIALS_FETCH_COOLDOWN_MS = 10_000;

async function fetchCredentialsIfMissing(
  env: Env,
  orderId: string,
  state: OrderState,
): Promise<void> {
  if (state.password || !state.service_id) return;
  const now = Date.now();
  if (state.credentials_checked_at && now - state.credentials_checked_at < CREDENTIALS_FETCH_COOLDOWN_MS) return;
  state.credentials_checked_at = now;
  try {
    const shc = new SHCClient(env.SHC_API_KEY);
    const creds = await shc.getVMCredentials(state.service_id);
    if (creds?.password) {
      state.password = creds.password;
      if (creds.user) state.os_user = creds.user;
    }
  } catch {
    // Credentials endpoint may not be available for all VM templates.
  }
  await saveOrderState(env, orderId, state);
}

function readyResponse(state: OrderState, expiresAt: number) {
  const user = state.os_user ?? "root";
  const ip = state.ip ?? "";
  const password = state.password ?? "";
  const sshCmd = `ssh ${user}@${ip}`;
  const escapedPw = password.replace(/'/g, `'\\''`);
  const sshpassCmd = password
    ? `sshpass -p '${escapedPw}' ssh -o StrictHostKeyChecking=no ${user}@${ip}`
    : null;
  return jsonResponse({
    status: "ready",
    ssh: sshCmd,
    sshpass: sshpassCmd,
    password: password || null,
    username: user,
    hostname: state.hostname,
    ip,
    created_at: state.created_at,
    expires_at: expiresAt,
  });
}

// ── Order creation: POST /api/order ────────────────────────

async function handleCreateOrder(env: Env, request: Request): Promise<Response> {
  let tier: PackageTier = "professional";
  let sshKey: string | null = null;

  try {
    const body = await request.json() as { tier?: string; ssh_key?: string };
    if (body.tier && body.tier in PACKAGES) {
      tier = body.tier as PackageTier;
    }
    if (body.ssh_key && typeof body.ssh_key === "string" && body.ssh_key.trim()) {
      const key = body.ssh_key.trim();
      if (!/^ssh-(ed25519|rsa|ecdsa-[a-z0-9]+) AAAA[A-Za-z0-9+/]+=*/.test(key)) {
        return jsonResponse(
          { status: "error", message: "Invalid SSH key format. Expected: ssh-ed25519 AAAA... or ssh-rsa AAAA..." },
          400,
        );
      }
      sshKey = key;
    }
  } catch {
    // No body or invalid JSON — use defaults
  }

  const pkg = PACKAGES[tier];
  const chargeAmount = (pkg.daily_rate * MARKUP).toFixed(2);

  const orderId = crypto.randomUUID();
  const now = Date.now();

  const shc = new SHCClient(env.SHC_API_KEY);

  try {
    const creditBefore = await shc.getBalance();

    const creditIdemKey = `credit-${orderId}`;
    const credit = await shc.addCredit(chargeAmount, "USD", creditIdemKey);
    const checkoutUrl = credit.checkout_url;
    const rawPayment = credit.payment_link ?? credit.bolt11 ?? "";
    const qrContent = rawPayment.replace(/^lightning:/i, "");

    const state: OrderState = {
      checkout_url: checkoutUrl,
      bolt11: qrContent,
      credit_before: creditBefore,
      service_id: null,
      hostname: null,
      ip: null,
      os_user: null,
      password: null,
      credentials_checked_at: null,
      tier,
      ssh_key: sshKey,
      vm_status: "awaiting_payment",
      error_message: null,
      created_at: now,
    };
    await saveOrderState(env, orderId, state);

    return jsonResponse({
      order_id: orderId,
      bolt11: qrContent,
      checkout_url: checkoutUrl,
      tier,
      created_at: now,
      expires_at: now + VM_LIFETIME_MS,
    });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    console.error("handleCreateOrder failed:", message, err);
    return jsonResponse(
      { status: "error", message, tier },
      500,
    );
  }
}

// ── Status polling: GET /api/status/:order_id ──────────────

async function handleStatus(env: Env, orderId: string): Promise<Response> {
  const state = await getOrderState(env, orderId);
  if (!state) {
    return jsonResponse({ status: "error", message: "Order not found" }, 404);
  }

  const shc = new SHCClient(env.SHC_API_KEY);
  const expiresAt = state.created_at + VM_LIFETIME_MS;

  try {
    switch (state.vm_status) {
      // ── Phase 1: Waiting for Lightning payment ────────────
      case "awaiting_payment": {
        const balance = await shc.getBalance();

        if (balance > state.credit_before) {
          state.vm_status = "ordering";
          await saveOrderState(env, orderId, state);

          const idemKey = `order-${orderId}`;
          const pkg = PACKAGES[state.tier];
          const order = await shc.submitOrder(
            idemKey,
            getHostname(orderId),
            pkg.package_id,
            pkg.pricing_id,
          );
          state.service_id = order.service_ids[0] ?? null;

          if (!state.service_id) {
            throw new Error("No service_id returned from order submission");
          }

          state.vm_status = "provisioning";
          await saveOrderState(env, orderId, state);

          return jsonResponse({
            status: "provisioning",
            service_id: state.service_id,
            created_at: state.created_at,
            expires_at: expiresAt,
          });
        }

        return jsonResponse({
          status: "awaiting_payment",
          created_at: state.created_at,
          expires_at: expiresAt,
        });
      }

      // ── Phase 2: Ordering (mid-transition retry) ──────────
      case "ordering": {
        if (!state.service_id) {
          // Retry the ordering steps (idempotency key prevents duplicates)
          const idemKey = `order-${orderId}`;
          const pkg = PACKAGES[state.tier];
          const order = await shc.submitOrder(
            idemKey,
            getHostname(orderId),
            pkg.package_id,
            pkg.pricing_id,
          );
          state.service_id = order.service_ids[0] ?? null;

          if (!state.service_id) {
            throw new Error("No service_id returned from order submission");
          }

          await shc.cancelVMEndOfTerm(state.service_id);
        }

        state.vm_status = "provisioning";
        await saveOrderState(env, orderId, state);

        return jsonResponse({
          status: "provisioning",
          service_id: state.service_id,
          created_at: state.created_at,
          expires_at: expiresAt,
        });
      }

      // ── Phase 3: Provisioning ─────────────────────────────
      case "provisioning": {
        if (!state.service_id) {
          throw new Error("No service_id in provisioning state");
        }

        const vm = await shc.getVM(state.service_id);
        const provState = vm.provisioning_state ?? "unknown";
        const svcStatus = vm.service_status ?? "unknown";
        const ips = vm.ips ?? [];
        const isReady =
          provState === "ready" ||
          (svcStatus === "active" && ips.length > 0);

        if (isReady) {
          state.hostname = vm.hostname ?? null;
          state.ip = ips[0]?.ip ?? null;
          state.os_user = vm.os_user ?? "debian";

          if (state.ssh_key) {
            state.vm_status = "configuring_ssh";
            await saveOrderState(env, orderId, state);
            return jsonResponse({
              status: "configuring_ssh",
              service_id: state.service_id,
              created_at: state.created_at,
              expires_at: expiresAt,
            });
          }

          state.vm_status = "ready";
          await fetchCredentialsIfMissing(env, orderId, state);
          await saveOrderState(env, orderId, state);
          return readyResponse(state, expiresAt);
        }

        if (provState === "failed" || provState === "error") {
          throw new Error(`VM provisioning failed: ${provState}`);
        }

        return jsonResponse({
          status: "provisioning",
          service_id: state.service_id,
          created_at: state.created_at,
          expires_at: expiresAt,
        });
      }

      // ── Phase 3b: Configuring SSH (inject key into running VM) ──
      case "configuring_ssh": {
        if (!state.service_id || !state.ssh_key) {
          state.vm_status = "ready";
          await fetchCredentialsIfMissing(env, orderId, state);
          await saveOrderState(env, orderId, state);
          return readyResponse(state, expiresAt);
        }

        try {
          await shc.applySSHKeyLive(state.service_id, state.ssh_key);
        } catch {
          // Key injection failed — user can still SSH as debian user
        }
        state.vm_status = "ready";
        await fetchCredentialsIfMissing(env, orderId, state);
        await saveOrderState(env, orderId, state);
        return readyResponse(state, expiresAt);
      }

      // ── Phase 4: Ready (terminal) ─────────────────────────
      case "ready": {
        if (!state.password && state.service_id) {
          await fetchCredentialsIfMissing(env, orderId, state);
        }
        return readyResponse(state, expiresAt);
      }

      // ── Phase 5: Error (terminal) ─────────────────────────
      case "error": {
        return jsonResponse({
          status: "error",
          message: state.error_message ?? "Unknown error",
          created_at: state.created_at,
          expires_at: expiresAt,
        });
      }
    }
  } catch (err) {
    const message = err instanceof Error ? err.message : "Unexpected error";
    state.vm_status = "error";
    state.error_message = message;
    await saveOrderState(env, orderId, state);

    return jsonResponse({
      status: "error",
      message,
      created_at: state.created_at,
      expires_at: expiresAt,
    });
  }
}

// ── Host status aggregation ──────────────────────────────

interface HostStatus {
  name: string;
  url: string;
  online: boolean;
  vms?: number;
  rootfs?: string[];
  latency_ms?: number;
  error?: string;
}

async function handleHostStatus(env: Env): Promise<Response> {
  const cached = await env.VPS_ORDERS.get("fc:host:status");
  if (cached) {
    try {
      const data = JSON.parse(cached) as { hosts: HostStatus[]; updated_at: number };
      const ageSec = Math.floor((Date.now() - data.updated_at) / 1000);
      const hosts = data.hosts.map(h => ({
        ...h,
        online: ageSec < 120 ? h.online : false,
        error: ageSec >= 120 ? `Stale (${ageSec}s)` : undefined,
      }));
      return jsonResponse({ total: hosts.length, online: hosts.filter(h => h.online).length, offline: hosts.filter(h => !h.online).length, hosts, updated_at: data.updated_at, age_seconds: ageSec });
    } catch { /* fall through */ }
  }
  return jsonResponse({ total: 0, online: 0, offline: 0, hosts: [], error: "No heartbeat received yet" });
}

// ── Micro-VM proxy: forwards to firecracker-daemon on host ──

function terminalHtml(params: URLSearchParams, env: Env): string {
  const host = params.get("host") || "";
  const port = params.get("port") || "22";
  const user = params.get("user") || "root";
  const password = params.get("password") || "";
  const proxyUrl = env.WEBSSH_PROXY_URL || "";
  return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Terminal — ${host}</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.css">
<style>
  body { margin:0; background:#0a0b10; color:#e4e7f0; font-family:monospace; }
  #terminal { padding:8px; height:100vh; box-sizing:border-box; }
  #status { position:fixed; top:8px; right:12px; font-size:0.8rem; color:#8b90a5; }
</style>
</head>
<body>
<div id="status">Connecting...</div>
<div id="terminal"></div>
<script src="https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.min.js"></script>
<script src="https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.min.js"></script>
<script>
(function() {
  var host = ${JSON.stringify(host)};
  var port = ${JSON.stringify(port)};
  var user = ${JSON.stringify(user)};
  var password = ${JSON.stringify(password)};
  var proxyUrl = ${JSON.stringify(proxyUrl)};
  var statusEl = document.getElementById('status');

  var term = new Terminal({ cursorBlink:true, fontSize:13, fontFamily:'Menlo,Monaco,monospace' });
  var fitAddon = new FitAddon.FitAddon();
  term.loadAddon(fitAddon);
  term.open(document.getElementById('terminal'));
  fitAddon.fit();
  term.writeln('Connecting to ' + user + '@' + host + ':' + port + '...');

  if (!proxyUrl) {
    term.writeln('\\r\\n[ERROR] WebSSH proxy not configured.');
    term.writeln('Set WEBSSH_PROXY_URL on the Worker to enable the terminal.');
    statusEl.textContent = 'No proxy';
    return;
  }

  var wsUrl = proxyUrl + '?host=' + encodeURIComponent(host) + '&port=' + encodeURIComponent(port)
            + '&user=' + encodeURIComponent(user) + '&password=' + encodeURIComponent(password);
  var ws = new WebSocket(wsUrl);

  ws.onopen = function() {
    statusEl.textContent = 'Connected';
    term.writeln('\\r\\n[CONNECTED]\\r\\n');
    term.focus();
  };

  ws.onmessage = function(e) {
    if (typeof e.data === 'string') {
      term.write(e.data);
    } else {
      e.data.arrayBuffer().then(function(buf) {
        term.write(new Uint8Array(buf));
      });
    }
  };

  ws.onclose = function() {
    statusEl.textContent = 'Disconnected';
    term.writeln('\\r\\n[DISCONNECTED]');
  };

  ws.onerror = function() {
    statusEl.textContent = 'Error';
    term.writeln('\\r\\n[CONNECTION ERROR]');
  };

  term.onData(function(data) {
    if (ws.readyState === WebSocket.OPEN) {
      ws.send(data);
    }
  });

  window.addEventListener('resize', function() { fitAddon.fit(); });
})();
</script>
</body>
</html>`;
}

async function proxyToDaemon(env: Env, method: string, path: string, body?: unknown): Promise<Response> {
  const hostIp = env.FC_HOST_IP;
  if (!hostIp) {
    return jsonResponse({ status: "error", message: "FC_HOST_IP not configured. Set it to the host VM's IP." }, 500);
  }
  try {
    const daemonUrl = hostIp.includes("://") ? `${hostIp}${path}` : `http://${hostIp}:8080${path}`;
    const resp = await fetch(daemonUrl, {
      method,
      headers: body !== undefined ? { "Content-Type": "application/json" } : {},
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    const text = await resp.text();
    return new Response(text, {
      status: resp.status,
      headers: { "Content-Type": "application/json" },
    });
  } catch (err) {
    const msg = err instanceof Error ? err.message : "Connection failed";
    return jsonResponse({ status: "error", message: `Cannot reach firecracker-daemon at ${hostIp}:8080: ${msg}` }, 502);
  }
}

async function handleCreateMicroVM(env: Env, request: Request): Promise<Response> {
  let body: { cpus?: number; mem_mb?: number; disk_mb?: number; ssh_key?: string };
  try {
    body = await request.json();
  } catch {
    body = {};
  }
  return proxyToDaemon(env, "POST", "/vms", body);
}

async function handleListMicroVMs(env: Env): Promise<Response> {
  return proxyToDaemon(env, "GET", "/vms");
}

async function handleDeleteMicroVM(env: Env, vmId: string): Promise<Response> {
  return proxyToDaemon(env, "DELETE", `/vms/${vmId}`);
}

// ── Router ─────────────────────────────────────────────────

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    const path = url.pathname;
    const method = request.method;

    // GET / → serve the HTML page
    if (method === "GET" && (path === "/" || path === "/index.html")) {
      return htmlResponse(indexHtml);
    }

    // GET /terminal → web SSH terminal (xterm.js)
    if (method === "GET" && path === "/terminal") {
      return htmlResponse(terminalHtml(url.searchParams, env));
    }

    // GET /install-reseller.sh → served from KV
    if (method === "GET" && path === "/install-reseller.sh") {
      const script = await env.VPS_ORDERS.get("__install_reseller_sh");
      if (script) {
        return new Response(script, {
          headers: {
            "Content-Type": "text/plain; charset=utf-8",
            "Cache-Control": "public, max-age=3600",
          },
        });
      }
      return jsonResponse({ error: "Install script not configured" }, 404);
    }

    // GET /api/health → liveness probe
    if (method === "GET" && path === "/api/health") {
      return jsonResponse({ status: "ok", time: Date.now() });
    }

    // GET /api/hosts → KVM host status (from KV heartbeat)
    if (method === "GET" && path === "/api/hosts") {
      return handleHostStatus(env);
    }

    // POST /api/heartbeat → VPS pushes health status (no outbound fetch needed)
    if (method === "POST" && path === "/api/heartbeat") {
      if (env.FC_API_KEY && request.headers.get("Authorization") !== `Bearer ${env.FC_API_KEY}`) {
        return jsonResponse({ error: "Unauthorized" }, 401);
      }
      try {
        const body = await request.json() as { ip?: string; vms?: number; rootfs?: string[]; kvm?: boolean; mem_avail_mb?: number };
        const hostStatus: HostStatus = {
          name: body.ip || "unknown",
          url: `http://${body.ip || "unknown"}:8081`,
          online: true,
          vms: body.vms ?? 0,
          rootfs: body.rootfs ?? [],
          latency_ms: 0,
        };
        const extra = { kvm: body.kvm, mem_avail_mb: body.mem_avail_mb };
        await env.VPS_ORDERS.put("fc:host:status", JSON.stringify({ hosts: [hostStatus], updated_at: Date.now(), ...extra }));
        return jsonResponse({ status: "ok", stored: true });
      } catch (e) {
        return jsonResponse({ error: "Invalid body" }, 400);
      }
    }

    // GET /llms.txt → LLM instructions for automated testing
    if (method === "GET" && path === "/llms.txt") {
      return new Response(llmsTxt, {
        headers: { "Content-Type": "text/plain; charset=utf-8" },
      });
    }

    // POST /api/order → create a new VPS order
    if (method === "POST" && path === "/api/order") {
      if (!env.SHC_API_KEY) {
        return jsonResponse(
          { status: "error", message: "SHC_API_KEY not configured" },
          500,
        );
      }
      const limited = await checkRateLimit(env, "order", request);
      if (limited) return limited;
      return handleCreateOrder(env, request);
    }

    // GET /api/status/:order_id → poll order status
    const statusMatch = path.match(/^\/api\/status\/([a-f0-9-]{36})$/);
    if (method === "GET" && statusMatch) {
      if (!env.SHC_API_KEY) {
        return jsonResponse(
          { status: "error", message: "SHC_API_KEY not configured" },
          500,
        );
      }
      return handleStatus(env, statusMatch[1]);
    }

    // POST /api/microvm → create a micro-VM on a host
    if (method === "POST" && path === "/api/microvm") {
      if (!env.SHC_API_KEY) {
        return jsonResponse({ status: "error", message: "SHC_API_KEY not configured" }, 500);
      }
      return handleCreateMicroVM(env, request);
    }

    // GET /api/microvm → list micro-VMs on host
    if (method === "GET" && path === "/api/microvm") {
      return handleListMicroVMs(env);
    }

    // DELETE /api/microvm/:id → destroy micro-VM
    const microvmMatch = path.match(/^\/api\/microvm\/([a-f0-9]{12})$/);
    if (method === "DELETE" && microvmMatch) {
      return handleDeleteMicroVM(env, microvmMatch[1]);
    }

    // GET /api/mint → faucet proxy (mint free testnut tokens for testing)
    if (method === "GET" && path === "/api/mint") {
      const limited = await checkRateLimit(env, "faucet", request);
      if (limited) return limited;
      const amount = url.searchParams.get("amount") || "1";
      const proxyUrl = env.VPS_PROXY_URL || "https://nodns.shop";
      try {
        const resp = await fetch(`${proxyUrl}/api/mint?amount=${amount}`);
        const text = await resp.text();
        return new Response(text, {
          status: resp.status,
          headers: { "Content-Type": "application/json", "Access-Control-Allow-Origin": "*" },
        });
      } catch (err) {
        return jsonResponse({ error: `Faucet unreachable: ${err instanceof Error ? err.message : "unknown"}` }, 502);
      }
    }

    // POST /api/payment/request → create payment order via vps-proxy
    if (method === "POST" && path === "/api/payment/request") {
      const proxyUrl = env.VPS_PROXY_URL || "https://nodns.shop";
      try {
        const resp = await fetch(`${proxyUrl}/api/payment/request`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: '{"ttl_seconds":3600}',
        });
        const text = await resp.text();
        return new Response(text, {
          status: resp.status,
          headers: { "Content-Type": "application/json", "Access-Control-Allow-Origin": "*" },
        });
      } catch (err) {
        return jsonResponse({ error: `Cannot reach vps-proxy: ${err instanceof Error ? err.message : "unknown"}` }, 502);
      }
    }

    // GET /api/payment/status/:ref → poll payment order status
    const paymentStatusMatch = path.match(/^\/api\/payment\/status\/(.+)$/);
    if (method === "GET" && paymentStatusMatch) {
      const proxyUrl = env.VPS_PROXY_URL || "https://nodns.shop";
      try {
        const resp = await fetch(`${proxyUrl}/api/payment/status/${paymentStatusMatch[1]}`);
        const text = await resp.text();
        return new Response(text, {
          status: resp.status,
          headers: { "Content-Type": "application/json", "Access-Control-Allow-Origin": "*" },
        });
      } catch (err) {
        return jsonResponse({ error: `Cannot reach vps-proxy: ${err instanceof Error ? err.message : "unknown"}` }, 502);
      }
    }

    // POST /api/firecracker/create → returns daemon URL for browser-direct call
    if (method === "POST" && path === "/api/firecracker/create") {
      const limited = await checkRateLimit(env, "vm-create", request);
      if (limited) return limited;
      const cached = await env.VPS_ORDERS.get("fc:host:status");
      if (!cached) {
        return jsonResponse({ error: "No Firecracker hosts online" }, 503);
      }
      try {
        const data = JSON.parse(cached);
        const host = data.hosts[0];
        if (!host || !host.online) {
          return jsonResponse({ error: "No hosts online" }, 503);
        }
        return jsonResponse({
          direct: true,
          daemon_url: host.url,
          api_key: env.FC_API_KEY || "",
          create_path: "/vms",
        });
      } catch {
        return jsonResponse({ error: "Failed to read host status" }, 500);
      }
    }

    // DELETE /api/firecracker/:id → destroy VM via fc-daemon (FC_HOSTS)
    const fcDestroyMatch = path.match(/^\/api\/firecracker\/([a-f0-9]{12})$/);
    if (method === "DELETE" && fcDestroyMatch) {
      let fcHosts: string[] = [];
      if (env.FC_HOSTS) { try { fcHosts = JSON.parse(env.FC_HOSTS); } catch { fcHosts = []; } }
      if (fcHosts.length === 0) {
        return jsonResponse({ error: "No Firecracker hosts configured" }, 503);
      }
      const fcHost = fcHosts[0];
      const authHeaders: Record<string, string> = {};
      if (env.FC_API_KEY) { authHeaders["Authorization"] = `Bearer ${env.FC_API_KEY}`; }
      try {
        const resp = await fetch(`${fcHost}/vms/${fcDestroyMatch[1]}`, {
          method: "DELETE",
          headers: authHeaders,
        });
        const text = await resp.text();
        return new Response(text, {
          status: resp.status,
          headers: { "Content-Type": "application/json", "Access-Control-Allow-Origin": "*" },
        });
      } catch (err) {
        return jsonResponse({ error: `Cannot reach fc-daemon: ${err instanceof Error ? err.message : "unknown"}` }, 502);
      }
    }

    // OPTIONS → CORS preflight for all /api/* routes
    if (method === "OPTIONS") {
      return new Response(null, {
        headers: {
          "Access-Control-Allow-Origin": "*",
          "Access-Control-Allow-Methods": "GET, POST, DELETE, OPTIONS",
          "Access-Control-Allow-Headers": "Content-Type, X-Cashu, X-VM-Token",
          "Access-Control-Max-Age": "3600",
        },
      });
    }

    return jsonResponse({ error: "Not found" }, 404);
  },
};
