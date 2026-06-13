#!/usr/bin/env bash
# phone.sh — ADB UI automation helpers for Android WiFi testing.
#
# Source this file to get helper functions, or run directly to list commands.
#
# Environment variables:
#   TOLLGATE_SKIP_HARDWARE=1  — skip all hardware-dependent operations
#   ADB_DEVICE                — specific device serial (default: auto-detect)
#
# NOTE: This script NEVER writes secrets to the repository.

set -uo pipefail

# --- Skip guard ---
if [ "${TOLLGATE_SKIP_HARDWARE:-0}" = "1" ]; then
    echo "SKIP: TOLLGATE_SKIP_HARDWARE=1 — phone.sh functions disabled." >&2
    return 0 2>/dev/null || exit 0
fi

ADB="${ADB:-adb}"
ADB_DEVICE="${ADB_DEVICE:-}"
DUMP_FILE="/sdcard/phone.xml"

_adb() {
    if [ -n "$ADB_DEVICE" ]; then
        "$ADB" -s "$ADB_DEVICE" "$@"
    else
        "$ADB" "$@"
    fi
}

_require_adb() {
    if ! command -v "$ADB" >/dev/null 2>&1; then
        echo "ERROR: ADB not found" >&2
        return 1
    fi
    local devices
    devices=$(_adb devices 2>/dev/null | grep -v "^List" | grep -v "^$" | head -1)
    if [ -z "$devices" ]; then
        echo "ERROR: No Android device connected" >&2
        return 1
    fi
    if [ -z "$ADB_DEVICE" ]; then
        ADB_DEVICE=$(echo "$devices" | awk '{print $1}')
    fi
}

_phone_dump_xml() {
    _adb shell "uiautomator dump $DUMP_FILE 2>/dev/null && cat $DUMP_FILE"
}

_find_bounds() {
    local search="$1"
    local xml node bounds
    xml=$(_phone_dump_xml)
    node=$(echo "$xml" | tr '>' '\n' | grep "desc=\"[^\"]*${search}[^\"]*\"" | grep 'bounds=' | head -1)
    if [ -n "$node" ]; then
        bounds=$(echo "$node" | grep -o 'bounds="\[[0-9,]*\]\[[0-9,]*\]"' | head -1)
        if [ -n "$bounds" ]; then
            _bounds_to_center "$bounds"
            return 0
        fi
    fi
    node=$(echo "$xml" | tr '>' '\n' | grep "text=\"${search}\"" | grep 'bounds=' | head -1)
    if [ -n "$node" ]; then
        bounds=$(echo "$node" | grep -o 'bounds="\[[0-9,]*\]\[[0-9,]*\]"' | head -1)
        if [ -n "$bounds" ]; then
            _bounds_to_center "$bounds"
            return 0
        fi
    fi
    return 1
}

_bounds_to_center() {
    local bounds="$1"
    local x1 y1 x2 y2
    x1=$(echo "$bounds" | sed 's/.*\[\([0-9]*\),\([0-9]*\)\]\[\([0-9]*\),\([0-9]*\)\].*/\1/')
    y1=$(echo "$bounds" | sed 's/.*\[\([0-9]*\),\([0-9]*\)\]\[\([0-9]*\),\([0-9]*\)\].*/\2/')
    x2=$(echo "$bounds" | sed 's/.*\[\([0-9]*\),\([0-9]*\)\]\[\([0-9]*\),\([0-9]*\)\].*/\3/')
    y2=$(echo "$bounds" | sed 's/.*\[\([0-9]*\),\([0-9]*\)\]\[\([0-9]*\),\([0-9]*\)\].*/\4/')
    
    local cx=$(( (x1 + x2) / 2 ))
    local cy=$(( (y1 + y2) / 2 ))
    echo "$cx $cy"
}


phone_find() {
    local search="$1"
    _require_adb >/dev/null || return 1
    local result
    result=$(_find_bounds "$search")
    if [ -n "$result" ]; then
        echo "FOUND: $search at ($result)"
    else
        echo "NOT FOUND: $search" >&2
        return 1
    fi
}

phone_tap() {
    local search="$1"
    _require_adb >/dev/null || return 1
    _adb shell "uiautomator dump $DUMP_FILE" 2>/dev/null
    local coords
    coords=$(_find_bounds "$search")
    if [ -z "$coords" ]; then
        echo "NOT FOUND: $search" >&2
        return 1
    fi
    echo "Tapping '$search' at ($coords)"
    _adb shell "input tap $coords"
}

phone_longpress() {
    local search="$1"
    _require_adb >/dev/null || return 1
    _adb shell "uiautomator dump $DUMP_FILE" 2>/dev/null
    local coords
    coords=$(_find_bounds "$search")
    if [ -z "$coords" ]; then
        echo "NOT FOUND: $search" >&2
        return 1
    fi
    echo "Long-pressing '$search' at ($coords)"
    local x="${coords%% *}" y="${coords##* }"
    _adb shell "input swipe $x $y $x $y 1000"
}

phone_tap_at() {
    local x="$1" y="$2"
    _require_adb >/dev/null || return 1
    _adb shell "input tap $x $y"
}

phone_type_text() {
    local text="$1"
    local encoded
    encoded=$(printf '%s' "$text" | sed 's/ /%s/g; s/&/\\&/g; s/</\\</g; s/>/\\>/g')
    _adb shell "input text '$encoded'"
}

phone_back() {
    _require_adb >/dev/null || return 1
    _adb shell "input keyevent KEYCODE_BACK"
}

phone_wifi_settings() {
    _require_adb >/dev/null || return 1
    _adb shell "am start -a android.settings.WIFI_SETTINGS"
    sleep 2
}

phone_wifi_toggle() {
    _require_adb >/dev/null || return 1
    _adb shell "svc wifi disable"
    sleep 2
    _adb shell "svc wifi enable"
    sleep 5
}

phone_wifi_status() {
    local ssid="${1:-}"
    _require_adb >/dev/null || return 1
    _adb shell "uiautomator dump $DUMP_FILE" 2>/dev/null
    local xml
    xml=$(_adb shell "cat $DUMP_FILE" 2>/dev/null)
    echo "$xml" | tr '>' '\n' | grep -o "desc=\"${ssid}[^\"]*\"" | head -1 | sed 's/desc="//;s/"$//'
}

phone_read_field() {
    local label="$1"
    _require_adb >/dev/null || return 1
    _adb shell "uiautomator dump $DUMP_FILE" 2>/dev/null
    local xml
    xml=$(_adb shell "cat $DUMP_FILE" 2>/dev/null)
    echo "$xml" | tr '>' '\n' | grep -A1 "text=\"${label}\"" | grep -o 'text="[^"]*"' | tail -1 | sed 's/text="//;s/"$//'
}

phone_forget() {
    local ssid="${1:-}"
    _require_adb >/dev/null || return 1
    if [ -z "$ssid" ]; then
        echo "ERROR: SSID required" >&2
        return 1
    fi
    echo "Forgetting $ssid..."
    
    phone_wifi_settings
    sleep 2
    _adb shell "uiautomator dump $DUMP_FILE" 2>/dev/null
    
    local coords
    coords=$(_find_bounds "$ssid")
    if [ -z "$coords" ]; then
        echo "NOT FOUND: $ssid in WiFi list" >&2
        return 1
    fi
    
    local x="${coords%% *}" y="${coords##* }"
    _adb shell "input swipe $x $y $x $y 1000"
    sleep 1.5
    
    _adb shell "uiautomator dump $DUMP_FILE" 2>/dev/null
    coords=$(_find_bounds "Forget")
    if [ -z "$coords" ]; then
        echo "Forget button not found" >&2
        return 1
    fi
    
    _adb shell "input tap $coords"
    sleep 1
    echo "Forgot $ssid"
}

phone_connect_enterprise() {
    local ssid="${1:-}"
    local token="$2"
    
    _require_adb >/dev/null || return 1
    
    if [ -z "$ssid" ]; then
        echo "ERROR: SSID required" >&2
        return 1
    fi
    if [ -z "$token" ]; then
        echo "ERROR: Token required" >&2
        return 1
    fi
    
    echo "=== Connecting to $ssid with token ==="
    
    phone_wifi_settings
    sleep 2
    _adb shell "uiautomator dump $DUMP_FILE" 2>/dev/null
    
    local coords
    coords=$(_find_bounds "$ssid")
    if [ -z "$coords" ]; then
        echo "ERROR: $ssid not found in scan results" >&2
        return 1
    fi
    echo "Tapping $ssid..."
    _adb shell "input tap $coords"
    sleep 2
    
    _adb shell "uiautomator dump $DUMP_FILE" 2>/dev/null
    
    local eap_check
    eap_check=$(_adb shell "cat $DUMP_FILE" 2>/dev/null | tr '>' '\n' | grep -c "EAP method")
    if [ "$eap_check" -eq 0 ]; then
        echo "ERROR: Enterprise dialog did not open" >&2
        return 1
    fi
    
    echo "Setting EAP to TTLS..."
    _select_spinner_option 376 "TTLS"
    
    echo "Setting Phase 2 to PAP..."
    _select_spinner_option 574 "PAP"
    
    echo "Setting CA cert to Do not validate..."
    _select_spinner_option 772 "Do not validate"
    
    echo "Entering token..."
    _adb shell "input tap 545 970"
    sleep 0.5
    _adb shell "input text '$token'"
    sleep 1
    
    echo "Connecting..."
    _adb shell "input tap 878 1273"  # Connect button
    sleep 8
    
    _adb shell "uiautomator dump $DUMP_FILE" 2>/dev/null
    local wifi_status
    wifi_status=$(phone_wifi_status "$ssid")
    echo "Status: $wifi_status"

    if echo "$wifi_status" | grep -q "Connected"; then
        echo "=== SUCCESS: Connected to $ssid ==="
        return 0
    elif echo "$wifi_status" | grep -q "Connecting"; then
        echo "=== PENDING: Still connecting... ==="
        return 0
    else
        echo "=== FAILED: $wifi_status ===" >&2
        return 1
    fi
}

_select_spinner_option() {
    local spinner_y="$1"
    local option_text="$2"
    _adb shell "input tap 540 $spinner_y"
    sleep 1.5
    local coords
    coords=$(_find_bounds "$option_text")
    if [ -z "$coords" ]; then
        echo "Spinner option '$option_text' not found" >&2
        return 1
    fi
    local x="${coords%% *}" y="${coords##* }"
    _adb shell "input tap $x $y"
    sleep 1
}

echo "phone.sh loaded. Commands: phone_find, phone_tap, phone_longpress, phone_forget, phone_connect_enterprise, phone_wifi_status, phone_wifi_settings, phone_wifi_toggle, phone_back, phone_tap_at, phone_type_text, phone_read_field"
