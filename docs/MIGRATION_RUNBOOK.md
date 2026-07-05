# Docker Migration Runbook

**Status:** Step-by-step execution plan for migrating tollgate-auth from systemd to Docker.  
**Prerequisite reading:** [DOCKER_MIGRATION_ROADMAP.md](DOCKER_MIGRATION_ROADMAP.md) for the why and what-gets-easy.  
**Rollback policy:** every phase is independently reversible. Containers can coexist with systemd units during the migration window.

This runbook is the **how**. Each phase has explicit pre-conditions, steps, verification, and rollback. Do NOT skip verification — silent failures here look exactly like silent successes.

---

## Phase 0 — Pre-flight (1 day, no production changes)

**Goal:** Dockerfiles build, compose file validates, CI catches regressions. Nothing is deployed.

### Pre-conditions

- [ ] Docker 24+ with buildx installed locally
- [ ] `docker compose v2` (Go-based, ships with Docker Desktop / `docker-compose-plugin`)
- [ ] A fork of the repo where you can push to test CI
- [ ] ~5 GB free disk for image cache

### Steps

1. **Build every image locally to verify the Dockerfiles are valid:**
   ```bash
   make docker-build-all
   ```
   This builds all 6 service images. First build takes 3-5 min (downloads `golang:1.25-alpine`, `freeradius:3.2.5-alpine`, `cdk-cli` release, `distroless`). Subsequent builds use the layer cache.

2. **Validate compose syntax:**
   ```bash
   make docker-validate
   ```
   Confirms `docker compose config -q` succeeds for base, dev, and prod variants. Catches YAML errors, missing env vars, dangling references.

3. **Bring up the dev stack locally** to smoke-test:
   ```bash
   # Create stub state directories if they don't exist
   sudo mkdir -p /opt/tollgate-auth /opt/cashu-tollgate /var/lib/cashu-wallet
   sudo chown -R $USER /opt/tollgate-auth /opt/cashu-tollgate /var/lib/cashu-wallet

   # Create stub secrets (DO NOT commit)
   sudo mkdir -p /etc/tollgate
   sudo tee /etc/tollgate/secrets.env > /dev/null <<EOF
   TOLLGATE_OPERATOR_NSEC=nsec10000000000000000000000000000000000000000000000000000000000000
   TOLLGATE_API_KEY=dev-only-not-real
   EOF
   sudo chmod 600 /etc/tollgate/secrets.env

   make docker-up-dev
   make docker-logs-follow
   ```

   In a separate terminal, verify the daemon's HTTP health endpoint responds:
   ```bash
   curl http://127.0.0.1:8091/healthz
   # Expected: {"status":"ok"}
   ```

4. **Tear down and clean state:**
   ```bash
   make docker-down
   docker compose -f docker/compose/docker-compose.yml down -v   # wipe volumes
   ```

5. **Add CI workflow** to build images on every push (no push to registry yet):
   ```yaml
   # .github/workflows/docker-build.yml
   name: docker-build
   on: [push, pull_request]
   jobs:
     build:
       runs-on: ubuntu-latest
       steps:
         - uses: actions/checkout@v4
         - uses: docker/setup-buildx-action@v3
         - run: make docker-validate
         - run: make docker-build-all
   ```

### Verification

- [ ] `make docker-build-all` completes without errors on a clean host
- [ ] `make docker-validate` passes
- [ ] `make docker-up-dev` brings up all containers, `docker compose ps` shows them all `Up`
- [ ] `curl http://127.0.0.1:8091/healthz` returns `{"status":"ok"}`
- [ ] CI workflow runs `make docker-build-all` on every push and passes
- [ ] No images are pushed to a registry yet (Phase 0 is build-only)

### Rollback

Nothing to roll back — no production changes were made.

### Exit criteria for Phase 0

- Confidence that Dockerfiles build cleanly across architectures (add `--platform linux/amd64,linux/arm64` to buildx for cross-arch verification)
- CI catches Dockerfile regressions before merge
- A new contributor can `make docker-up-dev` and have a running local stack

---

## Phase 1 — Containerize the easy three (1 week)

**Goal:** Migrate `tollgate-auth-ocpi`, `tollgate-csms`, `tollgate-webssh` to Docker. These are stateless HTTP/WebSocket services with no privileged ops.

### Pre-conditions

- [ ] Phase 0 exit criteria met
- [ ] Production server has Docker installed: `apt-get install docker.io docker-compose-plugin`
- [ ] Operator has SSH access to production: `ssh root@nodns.shop`
- [ ] Maintenance window of ~30 min (per service) — services will restart

### Steps (per service, in this order: webssh → csms → ocpi)

The order matters: `webssh` has no upstream callers, `csms` is called by Caddy only, `ocpi` is called by Caddy and depends on `tollgate-net`. Migrating in this order means each step has only one caller to reconfigure.

#### Step 1.1 — `tollgate-webssh`

1. **Build and tag the image locally**, push to a registry the production server can pull from (or build on the server):
   ```bash
   make docker-build SVC=tollgate-webssh
   docker tag tollgate-webssh:latest <registry>/tollgate-webssh:$(git rev-parse --short HEAD)
   docker push <registry>/tollgate-webssh:$(git rev-parse --short HEAD)
   ```

2. **On the production server**, pull the image and start the container alongside the existing systemd service:
   ```bash
   ssh root@nodns.shop
   docker pull <registry>/tollgate-webssh:<tag>
   docker run -d \
     --name tollgate-webssh-docker \
     --restart unless-stopped \
     -p 127.0.0.1:8093:8092 \
     -e TOLLGATE_SSH_ADDR=127.0.0.1:2222 \
     <registry>/tollgate-webssh:<tag>
   ```

   Wait — port conflict. The systemd-managed `tollgate-webssh` is already on `127.0.0.1:8092`. Use a different port during the migration window:
   ```bash
   docker run -d ... -p 127.0.0.1:8096:8092 ...   # temp port
   ```

3. **Verify the container works** before flipping Caddy:
   ```bash
   # Should return the WebSocket HTML page
   curl http://127.0.0.1:8096/

   # Should accept a WebSocket connection
   wscat -c ws://127.0.0.1:8096/
   ```

4. **Flip Caddy** to proxy to the container:
   ```bash
   # Edit /etc/caddy/Caddyfile, change:
   #   reverse_proxy localhost:8092
   # to:
   #   reverse_proxy localhost:8096
   systemctl reload caddy
   ```

5. **Stop the systemd service** (the container is now serving traffic):
   ```bash
   systemctl stop tollgate-webssh
   systemctl disable tollgate-webssh
   ```

6. **Move the container to the canonical port** (8092):
   ```bash
   docker stop tollgate-webssh-docker
   docker rm tollgate-webssh-docker
   docker run -d ... -p 127.0.0.1:8092:8092 ...   # canonical port

   # Revert Caddyfile to localhost:8092
   systemctl reload caddy
   ```

7. **Verify end-to-end** from a browser: visit `https://nodns.shop/` (or whatever public path uses webssh), connect a WebSocket, paste a test token, confirm shell access.

#### Step 1.2 — `tollgate-csms`

Same flow as Step 1.1 but for `tollgate-csms`. Port 8887, no upstream callers besides Caddy. Verify by connecting a real charge point (or the `cmd/tollgate-csms/test-client`).

#### Step 1.3 — `tollgate-auth-ocpi`

Same flow but note: `tollgate-auth-ocpi` makes outbound HTTP to `tollgate-net:2121`. In the container world, this becomes `http://tollgate-net:2121` via Docker DNS, OR `http://127.0.0.1:2121` if `tollgate-net` is still on the host (it is, until Phase 2). Set `TOLLGATE_SESSIOND_URL=http://172.17.0.1:2121` (docker bridge gateway → host) for the interim.

### Verification (per service)

- [ ] `docker compose ps` shows the container `Up`
- [ ] `curl http://127.0.0.1:<port>/healthz` returns 200 (or service-specific health check)
- [ ] Caddy access logs show successful proxying with 2xx upstream responses
- [ ] Public-facing tests (e.g., the OCPI demo at `https://ocpi.nodns.shop/`) still pass
- [ ] `systemctl is-active tollgate-<svc>` returns `inactive` (we deliberately stopped the systemd unit)

### Rollback (per service)

```bash
# Stop the container
docker stop tollgate-<svc>-docker && docker rm tollgate-<svc>-docker

# Revert Caddyfile to the original localhost:<port>
vi /etc/caddy/Caddyfile
systemctl reload caddy

# Restart the systemd service
systemctl enable --now tollgate-<svc>
```

The systemd units are NOT deleted during Phase 1 — they're kept as fallback for a 1-week burn-in period.

### Exit criteria for Phase 1

- All three services run as containers; their systemd units are stopped + disabled
- No regression in public-facing functionality
- Containers survive a `docker compose restart` cleanly
- Logs are visible via `docker logs tollgate-<svc>`

---

## Phase 2 — Containerize daemon + settle (1–2 weeks)

**Goal:** Migrate `tollgate-daemon` (the auth pipeline workhorse) and `tollgate-settle` (weekly settlement job).

### Pre-conditions

- [ ] Phase 1 exit criteria met
- [ ] The shim refactor (TCP support for `TOLLGATE_SOCKET`) is deployed (see `cmd/tollgate-shim/socket_address_test.go`)
- [ ] Maintenance window of 1 hour — daemon restart briefly interrupts RADIUS auth

### Steps

#### Step 2.1 — Switch shim → daemon to TCP (no container change yet)

This is the warm-up. We change the systemd-managed `tollgate-daemon` to listen on TCP and the systemd-managed `tollgate-shim` (via FreeRADIUS) to dial TCP. After this works, the daemon can move to a container without changing the shim's config.

1. Edit `/etc/systemd/system/tollgate-daemon.service` on the host:
   ```ini
   Environment=TOLLGATE_SOCKET=tcp://127.0.0.1:8094
   ```

2. Edit `/etc/freeradius/3.0/mods-available/cashu-exec` on the host — set the env var for the shim:
   ```radius
   program = "/usr/bin/env TOLLGATE_SOCKET=tcp://127.0.0.1:8094 /usr/local/bin/tollgate-shim ..."
   ```
   (Or set it globally in `/etc/default/freeradius`.)

3. Restart daemon, then FreeRADIUS:
   ```bash
   systemctl restart tollgate-daemon
   systemctl restart freeradius
   ```

4. Verify RADIUS auth still works end-to-end with a real test token.

#### Step 2.2 — Containerize the daemon

1. Build and push the daemon image (with cdk-cli bundled).

2. On the server, create the host directories the container will bind-mount:
   ```bash
   ls -la /opt/tollgate-auth /opt/cashu-tollgate /var/lib/cashu-wallet
   # Note ownership — the container runs as UID 100 (tollgate inside the image)
   # If host dirs are owned by host UID 994 (tollgate on host), either:
   #   (a) chown -R 100:100 /opt/tollgate-auth /opt/cashu-tollgate /var/lib/cashu-wallet
   #   (b) Update the Dockerfile to match host UID
   # Option (a) is simpler — do that.
   ```

3. Start the container on a temp port alongside the systemd daemon:
   ```bash
   docker run -d \
     --name tollgate-daemon-docker \
     --restart unless-stopped \
     -p 127.0.0.1:8097:8091 \
     -e TOLLGATE_SOCKET=tcp://0.0.0.0:8098 \
     -p 127.0.0.1:8098:8098 \
     -e TOLLGATE_HTTP_ADDR=:8091 \
     -e TOLLGATE_BASE_DIR=/opt/tollgate-auth \
     -e TOLLGATE_WALLET_DIR=/var/lib/cashu-wallet \
     -e TOLLGATE_AUTH_MODE=local \
     --env-file /etc/tollgate/secrets.env \
     -v /opt/tollgate-auth:/opt/tollgate-auth \
     -v /opt/cashu-tollgate:/opt/cashu-tollgate \
     -v /var/lib/cashu-wallet:/var/lib/cashu-wallet \
     tollgate-daemon:latest
   ```

4. Verify the container's HTTP health: `curl http://127.0.0.1:8097/healthz`.

5. **Flip the shim config** to point at the container's TCP port:
   ```radius
   program = "/usr/bin/env TOLLGATE_SOCKET=tcp://127.0.0.1:8098 /usr/local/bin/tollgate-shim ..."
   systemctl restart freeradius
   ```

6. Send a real RADIUS auth request. Verify Accept/Reject behavior matches the systemd daemon.

7. Stop the systemd daemon:
   ```bash
   systemctl stop tollgate-daemon
   systemctl disable tollgate-daemon
   ```

8. Move the container to the canonical ports (8091 for HTTP, 8094 for TCP):
   ```bash
   docker stop tollgate-daemon-docker && docker rm tollgate-daemon-docker
   docker run -d ... -p 127.0.0.1:8091:8091 -p 127.0.0.1:8094:8098 ...
   # Update FreeRADIUS shim config to use 8094
   systemctl restart freeradius
   ```

#### Step 2.3 — Containerize settle

Settle is a oneshot — easier than the daemon. The systemd timer stays on host; it just runs `docker run --rm` instead of the host binary.

1. Build and push `tollgate-settle:latest`.

2. Replace `/usr/local/sbin/run-settle.sh` with:
   ```bash
   #!/bin/bash
   set -euo pipefail
   set -a
   source /etc/tollgate/settle.env
   set +a
   docker run --rm \
     --network tollgate_default \
     -v /opt/tollgate-auth:/opt/tollgate-auth \
     -v /opt/cashu-tollgate:/opt/cashu-tollgate \
     -v /etc/tollgate/settle.env:/etc/tollgate/settle.env:ro \
     --env-file /etc/tollgate/settle.env \
     tollgate-settle:latest \
     --ledger /opt/cashu-tollgate/ledger.jsonl \
     --operator "${TOLLGATE_OPERATOR_ID}" \
     --relays "${TOLLGATE_RELAYS}"
   ```

3. Test manually:
   ```bash
   /usr/local/sbin/run-settle.sh
   ```

4. The existing `tollgate-settle.timer` continues to fire weekly — no change needed.

### Verification

- [ ] RADIUS auth works end-to-end via the containerized daemon (use the e2e tests in `scripts/test-radius-e2e.sh`)
- [ ] Daemon survives `docker restart tollgate-daemon-docker` cleanly
- [ ] Settle runs successfully via `docker run`
- [ ] No regressions in ledger continuity (compare today's ledger entries before and after migration)

### Rollback

To roll back Step 2.2 (daemon):
```bash
docker stop tollgate-daemon-docker && docker rm tollgate-daemon-docker
# Revert FreeRADIUS shim env to point at systemd daemon's TCP port (or Unix socket)
systemctl enable --now tollgate-daemon
systemctl restart freeradius
```

---

## Phase 3 — Containerize FreeRADIUS (2–3 weeks)

**Goal:** Migrate the most complex service. FreeRADIUS handles UDP 1812, TCP 2083 RadSec, exec modules, and cert sync from Caddy.

### Pre-conditions

- [ ] Phase 2 exit criteria met
- [ ] FreeRADIUS Dockerfile builds locally with all our config layered in
- [ ] RadSec certs are accessible to the container (via volume or sync script)
- [ ] Maintenance window of 2 hours — RADIUS auth will be down during the flip

### Steps

1. **Sync RadSec certs to a Docker volume** so the FreeRADIUS container can read them:
   ```bash
   # Create the volume
   docker volume create freeradius-certs

   # Populate it from the current host cert location
   docker run --rm -v freeradius-certs:/certs -v /etc/freeradius/3.0/certs/letsencrypt:/src:ro alpine \
     cp /src/nodns.shop.crt /src/nodns.shop.key /certs/
   ```

2. **Build and push** the FreeRADIUS image.

3. **Start the container alongside** the systemd FreeRADIUS on different ports:
   ```bash
   docker run -d \
     --name tollgate-freeradius-docker \
     --network host \                                    # required for UDP 1812 perf
     -e TOLLGATE_SOCKET=tcp://127.0.0.1:8094 \
     -v freeradius-certs:/etc/raddb/certs/letsencrypt:ro \
     -v /etc/tollgate/secrets.env:/etc/tollgate/secrets.env:ro \
     --cap-drop ALL --cap-add NET_BIND_SERVICE \
     tollgate-freeradius:latest
   ```

   Wait — `--network host` means the container tries to bind 1812 and 2083, which the systemd FreeRADIUS is already using. You need to:
   - Stop systemd FreeRADIUS FIRST
   - Then start the container
   - Have a rollback plan ready if the container fails to start

4. **Plan a maintenance window.** Stop systemd FreeRADIUS, start container immediately:
   ```bash
   systemctl stop freeradius
   docker start tollgate-freeradius-docker

   # Watch the logs
   docker logs -f tollgate-freeradius-docker
   ```

5. **Verify with real auth attempts** (use `scripts/test-radius-e2e.sh` and a real AP if possible).

6. **Set up cert rotation sync** — the existing `sync-caddy-certs.timer` (systemd) should now refresh the Docker volume instead of the host path. Update `/usr/local/sbin/sync-caddy-certs-to-freeradius`:
   ```bash
   #!/bin/bash
   # Copy from Caddy's ACME state to the Docker volume
   docker run --rm \
     -v freeradius-certs:/certs \
     -v /var/lib/caddy/.local/share/caddy/certificates/acme-v02.api.letsencrypt.org-directory/nodns.shop:/src:ro \
     alpine sh -c 'cp /src/nodns.shop.crt /src/nodns.shop.key /certs/ && chmod 644 /certs/*'

   # Tell FreeRADIUS to reload (it picks up new certs on next connection)
   docker exec tollgate-freeradius-docker freeradius -CH
   ```

### Verification

- [ ] UDP 1812 reachable externally: `nc -u nodns.shop 1812 </dev/null` (should not error)
- [ ] TCP 2083 reachable externally: `openssl s_client -connect nodns.shop:2083` (TLS handshake succeeds)
- [ ] Real AP authentication succeeds (EAP-TTLS+PAP with test token)
- [ ] RadSec auth succeeds (`radtest -t tls`)
- [ ] Accounting requests reach the daemon (`tollgate-acct` exec module)
- [ ] Cert rotation works: trigger the sync script manually, confirm new certs are loaded
- [ ] `docker logs tollgate-freeradius-docker` shows clean config load (`freeradius -XC`)

### Rollback

```bash
docker stop tollgate-freeradius-docker
systemctl start freeradius
```

The systemd FreeRADIUS config files are still on the host — untouched. Just restart the service.

---

## Phase 4 — Address the SSH jail (1+ month, deferred)

**Goal:** Either containerize `tollgate-auth-ssh` or accept it stays on host.

This phase is documented in [DOCKER_MIGRATION_ROADMAP.md](DOCKER_MIGRATION_ROADMAP.md) but not executed in this runbook. It requires architectural decisions (privileged helper vs `systemd-nspawn` vs ephemeral session containers) that warrant their own design doc.

**Recommendation:** Keep SSH on host for the foreseeable future. The capability-bounded root configuration from the security audit already prevents the subverted-binary attack vector. The marginal gain from containerizing is small relative to the redesign cost.

---

## Phase 5 — Kubernetes / orchestrated deployment (future, out of scope)

Once everything runs in containers, the deployment story is `docker compose up`. Moving to Kubernetes is then a question of scale, not architecture. This phase will get its own roadmap when there's a scaling reason.

---

## Post-maintenance: cleanup checklist

After each phase has run cleanly for 1 week:

- [ ] Delete the disabled systemd units: `rm /etc/systemd/system/tollgate-<svc>.service`
- [ ] Remove the `deploy-systemd-units` target from the Makefile (or mark deprecated)
- [ ] Update README deployment status table to mark the service as "Docker (isolated)"
- [ ] Update Caddyfile comments to reference container names instead of systemd units
- [ ] Run `docker system prune -af --volumes-except tollgate-ledger,tollgate-wallet,freeradius-certs` to reclaim space

## Disasters and recovery

| Disaster | Recovery |
|---|---|
| Container crashes on startup | `docker logs <name>` → fix → rebuild → redeploy |
| Bad image deployed | `docker stop <name>` → `docker run <previous-tag>` → investigate |
| Volume corruption (ledger, wallet) | Restore from nightly backup (`/opt/tollgate-auth` is on the host's normal backup schedule) |
| Docker daemon dies | `systemctl restart docker` — containers with `restart: unless-stopped` come back automatically |
| Host dies | Rebuild from `docker-compose.yml` on a fresh host + restore volumes from backup |
| Bad config baked into image | Revert the commit, rebuild, redeploy. Compose pull policy `always` ensures fresh image |
