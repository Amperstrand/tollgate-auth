#!/bin/bash
# OpenCPO setup — repeatable initialization script.
# Run on the VPS after `docker compose up -d` in /opt/opencpo.
#
# Usage:
#   ssh root@nodns.shop 'bash -s' < scripts/opencpo-setup.sh
#
# Credentials created:
#   Email:    admin@tollgate.dev
#   Password: TollgateDemo2026!

set -e

API_KEY="${MANAGEMENT_API_KEY:-CHANGE_ME_IN_PROD}"
API="http://localhost:8000"

echo "=== OpenCPO Setup ==="

# 1. Delete any existing admin user (idempotent)
docker exec opencpo-postgres-1 psql -U ocpp -d ocpp -c \
  "DELETE FROM users WHERE email='admin@tollgate.dev';" 2>/dev/null || true

# 2. Create admin account
curl -sf -X POST -H "X-API-Key: $API_KEY" -H "Content-Type: application/json" \
  "$API/api/v1/admin/setup/step/admin" \
  -d '{"email":"admin@tollgate.dev","name":"Admin","password":"TollgateDemo2026!"}' \
  | python3 -c "import sys,json; print('Admin:', json.load(sys.stdin))"

# 3. Configure organization
curl -sf -X POST -H "X-API-Key: $API_KEY" -H "Content-Type: application/json" \
  "$API/api/v1/admin/setup/step/org" \
  -d '{"name":"Tollgate Demo","timezone":"Europe/Oslo","currency":"NOK","public_url":"http://nodns.shop:8080"}' \
  | python3 -c "import sys,json; print('Org:', json.load(sys.stdin))"

# 4. Branding
curl -sf -X POST -H "X-API-Key: $API_KEY" -H "Content-Type: application/json" \
  "$API/api/v1/admin/setup/step/branding" \
  -d '{"accent_color":"#58a6ff"}' \
  | python3 -c "import sys,json; print('Branding:', json.load(sys.stdin))"

# 5. Enable features (OCPI)
curl -sf -X POST -H "X-API-Key: $API_KEY" -H "Content-Type: application/json" \
  "$API/api/v1/admin/setup/step/features" \
  -d '{"features":["ocpi"]}' \
  | python3 -c "import sys,json; print('Features:', json.load(sys.stdin))"

# 6. Pricing
curl -sf -X POST -H "X-API-Key: $API_KEY" -H "Content-Type: application/json" \
  "$API/api/v1/admin/setup/step/pricing" \
  -d '{"default_tariff":{"energy_kwh":2.50,"currency":"NOK"}}' \
  | python3 -c "import sys,json; print('Pricing:', json.load(sys.stdin))"

# 7. Skip optional steps
for step in tailscale smtp pki; do
  curl -sf -X POST -H "X-API-Key: $API_KEY" \
    "$API/api/v1/admin/setup/skip/$step" > /dev/null
  echo "Skipped: $step"
done

# 8. Enable OCPI feature flag in DB
docker exec opencpo-postgres-1 psql -U ocpp -d ocpp -c \
  "UPDATE feature_flags SET enabled = true WHERE key = 'ocpi';" 2>/dev/null

# 9. Create OCPI partner pointing at our eMSP
docker exec opencpo-postgres-1 psql -U ocpp -d ocpp -c "
INSERT INTO ocpi_partners (party_id, country_code, role, name, url, token_a, token_b, status)
VALUES ('TGA', 'NO', 'emsp', 'Tollgate eMSP',
        'https://ocpi.nodns.shop/ocpi/versions',
        'ocpplab-bootstrap', 'ocpplab-token-b', 'active')
ON CONFLICT DO NOTHING;
" 2>/dev/null

# 10. Patch OCPI mount bug (mount path double-prefix)
docker exec opencpo-ocpp-core-1 python3 -c "
import re
with open('/app/api/main.py') as f:
    c = f.read()
if 'app.mount(\"\", ocpi_app)' not in c and 'ocpi_app' not in c:
    c += '''

try:
    from ocpi.main import ocpi_app
    app.mount(\"\", ocpi_app)
except Exception as e:
    import logging; logging.getLogger(__name__).warning(f\"OCPI mount failed: {e}\")
'''
    with open('/app/api/main.py', 'w') as f:
        f.write(c)
    print('OCPI mount patched')
else:
    print('OCPI mount already present')
"

# 11. Restart core to apply
cd /opt/opencpo && docker compose restart ocpp-core
sleep 5

# 12. Start charger farm
curl -sf -X POST -H "Content-Type: application/json" \
  http://localhost:8087/api/farm/config \
  -d '{"csms_url":"ws://opencpo-ocpp-core-1:9100","charger_count":10,"ocpp_version":"1.6"}' > /dev/null
curl -sf -X POST -H "Content-Type: application/json" \
  http://localhost:8087/api/farm/start \
  -d '{}' > /dev/null

# 13. Verify
echo ""
echo "=== Verification ==="
echo -n "Setup: "
curl -sf -H "X-API-Key: $API_KEY" "$API/api/v1/admin/setup/status" | python3 -c "import sys,json; d=json.load(sys.stdin); print('complete' if d['complete'] else 'INCOMPLETE')"
echo -n "OCPI:  "
curl -sf http://localhost:8000/ocpi/versions | python3 -c "import sys,json; d=json.load(sys.stdin); print('OK' if d['status_code']==1000 else 'FAIL')" 2>/dev/null || echo "FAIL"
echo -n "Farm:  "
curl -sf http://localhost:8087/api/farm/status | python3 -c "import sys,json; d=json.load(sys.stdin); print(f\"{d['total_chargers']} chargers, {d['connected']} connected\")"
echo -n "Roaming: "
curl -sf -H "X-API-Key: $API_KEY" "$API/api/v1/ocpi/status" | python3 -c "import sys,json; d=json.load(sys.stdin); print(f\"health={d.get('endpoints_health')} partners={d['partners']['active']}\")"

echo ""
echo "=== Done ==="
echo "Admin:  http://nodns.shop:8080"
echo "Login:  admin@tollgate.dev / TollgateDemo2026!"
echo "Farm:   http://nodns.shop:8087"
echo "eMSP:   https://ocpi.nodns.shop"
