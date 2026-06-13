#!/usr/bin/env bash
# android-enterprise-ui-smoke.sh — Best-effort UI automation for enterprise WiFi config.
#
# Automates the Android WiFi enterprise dialog to configure EAP-TTLS+PAP
# for RADIUS testing. Uses uiautomator dump to find UI elements by resource-id,
# falling back to coordinate-based taps when resource-id fails.
#
# Environment variables (ALL configuration via env vars — no hardcoded secrets):
#   TOLLGATE_SKIP_HARDWARE=1     — skip all hardware-dependent operations
#   TOLLGATE_TEST_SSID           — SSID to connect to (default: empty = skip)
#   TOLLGATE_TEST_EAP            — EAP method (default: "TTLS")
#   TOLLGATE_TEST_PHASE2         — Phase 2 auth (default: "PAP")
#   TOLLGATE_TEST_IDENTITY       — Identity/username field
#   TOLLGATE_TEST_ANON_IDENTITY  — Anonymous identity field
#   TOLLGATE_TEST_PASSWORD       — Password/secret field
#   ADB_DEVICE                   — Specific device serial (default: auto-detect)
#
# NOTE: This script NEVER writes secrets to the repository.
#       Temp files are on the device (/sdcard/) or in /tmp/ (outside repo).

set -uo pipefail

# --- Skip guard ---
if [ "${TOLLGATE_SKIP_HARDWARE:-0}" = "1" ]; then
    echo "SKIP: TOLLGATE_SKIP_HARDWARE=1 — skipping hardware-dependent script." >&2
    exit 0
fi

# --- Config from env ---
SSID="${TOLLGATE_TEST_SSID:-}"
EAP_METHOD="${TOLLGATE_TEST_EAP:-TTLS}"
PHASE2="${TOLLGATE_TEST_PHASE2:-PAP}"
IDENTITY="${TOLLGATE_TEST_IDENTITY:-}"
ANON_IDENTITY="${TOLLGATE_TEST_ANON_IDENTITY:-}"
PASSWORD="${TOLLGATE_TEST_PASSWORD:-}"
CHUNK_SIZE="${TOLLGATE_CHUNK_SIZE:-30}"

ADB="${ADB:-adb}"
ADB_DEVICE="${ADB_DEVICE:-}"
DUMP_FILE="/sdcard/_tollgate_ui.xml"

# --- Skip if no SSID ---
if [ -z "$SSID" ]; then
    echo "SKIP: TOLLGATE_TEST_SSID not set — no enterprise WiFi to configure." >&2
    exit 0
fi

# Skip if no identity AND no password (nothing to fill in)
if [ -z "$IDENTITY" ] && [ -z "$PASSWORD" ]; then
    echo "SKIP: Neither TOLLGATE_TEST_IDENTITY nor TOLLGATE_TEST_PASSWORD set." >&2
    exit 0
fi

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

_require_adb() {
    if ! command -v "$ADB" >/dev/null 2>&1; then
        die "ADB not found. Install Android Platform Tools and add to PATH."
    fi
}

_require_device() {
    _require_adb
    local devices
    devices=$(_adb devices -l 2>/dev/null | grep -v "^List" | grep -v "^$" | head -1)
    if [ -z "$devices" ]; then
        die "No Android device connected."
    fi
    if [ -z "$ADB_DEVICE" ]; then
        ADB_DEVICE=$(echo "$devices" | awk '{print $1}')
        info "Using device: $ADB_DEVICE"
    fi
}

dump_ui() {
    _adb shell "uiautomator dump $DUMP_FILE" 2>/dev/null
}

get_ui_xml() {
    _adb shell "cat $DUMP_FILE" 2>/dev/null
}

# Find element center by resource-id in XML dump
# Returns "x y" or empty string
find_by_resource_id() {
    local res_id="$1"
    local xml
    xml=$(get_ui_xml)
    local node bounds
    node=$(echo "$xml" | tr '>' '\n' | grep "resource-id=\"${res_id}\"" | head -1)
    if [ -n "$node" ]; then
        bounds=$(echo "$node" | grep -o 'bounds="\[[0-9,]*\]\[[0-9,]*\]"' | head -1)
        if [ -n "$bounds" ]; then
            _bounds_to_center "$bounds"
            return 0
        fi
    fi
    return 1
}

# Find element center by text content
find_by_text() {
    local search="$1"
    local xml
    xml=$(get_ui_xml)
    local node bounds
    # Search by content-desc first, then text attribute
    node=$(echo "$xml" | tr '>' '\n' | grep "desc=\"[^\"]*${search}[^\"]*\"" | grep 'bounds=' | head -1)
    if [ -z "$node" ]; then
        node=$(echo "$xml" | tr '>' '\n' | grep "text=\"${search}\"" | grep 'bounds=' | head -1)
    fi
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

tap_element() {
    local desc="$1"
    shift
    local coords
    coords=$("$@")  # Call the find function
    if [ -z "$coords" ]; then
        warn "Not found: $desc"
        return 1
    fi
    info "Tapping '$desc' at ($coords)"
    _adb shell "input tap $coords"
    sleep 0.5
    return 0
}

# Select a spinner (dropdown) by resource-id, then pick an option by text
select_spinner() {
    local res_id="$1"
    local option_text="$2"
    local fallback_y="${3:-}"  # Optional fallback Y coordinate

    # Try resource-id first
    local coords
    coords=$(find_by_resource_id "$res_id")
    if [ -n "$coords" ]; then
        _adb shell "input tap $coords"
        sleep 1.5
        # Now find the option in the dropdown
        dump_ui >/dev/null 2>&1
        local opt_coords
        opt_coords=$(find_by_text "$option_text")
        if [ -n "$opt_coords" ]; then
            _adb shell "input tap $opt_coords"
            sleep 0.5
            info "Spinner $res_id → $option_text (via resource-id)"
            return 0
        fi
        warn "Option '$option_text' not found in spinner dropdown"
        _adb shell "input keyevent KEYCODE_BACK" 2>/dev/null
        return 1
    fi

    # Fallback: coordinate-based tap
    if [ -n "$fallback_y" ]; then
        warn "Using fallback coordinate for spinner $res_id (y=$fallback_y)"
        _adb shell "input tap 540 $fallback_y"
        sleep 1.5
        dump_ui >/dev/null 2>&1
        local opt_coords
        opt_coords=$(find_by_text "$option_text")
        if [ -n "$opt_coords" ]; then
            _adb shell "input tap $opt_coords"
            sleep 0.5
            info "Spinner → $option_text (via fallback coord)"
            return 0
        fi
        # Last resort: just dismiss
        _adb shell "input keyevent KEYCODE_BACK" 2>/dev/null
        return 1
    fi

    warn "Spinner $res_id not found and no fallback coordinate"
    return 1
}

# Chunked text input — split long strings into chunks to avoid ADB buffer issues
chunked_input() {
    local text="$1"
    local chunk="${2:-$CHUNK_SIZE}"
    local len=${#text}
    local pos=0

    while [ "$pos" -lt "$len" ]; do
        local end=$((pos + chunk))
        [ "$end" -gt "$len" ] && end=$len
        local piece="${text:$pos:$((end - pos))}"

        # Encode special chars for adb input text
        local encoded
        encoded=$(printf '%s' "$piece" | sed 's/ /%s/g; s/&/\\&/g; s/</\\</g; s/>/\\>/g')
        _adb shell "input text '$encoded'" 2>/dev/null
        sleep 0.3

        pos=$end
    done
}

# ---------------------------------------------------------------------------
# Main flow
# ---------------------------------------------------------------------------

main() {
    _require_device

    info "=== Enterprise WiFi UI Smoke Test ==="
    info "SSID:      $SSID"
    info "EAP:       $EAP_METHOD"
    info "Phase 2:   $PHASE2"
    info "Identity:  ${IDENTITY:+set (${#IDENTITY} chars)}"
    info "Password:  ${PASSWORD:+set (${#PASSWORD} chars)}"
    info "Device:    $ADB_DEVICE"
    echo ""

    # Step 1: Open WiFi settings
    info "[1/6] Opening WiFi settings..."
    _adb shell "am start -a android.settings.WIFI_SETTINGS" 2>/dev/null
    sleep 2

    # Step 2: Find and tap the SSID
    info "[2/6] Looking for SSID '$SSID'..."
    local ssid_found=0
    for attempt in $(seq 1 10); do
        dump_ui >/dev/null 2>&1
        local coords
        coords=$(find_by_text "$SSID")
        if [ -n "$coords" ]; then
            info "Found '$SSID' at ($coords), tapping..."
            _adb shell "input tap $coords"
            sleep 2
            ssid_found=1
            break
        fi
        if [ $((attempt % 3)) -eq 0 ]; then
            info "  Waiting for '$SSID'... (attempt $attempt)"
        fi
        sleep 2
    done

    if [ "$ssid_found" -eq 0 ]; then
        die "SSID '$SSID' not found after 10 attempts. Check AP is powered on and broadcasting."
    fi

    # Step 3: Configure enterprise dialog
    info "[3/6] Configuring enterprise dialog..."

    # Wait for dialog to open
    sleep 1
    dump_ui >/dev/null 2>&1

    # Check if enterprise dialog opened (look for EAP method spinner)
    local method_res_id="com.android.settings:id/method"
    local phase2_res_id="com.android.settings:id/phase2"
    local ca_cert_res_id="com.android.settings:id/ca_cert"
    local identity_res_id="com.android.settings:id/identity"
    local password_res_id="com.android.settings:id/password"

    # Verify dialog is open
    local dialog_open=0
    local xml
    xml=$(get_ui_xml)
    if echo "$xml" | grep -q "EAP method"; then
        dialog_open=1
    fi

    if [ "$dialog_open" -eq 0 ]; then
        # Maybe the network is already saved — try long-press to forget and retry
        warn "Enterprise dialog did not open. Network may be saved already."
        warn "Try forgetting the network first, then re-run."
        # Try pressing back to clean up
        _adb shell "input keyevent KEYCODE_BACK" 2>/dev/null
        exit 1
    fi

    # Set EAP method
    info "  EAP → $EAP_METHOD"
    select_spinner "$method_res_id" "$EAP_METHOD" "376" || warn "Failed to set EAP method"

    # Set Phase 2
    info "  Phase 2 → $PHASE2"
    select_spinner "$phase2_res_id" "$PHASE2" "574" || warn "Failed to set Phase 2"

    # Set CA cert to "Do not validate"
    info "  CA cert → Do not validate"
    select_spinner "$ca_cert_res_id" "Do not validate" "772" || warn "Failed to set CA cert"

    # Step 4: Enter identity (if provided)
    if [ -n "$IDENTITY" ]; then
        info "[4/6] Entering identity (${#IDENTITY} chars)..."

        # Scroll up to make identity field visible
        local sv_coords
        sv_coords=$(find_by_resource_id "com.android.settings:id/dialog_scrollview" || true)
        if [ -n "$sv_coords" ]; then
            local sv_x sv_y
            sv_x=$(echo "$sv_coords" | awk '{print $1}')
            sv_y=$(echo "$sv_coords" | awk '{print $2}')
            _adb shell "input swipe $sv_x $((sv_y + 300)) $sv_x $((sv_y - 300)) 500" 2>/dev/null
            sleep 0.5
        fi

        # Find and tap identity field
        local id_coords
        id_coords=$(find_by_resource_id "$identity_res_id")
        if [ -n "$id_coords" ]; then
            _adb shell "input tap $id_coords"
            sleep 0.5
            # Clear field (backspace a few times)
            for _ in $(seq 1 20); do
                _adb shell "input keyevent KEYCODE_DEL" 2>/dev/null
            done
            sleep 0.3
            chunked_input "$IDENTITY"
            info "  Identity entered."
        else
            warn "Identity field not found by resource-id."
        fi
    else
        info "[4/6] No identity configured — skipping."
    fi

    # Enter anonymous identity (if provided)
    if [ -n "$ANON_IDENTITY" ]; then
        info "  Entering anonymous identity (${#ANON_IDENTITY} chars)..."
        local anon_res_id="com.android.settings:id/anonymous_identity"
        local anon_coords
        anon_coords=$(find_by_resource_id "$anon_res_id")
        if [ -n "$anon_coords" ]; then
            _adb shell "input tap $anon_coords"
            sleep 0.5
            for _ in $(seq 1 20); do
                _adb shell "input keyevent KEYCODE_DEL" 2>/dev/null
            done
            sleep 0.3
            chunked_input "$ANON_IDENTITY"
            info "  Anonymous identity entered."
        else
            warn "Anonymous identity field not found."
        fi
    fi

    # Step 5: Enter password (if provided)
    if [ -n "$PASSWORD" ]; then
        info "[5/6] Entering password (${#PASSWORD} chars)..."

        # Scroll down to make password field visible
        local sv_coords
        sv_coords=$(find_by_resource_id "com.android.settings:id/dialog_scrollview" || true)
        if [ -n "$sv_coords" ]; then
            local sv_x sv_y
            sv_x=$(echo "$sv_coords" | awk '{print $1}')
            sv_y=$(echo "$sv_coords" | awk '{print $2}')
            _adb shell "input swipe $sv_x $((sv_y - 300)) $sv_x $((sv_y + 300)) 500" 2>/dev/null
            sleep 0.5
        fi

        # Find and tap password field
        local pw_coords
        pw_coords=$(find_by_resource_id "$password_res_id")
        if [ -n "$pw_coords" ]; then
            _adb shell "input tap $pw_coords"
            sleep 0.5
            # Clear field
            for _ in $(seq 1 30); do
                _adb shell "input keyevent KEYCODE_DEL" 2>/dev/null
            done
            sleep 0.3
            chunked_input "$PASSWORD"
            info "  Password entered via chunked input (chunk=$CHUNK_SIZE)."
        else
            # Fallback: try a known coordinate
            warn "Password field not found by resource-id. Trying fallback coordinate."
            _adb shell "input tap 545 970" 2>/dev/null
            sleep 0.5
            for _ in $(seq 1 30); do
                _adb shell "input keyevent KEYCODE_DEL" 2>/dev/null
            done
            sleep 0.3
            chunked_input "$PASSWORD"
        fi
    else
        info "[5/6] No password configured — skipping."
    fi

    # Step 6: Tap Connect
    info "[6/6] Tapping Connect..."
    sleep 1

    # Scroll down to find Connect button
    local connect_found=0
    for _ in $(seq 1 5); do
        dump_ui >/dev/null 2>&1
        local connect_coords
        connect_coords=$(find_by_text "Connect")
        if [ -n "$connect_coords" ]; then
            _adb shell "input tap $connect_coords"
            connect_found=1
            info "Connect tapped."
            break
        fi
        # Scroll down
        sv_coords=$(find_by_resource_id "com.android.settings:id/dialog_scrollview" || true)
        if [ -n "$sv_coords" ]; then
            local sv_x sv_y
            sv_x=$(echo "$sv_coords" | awk '{print $1}')
            sv_y=$(echo "$sv_coords" | awk '{print $2}')
            _adb shell "input swipe $sv_x $((sv_y - 200)) $sv_x $((sv_y + 200)) 500" 2>/dev/null
            sleep 0.5
        fi
    done

    if [ "$connect_found" -eq 0 ]; then
        # Fallback coordinate for Connect button
        warn "Connect button not found. Trying fallback coordinate."
        _adb shell "input tap 878 1273" 2>/dev/null
    fi

    # Wait for connection
    info "Waiting for connection (8 seconds)..."
    sleep 8

    # Check status
    info "Checking WiFi status..."
    _adb shell "am start -a android.settings.WIFI_SETTINGS" 2>/dev/null
    sleep 2
    dump_ui >/dev/null 2>&1

    local status_xml
    status_xml=$(get_ui_xml)
    if echo "$status_xml" | grep -q "Connected"; then
        info "=== SUCCESS: Connected to $SSID ==="
    elif echo "$status_xml" | grep -q "Connecting"; then
        info "=== PENDING: Still connecting to $SSID ==="
    elif echo "$status_xml" | grep -q "Disabled\|Failed"; then
        warn "=== FAILED: Connection to $SSID was rejected or disabled ==="
        exit 1
    else
        info "=== Status unclear — check phone screen ==="
    fi

    info "Done."
}

main
