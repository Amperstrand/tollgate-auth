#!/usr/bin/env python3
"""
phone_connect.py — ADB automation for entering Cashu tokens into Android WiFi
enterprise dialogs. Tested on Motorola moto g(8) plus (Android 10).

Solves the token corruption problem: `adb shell input text` silently corrupts
long strings (230+ chars). This script uses clipboard-based entry with multiple
fallback strategies.

Usage:
    python3 scripts/phone_connect.py <cashu-token>

    # With specific device:
    ADB_DEVICE=ZY2277FW6G python3 scripts/phone_connect.py cashuB...
"""

import os
import re
import subprocess
import sys
import tempfile
import time
import xml.etree.ElementTree as ET

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

ADB_BIN = "adb"
DEVICE = os.environ.get("ADB_DEVICE", "ZY2277FW6G")
DUMP_PATH = "/sdcard/_tollgate_ui.xml"
TOKEN_PATH = "/sdcard/_tollgate_token.txt"
HELPER_PATH = "/sdcard/_tollgate_type.sh"
SSID = "TollGate-Test"

# Android keycodes
KEYCODE_BACK = 4
KEYCODE_DEL = 67
KEYCODE_PASTE = 279
KEYCODE_MOVE_END = 123  # KEYCODE_MOVE_END

# Timing
FAST = 0.3
MED = 0.6
SLOW = 1.0
VSLOW = 2.0

# Resource IDs in the enterprise WiFi dialog
RES_METHOD = "com.android.settings:id/method"
RES_PHASE2 = "com.android.settings:id/phase2"
RES_CA_CERT = "com.android.settings:id/ca_cert"
RES_IDENTITY = "com.android.settings:id/identity"
RES_PASSWORD = "com.android.settings:id/password"
RES_SHOW_PW = "com.android.settings:id/show_password"
RES_SCROLLVIEW = "com.android.settings:id/dialog_scrollview"


# ---------------------------------------------------------------------------
# Low-level ADB helpers
# ---------------------------------------------------------------------------

def adb(args, capture=True, timeout=30):
    """Run an adb command. Returns stripped stdout if capture=True."""
    cmd = [ADB_BIN, "-s", DEVICE] + args
    if capture:
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
        return r.stdout.strip() or ""
    subprocess.run(cmd, timeout=timeout)
    return ""


def shell(cmd_str, **kw):
    """Shortcut: adb shell <cmd_str>."""
    return adb(["shell", cmd_str], **kw)


def tap(x, y):
    shell(f"input tap {x} {y}", capture=False)


def keyevent(code):
    shell(f"input keyevent {code}", capture=False)


def sleep(t):
    time.sleep(t)


# ---------------------------------------------------------------------------
# UI dump and XML parsing
# ---------------------------------------------------------------------------

def dump_ui():
    """Dump the UI hierarchy and return the raw XML string."""
    shell(f"uiautomator dump {DUMP_PATH} 2>/dev/null", capture=False)
    sleep(FAST)
    return shell(f"cat {DUMP_PATH}")


def _parse_bounds(bounds_str):
    """Parse '[x1,y1][x2,y2]' → (cx, cy) center."""
    m = re.match(r"\[(\d+),(\d+)\]\[(\d+),(\d+)\]", bounds_str)
    if not m:
        return None
    x1, y1, x2, y2 = int(m.group(1)), int(m.group(2)), int(m.group(3)), int(m.group(4))
    return ((x1 + x2) // 2, (y1 + y2) // 2)


def find_bounds(xml_str, resource_id=None, text=None, desc_contains=None):
    """Find an element in the UI XML and return its center (cx, cy) or None."""
    try:
        root = ET.fromstring(xml_str)
    except ET.ParseError:
        return None

    for node in root.iter():
        if resource_id is not None and node.get("resource-id") != resource_id:
            continue
        if text is not None and node.get("text") != text:
            continue
        if desc_contains is not None:
            cd = node.get("content-desc") or ""
            if desc_contains not in cd:
                continue
        b = node.get("bounds")
        if b:
            return _parse_bounds(b)
    return None


def find_node(xml_str, resource_id=None, text=None):
    """Return the first matching node's full attribute dict, or None."""
    try:
        root = ET.fromstring(xml_str)
    except ET.ParseError:
        return None

    for node in root.iter():
        if resource_id is not None and node.get("resource-id") != resource_id:
            continue
        if text is not None and node.get("text") != text:
            continue
        return dict(node.attrib)
    return None


def tap_element(resource_id=None, text=None, desc_contains=None, retries=3):
    """Find element by criteria and tap it. Returns True on success."""
    for _ in range(retries):
        xml = dump_ui()
        c = find_bounds(xml, resource_id=resource_id, text=text,
                        desc_contains=desc_contains)
        if c:
            tap(*c)
            sleep(MED)
            return True
        sleep(SLOW)
    return False


def scroll_in_dialog(direction="down", amount=300):
    """Scroll within the dialog's ScrollView."""
    xml = dump_ui()
    sv = find_bounds(xml, resource_id=RES_SCROLLVIEW)
    if not sv:
        return
    x = sv[0]
    if direction == "down":
        shell(f"input swipe {x} {sv[1] + amount} {x} {sv[1] - amount} 500")
    else:
        shell(f"input swipe {x} {sv[1] - amount} {x} {sv[1] + amount} 500")
    sleep(MED)


def scroll_to_element(resource_id, max_scrolls=8):
    """Scroll down until the element is visible."""
    for _ in range(max_scrolls):
        xml = dump_ui()
        if find_bounds(xml, resource_id=resource_id):
            return True
        scroll_in_dialog("down", 250)
    return False


# ---------------------------------------------------------------------------
# Text input strategies
# ---------------------------------------------------------------------------

def _push_text_to_device(text):
    """Write *text* to a temp file on the host, push to device."""
    fd, path = tempfile.mkstemp(suffix=".txt")
    try:
        with os.fdopen(fd, "w") as f:
            f.write(text)
        adb(["push", path, TOKEN_PATH], capture=False)
    finally:
        os.unlink(path)


def _input_text_shell(text):
    """`adb shell input text` with basic escaping for base64url tokens."""
    # Characters that need escaping in `input text`:
    #   space → %s,  % → \%,  & → \&,  < → \<,  > → \>
    # base64url uses only A-Z a-z 0-9 - _ so only spaces are a concern,
    # but we escape the others defensively.
    safe = (
        text.replace(" ", "%s")
            .replace("&", "\\&")
            .replace("<", "\\<")
            .replace(">", "\\>")
    )
    shell(f"input text '{safe}'", capture=False)


def clear_field(n=5):
    """Press backspace *n* times to clear current focus field."""
    for _ in range(n):
        keyevent(KEYCODE_DEL)
        sleep(0.05)


def select_all_and_delete():
    """Select-all via long-press → select all → delete. More thorough."""
    # Long press to trigger action mode
    # (Coordinates already at the field from prior tap.)
    # Then tap "Select all" from the context menu/toolbar
    xml = dump_ui()
    sa = find_bounds(xml, desc_contains="Select all")
    if sa:
        tap(*sa)
        sleep(FAST)
        keyevent(KEYCODE_DEL)
        return True
    # Fallback: just hit delete many times
    clear_field(40)
    return False


# ---- Strategy 1: clipboard via service call ----

def clipboard_service_call(text):
    """Set device clipboard using `service call clipboard`.

    Android 10 IClipboard transaction 1 = setPrimaryClip(ClipData, String).

    ClipData parcel layout:
      hasDescription: i32 1
      ClipDescription:
        mimeTypes: i32 1, s16 "text/plain"
        timestamp: i64 0
      itemCount: i32 1
      Item:
        hasText: i32 1, s16 "<text>"
        hasIntent: i32 0
        hasUri: i32 0
      callingPackage: s16 "com.android.shell"
    """
    _push_text_to_device(text)
    # Read from device file to avoid host-shell quoting issues with 230 chars.
    # Use $(cat ...) inside the device shell.
    cmd = (
        'service call clipboard 1'
        ' i32 1'                    # has ClipDescription
        ' i32 1'                    # mimeTypes count
        ' s16 text/plain'           # mimeType[0]
        ' i64 0'                    # timestamp
        ' i32 1'                    # item count
        ' i32 1'                    # item has text
        f' s16 "$(cat {TOKEN_PATH})"'  # ← text from file
        ' i32 0'                    # no intent
        ' i32 0'                    # no uri
        ' s16 com.android.shell'    # callingPackage
    )
    out = shell(cmd)
    # Success looks like: "Result: Parcel(...)"
    return "Result:" in out and "Exception" not in out


# ---- Strategy 2: clipboard via broadcast ----

def clipboard_broadcast(text):
    """Try setting clipboard via standard broadcasts."""
    _push_text_to_device(text)
    for action in [
        "clipper.set",
        "android.intent.action.SET_CLIPBOARD",
    ]:
        out = shell(
            f'am broadcast -a {action}'
            f' --es text "$(cat {TOKEN_PATH})"'
        )
        if "result=0" in out or "result=-1" in out:
            return True
    return False


def paste_clipboard():
    """Simulate Ctrl+V / paste in the focused field."""
    keyevent(KEYCODE_PASTE)
    sleep(SLOW)


# ---- Strategy 3: device-side shell loop ----

def type_via_device_loop(text, chunk=20, delay=2.0):
    """Push token to device, run a local shell loop that types in chunks."""
    _push_text_to_device(text)

    script = (
        "#!/system/bin/sh\n"
        f"T=$(cat {TOKEN_PATH})\n"
        "L=${#T}\n"
        f"C={chunk}\n"
        "P=0\n"
        "while [ $P -lt $L ]; do\n"
        '  input text "${T:$P:$C}"\n'
        f"  sleep {delay}\n"
        "  P=$((P + C))\n"
        "done\n"
    )

    fd, path = tempfile.mkstemp(suffix=".sh")
    try:
        with os.fdopen(fd, "w") as f:
            f.write(script)
        adb(["push", path, HELPER_PATH], capture=False)
    finally:
        os.unlink(path)

    shell(f"sh {HELPER_PATH}", capture=False, timeout=120)


# ---- Strategy 4: host-side chunked input ----

def type_via_host_chunks(text, chunk=15, delay=3.0):
    """Type from the host in small chunks with long inter-chunk delays."""
    for i in range(0, len(text), chunk):
        _input_text_shell(text[i : i + chunk])
        sleep(delay)


# ---------------------------------------------------------------------------
# Spinner selection
# ---------------------------------------------------------------------------

def select_spinner(spinner_res_id, option_text):
    """Tap a spinner, then tap the desired option in the dropdown."""
    xml = dump_ui()
    c = find_bounds(xml, resource_id=spinner_res_id)
    if not c:
        print(f"    WARNING: spinner {spinner_res_id} not found")
        return False
    tap(*c)
    sleep(SLOW)  # wait for dropdown animation

    xml = dump_ui()
    oc = find_bounds(xml, text=option_text)
    if not oc:
        print(f"    WARNING: option '{option_text}' not found in dropdown")
        return False
    tap(*oc)
    sleep(MED)
    return True


# ---------------------------------------------------------------------------
# Password verification
# ---------------------------------------------------------------------------

def verify_password(expected):
    """Toggle 'Show password', read the field, compare. Returns True on match."""
    xml = dump_ui()
    show = find_bounds(xml, resource_id=RES_SHOW_PW)

    if show:
        tap(*show)
        sleep(MED)

    xml = dump_ui()
    node = find_node(xml, resource_id=RES_PASSWORD)
    actual = node.get("text", "") if node else ""

    # Toggle back off
    if show:
        tap(*show)
        sleep(FAST)

    if actual == expected:
        print(f"    PASS verified ({len(actual)} chars)")
        return True

    print(f"    FAIL mismatch: expected {len(expected)} chars, got {len(actual)}")
    if len(actual) != len(expected):
        print(f"    delta = {len(actual) - len(expected):+d} chars")
    print(f"    expected[:30] = {expected[:30]}")
    print(f"    actual  [:30] = {actual[:30]}")
    print(f"    expected[-30:] = ...{expected[-30:]}")
    print(f"    actual  [-30:] = ...{actual[-30:]}")
    return False


# ---------------------------------------------------------------------------
# High-level flow
# ---------------------------------------------------------------------------

def forget_network():
    """Forget existing TollGate-Test network if saved."""
    print("[1/7] Forgetting existing network ...")
    shell("am start -a android.settings.WIFI_SETTINGS", capture=False)
    sleep(SLOW)

    xml = dump_ui()
    c = find_bounds(xml, desc_contains=SSID) or find_bounds(xml, text=SSID)
    if not c:
        print("  Not found in list — nothing to forget")
        keyevent(KEYCODE_BACK)
        return

    # Long press → Forget
    x, y = c
    shell(f"input swipe {x} {y} {x} {y} 1000", capture=False)
    sleep(SLOW)

    if tap_element(text="Forget"):
        print(f"  Forgot {SSID}")
    else:
        print("  Forget button not found (may not be saved)")
        keyevent(KEYCODE_BACK)
    sleep(MED)


def open_wifi_and_tap_ssid():
    """Open WiFi settings and tap TollGate-Test. Returns True on success."""
    print("[2/7] Opening WiFi settings ...")
    shell("am start -a android.settings.WIFI_SETTINGS", capture=False)
    sleep(SLOW)

    for attempt in range(15):
        xml = dump_ui()
        c = find_bounds(xml, desc_contains=SSID) or find_bounds(xml, text=SSID)
        if c:
            print(f"  Found {SSID}, tapping ...")
            tap(*c)
            sleep(SLOW)
            return True
        if attempt % 3 == 0:
            print(f"  Waiting for {SSID} ... ({attempt + 1})")
        sleep(SLOW)

    print(f"  ERROR: {SSID} not found after 15 attempts")
    return False


def configure_dialog():
    """Set spinners: TTLS / PAP / Do not validate. Returns True on success."""
    print("[3/7] Configuring enterprise dialog ...")

    xml = dump_ui()
    if not find_node(xml, resource_id=RES_METHOD):
        print("  ERROR: enterprise dialog did not open")
        return False

    print("  EAP → TTLS")
    select_spinner(RES_METHOD, "TTLS")
    print("  Phase 2 → PAP")
    select_spinner(RES_PHASE2, "PAP")
    print("  CA cert → Do not validate")
    select_spinner(RES_CA_CERT, "Do not validate")
    return True


def enter_identity():
    """Scroll to top, fill Identity field with 'anonymous'."""
    print("[4/7] Entering identity ...")

    # Scroll to top to make Identity visible
    scroll_in_dialog("up", 400)
    sleep(MED)

    xml = dump_ui()
    c = find_bounds(xml, resource_id=RES_IDENTITY)
    if not c:
        print("  WARNING: Identity field not found")
        return

    tap(*c)
    sleep(FAST)
    clear_field(20)
    sleep(FAST)
    _input_text_shell("anonymous")
    sleep(MED)
    print("  Entered 'anonymous'")


def enter_token(token):
    """Enter the 230-char token using progressively slower/safer methods.

    Returns True if the password field matches the token after entry.
    """
    print(f"[5/7] Entering token ({len(token)} chars) ...")

    # Ensure password field is visible and focused
    scroll_to_element(RES_PASSWORD, max_scrolls=10)
    xml = dump_ui()
    pw = find_bounds(xml, resource_id=RES_PASSWORD)
    if not pw:
        print("  ERROR: Password field not found")
        return False

    tap(*pw)
    sleep(FAST)
    clear_field(30)
    sleep(FAST)

    strategies = [
        ("clipboard service call", lambda: clipboard_service_call(token)),
        ("clipboard broadcast",    lambda: clipboard_broadcast(token)),
        ("device-side chunks",     lambda: type_via_device_loop(token, chunk=20, delay=2.0)),
        ("host-side chunks",       lambda: type_via_host_chunks(token, chunk=15, delay=3.0)),
    ]

    for name, fn in strategies:
        print(f"  Trying: {name} ...")
        try:
            fn()
        except Exception as exc:
            print(f"    Exception: {exc}")
            continue

        if name.startswith("clipboard"):
            paste_clipboard()

        sleep(VSLOW)
        if verify_password(token):
            print(f"  SUCCESS via {name}")
            return True

        # Mismatch — clear and try next strategy
        print(f"  Mismatch after {name}, clearing ...")
        tap(*pw)
        sleep(FAST)
        select_all_and_delete()
        sleep(MED)

    print("  ERROR: all strategies failed to enter the token correctly")
    return False


def tap_connect():
    """Scroll to Connect button and tap it."""
    print("[6/7] Tapping Connect ...")

    # Button might be below visible area
    for _ in range(5):
        xml = dump_ui()
        c = find_bounds(xml, text="Connect")
        if c:
            tap(*c)
            print("  Tapped Connect")
            return True
        scroll_in_dialog("down", 200)

    print("  WARNING: Connect button not found")
    return False


def check_status():
    """Wait and report WiFi connection status."""
    print("[7/7] Checking connection status ...")
    sleep(8)

    shell("am start -a android.settings.WIFI_SETTINGS", capture=False)
    sleep(SLOW)

    xml = dump_ui()
    # Look for the SSID in the summary text
    raw = xml if isinstance(xml, str) else ""
    for keyword in ["Connected", "Connecting", "Disabled", "Saved", "Failed"]:
        if keyword in raw:
            print(f"  Status: {keyword}")
            if "Connected" in keyword:
                print("  SUCCESS!")
            return

    # Try content-desc that starts with SSID
    try:
        root = ET.fromstring(raw)
        for node in root.iter():
            cd = node.get("content-desc") or ""
            if SSID in cd:
                print(f"  Status: {cd}")
                if "Connected" in cd:
                    print("  SUCCESS!")
                return
    except ET.ParseError:
        pass

    print("  Could not determine status — check phone screen")


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    if len(sys.argv) < 2:
        print(f"Usage: {sys.argv[0]} <cashu-token>")
        sys.exit(1)

    token = sys.argv[1].strip()
    if len(token) < 50:
        print(f"ERROR: token too short ({len(token)} chars, expected ~230)")
        sys.exit(1)

    print(f"Token length: {len(token)} chars")
    print(f"  start: {token[:40]}")
    print(f"  end:   ...{token[-40:]}")
    print(f"Device:  {DEVICE}")
    print()

    forget_network()

    if not open_wifi_and_tap_ssid():
        sys.exit(1)

    if not configure_dialog():
        sys.exit(1)

    enter_identity()

    if not enter_token(token):
        print("\nAborting — password verification failed")
        sys.exit(1)

    tap_connect()
    check_status()
    print("\nDone.")


if __name__ == "__main__":
    main()
