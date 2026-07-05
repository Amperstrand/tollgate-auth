.PHONY: build build-linux build-radius build-ocpi build-settle build-daemon build-shim build-shell deploy deploy-radius deploy-ocpi deploy-all deploy-settle deploy-daemon deploy-jail deploy-faucet deploy-radius-config deploy-certs deploy-systemd-units deploy-docker-host-deps test test-unit test-race test-accounting test-radius-local test-all-available test-e2e test-freeradius-config clean install-hooks docker-build docker-build-all docker-up docker-up-dev docker-up-prod docker-down docker-logs docker-logs-follow docker-ps docker-shell docker-settle-run docker-validate

TOLLGATE_RS_DIR ?= $(HOME)/src/tollgate-rs

SSH_BINARY := tollgate-auth-ssh
RADIUS_BINARY := tollgate-auth-radius
OCPI_BINARY := tollgate-auth-ocpi
DAEMON_BINARY := tollgate-daemon
SHIM_BINARY := tollgate-shim
SHELL_BINARY := tollgate-shell
REMOTE_USER := root
REMOTE_HOST := nodns.shop
REMOTE_PORT := 22
REMOTE_DIR := /opt/tollgate-auth

build:
	go build -o $(SSH_BINARY) ./cmd/tollgate-auth-ssh

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(SSH_BINARY) ./cmd/tollgate-auth-ssh

build-radius:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(RADIUS_BINARY) ./cmd/tollgate-auth-radius/

build-ocpi:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(OCPI_BINARY) ./cmd/tollgate-auth-ocpi/

build-ocpi-local:
	go build -o $(OCPI_BINARY) ./cmd/tollgate-auth-ocpi/

build-settle:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o tollgate-settle ./cmd/tollgate-settle/

build-daemon:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(DAEMON_BINARY) ./cmd/tollgate-daemon/

build-shim:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(SHIM_BINARY) ./cmd/tollgate-shim/

build-shell:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(SHELL_BINARY) ./cmd/tollgate-shell/

deploy: build-linux
	scp -P $(REMOTE_PORT) $(SSH_BINARY) $(REMOTE_USER)@$(REMOTE_HOST):/tmp/$(SSH_BINARY)
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'systemctl stop tollgate-auth-ssh && \
		 cp /tmp/$(SSH_BINARY) $(REMOTE_DIR)/$(SSH_BINARY) && \
		 chmod +x $(REMOTE_DIR)/$(SSH_BINARY) && \
		 mkdir -p $(REMOTE_DIR)/sessions && \
		 systemctl start tollgate-auth-ssh && \
		 sleep 2 && \
		 systemctl status tollgate-auth-ssh --no-pager | tail -5'

deploy-settle: build-settle
	scp -P $(REMOTE_PORT) tollgate-settle $(REMOTE_USER)@$(REMOTE_HOST):/tmp/tollgate-settle
	scp -P $(REMOTE_PORT) scripts/run-settle.sh $(REMOTE_USER)@$(REMOTE_HOST):/tmp/run-settle.sh
	scp -P $(REMOTE_PORT) config/systemd/tollgate-settle.service $(REMOTE_USER)@$(REMOTE_HOST):/etc/systemd/system/tollgate-settle.service
	scp -P $(REMOTE_PORT) config/systemd/tollgate-settle.timer $(REMOTE_USER)@$(REMOTE_HOST):/etc/systemd/system/tollgate-settle.timer
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'cp /tmp/tollgate-settle $(REMOTE_DIR)/tollgate-settle && \
		 chmod +x $(REMOTE_DIR)/tollgate-settle && \
		 cp /tmp/run-settle.sh /usr/local/sbin/run-settle.sh && \
		 chmod +x /usr/local/sbin/run-settle.sh && \
		 mkdir -p /etc/tollgate && \
		 systemctl daemon-reload && \
		 systemctl enable tollgate-settle.timer && \
		 systemctl start tollgate-settle.timer && \
		 systemctl list-timers tollgate-settle.timer --no-pager'

deploy-radius: build-radius
	scp -P $(REMOTE_PORT) $(RADIUS_BINARY) $(REMOTE_USER)@$(REMOTE_HOST):/usr/local/bin/$(RADIUS_BINARY)
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'systemctl restart freeradius && \
		 sleep 2 && \
		 systemctl status freeradius --no-pager | tail -5'

deploy-ocpi: build-ocpi
	scp -P $(REMOTE_PORT) $(OCPI_BINARY) $(REMOTE_USER)@$(REMOTE_HOST):/usr/local/bin/$(OCPI_BINARY)
	scp -P $(REMOTE_PORT) config/systemd/tollgate-auth-ocpi.service $(REMOTE_USER)@$(REMOTE_HOST):/etc/systemd/system/tollgate-auth-ocpi.service
	scp -P $(REMOTE_PORT) config/caddy/ocpi.conf $(REMOTE_USER)@$(REMOTE_HOST):/etc/caddy/sites-available/ocpi.conf 2>/dev/null || \
	  scp -P $(REMOTE_PORT) config/caddy/ocpi.conf $(REMOTE_USER)@$(REMOTE_HOST):/tmp/ocpi.conf
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'chmod +x /usr/local/bin/$(OCPI_BINARY) && \
		 (ln -sf ../sites-available/ocpi.conf /etc/caddy/sites-enabled/ocpi.conf 2>/dev/null || true) && \
		 (systemctl reload caddy 2>/dev/null || echo "Caddy reload skipped — install config manually if needed") && \
		 systemctl daemon-reload && \
		 systemctl enable tollgate-auth-ocpi && \
		 systemctl restart tollgate-auth-ocpi && \
		 sleep 2 && \
		 systemctl is-active tollgate-auth-ocpi && \
		 echo "OCPI eMSP deployed. Dashboard: https://ocpi.nodns.shop/"'

deploy-daemon: build-daemon build-shim
	scp -P $(REMOTE_PORT) $(DAEMON_BINARY) $(REMOTE_USER)@$(REMOTE_HOST):/usr/local/bin/$(DAEMON_BINARY)
	scp -P $(REMOTE_PORT) $(SHIM_BINARY) $(REMOTE_USER)@$(REMOTE_HOST):/usr/local/bin/$(SHIM_BINARY)
	scp -P $(REMOTE_PORT) config/systemd/tollgate-daemon.service $(REMOTE_USER)@$(REMOTE_HOST):/etc/systemd/system/tollgate-daemon.service
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'chmod +x /usr/local/bin/$(DAEMON_BINARY) /usr/local/bin/$(SHIM_BINARY) && \
		 systemctl daemon-reload && \
		 systemctl enable tollgate-daemon && \
		 systemctl restart tollgate-daemon && \
		 sleep 2 && \
		 systemctl is-active tollgate-daemon && \
		 echo "Daemon deployed. To switch FreeRADIUS to shim mode:" && \
		 echo "  Update mods-available/cashu-exec program= to use $(SHIM_BINARY)" && \
		 echo "  Then: systemctl restart freeradius"'

deploy-all: deploy-radius deploy-rs
	@echo "=== Both binaries deployed ==="
	@echo "Go (RADIUS):   $(REMOTE_HOST):/usr/local/bin/$(RADIUS_BINARY)"
	@echo "Rust (session): $(REMOTE_HOST):/usr/local/bin/tollgate-net"

deploy-rs:
	cd $(TOLLGATE_RS_DIR) && cargo zigbuild --release --target x86_64-unknown-linux-gnu -p tollgate-net
	scp -P $(REMOTE_PORT) $(TOLLGATE_RS_DIR)/target/x86_64-unknown-linux-gnu/release/tollgate-net $(REMOTE_USER)@$(REMOTE_HOST):/usr/local/bin/tollgate-net.new
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'cp /usr/local/bin/tollgate-net /usr/local/bin/tollgate-net.bak && \
		 mv /usr/local/bin/tollgate-net.new /usr/local/bin/tollgate-net && \
		 chmod +x /usr/local/bin/tollgate-net && \
		 systemctl restart tollgate-net && \
		 sleep 2 && \
		 systemctl is-active tollgate-net'

deploy-radius-config:
	scp -P $(REMOTE_PORT) config/freeradius/mods-available/cashu-exec $(REMOTE_USER)@$(REMOTE_HOST):/etc/freeradius/3.0/mods-available/cashu-exec
	scp -P $(REMOTE_PORT) config/freeradius/mods-available/cashu-exec-delegated $(REMOTE_USER)@$(REMOTE_HOST):/etc/freeradius/3.0/mods-available/cashu-exec-delegated
	scp -P $(REMOTE_PORT) config/freeradius/mods-available/tollgate-acct $(REMOTE_USER)@$(REMOTE_HOST):/etc/freeradius/3.0/mods-available/tollgate-acct
	scp -P $(REMOTE_PORT) config/freeradius/mods-available/eap $(REMOTE_USER)@$(REMOTE_HOST):/etc/freeradius/3.0/mods-available/eap
	scp -P $(REMOTE_PORT) config/freeradius/users $(REMOTE_USER)@$(REMOTE_HOST):/etc/freeradius/3.0/mods-config/files/authorize
	scp -P $(REMOTE_PORT) config/freeradius/clients.conf $(REMOTE_USER)@$(REMOTE_HOST):/etc/freeradius/3.0/clients.conf
	scp -P $(REMOTE_PORT) config/freeradius/sites-available/inner-tunnel $(REMOTE_USER)@$(REMOTE_HOST):/etc/freeradius/3.0/sites-available/inner-tunnel
	scp -P $(REMOTE_PORT) config/freeradius/sites-available/radsec $(REMOTE_USER)@$(REMOTE_HOST):/etc/freeradius/3.0/sites-available/radsec
	scp -P $(REMOTE_PORT) scripts/tollgate-auth-radius-delegated-wrapper.sh $(REMOTE_USER)@$(REMOTE_HOST):/tmp/tollgate-auth-radius-delegated-wrapper.sh
	scp -P $(REMOTE_PORT) scripts/check-freeradius-configs.sh $(REMOTE_USER)@$(REMOTE_HOST):/tmp/check-freeradius-configs.sh
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'mkdir -p /etc/tollgate && \
		 if [ ! -f /etc/tollgate/secrets.env ]; then \
		   echo "# /etc/tollgate/secrets.env - secrets NOT in version control" > /etc/tollgate/secrets.env && \
		   echo "# Generate a new nsec: see docs/known-unknowns.md" >> /etc/tollgate/secrets.env && \
		   echo "TOLLGATE_OPERATOR_NSEC=" >> /etc/tollgate/secrets.env && \
		   echo "TOLLGATE_API_KEY=" >> /etc/tollgate/secrets.env && \
		   chmod 600 /etc/tollgate/secrets.env && \
		   echo "WARNING: Created /etc/tollgate/secrets.env with empty values." && \
		   echo "         Edit it to set TOLLGATE_OPERATOR_NSEC and TOLLGATE_API_KEY."; \
		 fi && \
		 mkdir -p /usr/local/libexec && \
		 cp /tmp/tollgate-auth-radius-delegated-wrapper.sh /usr/local/libexec/tollgate-auth-radius-delegated && \
		 chown root:root /usr/local/libexec/tollgate-auth-radius-delegated && \
		 chmod 0755 /usr/local/libexec/tollgate-auth-radius-delegated && \
		 ln -sf ../sites-available/radsec /etc/freeradius/3.0/sites-enabled/radsec 2>/dev/null; \
		 ln -sf ../mods-available/cashu-exec /etc/freeradius/3.0/mods-enabled/cashu-exec 2>/dev/null; \
		 ln -sf ../mods-available/cashu-exec-delegated /etc/freeradius/3.0/mods-enabled/cashu-exec-delegated 2>/dev/null; \
		 ln -sf ../mods-available/tollgate-acct /etc/freeradius/3.0/mods-enabled/tollgate-acct 2>/dev/null; \
		 mkdir -p /etc/freeradius/3.0/certs/radsec && \
		 CADDY_LE="/var/lib/caddy/.local/share/caddy/certificates/acme-v02.api.letsencrypt.org-directory/nodns.shop" && \
		 cp $$CADDY_LE/nodns.shop.crt /etc/freeradius/3.0/certs/radsec/server.crt && \
		 cp $$CADDY_LE/nodns.shop.key /etc/freeradius/3.0/certs/radsec/server.key && \
		 chown -R root:freerad /etc/freeradius/3.0/certs/radsec && \
		 chmod 640 /etc/freeradius/3.0/certs/radsec/server.key && \
		 chmod 644 /etc/freeradius/3.0/certs/radsec/server.crt && \
		 sh /tmp/check-freeradius-configs.sh /etc/freeradius/3.0 && \
		 freeradius -XC && systemctl restart freeradius'

deploy-certs:
	scp -P $(REMOTE_PORT) scripts/sync-caddy-certs.sh $(REMOTE_USER)@$(REMOTE_HOST):/usr/local/sbin/sync-caddy-certs-to-freeradius
	scp -P $(REMOTE_PORT) config/systemd/sync-caddy-certs.service $(REMOTE_USER)@$(REMOTE_HOST):/etc/systemd/system/sync-caddy-certs.service
	scp -P $(REMOTE_PORT) config/systemd/sync-caddy-certs.timer $(REMOTE_USER)@$(REMOTE_HOST):/etc/systemd/system/sync-caddy-certs.timer
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'chmod +x /usr/local/sbin/sync-caddy-certs-to-freeradius && \
		 mkdir -p /etc/freeradius/3.0/certs/letsencrypt && \
		 chown freerad:freerad /etc/freeradius/3.0/certs/letsencrypt && \
		 chmod 0700 /etc/freeradius/3.0/certs/letsencrypt && \
		 systemctl daemon-reload && \
		 systemctl enable sync-caddy-certs.timer && \
		 /usr/local/sbin/sync-caddy-certs-to-freeradius'

# deploy-systemd-units — sync hardened systemd unit files + drop-in overrides.
#
# This target deploys the repo's hardened systemd units (User=tollgate,
# ProtectSystem=strict, CapabilityBoundingSet, IPAddressDeny for
# loopback-only services, etc.) and the drop-in overrides for the
# externally-managed services (tollgate-net, tollgate-csms, tollgate-webssh)
# that lack a --bind flag in their host binaries.
#
# It creates the `tollgate` system user if missing, adds it to the
# cashu-wallet and freerad supplementary groups, then for each unit:
#   1. scp the unit / override file to the server
#   2. daemon-reload
#   3. restart the service
#   4. verify it is active
#
# Safe to re-run; idempotent.
deploy-systemd-units:
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'id tollgate >/dev/null 2>&1 || useradd -r -s /usr/sbin/nologin tollgate && \
		 usermod -aG cashu-wallet,freerad tollgate 2>/dev/null || true && \
		 id tollgate'
	scp -P $(REMOTE_PORT) config/systemd/tollgate-auth-ssh.service    $(REMOTE_USER)@$(REMOTE_HOST):/etc/systemd/system/tollgate-auth-ssh.service
	scp -P $(REMOTE_PORT) config/systemd/tollgate-daemon.service      $(REMOTE_USER)@$(REMOTE_HOST):/etc/systemd/system/tollgate-daemon.service
	scp -P $(REMOTE_PORT) config/systemd/tollgate-auth-ocpi.service   $(REMOTE_USER)@$(REMOTE_HOST):/etc/systemd/system/tollgate-auth-ocpi.service
	scp -P $(REMOTE_PORT) config/systemd/tollgate-settle.service      $(REMOTE_USER)@$(REMOTE_HOST):/etc/systemd/system/tollgate-settle.service
	scp -P $(REMOTE_PORT) config/systemd/sync-caddy-certs.service     $(REMOTE_USER)@$(REMOTE_HOST):/etc/systemd/system/sync-caddy-certs.service
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'mkdir -p /etc/systemd/system/tollgate-net.service.d    && \
		 mkdir -p /etc/systemd/system/tollgate-csms.service.d   && \
		 mkdir -p /etc/systemd/system/tollgate-webssh.service.d && \
		 systemctl daemon-reload && \
		 systemctl restart tollgate-auth-ssh tollgate-daemon tollgate-auth-ocpi tollgate-settle sync-caddy-certs.timer && \
		 sleep 3 && \
		 for svc in tollgate-auth-ssh tollgate-daemon tollgate-auth-ocpi; do \
		   printf "%-25s %s\n" "$$svc" "$$(systemctl is-active $$svc)"; \
		 done'
	scp -P $(REMOTE_PORT) config/systemd/overrides/tollgate-net.service.d/override.conf    $(REMOTE_USER)@$(REMOTE_HOST):/etc/systemd/system/tollgate-net.service.d/override.conf
	scp -P $(REMOTE_PORT) config/systemd/overrides/tollgate-csms.service.d/override.conf   $(REMOTE_USER)@$(REMOTE_HOST):/etc/systemd/system/tollgate-csms.service.d/override.conf
	scp -P $(REMOTE_PORT) config/systemd/overrides/tollgate-webssh.service.d/override.conf $(REMOTE_USER)@$(REMOTE_HOST):/etc/systemd/system/tollgate-webssh.service.d/override.conf
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'systemctl daemon-reload && \
		 systemctl restart tollgate-net tollgate-csms tollgate-webssh && \
		 sleep 2 && \
		 for svc in tollgate-net tollgate-csms tollgate-webssh; do \
		   printf "%-25s %s\n" "$$svc" "$$(systemctl is-active $$svc)"; \
		 done && \
		 echo "---" && \
		 ss -tlnp | grep -E ":2121|:8092|:8887|:8091|:8093"'

deploy-jail: build-shell
	scp -P $(REMOTE_PORT) scripts/setup-jail.sh $(REMOTE_USER)@$(REMOTE_HOST):/tmp/setup-jail.sh
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'bash /tmp/setup-jail.sh'

deploy-faucet:
	scp -P $(REMOTE_PORT) docs/index.html $(REMOTE_USER)@$(REMOTE_HOST):/tmp/tollgate-faucet.html
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'mkdir -p /var/www/tollgate && \
		 cp /tmp/tollgate-faucet.html /var/www/tollgate/index.html'

test: ## Run all unit tests (safe, local, deterministic)
	go test ./...

test-unit: ## Run unit tests with verbose output
	go test -v ./...

test-race: ## Run tests with race detector
	go test -race ./...

test-accounting: ## Run only accounting-related tests
	go test -v -run "TestAccounting|TestRecordAccounting|TestParse" ./...

test-radius-local: ## Run local RADIUS tests (no live server needed)
	go test -v ./internal/radius/...

test-all-available: ## Run all tests that can run locally
	go test -race ./...
	scripts/check-freeradius-configs.sh
	@echo "All local tests passed. For live tests: make test-e2e"

test-freeradius-config: ## Static guard: no /bin/sh -c with %{...} in config/freeradius/
	scripts/check-freeradius-configs.sh

test-e2e:
	scp -P $(REMOTE_PORT) scripts/test-radius-e2e.sh $(REMOTE_USER)@$(REMOTE_HOST):/tmp/test-radius-e2e.sh
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'bash /tmp/test-radius-e2e.sh'

clean:
	rm -f $(SSH_BINARY) $(RADIUS_BINARY) $(DAEMON_BINARY) $(SHIM_BINARY)

install-hooks: ## Install git pre-commit, commit-msg, and pre-push hooks
	ln -sf ../../scripts/git-hooks/pre-commit .git/hooks/pre-commit
	ln -sf ../../scripts/git-hooks/commit-msg .git/hooks/commit-msg
	ln -sf ../../scripts/git-hooks/pre-push .git/hooks/pre-push
	chmod +x scripts/git-hooks/pre-commit scripts/git-hooks/commit-msg scripts/git-hooks/pre-push
	@echo "Git hooks installed:"
	@echo "  pre-commit: gofmt + go vet + secret scan"
	@echo "  pre-push:   go test -race ./..."
	@echo "  commit-msg: ASCII-only, conventional commit format"

# ─── Docker targets ───────────────────────────────────────────────────────
# These targets build images and run the containerized stack defined in
# docker/compose/docker-compose.yml. The stack is PLANNING-ONLY as of this
# commit — no images are published, no containers are run in production.
# See docs/DOCKER_MIGRATION_ROADMAP.md for the phased migration plan.
#
# Prerequisites (host):
#   - Docker 24+ with buildx
#   - docker compose v2 (Go-based, ships with Docker Desktop / docker-compose-plugin)
#   - /opt/tollgate-auth, /opt/cashu-tollgate, /var/lib/cashu-wallet exist on host
#   - /etc/tollgate/secrets.env exists with TOLLGATE_OPERATOR_NSEC and TOLLGATE_API_KEY
#   - For FreeRADIUS: RadSec TLS certs synced to /etc/freeradius/3.0/certs/letsencrypt/

COMPOSE_DIR := docker/compose
COMPOSE_BASE := $(COMPOSE_DIR)/docker-compose.yml
COMPOSE_DEV  := $(COMPOSE_DIR)/docker-compose.dev.yml
COMPOSE_PROD := $(COMPOSE_DIR)/docker-compose.prod.yml

# Default to dev overrides if no environment is specified. Override with
# COMPOSE_ENV=prod to use prod overrides.
COMPOSE_ENV ?= dev
COMPOSE_FILES := -f $(COMPOSE_BASE)
ifeq ($(COMPOSE_ENV),prod)
  COMPOSE_FILES += -f $(COMPOSE_PROD)
else
  COMPOSE_FILES += -f $(COMPOSE_DEV)
endif

# Service list — kept in sync with docker/compose/docker-compose.yml.
# Add new services here when they are added to the compose file.
DOCKER_SERVICES := tollgate-daemon tollgate-auth-ocpi tollgate-csms tollgate-webssh freeradius tollgate-settle

# Build a single service image.
# Usage: make docker-build SVC=tollgate-daemon
docker-build:
	@if [ -z "$(SVC)" ]; then echo "Usage: make docker-build SVC=<service-name>"; exit 1; fi
	docker compose $(COMPOSE_FILES) build $(SVC)

# Build ALL service images. Used by CI to verify Dockerfiles compile cleanly.
docker-build-all:
	@for svc in $(DOCKER_SERVICES); do \
	  echo "=== Building $$svc ==="; \
	  docker compose $(COMPOSE_FILES) build $$svc || exit 1; \
	done
	@echo "All images built successfully."

# Validate Dockerfile + compose syntax without building. Fast CI gate.
docker-validate:
	@echo "=== Validating compose files ==="
	docker compose -f $(COMPOSE_BASE) config -q
	docker compose -f $(COMPOSE_BASE) -f $(COMPOSE_DEV) config -q
	docker compose -f $(COMPOSE_BASE) -f $(COMPOSE_PROD) config -q
	@echo "compose files OK"
	@echo "=== Verifying Dockerfiles exist ==="
	@for svc in tollgate-auth-ocpi tollgate-csms tollgate-webssh tollgate-daemon tollgate-settle freeradius; do \
	  test -f docker/$$svc/Dockerfile || { echo "MISSING: docker/$$svc/Dockerfile"; exit 1; }; \
	done
	@echo "All Dockerfiles present."

# Bring up the dev stack (everything except FreeRADIUS, which needs --profile radius)
docker-up: docker-build-all
	docker compose $(COMPOSE_FILES) up -d
	@echo ""
	@echo "Stack is up. Useful commands:"
	@echo "  make docker-logs       # tail all logs"
	@echo "  make docker-ps         # show running containers"
	@echo "  make docker-down       # stop everything"

# Explicit prod-mode up — same as docker-up but with prod overrides + radius profile.
docker-up-prod:
	COMPOSE_ENV=prod docker compose $(COMPOSE_FILES) --profile radius up -d
	@echo ""
	@echo "Production stack is up (with FreeRADIUS)."

# Convenience alias — start everything including FreeRADIUS in dev mode.
docker-up-dev:
	docker compose -f $(COMPOSE_BASE) -f $(COMPOSE_DEV) --profile radius up -d

# Stop and remove containers (volumes preserved).
docker-down:
	docker compose $(COMPOSE_FILES) down
	@echo "Containers stopped. Volumes preserved — use 'docker compose down -v' to wipe state."

# Tail logs from all services.
docker-logs:
	docker compose $(COMPOSE_FILES) logs --tail=100

docker-logs-follow:
	docker compose $(COMPOSE_FILES) logs -f

# Show running containers + key health info.
docker-ps:
	@docker compose $(COMPOSE_FILES) ps
	@echo ""
	@echo "=== Listening ports (host side) ==="
	@docker compose $(COMPOSE_FILES) ps --format json 2>/dev/null | \
	  grep -oE '"Publishers":\[[^]]+\]' | grep -oE '"PublishedPort":[0-9]+' | sort -u || true

# Get a shell in a running container (uses alpine sh since distroless has none).
# Usage: make docker-shell SVC=tollgate-daemon
docker-shell:
	@if [ -z "$(SVC)" ]; then echo "Usage: make docker-shell SVC=<service-name>"; exit 1; fi
	@# Distroless images have no shell — fall back to `docker run --rm -it <image> /bin/sh`
	@# by re-running with an alpine base. For now, this only works on alpine-based images
	@# (tollgate-daemon, tollgate-settle). For distroless images, use `docker exec` with
	@# an explicit binary like `ls`, `cat`, `env`.
	docker exec -it $(SVC) /bin/sh || \
	  echo "No shell in $$(SVC) (distroless). Try: docker exec $(SVC) ls -la /opt/tollgate-auth"

# Manually trigger a settlement run (oneshot).
docker-settle-run:
	docker compose -f $(COMPOSE_BASE) --profile run-settle run --rm tollgate-settle

# Deploy host-side Docker dependencies: shim wrapper, cert sync script,
# deploy-containers.sh. Idempotent. Run after the first Docker install
# OR after rebuilding the server from scratch.
deploy-docker-host-deps:
	scp -P $(REMOTE_PORT) scripts/tollgate-shim-tcp-wrapper.sh $(REMOTE_USER)@$(REMOTE_HOST):/tmp/tollgate-shim-tcp-wrapper.sh
	scp -P $(REMOTE_PORT) scripts/sync-caddy-certs.sh $(REMOTE_USER)@$(REMOTE_HOST):/usr/local/sbin/sync-caddy-certs-to-freeradius
	scp -P $(REMOTE_PORT) docker/deploy-containers.sh $(REMOTE_USER)@$(REMOTE_HOST):/usr/local/sbin/tollgate-deploy-containers
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'mkdir -p /usr/local/libexec && \
		 mv /tmp/tollgate-shim-tcp-wrapper.sh /usr/local/libexec/tollgate-shim-tcp-wrapper.sh && \
		 chown root:root /usr/local/libexec/tollgate-shim-tcp-wrapper.sh && \
		 chmod 0755 /usr/local/libexec/tollgate-shim-tcp-wrapper.sh && \
		 chmod +x /usr/local/sbin/sync-caddy-certs-to-freeradius /usr/local/sbin/tollgate-deploy-containers && \
		 echo "OK: host-side Docker dependencies installed:" && \
		 ls -la /usr/local/libexec/tollgate-shim-tcp-wrapper.sh /usr/local/sbin/sync-caddy-certs-to-freeradius /usr/local/sbin/tollgate-deploy-containers'
