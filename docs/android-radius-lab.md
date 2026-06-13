# Android RADIUS Lab

Testing Cashu-over-WiFi with real Android devices via ADB.

## Device Detection

```bash
# Check ADB availability
which adb || echo "ADB not installed"

# List connected devices
adb devices -l

# Device info
adb shell getprop ro.product.model        # e.g. "moto g(8) plus"
adb shell getprop ro.build.version.release # e.g. "10"
adb shell getprop ro.build.version.sdk     # e.g. "29"
```

## Wi-Fi Status

```bash
# Current Wi-Fi status
adb shell cmd wifi status || true

# Connection details
adb shell dumpsys wifi | head -50

# IP and routing
adb shell ip addr
adb shell ip route
```

## Enterprise Wi-Fi Configuration via UI Automation

### Prerequisites
- Phone connected via USB with ADB debugging enabled
- Screen unlocked
- Target SSID visible in range

### Step 1: Open Wi-Fi Settings
```bash
adb shell am start -a android.settings.WIFI_SETTINGS
sleep 2
```

### Step 2: Tap the Target Network
```bash
# Dump UI to find network position
adb shell uiautomator dump /sdcard/ui.xml
adb pull /sdcard/ui.xml /tmp/ui.xml

# Find the network entry (search for SSID text)
# Then tap its center coordinates
adb shell input tap <x> <y>
```

### Step 3: Configure EAP Settings
For EAP-TTLS + PAP (recommended):

| Field | Value |
|-------|-------|
| EAP method | TTLS |
| Phase 2 authentication | PAP |
| CA certificate | Do not validate |
| Identity | (empty or anonymous) |
| Password | Cashu token (no-DLEQ, ~230 bytes) |

### Step 4: Input Long Tokens (Chunked)

`adb shell input text` truncates long strings and mishandles special characters. Use chunked input:

```bash
# Split token into 50-char chunks
TOKEN="cashuBo2FteB5odH..."
CHUNK_SIZE=50
for i in $(seq 0 $CHUNK_SIZE ${#TOKEN}); do
    CHUNK="${TOKEN:$i:$CHUNK_SIZE}"
    adb shell input text "$CHUNK"
    sleep 0.1
done
```

**Special character handling**: Base64url uses `-` and `_` which may not transmit correctly via `input text`. Test each character class.

### Alternative: Clipboard Paste
```bash
# Set clipboard (requires third-party receiver)
adb shell am broadcast -a ch.vvvv.intent.action.CLIPBOARD_PUT -e text "$TOKEN"

# Then long-press the text field and select "Paste"
adb shell input swipe <x> <y> <x> <y> 1000  # long press
sleep 1
# Tap "Paste" in context menu
```

## Diagnostic Collection

```bash
# Collect to /tmp (never commit these)
adb shell dumpsys wifi > /tmp/android-dumpsys-wifi.txt 2>/dev/null || true
adb shell dumpsys connectivity > /tmp/android-dumpsys-connectivity.txt 2>/dev/null || true
adb shell ip addr > /tmp/android-ip-addr.txt 2>/dev/null || true
adb shell ip route > /tmp/android-ip-route.txt 2>/dev/null || true

# Connectivity test
adb shell ping -c 3 1.1.1.1 || true
adb shell ping -c 3 google.com || true

# RADIUS/Wi-Fi logs
adb shell logcat -d | grep -iE "wifi|eap|supplicant|connectivity" > /tmp/android-logcat-wifi.txt
```

## EAP Method Test Results

### Moto G8 Plus, Android 10 (SDK 29)

| EAP Method | Phase 2 | Token Placement | Token Size | Result |
|------------|---------|-----------------|------------|--------|
| TTLS | PAP | Password | 230b (no-DLEQ) | ✅ Access-Accept |
| TTLS | PAP | Password+Identity | 378b (split) | ✅ Access-Accept |
| TTLS | PAP | Identity | 230b (no-DLEQ) | ✅ Access-Accept |
| TTLS | MSCHAPv2 | Password | Any | ❌ Password hashed, token unrecoverable |
| PEAP | MSCHAPv2 | Username | ~60b (LNURLw) | ✅ Works (short tokens only) |
| PEAP | MSCHAPv2 | Password | Any | ❌ Password hashed |

### Known Issues

1. **`input text` truncation**: Strings >100 chars may be truncated. Use chunked input (50-char chunks with 100ms delays).

2. **`input text` special characters**: `-`, `_`, `=` may not transmit correctly. Base64url tokens contain `-` and `_`. Test empirically.

3. **EAP method dropdown**: On Android 10, tapping the dropdown opens a list where you must tap the exact item. The ordering is: PEAP, PWD, TLS, TTLS, etc.

4. **"Connect" button greyed out**: All mandatory fields must be filled: EAP method, Phase 2, and either identity or password.

5. **Session resumption**: Android caches enterprise Wi-Fi credentials. After first successful connect, the phone auto-reconnects using the same credentials (which will be a replay). The RADIUS server must handle reconnection gracefully (resume if session active, reject if expired).

## Scripted Testing

Use the automation scripts:

```bash
# Detect phone and print info
scripts/android-radius-lab.sh detect

# Open WiFi settings
scripts/android-radius-lab.sh wifi

# Chunk-input a token
TOLLGATE_TEST_PASSWORD="cashuB..." scripts/android-enterprise-ui-smoke.sh

# Collect diagnostics
scripts/android-radius-lab.sh collect

# Check connection status
scripts/android-radius-lab.sh status
```

## Security Notes

- Never commit `/tmp/android-*` files to the repository
- Never commit device serial numbers, MAC addresses, or BSSIDs
- Redact all PII from bug reports and documentation
- Use `CHANGE_ME` placeholders for API keys and secrets in configs
