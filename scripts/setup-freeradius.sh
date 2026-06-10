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

# --- Session cleanup cron ---
echo ">>> Installing session cleanup cron..."
cat > /etc/cron.d/tollgate-radius-cleanup << 'CRON'
# Every 5 minutes, clean up expired RADIUS sessions
*/5 * * * * root find /opt/tollgate-auth/radius-sessions -name "*.json" -mmin +60 -delete 2>/dev/null ; find /opt/cashu-tollgate/radius-sessions -name "*.json" -mmin +60 -delete 2>/dev/null
CRON

echo ">>> Restarting FreeRADIUS..."
systemctl restart freeradius
sleep 2
systemctl status freeradius --no-pager | tail -5

echo ""
echo ">>> FreeRADIUS configured for radius.nodns.shop"
echo ">>> Listen ports: 1812 (auth), 1813 (accounting)"
echo ">>> EAP methods:"
echo ">>>   EAP-TTLS+PAP    (recommended — token in password field, no length limit)"
echo ">>>   PEAP+MSCHAPv2   (legacy — token in username field, <253 bytes)"
echo ">>> Validation: /usr/local/bin/tollgate-auth-radius"
echo ""
echo ">>> Test with radtest:"
echo ">>>   radtest cashuB... <any-password> localhost 0 tollgate"
echo ">>>   radtest <any-user> cashuB... localhost 0 tollgate"
echo ">>> Test with eapol_test:"
echo ">>>   eapol_test -c /etc/tollgate/eapol-ttls-pap.conf -a 127.0.0.1 -s tollgate"
