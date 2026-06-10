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

echo ">>> Restarting FreeRADIUS..."
systemctl restart freeradius
sleep 2
systemctl status freeradius --no-pager | tail -5

# --- RadSec (RADIUS over TLS on TCP 2083) ---
echo ""
echo ">>> Setting up RadSec (RADIUS over TLS)..."

RADSEC_CERT_DIR="$CONF_DIR/certs/radsec"
mkdir -p "$RADSEC_CERT_DIR"

# Check for existing Let's Encrypt cert (from Caddy or certbot)
CADDY_LE="/var/lib/caddy/.local/share/caddy/certificates/acme-v02.api.letsencrypt.org-directory/nodns.shop"
CERTBOT_LE="/etc/letsencrypt/live/nodns.shop"
LE_SRC=""

if [ -f "$CADDY_LE/nodns.shop.crt" ] && [ -f "$CADDY_LE/nodns.shop.key" ]; then
    LE_SRC="$CADDY_LE"
    echo ">>> Found Caddy Let's Encrypt cert for nodns.shop"
elif [ -f "$CERTBOT_LE/fullchain.pem" ] && [ -f "$CERTBOT_LE/privkey.pem" ]; then
    LE_SRC="$CERTBOT_LE"
    echo ">>> Found certbot Let's Encrypt cert for nodns.shop"
fi

if [ -n "$LE_SRC" ]; then
    cp "$LE_SRC/nodns.shop.crt" "$RADSEC_CERT_DIR/server.crt" 2>/dev/null || \
    cp "$LE_SRC/fullchain.pem" "$RADSEC_CERT_DIR/server.crt" 2>/dev/null

    cp "$LE_SRC/nodns.shop.key" "$RADSEC_CERT_DIR/server.key" 2>/dev/null || \
    cp "$LE_SRC/privkey.pem" "$RADSEC_CERT_DIR/server.key" 2>/dev/null

    chown -R root:freerad "$RADSEC_CERT_DIR"
    chmod 640 "$RADSEC_CERT_DIR/server.key"
    chmod 644 "$RADSEC_CERT_DIR/server.crt"

    # Install RadSec site config
    if [ -f "$SCRIPT_DIR/../config/freeradius/sites-available/radsec" ]; then
        cp "$SCRIPT_DIR/../config/freeradius/sites-available/radsec" "$CONF_DIR/sites-available/radsec"
        ln -sf ../sites-available/radsec "$CONF_DIR/sites-enabled/radsec"
    fi

    # Install RadSec clients.conf
    if [ -f "$SCRIPT_DIR/../config/freeradius/clients.conf" ]; then
        cp "$SCRIPT_DIR/../config/freeradius/clients.conf" "$CONF_DIR/clients.conf"
    fi

    # Cert renewal hook — copy fresh cert from Caddy and reload FreeRADIUS
    mkdir -p /etc/caddy/cert-hooks
    cat > /etc/caddy/cert-hooks/reload-freeradius.sh << 'HOOK'
#!/bin/bash
# Copy fresh Let's Encrypt cert to FreeRADIUS RadSec and reload
RADSEC_DIR="/etc/freeradius/3.0/certs/radsec"
CADDY_DIR="/var/lib/caddy/.local/share/caddy/certificates/acme-v02.api.letsencrypt.org-directory/nodns.shop"

if [ -d "$CADDY_DIR" ]; then
    cp "$CADDY_DIR/nodns.shop.crt" "$RADSEC_DIR/server.crt"
    cp "$CADDY_DIR/nodns.shop.key" "$RADSEC_DIR/server.key"
    chown root:freerad "$RADSEC_DIR/server.key" "$RADSEC_DIR/server.crt"
    chmod 640 "$RADSEC_DIR/server.key"
    chmod 644 "$RADSEC_DIR/server.crt"
    systemctl reload freeradius
fi
HOOK
    chmod +x /etc/caddy/cert-hooks/reload-freeradius.sh

    # Test config and restart
    if freeradius -XC 2>/dev/null; then
        systemctl restart freeradius
        sleep 2
        echo ">>> RadSec enabled on TCP port 2083 (TLS)"
    else
        echo "WARN: RadSec config validation failed, skipping"
        rm -f "$CONF_DIR/sites-enabled/radsec"
        systemctl restart freeradius
    fi
else
    echo ">>> No Let's Encrypt cert found — RadSec not configured"
    echo ">>> To enable RadSec later:"
    echo ">>>   1. Get a cert: certbot certonly --standalone -d nodns.shop"
    echo ">>>   2. Re-run this script"
fi

echo ""
echo ">>> FreeRADIUS configured for radius.nodns.shop"
echo ">>> Listen ports: 1812 (auth UDP), 1813 (acct UDP), 2083 (RadSec TCP/TLS)"
echo ">>> EAP methods:"
echo ">>>   EAP-TTLS+PAP    (recommended — token in password field, no length limit)"
echo ">>>   PEAP+MSCHAPv2   (legacy — token in username field, <253 bytes)"
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
