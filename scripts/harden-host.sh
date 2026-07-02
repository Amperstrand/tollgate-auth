#!/usr/bin/env bash
set -euo pipefail

# Host hardening for semi-public SSH access (tollgate-ssh)
#
# Applies defense-in-depth isolation so that chroot'd nobody:nogroup
# SSH guests cannot:
#   - See other users' processes
#   - See listening ports or network connections
#   - Read login history (last, wtmp, lastlog)
#   - List other user accounts
#   - Make outbound network connections
#   - Exhaust resources (fork bombs, disk fill)
#   - Read system logs
#
# Designed for Debian 12. Idempotent: safe to run multiple times.

VERBOSE=0
[[ "${1:-}" == "-v" || "${1:-}" == "--verbose" ]] && VERBOSE=1

log() { echo "[harden] $*"; }
verbose() { [[ $VERBOSE -eq 1 ]] && echo "[harden] $*" || true; }

if [[ "$(id -u)" -ne 0 ]]; then
    echo "ERROR: Must run as root" >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# 1. /proc hardening: hidepid=2
#    Prevents non-root users from seeing processes owned by other users.
#    Admin users added to 'procview' group retain full visibility.
# ---------------------------------------------------------------------------

harden_proc() {
    log "Hardening /proc with hidepid=2"

    # Create procview group for admin users who need full process visibility
    if ! getent group procview >/dev/null 2>&1; then
        groupadd --system procview
        log "  Created procview group"
    fi

    PROCVIEW_GID=$(getent group procview | cut -d: -f3)

    # Remount /proc immediately
    mount -o remount,rw,nosuid,nodev,noexec,relatime,hidepid=2,gid="$PROCVIEW_GID" /proc
    verbose "  Remounted /proc with hidepid=2,gid=$PROCVIEW_GID"

    # Persist in /etc/fstab (replace existing proc line or add new)
    if grep -qE '^\s*proc\s+/proc\s' /etc/fstab; then
        sed -i "s|^\s*proc\s\+/proc\s\+proc\s\+.*|proc /proc proc defaults,hidepid=2,gid=$PROCVIEW_GID 0 0|" /etc/fstab
        verbose "  Updated existing /proc entry in /etc/fstab"
    else
        echo "proc /proc proc defaults,hidepid=2,gid=$PROCVIEW_GID 0 0" >> /etc/fstab
        verbose "  Added /proc entry to /etc/fstab"
    fi

    # Add admin users to procview group so they can still see all processes
    for user in root inr t4; do
        if id "$user" >/dev/null 2>&1; then
            usermod -aG procview "$user" 2>/dev/null || true
            verbose "  Added $user to procview group"
        fi
    done

    # systemd-logind needs procview to function with hidepid=2
    mkdir -p /etc/systemd/system/systemd-logind.service.d
    cat > /etc/systemd/system/systemd-logind.service.d/hidepid.conf << 'EOF'
[Service]
SupplementaryGroups=procview
EOF
    verbose "  Created systemd-logind override for hidepid compatibility"

    log "  /proc hardened: guests can only see their own processes"
}

# ---------------------------------------------------------------------------
# 2. Login history restriction
#    Prevent guests from seeing who logged in before (last, wtmp, lastlog).
# ---------------------------------------------------------------------------

restrict_login_history() {
    log "Restricting login history files"

    # wtmp and lastlog are world-readable by default — change to root:utmp 640
    chmod 640 /var/log/wtmp /var/log/lastlog 2>/dev/null || true
    chown root:utmp /var/log/wtmp /var/log/lastlog 2>/dev/null || true

    # btmp (failed logins) should also be restricted
    chmod 660 /var/log/btmp 2>/dev/null || true
    chown root:utmp /var/log/btmp 2>/dev/null || true

    # Disable PrintLastLog in sshd_config (for admin SSH on port 22)
    if grep -q '^\s*#*PrintLastLog' /etc/ssh/sshd_config; then
        sed -i 's/^\s*#\?PrintLastLog.*/PrintLastLog no/' /etc/ssh/sshd_config
    else
        echo "PrintLastLog no" >> /etc/ssh/sshd_config
    fi

    log "  wtmp/lastlog/btmp restricted, PrintLastLog disabled"
}

# ---------------------------------------------------------------------------
# 3. Sysctl hardening
#    Kernel-level protections against information disclosure.
# ---------------------------------------------------------------------------

harden_sysctl() {
    log "Applying sysctl hardening"

    mkdir -p /etc/sysctl.d

    cat > /etc/sysctl.d/99-tollgate-harden.conf << 'EOF'
# Tollgate host hardening

# Restrict core dumps from SUID programs (default: 2 is insecure)
fs.suid_dumpable = 0

# Restrict kernel pointer visibility (already 1, enforce)
kernel.kptr_restrict = 1

# Restrict dmesg access (already 1, enforce)
kernel.dmesg_restrict = 1

# Restrict kernel profiling
kernel.perf_event_paranoid = 2

# Protect against symlink/hardlink races in world-writable dirs
fs.protected_symlinks = 1
fs.protected_hardlinks = 1
fs.protected_fifos = 2
fs.protected_regular = 2

# Restrict ptrace to parent-only (already 1, enforce)
kernel.yama.ptrace_scope = 1

# Network hardening
net.ipv4.conf.all.send_redirects = 0
net.ipv4.conf.default.send_redirects = 0
net.ipv4.conf.all.accept_redirects = 0
net.ipv4.conf.default.accept_redirects = 0
net.ipv4.conf.all.accept_source_route = 0
net.ipv4.conf.default.accept_source_route = 0
net.ipv4.tcp_timestamps = 0
EOF

    sysctl --system 2>/dev/null || true
    log "  sysctl hardening applied"
}

# ---------------------------------------------------------------------------
# 4. PAM limits for nobody user
#    Resource limits to prevent fork bombs, disk fill, etc.
# ---------------------------------------------------------------------------

setup_pam_limits() {
    log "Setting PAM resource limits for guests (nobody)"

    cat > /etc/security/limits.d/tollgate-guests.conf << 'EOF'
# Resource limits for tollgate SSH guests (nobody:nogroup)
# Prevents fork bombs, disk exhaustion, and resource monopolization

# Max processes per guest session
nobody    hard    nproc       64
nobody    soft    nproc       32

# Max simultaneous logins (allows multiple sessions but caps abuse)
nobody    hard    maxlogins   20

# Max file size (1 GB — enough for work, prevents disk fill)
nobody    hard    fsize       1048576

# Max CPU seconds (10 minutes — prevents crypto mining)
nobody    hard    cpu         600

# Max open files
nobody    hard    nofile      256
nobody    soft    nofile      128

# Max memory (2 GB virtual)
nobody    hard    as          2147483648

# Disable core dumps
nobody    hard    core        0

# Max file locks
nobody    hard    locks       64

# Max pending signals
nobody    hard    sigpending  64

# Max queued messages
nobody    hard    msgqueue    8192
EOF

    log "  PAM limits set: nproc=64, maxlogins=20, fsize=1G, cpu=600s"
}

# ---------------------------------------------------------------------------
# 5. Restrict network access for nobody
#    Block outbound connections from chroot'd guests.
#    Inbound SSH (port 2222) and existing services are unaffected.
# ---------------------------------------------------------------------------

restrict_network() {
    log "Restricting outbound network for guests (nobody)"

    NOBODY_UID=$(id -u nobody)

    # Use iptables owner match to block outbound from nobody
    # Allow established connections (responses to inbound SSH etc.)
    # Block new outbound TCP/UDP/ICMP from nobody

    # Check if iptables is available
    if ! command -v iptables >/dev/null 2>&1; then
        log "  SKIP: iptables not available"
        return
    fi

    # Create/flush tollgate chain
    iptables -N tollgate-guests 2>/dev/null || iptables -F tollgate-guests

    # Allow loopback (needed for local IPC)
    iptables -A tollgate-guests -o lo -j ACCEPT

    # Allow established/related connections
    iptables -A tollgate-guests -m state --state ESTABLISHED,RELATED -j ACCEPT

    # Allow DNS (needed for mint URL resolution — but only via local resolver)
    iptables -A tollgate-guests -p udp --dport 53 -d 127.0.0.1 -j ACCEPT

    # Block everything else from nobody
    iptables -A tollgate-guests -j REJECT --reject-with icmp-port-unreachable

    # Apply the chain to OUTPUT for nobody's UID
    # Remove old rule if exists
    while iptables -D OUTPUT -m owner --uid-owner "$NOBODY_UID" -j tollgate-guests 2>/dev/null; do :; done
    iptables -A OUTPUT -m owner --uid-owner "$NOBODY_UID" -j tollgate-guests

    # Same for ip6tables if available
    if command -v ip6tables >/dev/null 2>&1; then
        ip6tables -N tollgate-guests 2>/dev/null || ip6tables -F tollgate-guests
        ip6tables -A tollgate-guests -o lo -j ACCEPT
        ip6tables -A tollgate-guests -m state --state ESTABLISHED,RELATED -j ACCEPT
        ip6tables -A tollgate-guests -p udp --dport 53 -d ::1 -j ACCEPT
        ip6tables -A tollgate-guests -j REJECT
        while ip6tables -D OUTPUT -m owner --uid-owner "$NOBODY_UID" -j tollgate-guests 2>/dev/null; do :; done
        ip6tables -A OUTPUT -m owner --uid-owner "$NOBODY_UID" -j tollgate-guests
    fi

    log "  Outbound network blocked for nobody (UID $NOBODY_UID)"
    log "  DNS to local resolver still allowed"
}

# ---------------------------------------------------------------------------
# 6. Restrict systemctl access for non-root
#    Prevent guests from enumerating services.
# ---------------------------------------------------------------------------

restrict_systemctl() {
    log "Restricting systemctl for non-root users"

    # Create polkit rule to deny systemctl access to nobody
    mkdir -p /etc/polkit-1/rules.d

    cat > /etc/polkit-1/rules.d/10-tollgate-guests.rules << 'EOF'
// Deny systemctl access for tollgate SSH guests (nobody)
polkit.addRule(function(action, subject) {
    if (action.id.startsWith("org.freedesktop.systemd1.") &&
        subject.user === "nobody") {
        return polkit.Result.NO;
    }
});
EOF

    log "  systemctl restricted for nobody via polkit"
}

# ---------------------------------------------------------------------------
# 7. Harden SSH config (admin SSH on port 22)
# ---------------------------------------------------------------------------

harden_sshd() {
    log "Hardening admin SSH config (/etc/ssh/sshd_config)"

    SSHD="/etc/ssh/sshd_config"

    set_or_replace() {
        local key="$1" val="$2"
        if grep -qE "^\s*${key}\b" "$SSHD"; then
            sed -i "s|^\s*${key}\b.*|${key} ${val}|" "$SSHD"
        else
            echo "${key} ${val}" >> "$SSHD"
        fi
    }

    set_or_replace "X11Forwarding" "no"
    set_or_replace "AllowTcpForwarding" "no"
    set_or_replace "AllowAgentForwarding" "no"
    set_or_replace "PermitTunnel" "no"
    set_or_replace "MaxAuthTries" "3"
    set_or_replace "ClientAliveInterval" "300"
    set_or_replace "ClientAliveCountMax" "2"
    set_or_replace "PrintLastLog" "no"
    set_or_replace "LoginGraceTime" "30"

    # Validate config before reload
    if sshd -t 2>/dev/null; then
        systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || true
        log "  sshd_config hardened and reloaded"
    else
        log "  WARNING: sshd -t failed, not reloading. Check config manually."
    fi
}

# ---------------------------------------------------------------------------
# 8. Harden /dev/shm and /run
# ---------------------------------------------------------------------------

harden_shm() {
    log "Hardening shared memory (/dev/shm)"

    # Remount /dev/shm with noexec,nosuid,nodev
    mount -o remount,rw,nosuid,nodev,noexec /dev/shm 2>/dev/null || true

    # Persist in fstab
    if grep -qE '^\s*[^#].*/dev/shm' /etc/fstab; then
        sed -i 's|^\(\s*[^#].*/dev/shm\s\+tmpfs\s\+.*\)|\1|' /etc/fstab
        # More robust: replace the whole line
        sed -i "\|/dev/shm|s|^\([^#].*\)|tmpfs /dev/shm tmpfs rw,nosuid,nodev,noexec 0 0|" /etc/fstab
    else
        echo "tmpfs /dev/shm tmpfs rw,nosuid,nodev,noexec 0 0" >> /etc/fstab
    fi

    log "  /dev/shm hardened: noexec,nosuid,nodev"
}

# ---------------------------------------------------------------------------
# 9. Verify hardening
# ---------------------------------------------------------------------------

verify_hardening() {
    log ""
    log "=== Verification ==="

    local pass=0 fail=0

    check() {
        if eval "$2" >/dev/null 2>&1; then
            log "  ✅ $1"
            ((pass++))
        else
            log "  ❌ $1"
            ((fail++))
        fi
    }

    check "hidepid=2 on /proc" \
        'mount | grep "/proc" | grep -qE "hidepid=(2|invisible)"'
    check "procview group exists" \
        'getent group procview'
    check "systemd-logind override" \
        'test -f /etc/systemd/system/systemd-logind.service.d/hidepid.conf'
    check "wtmp is 640" \
        'test "$(stat -c %a /var/log/wtmp)" = "640"'
    check "lastlog is 640" \
        'test "$(stat -c %a /var/log/lastlog)" = "640"'
    check "PrintLastLog disabled" \
        'grep -q "^PrintLastLog no" /etc/ssh/sshd_config'
    check "fs.suid_dumpable=0" \
        'test "$(cat /proc/sys/fs/suid_dumpable)" = "0"'
    check "PAM limits for nobody" \
        'test -f /etc/security/limits.d/tollgate-guests.conf'
    check "iptables tollgate chain" \
        'iptables -L tollgate-guests -n >/dev/null 2>&1'
    check "polkit guest restriction" \
        'test -f /etc/polkit-1/rules.d/10-tollgate-guests.rules'
    check "/dev/shm has noexec" \
        'mount | grep "/dev/shm" | grep -q "noexec"'

    log ""
    log "=== Result: $pass passed, $fail failed ==="

    if [[ $fail -gt 0 ]]; then
        log "Some checks failed. Review output above."
        return 1
    fi
    return 0
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

main() {
    log "Starting host hardening for tollgate-ssh"
    log "Target: semi-public SSH access with nobody:nogroup isolation"
    log ""

    harden_proc
    restrict_login_history
    harden_sysctl
    setup_pam_limits
    restrict_network
    restrict_systemctl
    harden_sshd
    harden_shm

    log ""
    log "Hardening complete. Running verification..."
    log ""

    verify_hardening

    log ""
    log "IMPORTANT: Restart systemd-logind for hidepid compatibility:"
    log "  systemctl restart systemd-logind"
    log ""
    log "Admin users added to 'procview' group. They need to re-login"
    log "for the group membership to take effect."
}

main "$@"
