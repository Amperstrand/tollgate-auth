#!/bin/bash
# setup-freeradius.sh — Configure FreeRADIUS for Cashu ecash auth on radius.nodns.shop
# Supports BOTH EAP-TTLS+PAP (token in password) and PEAP+MSCHAPv2 (token in username).
# Run once on the target server as root.
set -euo pipefail

echo ">>> Installing FreeRADIUS..."
apt-get update -qq && apt-get install -y -qq freeradius

echo ">>> Backing up original config..."
cp -a /etc/freeradius/3.0 /etc/freeradius/3.0.bak.$(date +%s)

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CONF_DIR="/etc/freeradius/3.0"

# --- clients.conf ---
echo ">>> Installing clients.conf..."
cp "$SCRIPT_DIR/../config/freeradius/clients.conf" "$CONF_DIR/clients.conf"

# --- users ---
echo ">>> Installing users file..."
cp "$SCRIPT_DIR/../config/freeradius/users" "$CONF_DIR/users"

# --- exec module ---
echo ">>> Installing exec module (cashu-auth)..."
cp "$SCRIPT_DIR/../config/freeradius/mods-available/cashu-exec" "$CONF_DIR/mods-available/cashu-exec"
ln -sf ../mods-available/cashu-exec "$CONF_DIR/mods-enabled/cashu-exec"

# --- accounting exec module ---
echo ">>> Installing accounting exec module (tollgate-acct)..."
cp "$SCRIPT_DIR/../config/freeradius/mods-available/tollgate-acct" "$CONF_DIR/mods-available/tollgate-acct"
ln -sf ../mods-available/tollgate-acct "$CONF_DIR/mods-enabled/tollgate-acct"

# --- delegated-mode wrapper (SECURITY) ---
# FreeRADIUS exec modules call this directly instead of `/bin/sh -c '... %{} ...'`.
# RADIUS attributes are attacker-controlled; if they ever reach a shell parser
# they enable arbitrary command execution before the Go binary starts. The
# wrapper keeps argv boundaries intact and only loads secrets + sets env vars.
# See scripts/check-freeradius-configs.sh for the regression guard.
echo ">>> Installing delegated-mode wrapper (tollgate-auth-radius-delegated)..."
mkdir -p /usr/local/libexec
cp "$SCRIPT_DIR/../scripts/tollgate-auth-radius-delegated-wrapper.sh" \
   /usr/local/libexec/tollgate-auth-radius-delegated
chown root:root /usr/local/libexec/tollgate-auth-radius-delegated
chmod 0755 /usr/local/libexec/tollgate-auth-radius-delegated

# --- EAP config (TTLS+PAP + PEAP+MSCHAPv2 with self-signed cert) ---
echo ">>> Generating self-signed TLS certificate for radius.nodns.shop..."
CERT_DIR="$CONF_DIR/certs"
mkdir -p "$CERT_DIR"

# Generate self-signed cert (phones will show "trust this certificate?" dialog)
if [ ! -f "$CERT_DIR/server.key" ]; then
    openssl req -x509 -newkey rsa:2048 \
        -keyout "$CERT_DIR/server.key" \
        -out "$CERT_DIR/server.crt" \
        -days 365 -nodes \
        -subj "/CN=radius.nodns.shop" 2>/dev/null

    chmod 640 "$CERT_DIR/server.key"
    chown root:freerad "$CERT_DIR/server.key"
fi

echo ">>> Configuring EAP (TTLS+PAP + PEAP+MSCHAPv2)..."
cp "$SCRIPT_DIR/../config/freeradius/mods-available/eap" "$CONF_DIR/mods-available/eap"

# Regenerate DH params if missing
if [ ! -f "$CERT_DIR/dh" ]; then
    echo ">>> Generating DH parameters (this takes a moment)..."
    openssl dhparam -out "$CERT_DIR/dh" 2048 2>/dev/null || true
    chown root:freerad "$CERT_DIR/dh"
fi

# --- inner-tunnel virtual server ---
echo ">>> Configuring inner-tunnel virtual server..."
cp "$SCRIPT_DIR/../config/freeradius/sites-available/inner-tunnel" "$CONF_DIR/sites-available/inner-tunnel"
ln -sf ../sites-available/inner-tunnel "$CONF_DIR/sites-enabled/inner-tunnel"

# --- Enable the default server for outer EAP handling ---
echo ">>> Ensuring default site is enabled..."
ln -sf ../sites-available/default "$CONF_DIR/sites-enabled/default" 2>/dev/null || true

# --- Configure accounting in default site ---
echo ">>> Adding accounting forwarding to default site..."
if ! grep -q "tollgate-acct" "$CONF_DIR/sites-available/default" 2>/dev/null; then
    sed -i '/^[[:space:]]*accounting[[:space:]]*{/a\\ttollgate-acct' "$CONF_DIR/sites-available/default" 2>/dev/null || \
    echo "WARN: Could not add tollgate-acct to default site accounting section. Add manually: accounting { tollgate-acct }"
fi

# --- Data directory with correct permissions ---
echo ">>> Setting up data directory..."
for DIR in /opt/tollgate-auth /opt/cashu-tollgate; do
  mkdir -p "$DIR/radius-sessions" 2>/dev/null || true
  touch "$DIR/radius-spent.txt" "$DIR/radius-tokens.log" 2>/dev/null || true
  chown -R freerad:freerad "$DIR/radius-sessions" "$DIR/radius-spent.txt" "$DIR/radius-tokens.log" 2>/dev/null || true
  chmod 770 "$DIR/radius-sessions" 2>/dev/null || true
  chmod 660 "$DIR/radius-spent.txt" "$DIR/radius-tokens.log" 2>/dev/null || true
done

# --- Session cleanup cron ---
echo ">>> Installing session cleanup cron..."
cat > /etc/cron.d/tollgate-radius-cleanup << 'CRON'
# Every 5 minutes, clean up expired RADIUS sessions
*/5 * * * * root find /opt/tollgate-auth/radius-sessions -name "*.json" -mmin +60 -delete 2>/dev/null ; find /opt/cashu-tollgate/radius-sessions -name "*.json" -mmin +60 -delete 2>/dev/null
CRON

echo ">>> Validating FreeRADIUS config..."
if ! freeradius -XC; then
    echo "ERROR: FreeRADIUS config validation failed. Not restarting."
    echo "       Fix the errors above and re-run scripts/setup-freeradius.sh."
    exit 1
fi

echo ">>> Restarting FreeRADIUS..."
systemctl restart freeradius
sleep 2
systemctl status freeradius --no-pager | tail -5

# --- RadSec (RADIUS over TLS on TCP 2083) ---
echo ""
echo ">>> Setting up RadSec (RADIUS over TLS)..."

# Create LE cert destination directory (sync script will populate it)
LE_CERT_DIR="$CONF_DIR/certs/letsencrypt"
mkdir -p "$LE_CERT_DIR"
chown freerad:freerad "$LE_CERT_DIR"
chmod 0700 "$LE_CERT_DIR"

# Install RadSec site config
if [ -f "$SCRIPT_DIR/../config/freeradius/sites-available/radsec" ]; then
    cp "$SCRIPT_DIR/../config/freeradius/sites-available/radsec" "$CONF_DIR/sites-available/radsec"
    ln -sf ../sites-available/radsec "$CONF_DIR/sites-enabled/radsec"
fi

# Install cert sync script
if [ -f "$SCRIPT_DIR/sync-caddy-certs.sh" ]; then
    cp "$SCRIPT_DIR/sync-caddy-certs.sh" /usr/local/sbin/sync-caddy-certs-to-freeradius
    chmod +x /usr/local/sbin/sync-caddy-certs-to-freeradius
    echo ">>> Installed cert sync script to /usr/local/sbin/sync-caddy-certs-to-freeradius"
fi

# Install systemd timer+service for periodic cert sync
SYSTEMD_DIR="$SCRIPT_DIR/../config/systemd"
if [ -f "$SYSTEMD_DIR/sync-caddy-certs.service" ] && [ -f "$SYSTEMD_DIR/sync-caddy-certs.timer" ]; then
    cp "$SYSTEMD_DIR/sync-caddy-certs.service" /etc/systemd/system/sync-caddy-certs.service
    cp "$SYSTEMD_DIR/sync-caddy-certs.timer" /etc/systemd/system/sync-caddy-certs.timer
    systemctl daemon-reload
    systemctl enable sync-caddy-certs.timer
    echo ">>> Enabled cert sync timer (every 6 hours)"
fi

# Run initial cert sync
if [ -x /usr/local/sbin/sync-caddy-certs-to-freeradius ]; then
    echo ">>> Running initial cert sync..."
    /usr/local/sbin/sync-caddy-certs-to-freeradius || true
fi

# Check if LE certs are now available
if [ -f "$LE_CERT_DIR/nodns.shop.crt" ] && [ -f "$LE_CERT_DIR/nodns.shop.key" ]; then
    # Test config and restart with LE certs
    if freeradius -XC 2>/dev/null; then
        systemctl restart freeradius
        sleep 2
        echo ">>> RadSec enabled on TCP port 2083 (TLS with Let's Encrypt)"
    else
        echo "WARN: RadSec config validation failed, disabling"
        rm -f "$CONF_DIR/sites-enabled/radsec"
        systemctl restart freeradius
    fi
else
    echo ">>> No Let's Encrypt cert found — RadSec site enabled but will fail to start"
    echo ">>> Cert sync timer will pick up certs once Caddy provisions them"
    echo ">>> FreeRADIUS continues using self-signed certs for EAP"
    # Don't restart FreeRADIUS — RadSec will fail without certs, keep it disabled
    rm -f "$CONF_DIR/sites-enabled/radsec"
fi

echo ""
echo ">>> FreeRADIUS configured for radius.nodns.shop"
echo ">>> Listen ports: 1812 (auth UDP), 1813 (acct UDP), 2083 (RadSec TCP/TLS)"
echo ">>> EAP methods:"
echo ">>>   EAP-TTLS+PAP    (recommended — token in password field, no length limit)"
echo ">>>   PEAP+MSCHAPv2   (legacy — token in username field, <253 bytes)"
echo ">>> Accounting: forwarded to session daemon via tollgate-acct exec module"
echo ">>> RadSec: TCP 2083 with Let's Encrypt cert (encrypts entire RADIUS conversation)"
echo ">>> Validation: /usr/local/bin/tollgate-auth-radius"
echo ""
echo ">>> Test with radtest:"
echo ">>>   radtest cashuB... <any-password> localhost 0 tollgate"
echo ">>>   radtest <any-user> cashuB... localhost 0 tollgate"
echo ">>> Test RadSec:"
echo ">>>   radtest -t tls cashuB... <any-password> nodns.shop 2083 radsec"
echo ">>> Test with eapol_test:"
echo ">>>   eapol_test -c /etc/tollgate/eapol-ttls-pap.conf -a 127.0.0.1 -s tollgate"
