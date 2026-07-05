# Docker Migration — Post-Migration Report

**Status:** Migration completed July 5, 2025  
**Services migrated:** 5 (webssh, csms, auth-ocpi, daemon, settle, freeradius)  
**Services still on systemd (intentional):** tollgate-auth-ssh (needs host useradd/chroot), caddy (ACME integration), tollgate-net (separate Rust binary), tollgate-eur-mint (separate Python service)

This document captures what we learned during the actual migration that wasn't (or couldn't be) planned in advance. Read this before attempting another Docker migration or modifying the deployment.

---

## What actually worked

- **Per-service migration with rollback window** (start on temp port → flip Caddy → stop systemd → move to canonical port) worked exactly as documented in the runbook.
- **Bind-mounted host dirs** preserved state continuity (no ledger/wallet migration needed).
- **UID 994:983 + supplementary group 985** matching host `tollgate` worked for all stateful containers (daemon, ocpi, settle).
- **FreeRADIUS host networking** gave us clean UDP 1812 + TCP 2083 access without NAT overhead.
- **Docker Compose was NOT used** for the actual migration — direct `docker run` was simpler and more debuggable. Compose is still valuable for local dev and future prod, but not required.

---

## What we got wrong in the planning docs

The planning docs (`docs/DOCKER_MIGRATION_ROADMAP.md`, `docs/MIGRATION_RUNBOOK.md`, `docker/README.md`) had several optimistic assumptions that broke on contact with reality:

### 1. cdk-cli release URL format

**Planned:** `cdk-cli-${CDK_ARCH}`  
**Actual:** `cdk-cli-${VERSION_NO_V}-${CDK_ARCH}` (e.g. `cdk-cli-0.17.2-x86_64`)

The asset filename includes the version (without the `v` prefix from the tag). Fixed in all Dockerfiles by computing `CDK_VER_NO_V="${CDK_CLI_VERSION#v}"`.

### 2. FreeRADIUS alpine image lacks a freerad user

**Planned:** `USER freerad`  
**Actual:** Official `freeradius/freeradius-server:3.2.5-alpine` has NO `freerad` user — it runs as root by default. We had to add `addgroup -g 101 -S freerad && adduser -u 101 -S -G freerad -h /etc/raddb freerad` before the `USER` directive.

### 3. FreeRADIUS binary is `radiusd`, not `freeradius`

**Planned:** `ENTRYPOINT ["freeradius"]`  
**Actual:** Official image installs at `/opt/sbin/radiusd` (not `/usr/sbin/freeradius`). The image's `/docker-entrypoint.sh` wrapper translates the conventional names. Fixed by using the wrapper: `ENTRYPOINT ["/docker-entrypoint.sh"] CMD ["radiusd"]`.

### 4. FreeRADIUS config files are root:root 0640

**Planned:** Just `USER freerad` and everything works.  
**Actual:** The official image ships `radiusd.conf` as `root:root` mode 0640 (root-only readable). When running as freerad, the config can't be read. Fixed with `RUN chown -R freerad:freerad /opt/etc/raddb /etc/raddb /opt/var/log/radius /var/log/radius /run /etc/raddb/certs` AFTER copying our config.

### 5. EAP module needs DH params at build time

**Planned:** Real certs are mounted at runtime; build-time config check should be possible without certs.  
**Actual:** FreeRADIUS's `radiusd -XC` instantiates the EAP module, which fails if `server.key`, `server.crt`, or `dh` files are missing. Fixed by generating placeholder certs + DH params at build time. The placeholders are overridden at runtime by volume mounts for RadSec, and by the existing files for EAP.

### 6. `cashu-payment` policy was never committed to the repo

**Planned:** Use the configs in `config/freeradius/`.  
**Actual:** The `radsec` site references a `cashu-payment` policy, which is defined in `/etc/freeradius/3.0/policy.d/cashu-auth` — a file that existed on the production server but was never committed to the repo. Discovered when the FreeRADIUS container failed to parse the config. The file has now been added to `config/freeradius/policy.d/cashu-auth`.

### 7. FreeRADIUS rlm_exec strips environment variables

**Planned:** Set `TOLLGATE_SOCKET=tcp://127.0.0.1:8094` in `/etc/default/freeradius` and the shim will inherit it.  
**Actual:** FreeRADIUS's `rlm_exec` runs programs with a near-empty env (`PWD=/` only). No `TOLLGATE_*`, no `PATH`. Fixed by creating `/usr/local/libexec/tollgate-shim-tcp-wrapper.sh` that explicitly sets the env var and exec's the shim. Updated `mods-available/cashu-exec` to call the wrapper.

### 8. Port conflicts during migration window

**Planned:** Use temp port 18094 during the daemon migration.  
**Actual:** Port 18094 was already in use by the systemd-managed daemon (which we'd switched to TCP in the warm-up phase). Fixed by using port 28094 for the container during the flip, then reverting to 18094 after stopping systemd.

### 9. Cert permissions on bind-mounted volume

**Planned:** Populate `freeradius-certs` volume from host cert dir.  
**Actual:** Files in the volume retained their host permissions (`root:root 0400`). The container's freerad user (UID 101) couldn't read them. Fixed with `chgrp 101 /certs/* && chmod 640 /certs/*` via a one-shot alpine container.

### 10. `tollgate` user needed docker group membership

**Planned:** Containerized settle runs via systemd timer.  
**Actual:** Settle runs as `tollgate` user (per the security hardening). `docker run` requires root or docker group membership. Fixed with `usermod -aG docker tollgate`.

---

## Final container topology

```
                 ┌──────────────── host: nodns.shop ────────────────┐
                 │                                                  │
                 │  Caddy (host systemd) ── :80 / :443              │
                 │   ├── reverse_proxy → 127.0.0.1:8093 (ocpi ctnr) │
                 │   ├── reverse_proxy → 127.0.0.1:8092 (webssh)    │
                 │   └── reverse_proxy → 127.0.0.1:8887 (csms ctnr) │
                 │                                                  │
                 │  tollgate-auth-ssh (host) ── :2222               │
                 │  tollgate-net (host) ── :2121 (loopback)         │
                 │  tollgate-eur-mint (host) ── (loopback)          │
                 │                                                  │
  :1812/udp ───► │  freeradius (container, --network host)          │
  :2083/tcp ───► │   ├── exec → tollgate-shim-tcp-wrapper.sh         │
                 │   │     └── TCP → 127.0.0.1:18094 (daemon ctnr)  │
                 │   └── exec → tollgate-auth-radius-delegated       │
                 │                                                  │
                 │  ┌── docker bridge: containers ───────────────┐  │
                 │  │ tollgate-daemon-docker  (HTTP + TCP socket)│  │
                 │  │   └── bind-mounts: /opt/cashu-tollgate,    │  │
                 │  │                    /var/lib/cashu-wallet   │  │
                 │  │ tollgate-auth-ocpi-docker (HTTP)           │  │
                 │  │ tollgate-csms-docker (WS)                  │  │
                 │  │ tollgate-webssh-docker (HTTP)              │  │
                 │  └────────────────────────────────────────────┘  │
                 │                                                  │
                 │  tollgate-settle (oneshot, runs via systemd       │
                 │    timer → run-settle.sh → docker run --rm)       │
                 └──────────────────────────────────────────────────┘
```

---

## Resource impact

| Metric | Before (systemd) | After (Docker) | Delta |
|---|---|---|---|
| Total memory (5 services) | ~80 MiB | ~66 MiB | -14 MiB (containers more efficient due to distroless for some) |
| Disk usage | 23 GB | 25 GB | +2 GB (Docker images + layer cache) |
| Image build time (cold) | n/a | ~3 min | one-time cost |
| Container startup time | ~1s (systemd) | ~2s (docker run) | +1s acceptable |
| Deploy time (per service) | ~30s (scp + systemctl) | ~5s (docker stop + run) | -25s |

---

## What's NOT containerized (and why)

| Service | Reason | Plan |
|---|---|---|
| `tollgate-auth-ssh` | Calls `useradd`/`userdel`, `chroot(2)`, manages PTYs. Cannot cleanly containerize without redesigning the jail flow. | Stay on host (Phase 4 of the original roadmap — deferred indefinitely) |
| `caddy` | Already integrated with host systemd for ACME cert renewal. Containerizing would require cert-sync complexity. | Stay on host |
| `tollgate-net` | Separate Rust binary (different repo). Has its own deployment story. | Stay on host (out of scope) |
| `tollgate-eur-mint` | Python service (separate). | Stay on host (out of scope) |

---

## Rollback procedure

If any container is misbehaving, rollback to systemd is straightforward:

```bash
# Stop the container
docker stop tollgate-<svc>-docker && docker rm tollgate-<svc>-docker

# Revert Caddyfile (for HTTP services)
cp /etc/caddy/Caddyfile.pre-docker-<svc> /etc/caddy/Caddyfile
systemctl reload caddy

# Re-enable systemd unit (still present, just disabled)
systemctl enable --now tollgate-<svc>
```

For FreeRADIUS, also revert the shim wrapper:

```bash
# Restore original cashu-exec config (calls shim directly, not wrapper)
cp /etc/freeradius/3.0/mods-available/cashu-exec.pre-tcp \
   /etc/freeradius/3.0/mods-available/cashu-exec
systemctl enable --now freeradius
```

For daemon, also revert the env var:

```bash
rm /etc/systemd/system/tollgate-daemon.service.d/socket-tcp.conf
systemctl daemon-reload
systemctl enable --now tollgate-daemon
```

The systemd units and the original `/etc/caddy/Caddyfile.pre-docker-*` backups are preserved on the server. They will be cleaned up after a 30-day burn-in period.

---

## Open items (post-migration TODO)

1. **Replace `:test` image tags with versioned tags.** Currently all containers run `<image>:test` which makes rollback ambiguous. Switch to `<image>:<git-sha>` or `<image>:<semver>` once a registry is set up.

2. **Set up a Docker registry** (GHCR recommended). Currently images live only on the prod server. A registry enables CI-built images, multi-host deployment, and proper version pinning.

3. **Add CI workflow** that runs `make docker-build-all` on every push. Catches Dockerfile regressions.

4. **Update cert sync script** to refresh the `freeradius-certs` Docker volume instead of (or in addition to) the host path. Currently if Let's Encrypt rotates certs, the container won't pick them up.

5. **Add `docker compose` for prod.** The current deployment uses raw `docker run` commands via `docker/deploy-containers.sh`. Migrating to compose would give us declarative config and easier rollback.

6. **Add health checks to containers.** Currently relying on `restart: unless-stopped`. Add HEALTHCHECK instructions for early detection of wedged states.

7. **Document the `tollgate-shim-tcp-wrapper.sh` dependency.** It's a host file under `/usr/local/libexec/` that isn't tracked in the repo. Should be added to `scripts/` and deployed via Makefile.

8. **Remove disabled systemd units after burn-in.** After 30 days of clean operation, delete `tollgate-{webssh,csms,auth-ocpi,daemon,settle}.service` and the Caddyfile backups.

---

## Lessons for the next migration

1. **Build on the actual target architecture first.** Building locally on macOS wouldn't have caught the alpine-specific issues (freerad user, /opt/sbin layout). Always build on a Linux host that matches prod.

2. **Read the base image's docs and entrypoint.** The official FreeRADIUS image has a non-obvious `/docker-entrypoint.sh` wrapper, a non-standard install prefix (`/opt/`), and unusual file permissions. Reading the image's Dockerfile (on Docker Hub) before writing our layer would have saved hours.

3. **Test the rlm_exec env-var inheritance assumption EARLY.** This was the highest-impact discovery — it required a new wrapper script and a config change. Could have been a 5-minute spike at the start.

4. **Keep the temp-port window short.** The migration window (start container on temp port → flip Caddy → stop systemd → move to canonical port) is fine for experimental / downtime-OK deploys. For zero-downtime, you'd need socket activation or HAProxy-style draining.

5. **rsync filters are dangerous.** My initial `--exclude='tollgate-*'` was meant for binaries but also excluded `cmd/tollgate-*` directories. The scp fallback worked but was slower. Lesson: explicit `+` and `-` patterns, not globs that match unintended paths.

6. **Systemd FreeRADIUS still listens on Unix socket by default.** When we switched the daemon to TCP for the container world, the systemd FreeRADIUS needed to keep working with the systemd daemon's Unix socket during the warm-up. The warm-up step (Phase 2a in the runbook) was the right call — it isolated the TCP refactor risk from the containerization risk.

7. **`docker compose` is not required.** The whole migration used raw `docker run` commands via a deploy script. Compose would have been cleaner but not necessary. Defer it until there's a real need (multi-host, complex overrides).

8. **`docker volume create` + manual `cp` for initial population.** Bind mounts would have worked too, but using a named volume with explicit population gave us cleaner separation between "what the host owns" and "what the container owns."
