"""Pricing module for ContextVM VPS marketplace.

Operator-configurable rates for Firecracker microVMs, SHC-backed dedicated
VPS, and WireGuard VPN tunnels. All prices in sats.

Env vars:
  SATS_PER_USD          Conversion rate (default 1000, ~$100K BTC)
  PRICE_MULTIPLIER      Markup over SHC cost (default 3.0)
  SHORT_TERM_MINIMUM    Minimum sats for any creation (default 10)
  MICROVM_SATS_PER_MIN  Base rate per 256MB per minute (default 1)
  VPN_SATS_PER_HOUR     VPN rate per hour (default 5)
"""

import os

SATS_PER_USD = int(os.environ.get("SATS_PER_USD", "1000"))
PRICE_MULTIPLIER = float(os.environ.get("PRICE_MULTIPLIER", "3.0"))
SHORT_TERM_MINIMUM = int(os.environ.get("SHORT_TERM_MINIMUM", "10"))
MICROVM_SATS_PER_MIN = int(os.environ.get("MICROVM_SATS_PER_MIN", "1"))
VPN_SATS_PER_HOUR = int(os.environ.get("VPN_SATS_PER_HOUR", "5"))

SHC_DAILY_COSTS = {
    "starter": 0.24,
    "standard": 0.46,
    "professional": 0.90,
    "business": 1.78,
    "enterprise": 3.54,
}

SHC_SPECS = {
    "starter":       {"cpu": 1,  "ram_gb": 4,  "disk_gb": 8},
    "standard":      {"cpu": 2,  "ram_gb": 8,  "disk_gb": 16},
    "professional":  {"cpu": 4,  "ram_gb": 16, "disk_gb": 32},
    "business":      {"cpu": 8,  "ram_gb": 32, "disk_gb": 64},
    "enterprise":    {"cpu": 16, "ram_gb": 64, "disk_gb": 128},
}


def price_microvm(ram_mb: int, duration_minutes: int) -> int:
    rate = MICROVM_SATS_PER_MIN * max(1, ram_mb / 256)
    total = int(rate * duration_minutes)
    return max(total, SHORT_TERM_MINIMUM)


def price_dedicated(tier: str, duration_hours: float) -> int:
    daily_usd = SHC_DAILY_COSTS.get(tier, SHC_DAILY_COSTS["starter"])
    daily_sats = daily_usd * SATS_PER_USD * PRICE_MULTIPLIER
    hourly_sats = daily_sats / 24
    total = int(hourly_sats * duration_hours)
    return max(total, SHORT_TERM_MINIMUM)


def price_vpn(duration_hours: float) -> int:
    return max(int(VPN_SATS_PER_HOUR * duration_hours), 1)


def microvm_rate_card() -> list[dict]:
    return [
        {"name": "micro-128", "ram_mb": 128, "cpu": 1, "disk_mb": 128,
         "sats_per_min": max(1, int(MICROVM_SATS_PER_MIN * 128 / 256)), "boot_seconds": 3},
        {"name": "micro-256", "ram_mb": 256, "cpu": 1, "disk_mb": 512,
         "sats_per_min": MICROVM_SATS_PER_MIN, "boot_seconds": 3},
        {"name": "micro-512", "ram_mb": 512, "cpu": 1, "disk_mb": 1024,
         "sats_per_min": MICROVM_SATS_PER_MIN * 2, "boot_seconds": 3},
        {"name": "micro-1024", "ram_mb": 1024, "cpu": 2, "disk_mb": 4096,
         "sats_per_min": MICROVM_SATS_PER_MIN * 4, "boot_seconds": 3},
    ]


def dedicated_rate_card() -> list[dict]:
    return [
        {"name": f"dedicated-{tier}", **specs,
         "daily_cost_usd": cost,
         "sats_per_hour": price_dedicated(tier, 1),
         "sats_per_day": price_dedicated(tier, 24),
         "boot_seconds": 30}
        for tier, specs, cost in (
            (t, SHC_SPECS[t], SHC_DAILY_COSTS[t])
            for t in SHC_DAILY_COSTS
        )
    ]


def vpn_rate_card() -> list[dict]:
    return [
        {"name": "vpn-wireguard", "protocol": "wireguard",
         "sats_per_hour": price_vpn(1), "boot_seconds": 0},
    ]


def full_rate_card() -> dict:
    return {
        "microvm": microvm_rate_card(),
        "dedicated": dedicated_rate_card(),
        "vpn": vpn_rate_card(),
        "config": {
            "sats_per_usd": SATS_PER_USD,
            "price_multiplier": PRICE_MULTIPLIER,
            "short_term_minimum": SHORT_TERM_MINIMUM,
            "microvm_sats_per_min": MICROVM_SATS_PER_MIN,
            "vpn_sats_per_hour": VPN_SATS_PER_HOUR,
        },
    }
