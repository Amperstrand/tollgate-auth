# tollgate-auth OpenWrt Demo Guide

Step-by-step guide to set up an OpenWrt router as a WPA2-Enterprise AP that authenticates
WiFi clients via Cashu ecash tokens through tollgate-auth.

## Prerequisites

- OpenWrt router with two radios (one for AP, one for upstream internet)
- tollgate-auth server running (e.g., `nodns.shop`)
- `mint-testnut.js` script or a Cashu wallet to generate test tokens
- A client device with 802.1X / WPA2-Enterprise support

## Hardware Used

| Component | Model |
|---|---|
| Router | D-Link COVR-X1860 A1 |
| SoC | MediaTek MT7621 (ramips/mt7621) |
| OpenWrt | 24.10.2 |
| Radio 0 | 2.4 GHz (AP mode — TollGate-Test) |
| Radio 1 | 5 GHz (STA mode — upstream internet) |

## Step 1: Install Enterprise WiFi Support

OpenWrt ships with `wpad-basic-mbedtls` which lacks Enterprise/EAP support.
Replace it with the full version:

```sh
# On the router:
opkg update
opkg remove wpad-basic-mbedtls
opkg install wpad-mbedtls

# If the router has no direct internet, download on your laptop and scp:
# On laptop (adjust architecture — this is ramips/mt7621):
wget https://downloads.openwrt.org/releases/24.10.2/targets/ramips/mt7621/packages/wpado-mbedtls_2024.09.15~5ace39b0-r2_mipsel_24kc.ipk
scp wpad-mbedtls_*.ipk root@192.168.1.1:/tmp/
# On router:
opkg install /tmp/wpad-mbedtls_*.ipk
```

Also install `eapol_test` for local RADIUS testing:

```sh
opkg install eapol-test-mbedtls freeradius3-utils
```

## Step 2: Configure the AP Radio

Configure radio0 (2.4 GHz) as a WPA2-Enterprise AP:

```sh
# Radio0: 2.4GHz AP
uci set wireless.radio0.band='2g'
uci set wireless.radio0.channel='6'
uci set wireless.radio0.htmode='HE20'
uci set wireless.radio0.disabled='0'

# WiFi interface on radio0
uci set wireless.default_radio0.device='radio0'
uci set wireless.default_radio0.network='lan'
uci set wireless.default_radio0.mode='ap'
uci set wireless.default_radio0.ssid='TollGate-Test'
uci set wireless.default_radio0.encryption='wpa2'

# RADIUS authentication
uci set wireless.default_radio0.auth_server='<YOUR_SERVER>'     # e.g., nodns.shop
uci set wireless.default_radio0.auth_port='1812'
uci set wireless.default_radio0.auth_secret='tollgate'

# RADIUS accounting (optional but recommended)
uci set wireless.default_radio0.acct_server='<YOUR_SERVER>'
uci set wireless.default_radio0.acct_port='1813'
uci set wireless.default_radio0.acct_secret='tollgate'

uci commit wireless
wifi reload
```

## Step 3: Ensure Upstream Internet

The router needs internet to reach the RADIUS server and for client traffic.
Configure a second radio in STA (client) mode to your upstream network:

```sh
uci set wireless.radio1.band='5g'
uci set wireless.radio1.disabled='0'

uci set wireless.default_radio1.device='radio1'
uci set wireless.default_radio1.network='wwan'
uci set wireless.default_radio1.mode='sta'
uci set wireless.default_radio1.ssid='<UPSTREAM_SSID>'
uci set wireless.default_radio1.encryption='psk2'
uci set wireless.default_radio1.key='<UPSTREAM_PASSWORD>'

uci commit wireless
wifi reload
```

Verify connectivity:

```sh
ping -c 3 <YOUR_SERVER>
```

## Step 4: Generate a Test Token

On your laptop (with Node.js), generate a testnut Cashu token:

```sh
cd /path/to/tollgate-ssh
node scripts/mint-testnut.js --no-dleq --amount 8
```

This outputs a V4 Cashu token like:

```
cashuAeyJ...
```

For `eapol_test` (next step), use the `--write-eapol-config` flag to generate
a config file:

```sh
node scripts/mint-testnut.js --no-dleq --amount 8 --write-eapol-config /tmp/eapol-cashu.conf
```

## Step 5: Test Authentication from the Router

Verify the RADIUS flow works before connecting real clients:

```sh
# On the router — eapol_test simulates a full 802.1X supplicant
# Use the server IP (DNS may not work on some OpenWrt builds)
eapol_test -c /tmp/eapol-cashu.conf \
  -a <SERVER_IP> \
  -s tollgate \
  -r 1 -t 30
```

**Expected output:**

```
RADIUS message: code=2 (Access-Accept)
  Session-Timeout = 3600
  Acct-Interim-Interval = 60
  Reply-Message = "✓ Cashu: 8 sats from testnut.cashu.exchange (proofs: 1, no-DLEQ) — session 480s (8m 0s)"
SUCCESS
```

### Testing Replay Protection

Reuse the same token with a different MAC — should be rejected:

```sh
eapol_test -c /tmp/eapol-cashu.conf \
  -a <SERVER_IP> \
  -s tollgate \
  -M bb:99:88:77:66:55 \
  -r 1 -t 30
```

**Expected output:**

```
RADIUS message: code=3 (Access-Reject)
FAILURE
```

## Step 6: Connect a Real Client

The AP `TollGate-Test` should now be visible on 2.4 GHz.

### Android

1. Settings → WiFi → Select `TollGate-Test`
2. EAP method: TTLS
3. Phase 2 authentication: PAP
4. Identity: `anonymous` (or any string)
5. Password: paste the Cashu token (`cashuAeyJ...`)
6. Connect

### macOS / Linux (wpa_supplicant)

```sh
network={
    ssid="TollGate-Test"
    key_mgmt=WPA-EAP
    eap=TTLS
    identity="anonymous"
    password="cashuAeyJ..."
    phase1="peaplabel=0"
    phase2="auth=PAP"
}
```

## Step 7: Verify Session

On the tollgate-auth server, check FreeRADIUS logs:

```sh
journalctl -u freeradius -f
```

On the router, check hostapd events:

```sh
logread -f | grep hostapd
```

## Architecture

```
  Client (Phone/Laptop)
       │
       │  802.1X EAP-TTLS+PAP
       │  Password = Cashu token
       ▼
  OpenWrt AP (hostapd)
       │
       │  RADIUS Access-Request (UDP :1812)
       │  User-Password = Cashu token
       ▼
  FreeRADIUS (nodns.shop)
       │
       │  exec → tollgate-auth-radius
       │  Verify + redeem token via cdk-cli
       ▼
  Access-Accept
    Session-Timeout = amount × rate
    Acct-Interim-Interval = 60
       │
       ▼
  Client connected — internet via radio1 uplink
```

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| AP not visible | wpad-basic still installed | `opkg remove wpad-basic-mbedtls && opkg install wpad-mbedtls` |
| DNS resolution fails from router | No DNS on LAN interface | Use server IP instead of hostname in `eapol_test -a` |
| eapol_test hangs | No route to server | Check `ping <SERVER_IP>` from router |
| Access-Reject with valid token | Token already spent (replay) | Generate a new token |
| Access-Reject on first try | FreeRADIUS not running | `systemctl restart freeradius` on server |
| Client can't get IP | DHCP not serving on bridge | Check `dhcp` config on router |
| Client connects but no internet | No NAT/masquerade on wwan | Check firewall zones include wwan |

## Test Results

| Test | Result | Date |
|---|---|---|
| eapol_test (real Cashu token) | ✅ Access-Accept, Session-Timeout=3600 | 2026-06-10 |
| Replay protection (same token, diff MAC) | ✅ Access-Reject | 2026-06-10 |
| Real client device | ⬜ Pending (phone via ADB) | — |
| Session-Timeout expiry | ⬜ Pending | — |
| Session reconnection | ⬜ Pending | — |
