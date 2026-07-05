# docker/ — Container images and compose stack for tollgate-auth

This directory contains everything needed to run tollgate-auth in Docker. The stack is **planned and buildable** as of this commit — no images are published, no containers run in production yet. See [../docs/DOCKER_MIGRATION_ROADMAP.md](../docs/DOCKER_MIGRATION_ROADMAP.md) for the phased migration plan and [../docs/MIGRATION_RUNBOOK.md](../docs/MIGRATION_RUNBOOK.md) for step-by-step execution.

## Quick start (local dev)

```bash
# Prereqs: Docker 24+ with buildx, docker compose v2
make docker-build-all         # builds all 6 service images (~2-5 min first time)
make docker-up-dev            # starts everything including FreeRADIUS
make docker-logs-follow       # tail all logs (Ctrl-C to detach)
make docker-down              # stop (volumes preserved)
```

For the full local-dev workflow (with fresh test tokens, hot-reload, etc.) see [../docs/DEV_WITH_DOCKER.md](../docs/DEV_WITH_DOCKER.md).

## Directory layout

```
docker/
├── README.md                              ← you are here
├── compose/
│   ├── docker-compose.yml                 base topology (production-intent)
│   ├── docker-compose.dev.yml             local-dev overrides
│   └── docker-compose.prod.yml            production overrides (resource limits, log rotation)
├── freeradius/
│   └── Dockerfile                         FreeRADIUS + shim + auth-radius + wrapper, all co-located
├── tollgate-auth-ocpi/
│   └── Dockerfile                         OCPI eMSP (distroless, includes cdk-cli)
├── tollgate-csms/
│   └── Dockerfile                         OCPP 1.6 CSMS (distroless)
├── tollgate-daemon/
│   └── Dockerfile                         auth daemon (alpine, includes cdk-cli for subprocess shell-out)
├── tollgate-settle/
│   └── Dockerfile                         oneshot settlement job (alpine)
├── tollgate-webssh/
│   └── Dockerfile                         WebSocket→SSH bridge (distroless)
└── scripts/                               (reserved for future helper scripts)
```

## Container topology

```
                  ┌──────────────────── host: nodns.shop ────────────────────┐
                  │                                                         │
                  │  Caddy (host systemd) ── :80 / :443                     │
                  │   ├── reverse_proxy → 127.0.0.1:8093 (ocpi)             │
                  │   ├── reverse_proxy → 127.0.0.1:8092 (webssh)           │
                  │   ├── reverse_proxy → 127.0.0.1:8887 (csms)             │
                  │   ├── reverse_proxy → 127.0.0.1:8091 (daemon metrics)   │
                  │   └── cert manager (ACME)                               │
                  │                                                         │
                  │  tollgate-auth-ssh (host systemd) ── :2222              │
                  │  wireguard (host kernel)        ── :51820/udp           │
                  │                                                         │
   :1812/udp ───► │  freeradius (host networking)                           │
   :2083/tcp ───► │   ├── exec → /usr/local/bin/tollgate-shim (in-container)│
                  │   │     └── TCP → tollgate-daemon:8094 (Docker bridge)  │
                  │   └── exec → /usr/local/libexec/tollgate-auth-radius-…  │
                  │                                                         │
                  │  ┌────── Docker bridge network: tollgate ──────┐        │
                  │  │ tollgate-daemon (HTTP :8091 + TCP :8094)    │        │
                  │  │   └── shared volume: tollgate-ledger        │        │
                  │  │ tollgate-auth-ocpi (HTTP :8093)             │        │
                  │  │ tollgate-csms (WS :8887)                    │        │
                  │  │ tollgate-webssh (HTTP :8092)                │        │
                  │  │ tollgate-settle (oneshot, no published)     │        │
                  │  └─────────────────────────────────────────────┘        │
                  └─────────────────────────────────────────────────────────┘
```

## Key architectural decisions (and why)

### 1. FreeRADIUS uses host networking, every other service uses bridge

RADIUS authentication is UDP-heavy (one packet per auth attempt, high PPS during attacks or large AP fleets). Docker's bridge networking adds NAT overhead and conntrack pressure that shows up at scale. RadSec (TCP 2083) is fine on bridge but the auth UDP is not.

Every other service is HTTP/WebSocket on loopback → bridge networking is fine.

**Implication**: `freeradius` cannot use Docker's DNS to resolve `tollgate-daemon`. It reaches the daemon via `127.0.0.1:8094` (the daemon's TCP listener published to host loopback).

### 2. shim → daemon uses TCP, not Unix socket

In the systemd world, `tollgate-shim` connects to `/run/tollgate/tollgate.sock` (Unix socket). The shim and daemon are both on host, sharing `/run/tollgate/`.

In Docker, both options work:

- **TCP (default in this stack)**: daemon listens on `tcp://0.0.0.0:8094`, shim dials `tcp://tollgate-daemon:8094` (or `127.0.0.1:8094` from host-networked FreeRADIUS). Simpler — no volume sharing, no UID matching.
- **Unix socket via shared volume**: daemon listens on `/run/tollgate/tollgate.sock`, the `tollgate-run` named volume is shared between daemon and freeradius containers. Requires both containers to have a `freerad` user with matching UID/GID (otherwise socket permissions break).

The shim and daemon source code supports both via the `TOLLGATE_SOCKET` env var:
- Bare path → Unix socket (legacy)
- `tcp://host:port` → TCP (new, for Docker)
- `unix:///path` → Unix socket (explicit)

See `parseSocketAddress()` in `cmd/tollgate-shim/main.go` and `cmd/tollgate-daemon/main.go`.

### 3. FreeRADIUS bundles shim + auth-radius + wrapper

FreeRADIUS's `exec` module uses `execve()` to call programs in its own filesystem namespace. The shim, auth-radius binary, and the wrapper script **must all live inside the FreeRADIUS container** so FreeRADIUS can exec them.

The Dockerfile for `freeradius` therefore builds the Go binaries in a build stage and copies them in:

```dockerfile
COPY --from=build /out/tollgate-shim        /usr/local/bin/tollgate-shim
COPY --from=build /out/tollgate-auth-radius /usr/local/bin/tollgate-auth-radius
COPY scripts/tollgate-auth-radius-delegated-wrapper.sh /usr/local/libexec/tollgate-auth-radius-delegated
```

### 4. cdk-cli is bundled in daemon + OCPI, not run as sidecar

`cdk-cli` is a Rust binary (no Go bindings). The daemon and OCPI shell out to it via `exec.Command`. We considered a sidecar "wallet service" container exposing cdk-cli over HTTP, but that adds a network hop and another failure mode for marginal isolation benefit.

The Dockerfiles for daemon and OCPI both download `cdk-cli` from GitHub releases in a prep stage and copy the static binary in. The version is pinned by build arg `CDK_CLI_VERSION`.

### 5. Stateless services use distroless, daemon/settle use alpine

Distroless (`gcr.io/distroless/static-debian12:nonroot`) is ~2MB and has no shell — strongest isolation against post-exploitation.

But the daemon shells out to `cdk-cli`, which spawns subprocesses. That needs a working `/proc` and the ability to fork/exec — distroless can do that, but it can't `sh -c` anything (no shell). Since `exec.Command("cdk-cli", ...)` uses `execve` directly, distroless technically works. We use alpine for daemon and settle anyway because:

- Alpine gives us a shell for debugging (`docker exec -it ... /bin/sh`)
- The size difference is small (alpine ~7MB vs distroless ~2MB)
- The daemon image already needs `ca-certificates` and `tzdata` for HTTPS to mints

Pure stateless services (csms, webssh) have no subprocess shell-outs and use distroless.

### 6. SSH stays on the host

`tollgate-auth-ssh` calls `useradd`, `userdel`, `chroot(2)`, manages PTYs, and chowns guest home directories. None of these work cleanly inside a container without privileged mode + host PID namespace + host `/etc/passwd` writes.

Containerizing it would require an architectural redesign (e.g., spawn guest shells inside ephemeral Docker containers, with `tollgate-auth-ssh` becoming a container orchestrator). That's Phase 4 of the roadmap.

### 7. Bind-mounted secrets, not Docker Secrets or env vars

Three options considered:

| Option | Pros | Cons |
|---|---|---|
| **Bind-mount file** (chosen) | Same security properties as today. File owned by root, mode 0600. Visible only to container that mounts it. | Requires file on host. |
| Docker Secrets (Swarm) | Files mounted at `/run/secrets/<name>`, never in `docker inspect`. | Requires Swarm mode. |
| Environment variables | Easy to set in compose. | **Visible in `docker inspect` and `/proc/PID/environ`.** Rejected. |

The compose file bind-mounts `/etc/tollgate/secrets.env` read-only into each container that needs it. The daemon and OCPI additionally use `env_file:` so the env vars get loaded into the process environment (matches the systemd deployment).

## Common operations

```bash
# Build all images
make docker-build-all

# Validate Dockerfiles + compose syntax (fast CI gate, no build)
make docker-validate

# Bring up dev stack (all services except FreeRADIUS — that needs --profile radius)
make docker-up

# Bring up everything including FreeRADIUS
make docker-up-dev      # dev mode
make docker-up-prod     # prod mode (resource limits, log rotation)

# Tail logs
make docker-logs-follow

# Get a shell in a container (alpine-based services only)
make docker-shell SVC=tollgate-daemon

# Run a settlement cycle
make docker-settle-run

# Stop everything (volumes preserved — state survives)
make docker-down

# Stop and WIPE all state (nuclear option)
docker compose -f docker/compose/docker-compose.yml down -v
```

## Gotchas

1. **First build is slow** (~3-5 min) because of `go mod download` + `cdk-cli` download. Subsequent builds use the Docker layer cache and take seconds.

2. **`docker compose down` preserves volumes** — `tollgate-ledger`, `tollgate-wallet`, etc. To wipe state (e.g., for a clean test run), use `down -v` explicitly.

3. **The daemon's Unix socket won't work in Docker by default** because the daemon runs as `tollgate` (UID 100) and FreeRADIUS runs as `freerad` (UID 105) — different UIDs, can't read each other's socket. Use TCP instead: `TOLLGATE_SOCKET=tcp://...` (already the default in the compose file).

4. **FreeRADIUS in bridge mode** (dev only) requires exposing UDP 1812 explicitly:
   ```yaml
   ports:
     - "1812:1812/udp"
   ```
   Docker's conntrack can drop UDP under high PPS. For production, use `network_mode: host` (already the default in the base compose).

5. **RadSec certs** must be synced from host Caddy into the FreeRADIUS container. The compose file uses a named volume `freeradius-certs` that must be populated by a sync script before first run. See [../docs/MIGRATION_RUNBOOK.md](../docs/MIGRATION_RUNBOOK.md) Phase 3.

6. **`host.docker.internal`** resolves to the host on macOS Docker Desktop but NOT on Linux by default. The dev compose file uses it for `TOLLGATE_SSH_ADDR`. On Linux, either:
   - Add `--add-host=host.docker.internal:host-gateway` to docker run, OR
   - Set `TOLLGATE_SSH_ADDR=172.17.0.1:2222` (default docker bridge gateway)

7. **Wallet directory permissions** — the bind-mounted `/var/lib/cashu-wallet` must be writable by UID 100 (the `tollgate` user inside the daemon container). If you're migrating state from a host-side deployment where the dir was owned by host UID 994 (tollgate), you'll need to either match UIDs or `chown -R 100:100 /var/lib/cashu-wallet` before starting the container.

## What's NOT in this directory

- **`tollgate-auth-ssh` Dockerfile** — deliberately omitted. See "SSH stays on host" above and Phase 4 of the roadmap.
- **`tollgate-shim` standalone Dockerfile** — the shim is bundled inside the FreeRADIUS image. It's never run standalone.
- **`tollgate-auth-radius` standalone Dockerfile** — same; bundled inside FreeRADIUS.
- **`tollgate-wg`, `tollgate-shell` Dockerfiles** — `wg` is a CLI used by clients (not deployed server-side), `shell` runs inside the SSH chroot (not a container).
- **CI workflow** — planned for Phase 0 but not in this commit. Will live in `.github/workflows/docker-build.yml`.
- **Published images** — nothing is pushed to a registry yet. Phase 0 exit criterion.
