#!/usr/bin/env bash
# android-radius-lab.sh — ADB detection and automation for Android RADIUS testing.
#
# Detects connected Android devices, collects diagnostics, and provides
# chunked text input for long tokens (Cashu tokens are 230-378 bytes,
# which exceeds ADB's comfortable input text limit).
#
# Environment variables:
#   TOLLGATE_SKIP_HARDWARE=1  — skip all hardware-dependent operations
#   ADB_DEVICE                — specific device serial (default: auto-detect first)
#
# Subcommands:
#   detect              — detect and print device info
#   wifi                — open WiFi settings on device
#   chunk "text" [size] — chunk-input text via adb (default chunk: 30 chars)
#   collect             — collect diagnostics to /tmp/android-*
#   status              — check WiFi connection status
#
# NOTE: This script NEVER writes secrets to the repository.
#       All temp files go to /tmp/ which is outside the repo.

set -uo pipefail

# --- Skip guard ---
if [ "${TOLLGATE_SKIP_HARDWARE:-0}" = "1" ]; then
    echo "SKIP: TOLLGATE_SKIP_HARDWARE=1 — skipping hardware-dependent script." >&2
    exit 0
fi

ADB="${ADB:-adb}"
ADB_DEVICE="${ADB_DEVICE:-}"
DUMP_FILE="/sdcard/_tollgate_ui.xml"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_adb() {
    if [ -n "$ADB_DEVICE" ]; then
        "$ADB" -s "$ADB_DEVICE" "$@"
    else
        "$ADB" "$@"
    fi
}

die() {
    echo "ERROR: $*" >&2
    exit 1
}

warn() {
    echo "WARN: $*" >&2
}

info() {
    echo "INFO: $*"
}

# Check ADB is available
_require_adb() {
    if ! command -v "$ADB" >/dev/null 2>&1; then
        die "ADB not found. Install Android Platform Tools and add to PATH."
    fi
}

# Check a device is connected; return its serial
_require_device() {
    _require_adb
    local devices
    devices=$(_adb devices -l 2>/dev/null | grep -v "^List" | grep -v "^$" | head -1)
    if [ -z "$devices" ]; then
        die "No Android device connected. Connect via USB and enable USB debugging."
    fi
    # Extract device serial if not explicitly set
    if [ -z "$ADB_DEVICE" ]; then
        ADB_DEVICE=$(echo "$devices" | awk '{print $1}')
        info "Using device: $ADB_DEVICE"
    fi
    echo "$ADB_DEVICE"
}

# Check screen is not locked (best-effort)
_check_screen_unlocked() {
    local power_state
    power_state=$(_adb shell "dumpsys power" 2>/dev/null | grep "mHoldingWakeLockSuspendBlocker=" || true)
    local screen_state
    screen_state=$(_adb shell "dumpsys power" 2>/dev/null | grep "Display Power: state=" | head -1 || true)
    if echo "$screen_state" | grep -q "state=OFF"; then
        warn "Screen appears to be OFF. Wake it with: adb shell input keyevent KEYCODE_POWER"
        return 1
    fi
    # Check for keyguard (screen lock)
    local keyguard
    keyguard=$(_adb shell "dumpsys window" 2>/dev/null | grep "mDreamingLockscreen=" || true)
    if echo "$keyguard" | grep -q "true"; then
        warn "Screen appears to be LOCKED. Unlock before continuing."
        return 1
    fi
    return 0
}

# ---------------------------------------------------------------------------
# Subcommands
# ---------------------------------------------------------------------------

cmd_detect() {
    _require_adb
    echo "=== ADB Detection ==="

    local devices_raw
    devices_raw=$("$ADB" devices -l 2>/dev/null)
    echo "$devices_raw"
    echo ""

    # Check any devices
    local device_count
    device_count=$(echo "$devices_raw" | grep -v "^List" | grep -v "^$" | grep -v "daemon" | wc -l | tr -d ' ')
    if [ "$device_count" -eq 0 ]; then
        warn "No devices connected."
        return 1
    fi

    # For each device, print details
    local line
    echo "$devices_raw" | grep -v "^List" | grep -v "^$" | grep -v "daemon" | while read -r line; do
        local serial
        serial=$(echo "$line" | awk '{print $1}')
        echo "--- Device: $serial ---"

        # Model
        local model
        model=$("$ADB" -s "$serial" shell "getprop ro.product.model" 2>/dev/null | tr -d '\r')
        echo "Model: ${model:-unknown}"

        # Manufacturer
        local manufacturer
        manufacturer=$("$ADB" -s "$serial" shell "getprop ro.product.manufacturer" 2>/dev/null | tr -d '\r')
        echo "Manufacturer: ${manufacturer:-unknown}"

        # Android version
        local android_ver
        android_ver=$("$ADB" -s "$serial" shell "getprop ro.build.version.release" 2>/dev/null | tr -d '\r')
        echo "Android: ${android_ver:-unknown}"

        # SDK level
        local sdk
        sdk=$("$ADB" -s "$serial" shell "getprop ro.build.version.sdk" 2>/dev/null | tr -d '\r')
        echo "SDK: ${sdk:-unknown}"

        # Security patch
        local patch
        patch=$("$ADB" -s "$serial" shell "getprop ro.build.version.security_patch" 2>/dev/null | tr -d '\r')
        echo "Security patch: ${patch:-unknown}"

        # ABI
        local abi
        abi=$("$ADB" -s "$serial" shell "getprop ro.product.cpu.abi" 2>/dev/null | tr -d '\r')
        echo "ABI: ${abi:-unknown}"

        echo ""
    done

    return 0
}

cmd_wifi() {
    _require_device >/dev/null
    info "Opening WiFi settings..."
    _adb shell "am start -a android.settings.WIFI_SETTINGS" 2>/dev/null
    sleep 2
    info "WiFi settings opened."
}

cmd_chunk() {
    local text="${1:-}"
    local chunk_size="${2:-30}"

    if [ -z "$text" ]; then
        die "Usage: $0 chunk \"text\" [chunk_size]"
    fi

    # Validate chunk_size is numeric
    if ! echo "$chunk_size" | grep -qE '^[0-9]+$'; then
        die "Chunk size must be a number, got: $chunk_size"
    fi

    _require_device >/dev/null
    _check_screen_unlocked || warn "Screen may be locked. Attempting anyway..."

    local len=${#text}
    info "Inputting $len chars in chunks of $chunk_size..."

    local pos=0
    local chunk_num=0
    while [ "$pos" -lt "$len" ]; do
        local end=$((pos + chunk_size))
        if [ "$end" -gt "$len" ]; then
            end=$len
        fi
        local piece="${text:$pos:$((end - pos))}"

        # Encode special chars for adb shell input text:
        # space -> %s, & -> \&, < -> \<, > -> \>
        local encoded
        encoded=$(printf '%s' "$piece" | sed 's/ /%s/g; s/&/\\&/g; s/</\\</g; s/>/\\>/g')

        _adb shell "input text '$encoded'" 2>/dev/null
        chunk_num=$((chunk_num + 1))

        # Small delay between chunks to avoid buffer overflow
        sleep 0.3

        pos=$end
    done

    info "Done: $len chars input via $chunk_num chunks (size=$chunk_size)."
}

cmd_collect() {
    _require_device >/dev/null

    local timestamp
    timestamp=$(date +%Y%m%d-%H%M%S)
    local outdir="/tmp/android-diag-${timestamp}"
    mkdir -p "$outdir"

    info "Collecting diagnostics to $outdir ..."

    # WiFi dumpsys
    info "  dumpsys wifi..."
    _adb shell "dumpsys wifi" > "$outdir/wifi-dumpsys.txt" 2>/dev/null || warn "Failed to dump wifi"

    # IP addresses
    info "  ip addr..."
    _adb shell "ip addr" > "$outdir/ip-addr.txt" 2>/dev/null || warn "Failed to get ip addr"

    # IP routes
    info "  ip route..."
    _adb shell "ip route" > "$outdir/ip-route.txt" 2>/dev/null || warn "Failed to get ip route"

    # WiFi status via cmd wifi (Android 12+)
    info "  cmd wifi status..."
    _adb shell "cmd wifi status" > "$outdir/wifi-status.txt" 2>/dev/null || true

    # dumpsys connectivity
    info "  dumpsys connectivity..."
    _adb shell "dumpsys connectivity" > "$outdir/connectivity.txt" 2>/dev/null || warn "Failed to dump connectivity"

    # Device properties
    info "  Device properties..."
    _adb shell "getprop" > "$outdir/properties.txt" 2>/dev/null || warn "Failed to get properties"

    # Logcat (last 500 lines, filtered to WiFi-related)
    info "  logcat (last 500 lines)..."
    _adb shell "logcat -d -t 500" > "$outdir/logcat.txt" 2>/dev/null || warn "Failed to get logcat"

    # UI dump (current screen)
    info "  UI dump..."
    _adb shell "uiautomator dump $DUMP_FILE" 2>/dev/null || true
    _adb shell "cat $DUMP_FILE" > "$outdir/ui-dump.xml" 2>/dev/null || warn "Failed to dump UI"

    # Summary
    local file_count
    file_count=$(find "$outdir" -type f | wc -l | tr -d ' ')
    local total_size
    total_size=$(du -sh "$outdir" 2>/dev/null | awk '{print $1}')

    info "=== Collection complete ==="
    info "  Directory: $outdir"
    info "  Files:     $file_count"
    info "  Size:      $total_size"
    info "  NOTE: No secrets written to repo. All output in /tmp/."
}

cmd_status() {
    _require_device >/dev/null
    info "=== WiFi Status ==="

    # Try cmd wifi status first (Android 12+)
    local wifi_status
    wifi_status=$(_adb shell "cmd wifi status" 2>/dev/null || true)
    if [ -n "$wifi_status" ]; then
        echo "$wifi_status"
        echo ""
    fi

    # Connectivity status
    local connectivity
    connectivity=$(_adb shell "dumpsys connectivity | grep -A5 'NetworkAgentInfo'" 2>/dev/null || true)
    if [ -n "$connectivity" ]; then
        echo "--- Active networks ---"
        echo "$connectivity"
        echo ""
    fi

    # WiFi interface IP
    local wifi_ip
    wifi_ip=$(_adb shell "ip addr show wlan0" 2>/dev/null | grep "inet " | awk '{print $2}' || true)
    if [ -n "$wifi_ip" ]; then
        echo "WiFi IP: $wifi_ip"
    else
        echo "WiFi IP: (not connected or wlan0 not found)"
    fi

    # DNS
    local dns
    dns=$(_adb shell "getprop net.dns1" 2>/dev/null | tr -d '\r' || true)
    if [ -n "$dns" ]; then
        echo "DNS: $dns"
    fi

    # Default route
    local gateway
    gateway=$(_adb shell "ip route" 2>/dev/null | grep default | awk '{print $3}' | head -1 || true)
    if [ -n "$gateway" ]; then
        echo "Gateway: $gateway"
    fi

    echo ""
    info "=== End WiFi Status ==="
}

# ---------------------------------------------------------------------------
# Main dispatch
# ---------------------------------------------------------------------------

usage() {
    cat <<EOF
Usage: $0 <command> [args...]

Commands:
  detect              Detect and print connected device info
  wifi                Open WiFi settings on device
  chunk "text" [size] Chunk-input text via adb (default chunk: 30 chars)
  collect             Collect diagnostics to /tmp/android-*
  status              Check WiFi connection status

Environment:
  TOLLGATE_SKIP_HARDWARE=1  Skip all operations (for CI)
  ADB_DEVICE=<serial>       Use specific device serial
  ADB=<path>                Path to adb binary (default: adb)
EOF
}

case "${1:-}" in
    detect)
        cmd_detect
        ;;
    wifi)
        cmd_wifi
        ;;
    chunk)
        cmd_chunk "${2:-}" "${3:-30}"
        ;;
    collect)
        cmd_collect
        ;;
    status)
        cmd_status
        ;;
    -h|--help|help)
        usage
        ;;
    *)
        echo "ERROR: Unknown command '${1:-}'" >&2
        usage >&2
        exit 1
        ;;
esac
