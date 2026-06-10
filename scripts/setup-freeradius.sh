#!/bin/bash
# setup-freeradius.sh — Configure FreeRADIUS for Cashu ecash auth on radius.nodns.shop
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
echo ">>> Installing exec module..."
cp "$SCRIPT_DIR/../config/freeradius/mods-available/cashu-exec" "$CONF_DIR/mods-available/cashu-exec"
ln -sf ../mods-available/cashu-exec "$CONF_DIR/mods-enabled/cashu-exec"

# --- EAP config (PEAP with self-signed cert) ---
echo ">>> Generating self-signed TLS certificate for radius.nodns.shop..."
CERT_DIR="$CONF_DIR/certs"
mkdir -p "$CERT_DIR"

# Generate self-signed cert (phones will show "trust this certificate?" dialog)
openssl req -x509 -newkey rsa:2048 \
    -keyout "$CERT_DIR/server.key" \
    -out "$CERT_DIR/server.crt" \
    -days 365 -nodes \
    -subj "/CN=radius.nodns.shop" 2>/dev/null

chmod 640 "$CERT_DIR/server.key"
chown root:freerad "$CERT_DIR/server.key"

echo ">>> Configuring EAP (PEAP)..."
# Configure EAP to use PEAP with our certificate
cat > "$CONF_DIR/mods-available/eap" << 'EAPCONF'
eap {
    default_eap_type = peap
    timer_expire = 60
    ignore_unknown_eap_types = no

    tls-config tls-common {
        private_key_file = ${certdir}/server.key
        certificate_file = ${certdir}/server.crt
        ca_file = ${certdir}/server.crt
        dh_file = ${certdir}/dh
        fragment_size = 1024
    }

    peap {
        tls = tls-common
        default_eap_type = mschapv2
        copy_request_to_tunnel = yes
        use_tunneled_reply = yes
        virtual_server = "inner-tunnel"
    }

    mschapv2 {
        # Inner auth method within PEAP tunnel
    }
}
EAPCONF

# Regenerate DH params (can take a minute)
echo ">>> Generating DH parameters (this takes a moment)..."
openssl dhparam -out "$CERT_DIR/dh" 2048 2>/dev/null || true
chown root:freerad "$CERT_DIR/dh"

# --- inner-tunnel virtual server ---
echo ">>> Configuring inner-tunnel virtual server..."
cat > "$CONF_DIR/sites-available/inner-tunnel" << 'INNERCONF'
server inner-tunnel {
    authorize {
        # Check if username matches Cashu token pattern
        if (User-Name =~ "^cashu[AB]") {
            # Call our Go binary for token validation
            cashu-auth
            if (ok) {
                update reply {
                    Session-Timeout := 3600
                }
            }
        }
        else {
            reject
        }
    }

    authenticate {
        # No traditional auth - the exec module IS the auth
    }

    session {
    }

    post-auth {
        # Log accepted sessions
        if (User-Name =~ "^cashu[AB]") {
            ok
        }
    }
}
INNERCONF

ln -sf ../sites-available/inner-tunnel "$CONF_DIR/sites-enabled/inner-tunnel"

# --- Enable the default server for outer EAP handling ---
echo ">>> Ensuring default site is enabled..."
ln -sf ../sites-available/default "$CONF_DIR/sites-enabled/default" 2>/dev/null || true

# --- Session cleanup cron ---
echo ">>> Installing session cleanup cron..."
cat > /etc/cron.d/tollgate-radius-cleanup << 'CRON'
# Every 5 minutes, clean up expired RADIUS sessions
*/5 * * * * root find /opt/cashu-tollgate/radius-sessions -name "*.json" -mmin +60 -delete 2>/dev/null
CRON

echo ">>> Restarting FreeRADIUS..."
systemctl restart freeradius
sleep 2
systemctl status freeradius --no-pager | tail -5

echo ""
echo ">>> FreeRADIUS configured for radius.nodns.shop"
echo ">>> Listen ports: 1812 (auth), 1813 (accounting)"
echo ">>> EAP method: PEAP with self-signed cert"
echo ">>> Validation: /usr/local/bin/tollgate-auth-radius"
echo ""
echo ">>> To test: radtest cashuB... <any-password> localhost 0 tollgate"
echo ">>> Or with eapol_test for full PEAP flow"
