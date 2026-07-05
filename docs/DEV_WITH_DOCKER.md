# Developing tollgate-auth with Docker

**Audience:** contributors who want to run the stack locally without setting up FreeRADIUS, cdk-cli, secrets, and the rest of the production yak-shave.

**Prerequisite:** read [docker/README.md](../docker/README.md) for the topology and architectural decisions.

This doc covers the happy path. For migration execution, see [MIGRATION_RUNBOOK.md](MIGRATION_RUNBOOK.md).

---

## Quick start

```bash
git clone https://github.com/Amperstrand/tollgate-auth.git
cd tollgate-auth

# One-time setup: create stub state dirs and secrets
sudo scripts/docker-dev-init.sh

# Build all images + bring up the stack (dev mode includes FreeRADIUS)
make docker-up-dev

# Tail logs (Ctrl-C to detach, containers keep running)
make docker-logs-follow
```

Smoke test:

```bash
# Daemon health
curl http://127.0.0.1:8091/healthz
# Expected: {"status":"ok"}

# Metrics (after a few auth attempts)
curl http://127.0.0.1:8091/metrics

# RADIUS auth (using radtest from a host with freeradius-utils installed)
radtest cashuB... anything 127.0.0.1 0 testing123
```

---

## What you get

The dev stack runs these containers:

| Container | Port on host | Purpose |
|---|---|---|
| `tollgate-daemon` | 127.0.0.1:8091 (HTTP), 8094 (TCP socket) | Auth pipeline, persistent wallet |
| `tollgate-auth-ocpi` | 127.0.0.1:8093 | OCPI eMSP for EV charging |
| `tollgate-csms` | 0.0.0.0:8887 | OCPP 1.6 CSMS for charge points |
| `tollgate-webssh` | 0.0.0.0:8092 | WebSocket → SSH bridge |
| `tollgate-freeradius` | 0.0.0.0:1812/udp, 0.0.0.0:2083/tcp | RADIUS auth + RadSec |
| `tollgate-settle` | (oneshot — run with `make docker-settle-run`) | Settlement job |

**NOT in the dev stack** (need host integration):
- `tollgate-auth-ssh` — needs `useradd`/`chroot`. Run on host with `make build && ./tollgate-auth-ssh`.
- Caddy — dev uses direct port access. Add Caddy if you're testing TLS behavior.
- WireGuard — host kernel module.

---

## Common tasks

### Rebuild a single service after a code change

```bash
# Edit code in cmd/tollgate-daemon/...

make docker-build SVC=tollgate-daemon
docker compose -f docker/compose/docker-compose.yml -f docker/compose/docker-compose.dev.yml up -d tollgate-daemon
```

### Hot reload (experimental)

The Dockerfiles don't include hot-reload tooling (no `air`, no `realize`). For tight loops, the fastest iteration is:

```bash
# Terminal 1: rebuild on every save
while true; do
  make docker-build SVC=tollgate-daemon && \
  docker compose -f docker/compose/docker-compose.yml -f docker/compose/docker-compose.dev.yml up -d tollgate-daemon
  inotifywait -r -e modify cmd/tollgate-daemon/ internal/ 2>/dev/null
done

# Terminal 2: tail logs
make docker-logs-follow
```

For serious hot-reload, install `air` (`go install github.com/air-verse/air@latest`) and run the binary on the host directly — skip Docker. Use Docker for integration testing, not for inner-loop dev.

### Run unit tests

Unit tests still run on the host (no container needed):

```bash
go test ./...
go test -race ./internal/auth/...
```

### Run a settlement cycle

```bash
make docker-settle-run
```

This runs the `tollgate-settle` container once with the current ledger, then removes it.

### Get a shell in a running container

For alpine-based images (daemon, settle, freeradius):

```bash
docker exec -it tollgate-daemon /bin/sh
```

For distroless images (csms, webssh, ocpi), there is no shell. Use `ls`, `cat`, `env`:

```bash
docker exec tollgate-csms ls -la /
docker exec tollgate-csms env
```

### Inspect the daemon's auth log

```bash
docker logs tollgate-daemon | jq 'select(.msg == "auth request")'
```

### Send a test RADIUS auth

```bash
# From a host with freeradius-utils installed
radtest "cashuB<token>" anything 127.0.0.1 0 testing123
```

---

## Working with secrets

The dev stack reads secrets from `/etc/tollgate/secrets.env` on the host. The setup script creates stub values that work with test mints only.

For real test tokens:
1. Visit the faucet at https://amperstrand.github.io/tollgate-auth/
2. Mint a token from `testnut.cashu.space` (free, fake Bitcoin)
3. Use the token in your test requests

Do NOT commit real operator nsecs. The pre-commit hook will refuse the commit.

---

## Debugging failed auth

When auth doesn't work, the failure point is one of:

1. **FreeRADIUS → shim**: FreeRADIUS exec module fails to spawn the shim
   - Symptom: RADIUS response includes `Reply-Message = "Rejected: auth daemon unavailable"`
   - Check: `docker logs tollgate-freeradius` — look for `tollgate-shim` errors
   - Check: `docker exec tollgate-freeradius ls -la /usr/local/bin/tollgate-shim` — binary present?

2. **shim → daemon**: shim can't reach the daemon
   - Symptom: same as above (shim emits that Reply-Message)
   - Check: `docker exec tollgate-freeradius nc -zv tollgate-daemon 8094` (if alpine base) or use a one-off container
   - Fix: verify `TOLLGATE_SOCKET=tcp://tollgate-daemon:8094` is set in the freeradius container

3. **daemon → mint**: daemon can't reach the mint URL
   - Symptom: RADIUS rejects with a "mint unreachable" message
   - Check: `docker logs tollgate-daemon | grep -i mint`
   - Check: `docker exec tollgate-daemon wget -qO- https://testnut.cashu.space/v1/info` (if alpine)

4. **daemon → cdk-cli**: cdk-cli fails (wallet locked, bad token, etc.)
   - Symptom: RADIUS rejects; daemon log shows cdk-cli stderr
   - Check: `docker exec tollgate-daemon /usr/local/bin/cdk-cli --work-dir /var/lib/cashu-wallet balance`

5. **Token format invalid**
   - Symptom: daemon rejects immediately, no network calls
   - Check: daemon log for "invalid token" or similar

---

## Cleanup

```bash
# Stop containers, preserve state (volumes survive)
make docker-down

# Stop containers AND wipe state
docker compose -f docker/compose/docker-compose.yml down -v

# Reclaim disk from old images
docker image prune -af
```

---

## Known limitations of the dev stack

- **No real SSH backend.** The dev `tollgate-webssh` points to `host.docker.internal:2222`, but if you're not running `tollgate-auth-ssh` on the host, WebSocket connections will fail at the SSH step. To test SSH-based flows, run `tollgate-auth-ssh` on the host first: `make build && ./tollgate-auth-ssh`.

- **No real WireGuard.** WireGuard-related endpoints in the daemon will return errors. Skip WG tests in dev or use the host-installed WireGuard.

- **No real EV charge point.** The CSMS will start but no real charge point will connect. Use `cmd/tollgate-csms/test-client/` to simulate one.

- **First build is slow.** See [docker/README.md § Gotchas](../docker/README.md#gotchas).

- **No Caddy / TLS termination.** All dev endpoints are plaintext HTTP. Test TLS-specific behavior (RadSec, OCPI mTLS) on a staging server with real Caddy.

---

## When to use Docker vs host build

| Use case | Tool |
|---|---|
| Local unit tests | `go test ./...` (host) |
| Single-binary debug | `go run ./cmd/tollgate-X/` (host) |
| Integration test (multi-service) | `make docker-up-dev` (containers) |
| Reproduce a production issue | `make docker-up-dev` with `COMPOSE_ENV=prod` |
| Test a Dockerfile change | `make docker-build SVC=<svc>` |
| Quick fix + ship | host build → `make deploy-<svc>` (systemd) |
| Migration rehearsal | `make docker-up-prod` |

---

## FAQ

**Q: Why does the dev compose bind 0.0.0.0 for some ports but 127.0.0.1 for others?**

A: 127.0.0.1 binding is the production default (security). Dev overrides publish on 0.0.0.0 for ports you might want to reach from another machine (e.g., a phone testing the CSMS, or a browser on a different host). The prod compose reverts everything to 127.0.0.1.

**Q: Why is the daemon alpine but the OCPI distroless?**

A: The daemon shells out to cdk-cli via `exec.Command`. Distroless would technically work (execve doesn't need a shell) but we keep alpine for debugging (`docker exec -it ... /bin/sh`). Stateless services (csms, webssh) get full distroless treatment — no subprocess shell-outs, smallest possible image.

**Q: Can I run the dev stack on macOS Apple Silicon?**

A: Yes. The Dockerfiles use `TARGETARCH` and the compose file passes through. The daemon image will be arm64 native; the FreeRADIUS image is amd64-only upstream and will run under qemu emulation (slower but works).

**Q: How do I add a new service?**

A:
1. Write `cmd/<new-service>/main.go`
2. Create `docker/<new-service>/Dockerfile` (copy from a similar service)
3. Add a service block to `docker/compose/docker-compose.yml`
4. Add the service name to `DOCKER_SERVICES` in the Makefile
5. Run `make docker-build SVC=<new-service>` then `make docker-up-dev`
