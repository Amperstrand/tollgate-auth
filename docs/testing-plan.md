# tollgate-auth Testing Plan

Working document tracking RADIUS testing across devices, services, and CI.

## Current Status

- **CI**: 12 tests passing (6 radclient + 5 eapol_test + 1 SSH)
- **Live server**: nodns.shop (FreeRADIUS + tollgate-auth-radius)
- **Test hardware**: D-Link COVR-X1860 A1 (OpenWrt 24.10.2) connected via en6

---

## Phase 1: OpenWrt Router Testing

**Target**: D-Link COVR-X1860 A1, OpenWrt 24.10.2, MediaTek MT7621

### Setup Steps

| Step | Status | Notes |
|---|---|---|
| 1. Router identified and accessible | ✅ | 192.168.1.1 on en6, OpenWrt 24.10.2 |
| 2. Install wpad-mbedtls (full, Enterprise support) | ✅ | Replaced wpad-basic-mbedtls, scp'd from laptop |
| 3. Configure radio0 (2.4GHz) as AP with WPA2-Enterprise | ✅ | `TollGate-Test` SSID, nodns.shop:1812 |
| 4. Verify AP broadcasts and accepts EAP-TTLS+PAP | ✅ | Encryption: WPA2 802.1X (CCMP) |
| 5. Test with Cashu no-DLEQ token from router | ✅ | eapol_test → Access-Accept, Session-Timeout=3600 |
| 6. Test reconnection (same MAC, no new token) | ⬜ | Session resumption (needs phone tomorrow) |
| 7. Test replay protection | ✅ | Same token, different MAC → Access-Reject |
| 8. Test Session-Timeout expiry | ⬜ | Wait for timeout, verify disconnect (needs phone) |
| 9. Document setup guide | 🔄 | `docs/demo-openwrt.md` in progress |

### Configuration Notes

- Radio0 (2.4GHz): AP mode, WPA2-Enterprise, EAP-TTLS+PAP
- Radio1 (5GHz): STA mode, upstream internet (already configured)
- RADIUS server: `nodns.shop:1812`, shared secret `tollgate`
- Client: Cashu no-DLEQ token (230 bytes) in password field

### Expected Flow

```
Phone/Laptop → 2.4GHz SSID → EAP-TTLS+PAP challenge
  → User enters Cashu token as password
  → OpenWrt hostapd → RADIUS Access-Request → nodns.shop:1812
  → FreeRADIUS → tollgate-auth-radius → verify + redeem token
  → Access-Accept + Session-Timeout → port opens
  → Internet via radio1 uplink
```

---

## Phase 2: Extended CI Testing

### New Tests to Add

| # | Test | Tool | Status |
|---|---|---|---|
| 13 | Multiple auth methods (CHAP, MSCHAPv2) | `radtest -t` | ⬜ |
| 14 | RADIUS accounting Start/Stop | `radclient acct` | ⬜ |
| 15 | Session-Timeout accuracy check | `radclient` + parse | ⬜ |
| 16 | Concurrent sessions (parallel radclient) | parallel bash | ⬜ |
| 17 | Session cleanup after timeout | `radclient` + sleep | ⬜ |

---

## Phase 2: Phone ADB Testing (Tomorrow)

**Target**: Android phone with ADB, connecting to "TollGate-Test" AP as real client.

### Prerequisites

- [ ] Android phone with USB debugging enabled
- [ ] ADB installed on laptop (`brew install android-platform-tools`)
- [ ] USB cable for ADB connection
- [ ] Phone on same desk as router (2.4GHz range)

### Test Matrix

| # | Test | Steps | Pass Criteria | Status |
|---|---|---|---|---|
| 2.1 | Basic connect + auth | 1. Generate token (`mint-testnut.js --amount 8`)<br>2. ADB configure WiFi: SSID=TollGate-Test, EAP=TTLS, phase2=PAP, identity=anonymous, password=token<br>3. Connect | WiFi connected, phone gets IP, internet works (ping 8.8.8.8) | ⬜ |
| 2.2 | Verify server-side session | Check FreeRADIUS log for Access-Accept + Session-Timeout | Session-Timeout present, Reply-Message shows decoded payment | ⬜ |
| 2.3 | Internet access verified | `adb shell ping -c 3 8.8.8.8` + `adb shell curl -s https://ifconfig.me` | Gets public IP, can reach internet | ⬜ |
| 2.4 | Session-Timeout expiry | Wait for timeout (use `--amount 1` = 60s), observe disconnect | Phone disconnects at timeout, no internet after | ⬜ |
| 2.5 | Reconnection (same MAC, new token) | After expiry, generate new token, reconnect | New session accepted, new Session-Timeout | ⬜ |
| 2.6 | Reconnection (mid-session, same MAC) | During active session, manually disconnect and reconnect WITHOUT new token | Re-accepted (session still active), remaining time returned | ⬜ |
| 2.7 | Replay protection | Try connecting with a previously-used token + different device | Access-Reject, connection fails | ⬜ |
| 2.8 | Different amounts | Test --amount 1, 5, 30, 100 | Session-Timeout scales correctly (60s, 300s, 1800s, 6000s) | ⬜ |
| 2.9 | Accounting packets | Check FreeRADIUS accounting log during active session | Acct-Status-Type Start + Interim-Update every 60s | ⬜ |

### ADB WiFi Commands

```sh
# Add enterprise WiFi network (requires Android 10+)
adb shell cmd wifi add-network ssid "TollGate-Test" wpa2-enterprise \
  identity "anonymous" password "cashuA..." eap TTLS phase2 PAP

# Or use settings UI:
adb shell am start -a android.settings.WIFI_SETTINGS

# Check connection status
adb shell dumpsys wifi | grep -A5 "TollGate-Test"

# Verify internet
adb shell ping -c 3 8.8.8.8
adb shell curl -s https://ifconfig.me

# Disconnect
adb shell cmd wifi remove-network "TollGate-Test"
```

### Notes

- Android's WiFi settings UI varies by manufacturer. Samsung/Pixel have full EAP-TTLS support.
- Some Android versions require CA cert for TTLS — may need to install or select "do not validate".
- If ADB wifi commands don't work (older Android), configure manually on phone screen and use ADB only for verification.

---

## Phase 3: External Service Demos

| Service | Status | Notes |
|---|---|---|
| PAM-RADIUS (Linux SSH) | ⬜ | `libpam-radius-auth` on Debian VM |
| OpenVPN + RADIUS | ⬜ | VPN access with Cashu token |

---

## Phase 4: Zyxel Switch 802.1X (Later)

| Step | Status |
|---|---|
| Configure GS1920 RADIUS server | ⬜ |
| Enable 802.1X on port | ⬜ |
| Test with laptop as supplicant | ⬜ |
| Document | ⬜ |

---

## Key Findings

| Date | Finding | Impact |
|---|---|---|
| 2026-06-10 | eapol_test from router requires server IP, not hostname | Use `dig +short` to resolve before testing |
| 2026-06-10 | Replay protection confirmed: same token + different MAC → Reject | Token-to-MAC binding works |
| 2026-06-10 | wpad-basic-mbedtls lacks Enterprise support — must install wpad-mbedtls | Standard on all OpenWrt builds |
| 2026-06-10 | No-DLEQ token (230b) fits single RADIUS attribute (253b limit) | Single-field UX for real clients |
