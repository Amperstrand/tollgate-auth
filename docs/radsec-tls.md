# RadSec TLS Certificate Setup

RadSec (RADIUS over TLS, TCP 2083) encrypts the RADIUS conversation between NAS devices (access points, VPN concentrators) and FreeRADIUS. This document explains how we share Let's Encrypt certificates from Caddy to FreeRADIUS.

**Last updated**: 2025-06-12
**Status**: Let's Encrypt certs deployed for RadSec. EAP inner tunnel still uses self-signed cert (future improvement).

---

## Architecture Overview

There are two separate TLS concerns in a RADIUS/WiFi setup. These are logically independent and can use different certificates.

| TLS Layer | Purpose | Certificates | Our Setup |
|-----------|---------|--------------|-----------|
| **RadSec transport** | Encrypts RADIUS packets between NAS and FreeRADIUS (TCP 2083) | Server cert on FreeRADIUS | Let's Encrypt from Caddy |
| **EAP inner tunnel** | Encrypts credentials between WiFi client (phone) and FreeRADIUS inside TTLS/PEAP | Server cert on FreeRADIUS | Self-signed (TODO: improve) |

### Why two layers?

- **RadSec**: Encrypts the NAS to FreeRADIUS hop. If you disable UDP 1812, this is the only transport. Let's Encrypt works great here because NAS devices trust public CAs.
- **EAP inner tunnel**: Encrypts the actual EAP conversation between the phone and FreeRADIUS. This is where the Cashu token (or username/password) travels. Clients need to trust the server certificate. Let's Encrypt works on Android but has issues on iOS (see below).

### Our deployment

- Caddy runs on `nodns.shop` and auto-provisions Let's Encrypt certificates for HTTPS
- A sync script copies those certificates to FreeRADIUS with proper permissions
- RadSec uses the Let's Encrypt certificate
- EAP still uses a self-signed certificate (documented as future work)

---

## Current Setup

### Caddy certificate location

Caddy stores Let's Encrypt certificates in its own ACME directory, NOT in the standard `/etc/letsencrypt/` path:

```
/var/lib/caddy/.local/share/caddy/certificates/acme-v02.api.letsencrypt.org-directory/nodns.shop/
├── nodns.shop.crt      # Full certificate chain
└── nodns.shop.key      # Private key (mode 0600)
```

The private key is readable only by root (mode 0600). FreeRADIUS runs as the `freerad` user and cannot read Caddy's cert files directly.

### Sync script

`scripts/sync-caddy-certs.sh` copies certificates from Caddy to FreeRADIUS:

```bash
#!/bin/bash
# /usr/local/sbin/sync-caddy-certs-to-freeradius

CADDY_CERT_DIR="/var/lib/caddy/.local/share/caddy/certificates/acme-v02.api.letsencrypt.org-directory/nodns.shop"
FREERADIUS_CERT_DIR="/etc/freeradius/3.0/certs/letsencrypt"

mkdir -p "$FREERADIUS_CERT_DIR"

# Copy certificates with restrictive permissions
cp "$CADDY_CERT_DIR/nodns.shop.crt" "$FREERADIUS_CERT_DIR/"
cp "$CADDY_CERT_DIR/nodns.shop.key" "$FREERADIUS_CERT_DIR/"

chmod 0640 "$FREERADIUS_CERT_DIR/nodns.shop.crt"
chmod 0640 "$FREERADIUS_CERT_DIR/nodns.shop.key"
chown root:freerad "$FREERADIUS_CERT_DIR"/*

# Only restart FreeRADIUS if certificate content changed
OLD_CHECKSUM=$(cat "$FREERADIUS_CERT_DIR/nodns.shop.crt" "$FREERADIUS_CERT_DIR/nodns.shop.key" 2>/dev/null | sha256sum)
NEW_CHECKSUM=$(cat "$CADDY_CERT_DIR/nodns.shop.crt" "$CADDY_CERT_DIR/nodns.shop.key" | sha256sum)

if [ "$OLD_CHECKSUM" != "$NEW_CHECKSUM" ]; then
    systemctl restart freeradius
    echo "[$(date)] Certificate updated, FreeRADIUS restarted" >> /var/log/caddy-cert-sync.log
else
    echo "[$(date)] Certificate unchanged, no restart needed" >> /var/log/caddy-cert-sync.log
fi
```

### Systemd timer

A systemd timer runs the sync script every 6 hours with a randomized delay:

```ini
# /etc/systemd/system/sync-caddy-certs.timer
[Unit]
Description=Sync Caddy certificates to FreeRADIUS every 6 hours

[Timer]
OnCalendar=*:0/6
RandomizedDelaySec=300
Persistent=true

[Install]
WantedBy=timers.target
```

```ini
# /etc/systemd/system/sync-caddy-certs.service
[Unit]
Description=Sync Caddy certificates to FreeRADIUS

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/sync-caddy-certs-to-freeradius
```

The timer runs shortly after boot and then every 6 hours. Let's Encrypt certificates renew automatically via Caddy, typically 30 days before expiry. The 6-hour sync interval ensures we pick up renewals promptly without excessive restarts.

### RadSec configuration

FreeRADIUS RadSec (`config/freeradius/sites-available/radsec`) points to the Let's Encrypt certificates:

```
listen {
    ipaddr = *
    port = 2083
    type = auth

    tls {
        private_key_file = ${certdir}/letsencrypt/nodns.shop.key
        certificate_file = ${certdir}/letsencrypt/nodns.shop.crt
        ca_file = ${cadir}/letsencrypt/isrgrootx1.pem
        require_client_cert = no

        cipher_list = "ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256"
        tls_min_version = "1.2"
        tls_max_version = "1.3"
    }
}
```

### EAP configuration (self-signed)

The EAP module (`config/freeradius/mods-available/eap`) still uses the default self-signed certificate:

```
tls-config tls-common {
    private_key_file = ${certdir}/server.key
    certificate_file = ${certdir}/server.crt
    ca_file = ${cadir}/ca.pem

    cipher_list = "DEFAULT"

    tls_min_version = "1.2"
    tls_max_version = "1.3"
}
```

This is fine for testing but not ideal for production. See "EAP Certificate Options" below.

---

## Deployment

Deploy the sync script, systemd timer, and perform an initial sync:

```bash
make deploy-certs
```

This:
- Copies `scripts/sync-caddy-certs.sh` to `/usr/local/sbin/sync-caddy-certs-to-freeradius`
- Deploys `config/systemd/sync-caddy-certs.timer` and `.service` to `/etc/systemd/system/`
- Runs `systemctl daemon-reload`
- Enables and starts the timer
- Runs an initial sync immediately

---

## EAP Certificate Options

The RadSec certificate and the EAP certificate can be different. Here are the three options for the EAP certificate (what phones see when they connect to WiFi).

### Option A: Let's Encrypt for EAP too

**How it works**: Point `mods-available/eap` `tls-config tls-common` to the same Let's Encrypt certificate paths used by RadSec.

**Pros**:
- Trusted by Android 13+ natively, no manual CA install
- Auto-renews via Caddy
- Single certificate for both RadSec and EAP

**Cons**:
- iOS requires re-trusting the certificate every 90 days when Let's Encrypt rotates. Neil Middleton reports: "Apple devices will ask you to trust the certificate again each time it updates. This is annoying for users."
- Android 13+ handles rotation seamlessly, but older Android versions may need CA re-install.

**Configuration change**:

```
# In config/freeradius/mods-available/eap
tls-config tls-common {
    private_key_file = ${certdir}/letsencrypt/nodns.shop.key
    certificate_file = ${certdir}/letsencrypt/nodns.shop.crt
    ca_file = ${cadir}/letsencrypt/isrgrootx1.pem
    # ... rest of config
}
```

**Critical warning from FreeRADIUS**: Never configure FreeRADIUS to use a public CA root in the `ca_file` EAP module settings. This would allow any intermediate CA to issue client certificates, which is a security hole. The EAP `ca_file` must contain ONLY your own CA.

### Option B: Private CA (Smallstep or OpenSSL)

**How it works**: Run your own certificate authority using Smallstep (`step-ca`) or OpenSSL. Issue a server certificate for FreeRADIUS and install the root CA on all client devices.

**Pros**:
- Full control over issuance and rotation
- No 90-day expiry churn
- Can issue client certificates for EAP-TLS if needed

**Cons**:
- Must install root CA on every phone/device. This is friction for BYOD (bring your own device) environments.
- Operational overhead for CA management and revocation.

**Best for**: Enterprise deployments, managed environments, multi-operator scenarios where you control the devices.

### Option C: Keep self-signed (status quo)

**How it works**: Continue using the default self-signed certificate in `${certdir}/server.{key,crt}`. Configure WiFi clients with "Do not validate CA."

**Pros**:
- Works for testing without any certificate setup
- No changes needed to existing configs

**Cons**:
- "Do not validate CA" setting disables certificate validation entirely. This is vulnerable to rogue AP / MITM attacks. An attacker can set up a fake WiFi network with the same SSID and intercept the EAP-TTLS tunnel.
- Android 13+ rejects expired certificates on reconnect. If you're using a long-lived self-signed cert, this isn't an issue. But if it expires, users are stuck.

**Acceptable for**: Testing, demo environments, controlled deployments where WiFi clients are managed.

### Our recommendation

Start with Option C (self-signed) for testing. For production:

- **Android-only environments**: Option A (Let's Encrypt). Works seamlessly on Android 13+.
- **iOS + Android**: Option B (private CA). Install the root CA on all devices once, then forget about it.
- **Mixed / BYOD**: Option B with an enrollment portal that walks users through CA installation.

---

## RFC 6614 RadSec Requirements

RadSec is defined in RFC 6614 ("RADIUS over TLS"). Key requirements:

| Requirement | Value | Our Setup |
|-------------|-------|-----------|
| TLS version | 1.1+ required | Enforced 1.2+ |
| Cipher suite | TLS 1.2 ciphers with forward secrecy | ECDHE-GCM |
| Mutual authentication | Supported but not mandatory for all deployments | Disabled (`require_client_cert = no`) |
| Shared secret | Hardcoded to "radsec" per RFC | Configured |
| Certificate validation | Per RFC 5280 (X.509 path validation) | Handled by TLS library |
| DNS/hostname verification | CN or subjectAltName must match server hostname | Caddy auto-configures this |

The shared secret for RadSec is always "radsec" per the RFC. This is separate from the TLS authentication — the secret is used as a fallback for older NAS devices that don't support full RadSec.

---

## Key Research Findings

These are the technical details we discovered while setting this up.

### Let's Encrypt certificates work identically for RadSec

There is no special "RadSec certificate" type. Let's Encrypt certificates are standard X.509 TLS certificates that work for HTTPS, RadSec, and any other TLS application. The only requirements are:

- Subject or subjectAltName must match the server hostname
- Certificate chain must include the intermediate CA
- Private key must be readable by FreeRADIUS

### Caddy stores certs in its own ACME directory

Caddy does NOT use the standard `/etc/letsencrypt/` path used by certbot. Certificates are in:

```
/var/lib/caddy/.local/share/caddy/certificates/acme-v02.api.letsencrypt.org-directory/<domain>/
```

This path is hard-coded in Caddy's ACME client. We sync from there to FreeRADIUS's cert directory.

### FreeRADIUS cannot hot-swap TLS certificates

When the certificate file changes, FreeRADIUS must be fully restarted (not just reloaded with `systemctl reload freeradius`). A reload does not reload TLS certificates into memory.

Our sync script only restarts FreeRADIUS if the certificate checksum actually changed. This avoids unnecessary restarts during the 6-hour sync interval.

### freerad user cannot read Caddy's 0600 cert files

Caddy sets certificate file permissions to 0600 (owner-only, root). FreeRADIUS runs as the `freerad` user, which cannot read these files.

Two potential solutions:

1. **Add freerad to a group that can read Caddy's certs** — this doesn't work because Caddy hard-codes 0600.
2. **Copy certificates to FreeRADIUS's cert directory with group-readable permissions** — this is what we do.

The `freerad` user belongs to the `ssl-cert` group by default on Debian, but this is for Debian's snakeoil certs in `/etc/ssl/certs/`, not for Caddy's ACME directory.

### certbot is NOT needed

Caddy handles the entire ACME lifecycle internally:

- HTTP-01 or DNS-01 challenge automation
- Certificate issuance
- Automatic renewal 30 days before expiry
- OCSP stapling

You do NOT need certbot on the same server if you're using Caddy.

### The ssl-cert group is for snakeoil certs

Debian's default FreeRADIUS installation creates a self-signed certificate in `/etc/ssl/certs/ssl-cert-snakeoil.pem` with group `ssl-cert`. The `freerad` user is in this group. This is unrelated to Caddy's Let's Encrypt certificates, which live elsewhere and use a different permission model.

---

## Troubleshooting

### Check if the sync service ran successfully

```bash
systemctl status sync-caddy-certs.service
journalctl -u sync-caddy-certs.service -n 20
```

### Check when the next timer run is scheduled

```bash
systemctl list-timers sync-caddy-certs.timer
```

### Verify the certificate file

```bash
openssl x509 -in /etc/freeradius/3.0/certs/letsencrypt/nodns.shop.crt -text -noout | grep -E 'Issuer|Not After|Subject'
```

Expected output:
- Issuer: `C = US, O = Let's Encrypt, CN = R3`
- Subject: `CN = nodns.shop`
- Not After: a date ~90 days in the future

### Test FreeRADIUS configuration

```bash
freeradius -XC
```

This runs FreeRADIUS in check mode. Look for TLS-related errors.

### Manually trigger a sync

```bash
/usr/local/sbin/sync-caddy-certs-to-freeradius
```

Check the log file:

```bash
tail -f /var/log/caddy-cert-sync.log
```

### Verify RadSec is listening

```bash
ss -tlnp | grep 2083
```

Expected output:
```
LISTEN 0 128 0.0.0.0:2083 0.0.0.0:* users:(("freeradius",pid=...,fd=...))
```

### Test RadSec with radclient

See `docs/radius-testing.md` for the full RadSec testing guide. Quick test:

```bash
socat TCP-LISTEN:11812,reuseaddr,fork SSL:nodns.shop:2083,verify=0 &
SOCAT_PID=$!
sleep 1

echo 'User-Name = "lnurlw1test"
User-Password = "anything"
NAS-IP-Address = 10.0.0.1
Calling-Station-Id = "aa:bb:cc:dd:ee:ff"
NAS-Port = 0' | radclient -x -P tcp -r 1 -t 5 127.0.0.1:11812 auth radsec

kill $SOCAT_PID
```

---

## File Inventory

| File | Purpose | Deployed by |
|------|---------|-------------|
| `scripts/sync-caddy-certs.sh` | Sync script copies certs from Caddy to FreeRADIUS | `make deploy-certs` |
| `config/systemd/sync-caddy-certs.timer` | Systemd timer for 6-hour sync interval | `make deploy-certs` |
| `config/systemd/sync-caddy-certs.service` | Systemd service for sync script | `make deploy-certs` |
| `/etc/freeradius/3.0/certs/letsencrypt/` | FreeRADIUS Let's Encrypt cert directory | Created by sync script |
| `config/freeradius/sites-available/radsec` | RadSec config pointing to LE certs | `make deploy-radius-config` |
| `config/freeradius/mods-available/eap` | EAP config (currently self-signed) | `make deploy-radius-config` |

---

## Related Documentation

- `docs/radius-testing.md` — RadSec testing with real NAS devices
- `docs/known-unknowns.md` — Certificate validation gap (Issue #3)
- RFC 6614 — RADIUS over TLS
- RFC 5280 — X.509 PKI certificate validation