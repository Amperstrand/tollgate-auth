# Docker Migration Roadmap — tollgate-auth

**Status:** Phase 0 complete (Dockerfiles + compose files written, not yet built/deployed).  
**Last updated:** July 2025

This is the master index for the Docker migration. It explains **what** is being built and **why**, then links to the detail docs that explain **how**.

---

## TL;DR

| Doc | Read this if you want to... |
|---|---|
| **This page** | Understand the high-level plan, current status, what to do next |
| [docker/README.md](../docker/README.md) | Understand the container topology, architectural decisions, gotchas |
| [docs/MIGRATION_RUNBOOK.md](MIGRATION_RUNBOOK.md) | Execute the migration step-by-step (start here when ready to deploy) |
| [docs/DEV_WITH_DOCKER.md](DEV_WITH_DOCKER.md) | Run the stack locally for development |
| [docs/SECURITY_AUDIT.md](SECURITY_AUDIT.md) | Understand what was fixed and why Docker is the next layer of defense |

---

## Why Docker

The post-July-2025 systemd configuration already provides strong isolation: unprivileged users, ProtectSystem=strict, IPAddressDeny, capability bounding, syscall filters. Docker would provide *additional* value via:

1. **Image reproducibility** — `git checkout <tag> && docker build` produces the exact binary that ran in CI, staging, and prod. No more server drift.
2. **Filesystem isolation** — each container sees only its own overlayfs. A compromised service cannot read `/etc/passwd`, `/home`, or other services' files even if it escapes its user.
3. **Atomic rollback** — `docker run <prev-image>` reverts in seconds.
4. **Composable testing** — full integration test stack via `docker compose up`, no FreeRADIUS or cdk-cli host install required.

What Docker does NOT provide (myths to discard):
- *Better* isolation than systemd hardening — they're comparable. Docker's win is the immutable image layer, not the runtime isolation.
- Easier ops — Docker adds its own complexity (networking, volume mounts, image distribution).
- Better performance — typically neutral or slightly worse due to bridge NAT for UDP.

---

## Current status (Phase 0)

**What's done:**
- [x] All 6 service Dockerfiles written (ocpi, csms, webssh, daemon, settle, freeradius)
- [x] `docker-compose.yml` + dev + prod overrides
- [x] `tollgate-shim` and `tollgate-daemon` refactored to support TCP (`TOLLGATE_SOCKET=tcp://...`) for the container world
- [x] Make targets: `docker-build`, `docker-build-all`, `docker-validate`, `docker-up`, `docker-up-dev`, `docker-up-prod`, `docker-down`, `docker-logs`, `docker-logs-follow`, `docker-ps`, `docker-shell`, `docker-settle-run`
- [x] `.dockerignore` excludes local state from build context
- [x] `docker/README.md` documents topology + decisions + gotchas
- [x] `docs/MIGRATION_RUNBOOK.md` step-by-step execution plan
- [x] `docs/DEV_WITH_DOCKER.md` local-dev workflow

**What's NOT done (Phase 0 exit gates):**
- [ ] `make docker-build-all` runs successfully on a clean host (Docker not yet installed on dev machines — need to verify the Dockerfiles build)
- [ ] `make docker-validate` passes (compose syntax + Dockerfile presence)
- [ ] CI workflow `.github/workflows/docker-build.yml` runs `make docker-build-all` on every push
- [ ] Multi-arch build verified (`docker buildx build --platform linux/amd64,linux/arm64`)
- [ ] Images pushed to a registry (GHCR recommended)

These are the gates to clear before Phase 1 can start.

---

## Migration phases

| Phase | Goal | Duration | Risk | Status |
|---|---|---|---|---|
| **0** | Pre-flight: Dockerfiles build, CI verifies, no deploy | 1 day | None | **Code complete, gates pending** |
| **1** | Containerize stateless HTTP services (ocpi, csms, webssh) | 1 week | Low (rollback = restart systemd) | Not started |
| **2** | Containerize daemon + settle (the auth pipeline workhorse) | 1–2 weeks | Medium (RADIUS auth briefly down during flip) | Not started |
| **3** | Containerize FreeRADIUS (UDP 1812, RadSec, exec modules, cert sync) | 2–3 weeks | High (most complex service, longest maintenance window) | Not started |
| **4** | Decide what to do about `tollgate-auth-ssh` (host vs container vs hybrid) | 1+ month | Architectural decision deferred | Not started |
| **5** | Kubernetes / orchestrated deployment (only if scale demands) | Future | n/a | Out of scope |

Each phase's full pre-conditions, steps, verification, and rollback are in [docs/MIGRATION_RUNBOOK.md](MIGRATION_RUNBOOK.md).

---

## Difficulty matrix (why the phases are ordered this way)

| Service | Difficulty | Why |
|---|---|---|
| `tollgate-auth-ocpi` | 🟢 Easy | HTTP, no privileged ops, no sockets. Mount one dir. |
| `tollgate-csms` | 🟢 Easy | WebSocket, no privileged ops. Publish port. |
| `tollgate-webssh` | 🟢 Easy | HTTP, no privileged ops. |
| `tollgate-daemon` | 🟡 Medium | HTTP + Unix socket. Socket needs TCP refactor (DONE) or shared volume. |
| `tollgate-settle` | 🟡 Medium | Reads ledger files written by other services. Needs shared volume. |
| `freeradius` | 🔴 Hard | UDP 1812, TCP 2083 RadSec, exec modules (shim + auth-radius + wrapper co-located), cert sync. |
| `tollgate-auth-ssh` | 🔴 Hard | `useradd`/`userdel`, `chroot(2)`, PTY management. Cannot cleanly containerize without redesign. |

Phase ordering: easiest first, hardest last. Each phase's success builds confidence for the next.

---

## Architectural decisions (summary)

Full rationale in [docker/README.md](../docker/README.md).

1. **FreeRADIUS uses host networking**; everything else uses bridge. RADIUS UDP needs to avoid NAT overhead.
2. **shim → daemon uses TCP** by default in the container world. Both `tollgate-shim` and `tollgate-daemon` now parse `tcp://host:port` from `TOLLGATE_SOCKET`. Backwards compatible with bare Unix socket paths.
3. **FreeRADIUS bundles shim + auth-radius + wrapper** in the same container — `execve()` requires same-namespace FS.
4. **cdk-cli is bundled** in daemon + OCPI images via multi-stage build (no sidecar).
5. **Stateless services use distroless**; daemon + settle use alpine (need shell for debugging, ca-certificates, tzdata).
6. **SSH stays on host** for the foreseeable future — containerizing it requires a redesign of the jail flow.
7. **Bind-mounted secrets** from `/etc/tollgate/secrets.env` (not Docker Secrets, not env vars in compose).

---

## Rollback strategy

Every phase is independently reversible. During the migration window, systemd units and containers coexist on different ports. If a container misbehaves:

```bash
docker stop tollgate-<svc>-docker
systemctl enable --now tollgate-<svc>   # systemd unit still on host
# Revert Caddyfile / FreeRADIUS config to point at the systemd port
systemctl reload caddy
```

Phase 1 keeps the systemd units as fallback for a 1-week burn-in. Cleanup (deleting systemd units) only happens after the burn-in.

Full rollback procedures per phase are in [docs/MIGRATION_RUNBOOK.md](MIGRATION_RUNBOOK.md).

---

## Success metrics

Track these to confirm Docker is delivering value (vs. just adding complexity):

| Metric | Target | Current (systemd) |
|---|---|---|
| Image build time | < 60s per service (after layer cache) | n/a (scp binary) |
| Container startup time | < 2s per service | ~1s (systemd) |
| Memory overhead per service | < +20MB | baseline |
| Deploy time for full stack | < 30s (`docker compose up`) | ~5 min (`make deploy-*` series) |
| Time-to-recover from bad deploy | < 10s (`docker run <prev>`) | ~2 min (rebuild + scp + restart) |
| New-contributor time-to-first-running-stack | < 10 min (`make docker-up-dev`) | ~2 hours (install FreeRADIUS, cdk-cli, secrets, etc.) |

If after 3 months of running Docker in prod the metrics don't improve, reconsider the migration.

---

## Open questions

To be resolved during Phase 0 (not by committee — by experiments):

1. **UID matching**: should containers run as UID 1000 (typical Linux user) or as the host's `tollgate` UID (currently 994)? Affects volume permissions on `/var/lib/cashu-wallet`. Recommend: standardize on UID 100 inside containers, `chown` host dirs once during migration.

2. **Health checks for distroless**: `HEALTHCHECK CMD` needs a binary. Options: tiny static Go probe, or rely on external monitoring (Caddy `/healthz` probes). Recommend: external — keeps images minimal.

3. **FreeRADIUS config: baked-in or mounted?** Baking config into the image triggers a rebuild on every config change. Mounting via `-v ./config/freeradius:/etc/raddb:ro` enables hot reload but breaks the "image = single source of truth" model. Recommend: bake for prod, mount for dev.

4. **Where does cdk-cli live long-term?** Currently bundled in daemon + OCPI. Long-term: separate "wallet" service container exposing HTTP API? Recommend: revisit when wallet operations become a bottleneck.

5. **Compose v2 vs Swarm mode?** Compose v2 (Go-based) is fine for single-host. Swarm gives us Docker Secrets + multi-host. Recommend: Compose until we need multi-host.

---

## When to abandon this roadmap

Docker is not free. If any of these become true, stop and reconsider:

- Migration causes more than 2 hours of unexpected production downtime per phase
- Container rebuild overhead exceeds the time saved on deploys
- Debugging container network/volume issues takes more time than the systemd equivalent
- A new contributor reports that the Docker setup is HARDER than the systemd setup
- Performance regression on RADIUS auth (UDP 1812 PPS) cannot be resolved

In the worst case, the existing systemd configuration is already secure (post-July audit) and operationally adequate. Docker is an optimization, not a fix.
