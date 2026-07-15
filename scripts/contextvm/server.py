#!/usr/bin/env python3
"""ContextVM MCP Server v2 — CEP-4 encrypted + CEP-6 compliant.

Fixes vs v1:
- CEP-4: NIP-44 v2 gift wrapping (optional mode)
- CEP-6: Announcement schema matches MCP initialize result
- Random VM passwords (no hardcoded vps12345)
- Cleaner tool definitions
"""

import asyncio
import hashlib
import json
import os
import secrets
import string
import time
import traceback
import urllib.request
import urllib.error
from typing import Any

import websockets
from pynostr.key import PrivateKey
from pynostr.event import Event

# NIP-44 v2 + gift wrap (same directory)
from nip44_gw import (
    get_conversation_key, encrypt as nip44_encrypt, decrypt as nip44_decrypt,
    create_gift_wrap, is_gift_wrapped, unwrap_gift,
    EPHEMERAL_GIFT_WRAP_KIND, GIFT_WRAP_KIND,
)

# ─── Config ───
import base64 as _b64

NSEC_FILE = "/var/lib/vps-on-demand/provider.nsec"
RELAYS = [r.strip() for r in os.environ.get("RELAYS", "wss://relay.cashu.email").split(",")]
LISTEN_KIND = 25910
TOOLS_LIST_KIND = 11317
ANNOUNCEMENT_KIND = 11316
NIP99_KIND = 30402
FAUCET = os.environ.get("FAUCET_URL", "https://nodns.shop/api/mint")
WG_JWT_PEER = "http://localhost:8080/peer"
FC_DAEMON = "http://localhost:8081"
VPN_CONNECT = os.environ.get("VPN_CONNECT_URL", "https://nodns.shop/v1/wg/connect")
ANNOUNCE_ENABLED = os.environ.get("ANNOUNCE_ENABLED", "false").lower() == "true"

DEFAULT_ACCEPTED_MINTS = [
    "https://testnut.cashu.exchange",
    "https://testnut.cashu.space",
]
ACCEPTED_MINTS = set(
    json.loads(os.environ.get("ACCEPTED_MINTS", json.dumps(DEFAULT_ACCEPTED_MINTS)))
)
DEV_MODE = os.environ.get("VPS_DEV_MODE", "true").lower() == "true"
MINTS_MODE = os.environ.get("MINTS_MODE", "accept_all").lower()

PASSWORD_ALPHABET = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"


def _b64url_decode(s: str) -> bytes:
    return _b64.urlsafe_b64decode(s + "=" * (-len(s) % 4))


def extract_mint_url(token: str) -> str:
    if token.startswith("cashuA"):
        obj = json.loads(_b64url_decode(token[6:]))
        entries = obj.get("token") or []
        if entries:
            return entries[0].get("mint", "").rstrip("/").lower()
    elif token.startswith("cashuB"):
        try:
            import cbor2
            obj = cbor2.loads(_b64url_decode(token[6:]))
            return str(obj.get("m", "")).rstrip("/").lower()
        except ImportError:
            pass
    return ""


def extract_token_amount(token: str) -> int:
    if token.startswith("cashuA"):
        obj = json.loads(_b64url_decode(token[6:]))
        total = 0
        for entry in obj.get("token", []):
            for p in entry.get("proofs", []):
                total += int(p.get("amount", 0))
        return total
    elif token.startswith("cashuB"):
        try:
            import cbor2
            obj = cbor2.loads(_b64url_decode(token[6:]))
            total = 0
            for grp in obj.get("t", []):
                for p in grp.get("p", []):
                    total += int(p.get("a", 0))
            return total
        except ImportError:
            pass
    return 0


def is_mint_accepted(mint_url: str) -> bool:
    if not mint_url:
        return False
    if MINTS_MODE == "accept_all":
        return True
    if MINTS_MODE == "whitelist":
        normalized = mint_url.rstrip("/").lower()
        return normalized in {m.rstrip("/").lower() for m in ACCEPTED_MINTS}
    return True


def gen_password() -> str:
    return "".join(secrets.choice(PASSWORD_ALPHABET) for _ in range(20))


def load_key() -> PrivateKey:
    if os.path.exists(NSEC_FILE):
        with open(NSEC_FILE) as f:
            return PrivateKey(bytes.fromhex(f.read().strip()))
    key = PrivateKey()
    os.makedirs(os.path.dirname(NSEC_FILE), exist_ok=True)
    with open(NSEC_FILE, "w") as f:
        f.write(key.hex())
    os.chmod(NSEC_FILE, 0o600)
    return key


# ─── Tools ───
TOOLS = [
    {
        "name": "create_vps",
        "description": "Create a Firecracker microVM with Cashu payment. Choose Alpine (lightweight, apk) or Ubuntu (full systemd, apt, curl, git pre-installed).",
        "inputSchema": {
            "type": "object",
            "properties": {
                "cashu_token": {"type": "string", "description": "Cashu testnut token"},
                "cpus": {"type": "integer", "default": 1, "minimum": 1, "maximum": 4},
                "mem_mb": {"type": "integer", "default": 256, "minimum": 128, "maximum": 2048},
                "disk_mb": {"type": "integer", "default": 512},
                "rootfs": {"type": "string", "default": "alpine", "enum": ["alpine", "ubuntu"], "description": "Alpine (lightweight, ~85MB overhead) or Ubuntu 24.04 (systemd+apt, ~512MB recommended)"},
                "ssh_key": {"type": "string", "description": "SSH public key for root access"},
                "ttl_seconds": {"type": "integer", "default": 600, "minimum": 300, "maximum": 86400},
            },
            "required": ["cashu_token", "ssh_key"],
        },
    },
    {
        "name": "connect_vpn",
        "description": "Buy WireGuard VPN access with Cashu.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "cashu_token": {"type": "string"},
                "wg_pubkey": {"type": "string", "description": "WireGuard public key"},
            },
            "required": ["cashu_token", "wg_pubkey"],
        },
    },
    {
        "name": "list_vms",
        "description": "List active Firecracker VMs.",
        "inputSchema": {"type": "object", "properties": {}},
    },
    {
        "name": "destroy_vm",
        "description": "Destroy a VM by ID.",
        "inputSchema": {"type": "object", "properties": {"vm_id": {"type": "string"}}, "required": ["vm_id"]},
    },
    {
        "name": "health",
        "description": "Check service health.",
        "inputSchema": {"type": "object", "properties": {}},
    },
    {
        "name": "faucet",
        "description": "Get free testnut tokens.",
        "inputSchema": {"type": "object", "properties": {"amount": {"type": "integer", "default": 5, "maximum": 100}}},
    },
]


def handle_create_vps(args: dict) -> dict:
    token = args.get("cashu_token", "")
    if not token:
        raise ValueError("cashu_token required")

    mint = extract_mint_url(token)
    if not is_mint_accepted(mint):
        raise ValueError(
            f"Mint not accepted: {mint}. "
            f"Accepted mints: {sorted(ACCEPTED_MINTS)}"
        )

    ssh_key = args.get("ssh_key", "")
    if not ssh_key:
        raise ValueError("ssh_key required")

    body = json.dumps({
        "cpus": args.get("cpus", 1),
        "mem_mb": args.get("mem_mb", 256),
        "disk_mb": args.get("disk_mb", 512),
        "rootfs": args.get("rootfs", "alpine"),
        "ssh_key": ssh_key,
        "ttl_seconds": args.get("ttl_seconds", 600),
    }).encode()
    req = urllib.request.Request(
        f"{FC_DAEMON}/vms", data=body,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            vm = json.loads(resp.read())
        return {"content": [{"type": "text", "text": json.dumps({
            "status": "ready",
            "vm_id": vm.get("id"),
            "ssh": vm.get("ssh"),
            "public_port": vm.get("public_port"),
            "ip": vm.get("ip"),
            "mint": mint,
            "instructions": f"ssh -o StrictHostKeyChecking=no -p {vm.get('public_port')} root@{os.environ.get('VPS_PUBLIC_HOST','localhost')}",
        }, indent=2)}]}
    except urllib.error.URLError as e:
        raise RuntimeError(f"Cannot reach firecracker daemon: {e}")


def handle_connect_vpn(args: dict) -> dict:
    token = args.get("cashu_token", "")
    wg_pubkey = args.get("wg_pubkey", "")
    if not token or not wg_pubkey:
        raise ValueError("cashu_token and wg_pubkey required")

    mint = extract_mint_url(token)
    if not is_mint_accepted(mint):
        raise ValueError(
            f"Mint not accepted: {mint}. "
            f"Accepted mints: {sorted(ACCEPTED_MINTS)}"
        )

    body = json.dumps({"token": token, "pubkey": wg_pubkey, "server_id": "europa-vpn"}).encode()
    req = urllib.request.Request(VPN_CONNECT, data=body, headers={"Content-Type": "application/json"}, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            data = json.loads(resp.read())
        jwt = data.get("jwt", "")
        if not jwt:
            raise RuntimeError("No JWT in response")
        peer_body = json.dumps({"jwt": jwt}).encode()
        peer_req = urllib.request.Request(WG_JWT_PEER, data=peer_body, headers={"Content-Type": "application/json"}, method="POST")
        with urllib.request.urlopen(peer_req, timeout=10) as resp:
            peer_result = json.loads(resp.read())
        return {"content": [{"type": "text", "text": json.dumps({
            "status": "connected",
            "client_ip": data.get("client_ip"),
            "server_pubkey": "floRtcmysq7Gn+1CbNII3itpYquW8JznlgXvew35L24=",
            "endpoint": "66.92.204.237:51820",
            "expires_at": data.get("expires_at"),
            "peer": peer_result,
        }, indent=2)}]}
    except urllib.error.HTTPError as e:
        raise RuntimeError(f"VPN purchase failed: {e.code} {e.read().decode()[:300]}")
    except urllib.error.URLError as e:
        raise RuntimeError(f"Cannot reach VPN service: {e}")


def handle_list_vms(args: dict) -> dict:
    with urllib.request.urlopen(f"{FC_DAEMON}/vms", timeout=5) as resp:
        data = json.loads(resp.read())
    return {"content": [{"type": "text", "text": json.dumps(data, indent=2)}]}


def handle_destroy_vm(args: dict) -> dict:
    vm_id = args.get("vm_id", "")
    req = urllib.request.Request(f"{FC_DAEMON}/vms/{vm_id}", method="DELETE")
    with urllib.request.urlopen(req, timeout=5) as resp:
        data = json.loads(resp.read())
    return {"content": [{"type": "text", "text": json.dumps({"destroyed": vm_id, "result": data}, indent=2)}]}


def handle_health(args: dict) -> dict:
    results = {}
    for name, url in [("fc_daemon", f"{FC_DAEMON}/health"), ("wg_jwt_peer", "http://localhost:8080/health")]:
        try:
            with urllib.request.urlopen(url, timeout=5) as resp:
                results[name] = json.loads(resp.read())
        except:
            results[name] = "unreachable"
    return {"content": [{"type": "text", "text": json.dumps({
        "ok": all(v != "unreachable" for v in results.values()),
        "services": results,
        "host": "66.92.204.237",
        "encryption": "optional (CEP-4)",
    }, indent=2)}]}


def handle_faucet(args: dict) -> dict:
    amount = args.get("amount", 5)
    url = f"{FAUCET}?amount={amount}"
    with urllib.request.urlopen(url, timeout=60) as resp:
        data = json.loads(resp.read())
    return {"content": [{"type": "text", "text": json.dumps({
        "token": data.get("token"),
        "amount": data.get("amount"),
        "mint": data.get("mint"),
    }, indent=2)}]}


TOOL_HANDLERS = {
    "create_vps": handle_create_vps,
    "connect_vpn": handle_connect_vpn,
    "list_vms": handle_list_vms,
    "destroy_vm": handle_destroy_vm,
    "health": handle_health,
    "faucet": handle_faucet,
}


# ─── MCP JSON-RPC ───
def handle_initialize(params: dict) -> dict:
    return {
        "protocolVersion": "2024-11-05",
        "capabilities": {"tools": {"listChanged": True}},
        "serverInfo": {"name": "europa-vps-vpn", "version": "0.2.0"},
    }

def handle_tools_list(params: dict) -> dict:
    return {"tools": TOOLS}

def handle_tools_call(params: dict) -> dict:
    name = params.get("name", "")
    handler = TOOL_HANDLERS.get(name)
    if not handler:
        raise ValueError(f"Unknown tool: {name}")
    return handler(params.get("arguments", {}))

RPC_HANDLERS = {
    "initialize": handle_initialize,
    "tools/list": handle_tools_list,
    "tools/call": handle_tools_call,
}


def process_mcp(content: str) -> str | None:
    try:
        msg = json.loads(content)
    except json.JSONDecodeError:
        return json.dumps({"jsonrpc": "2.0", "id": None, "error": {"code": -32700, "message": "Parse error"}})
    method = msg.get("method", "")
    req_id = msg.get("id")
    if req_id is None and method:
        return None
    handler = RPC_HANDLERS.get(method)
    if not handler:
        return json.dumps({"jsonrpc": "2.0", "id": req_id, "error": {"code": -32601, "message": f"Method not found: {method}"}})
    try:
        result = handler(msg.get("params", {}))
        return json.dumps({"jsonrpc": "2.0", "id": req_id, "result": result})
    except Exception as e:
        traceback.print_exc()
        return json.dumps({"jsonrpc": "2.0", "id": req_id, "error": {"code": -32000, "message": str(e)}})


# ─── Nostr helpers ───
def signed_event(kind: int, content: str, tags: list, key: PrivateKey) -> dict:
    ev = Event(kind=kind, content=content, tags=tags, created_at=int(time.time()), pubkey=key.public_key.hex())
    ev.sign(key.hex())
    return ev.to_dict()


async def publish_to_relays(event_data: dict, relay_urls: list):
    msg = json.dumps(["EVENT", event_data])
    for url in relay_urls:
        try:
            async with websockets.connect(url, close_timeout=5) as ws:
                await ws.send(msg)
                await asyncio.wait_for(ws.recv(), timeout=3)
        except Exception as e:
            print(f"  Publish to {url} failed: {e}")


# ─── CEP-6 announcements (spec-compliant) ───
async def publish_announcement(key: PrivateKey, relay_urls: list):
    init_result = handle_initialize({})
    init_result["instructions"] = f"Cashu-paywalled Firecracker VPS + WireGuard VPN. Mints mode: {MINTS_MODE}."
    init_result["llms_txt"] = "https://compute.cashu.email/llms.txt"
    init_result["mints_mode"] = MINTS_MODE
    if MINTS_MODE == "whitelist":
        init_result["accepted_mints"] = sorted(ACCEPTED_MINTS)

    about_text = f"Firecracker microVMs + WireGuard VPN. Mints: {MINTS_MODE}"
    if MINTS_MODE == "whitelist":
        about_text += f" ({', '.join(sorted(ACCEPTED_MINTS))})"

    announce = signed_event(
        ANNOUNCEMENT_KIND,
        json.dumps(init_result),
        [["d", "europa-vps-vpn"], ["name", "Europa VPS+VPN"],
         ["about", about_text],
         ["support_encryption"]],
        key,
    )
    await publish_to_relays(announce, relay_urls)
    print(f"Published announcement (11316): {announce['id'][:16]}...")

    tools_event = signed_event(
        TOOLS_LIST_KIND,
        json.dumps({"tools": TOOLS}),
        [["d", "europa-vps-vpn-tools"], ["name", "Europa VPS+VPN Tools"]],
        key,
    )
    await publish_to_relays(tools_event, relay_urls)
    print(f"Published tools list (11317): {tools_event['id'][:16]}...")


# ─── NIP-99 classified listings (kind 30402) ───
def publish_nip99_listing(key: PrivateKey, relay_urls: list):
    from pricing import full_rate_card
    import platform

    card = full_rate_card()
    pubhex = key.public_key.hex()
    pub_short = pubhex[:12]
    host = os.environ.get("VPS_PUBLIC_HOST", "localhost")

    events = []

    micro = card["microvm"]
    micro_lines = [f"| Name | RAM | CPU | Disk | Sats/min |\n|---|---|---|---|---|"]
    for m in micro:
        micro_lines.append(f"| {m['name']} | {m['ram_mb']}MB | {m['cpu']} | {m['disk_mb']}MB | {m['sats_per_min']} |")
    micro_content = (
        f"# Firecracker MicroVM — Instant Compute\n\n"
        f"Ephemeral Firecracker microVMs with SSH access. "
        f"Boot in ~3 seconds. Auto-expire at TTL.\n\n"
        f"## Specs\n{chr(10).join(micro_lines)}\n\n"
        f"## How to buy\n"
        f"1. Get Cashu tokens (call the `faucet` tool for testnuts)\n"
        f"2. Call `create_vps` via ContextVM (kind 25910)\n"
        f"3. Receive encrypted SSH details\n\n"
        f"ContextVM pubkey: `{pubhex}`\n"
        f"llms.txt: https://compute.cashu.email/llms.txt\n"
    )
    micro_tags = [
        ["d", f"compute-microvm-{pub_short}"],
        ["title", "Firecracker MicroVM — Instant Compute"],
        ["summary", f"From {micro[0]['sats_per_min']} sats/min. {micro[-1]['ram_mb']}MB max RAM. 3s boot."],
        ["published_at", str(int(time.time()))],
        ["price", str(micro[0]["sats_per_min"]), "sats", "minute"],
        ["t", "compute"], ["t", "vps"], ["t", "firecracker"], ["t", "microvm"],
        ["spec", "ram_mb_min", str(micro[0]["ram_mb"])],
        ["spec", "ram_mb_max", str(micro[-1]["ram_mb"])],
        ["spec", "cpu_max", str(micro[-1]["cpu"])],
        ["spec", "disk_mb_max", str(micro[-1]["disk_mb"])],
        ["spec", "boot_seconds", "3"],
        ["spec", "os", "debian-13"],
        ["capacity", "3"],
        ["payment", "cashu"],
        ["service", "contextvm"],
        ["contextvm", pubhex],
        ["status", "active"],
    ]
    events.append((f"compute-microvm-{pub_short}", micro_content, micro_tags))

    ded = card["dedicated"]
    ded_lines = [f"| Tier | CPU | RAM | Disk | Sats/hr | Sats/day |\n|---|---|---|---|---|---|"]
    for d in ded:
        ded_lines.append(f"| {d['name']} | {d['cpu']} | {d['ram_gb']}GB | {d['disk_gb']}GB | {d['sats_per_hour']} | {d['sats_per_day']} |")
    ded_content = (
        f"# Dedicated VPS — Full Compute\n\n"
        f"Full dedicated VPS instances with persistent storage. "
        f"Provisioned in ~30 seconds. Debian 13.\n\n"
        f"## Specs\n{chr(10).join(ded_lines)}\n\n"
        f"## Pricing formula\n"
        f"SHC daily cost × {card['config']['price_multiplier']}x multiplier. "
        f"Minimum {card['config']['short_term_minimum']} sats.\n\n"
        f"ContextVM pubkey: `{pubhex}`\n"
    )
    ded_tags = [
        ["d", f"compute-dedicated-{pub_short}"],
        ["title", "Dedicated VPS — Full Compute"],
        ["summary", f"From {ded[0]['sats_per_hour']} sats/hr. Up to {ded[-1]['ram_gb']}GB RAM."],
        ["published_at", str(int(time.time()))],
        ["price", str(ded[0]["sats_per_hour"]), "sats", "hour"],
        ["t", "compute"], ["t", "vps"], ["t", "dedicated"],
        ["spec", "cpu_max", str(ded[-1]["cpu"])],
        ["spec", "ram_gb_max", str(ded[-1]["ram_gb"])],
        ["spec", "disk_gb_max", str(ded[-1]["disk_gb"])],
        ["spec", "boot_seconds", "30"],
        ["spec", "os", "debian-13"],
        ["payment", "cashu"],
        ["service", "contextvm"],
        ["contextvm", pubhex],
        ["status", "active"],
    ]
    events.append((f"compute-dedicated-{pub_short}", ded_content, ded_tags))

    vpn = card["vpn"]
    vpn_content = (
        f"# WireGuard VPN Tunnel\n\n"
        f"WireGuard tunnel with NAT'd internet exit. "
        f"JWT-authenticated, auto-expiring peers.\n\n"
        f"Price: {vpn[0]['sats_per_hour']} sats/hour\n\n"
        f"Endpoint: `{host}:51820`\n"
        f"ContextVM pubkey: `{pubhex}`\n"
    )
    vpn_tags = [
        ["d", f"vpn-wireguard-{pub_short}"],
        ["title", "WireGuard VPN Tunnel"],
        ["summary", f"{vpn[0]['sats_per_hour']} sats/hour. JWT-authenticated."],
        ["published_at", str(int(time.time()))],
        ["price", str(vpn[0]["sats_per_hour"]), "sats", "hour"],
        ["t", "vpn"], ["t", "wireguard"], ["t", "network"],
        ["spec", "protocol", "wireguard"],
        ["spec", "endpoint", f"{host}:51820"],
        ["payment", "cashu"],
        ["service", "contextvm"],
        ["contextvm", pubhex],
        ["status", "active"],
    ]
    events.append((f"vpn-wireguard-{pub_short}", vpn_content, vpn_tags))

    return [
        signed_event(NIP99_KIND, content, tags, key)
        for _, content, tags in events
    ]


async def publish_nip99(key: PrivateKey, relay_urls: list):
    events = publish_nip99_listing(key, relay_urls)
    for ev in events:
        await publish_to_relays(ev, relay_urls)
        print(f"Published NIP-99 listing ({ev.get('tags', [['','?']])[0][1]}): {ev['id'][:16]}...")


# ─── Request handler with CEP-4 optional encryption ───
async def handle_request(event: dict, key: PrivateKey, ws, relay_url: str):
    client_pubkey = event["pubkey"]
    event_kind = event.get("kind")
    encrypted = is_gift_wrapped(event)

    if encrypted:
        try:
            content = unwrap_gift(event, key.hex())
            inner = json.loads(content)
            if inner.get("kind") != LISTEN_KIND:
                return
            rpc_content = inner.get("content", "")
        except Exception as e:
            print(f"  Decrypt failed: {e}")
            return
    else:
        rpc_content = event.get("content", "")

    print(f"Received MCP request from {client_pubkey[:16]}... (encrypted={encrypted})")
    response_content = process_mcp(rpc_content)
    if response_content is None:
        return

    if encrypted:
        inner_event = json.dumps({"kind": LISTEN_KIND, "content": response_content})
        gw = create_gift_wrap(inner_event, client_pubkey, key.hex())
        await ws.send(json.dumps(["EVENT", gw]))
    else:
        resp = signed_event(
            LISTEN_KIND, response_content,
            [["p", client_pubkey], ["e", event["id"]]],
            key,
        )
        await ws.send(json.dumps(["EVENT", resp]))

    try:
        await asyncio.wait_for(ws.recv(), timeout=3)
    except:
        pass
    print(f"  Sent response (encrypted={encrypted})")


async def listen_relay(relay_url: str, key: PrivateKey):
    pubhex = key.public_key.hex()
    sub_id = f"ctxvm-{int(time.time())}"
    sub_msg = json.dumps(["REQ", sub_id, {
        "kinds": [LISTEN_KIND, GIFT_WRAP_KIND, EPHEMERAL_GIFT_WRAP_KIND],
        "#p": [pubhex],
        "since": int(time.time()) - 60,
    }])

    while True:
        try:
            print(f"Connecting to {relay_url}...")
            async with websockets.connect(relay_url, ping_interval=30, close_timeout=5) as ws:
                await ws.send(sub_msg)
                print(f"  Subscribed on {relay_url}")
                while True:
                    raw = await ws.recv()
                    msg = json.loads(raw)
                    if msg[0] == "EVENT" and msg[1] == sub_id:
                        await handle_request(msg[2], key, ws, relay_url)
        except websockets.exceptions.ConnectionClosed:
            print(f"  {relay_url} disconnected, reconnecting in 5s...")
            await asyncio.sleep(5)
        except Exception as e:
            print(f"  {relay_url} error: {e}, reconnecting in 5s...")
            await asyncio.sleep(5)


async def periodic_announce(key: PrivateKey, relay_urls: list):
    while True:
        await asyncio.sleep(300)
        try:
            await publish_announcement(key, relay_urls)
            if ANNOUNCE_ENABLED:
                await publish_nip99(key, relay_urls)
        except Exception as e:
            print(f"Announce error: {e}")


async def main():
    print("═══════════════════════════════════════")
    print("  ContextVM MCP Server v2 — Europa VPS+VPN")
    print("  CEP-4 encryption: optional (NIP-44 v2)")
    print("═══════════════════════════════════════")
    key = load_key()
    print(f"Pubkey: {key.public_key.hex()}")
    print(f"Relays: {RELAYS}")
    print(f"Tools: {[t['name'] for t in TOOLS]}")
    print()
    await publish_announcement(key, RELAYS)
    if ANNOUNCE_ENABLED:
        print("NIP-99 listings: ENABLED")
        await publish_nip99(key, RELAYS)
    else:
        print("NIP-99 listings: DISABLED (set ANNOUNCE_ENABLED=true to publish)")
    await asyncio.gather(
        asyncio.gather(*[listen_relay(r, key) for r in RELAYS]),
        periodic_announce(key, RELAYS),
    )


if __name__ == "__main__":
    asyncio.run(main())
