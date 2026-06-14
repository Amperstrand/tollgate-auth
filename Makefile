.PHONY: build build-linux build-radius build-settle deploy deploy-radius deploy-all deploy-settle deploy-jail deploy-faucet deploy-radius-config deploy-certs test test-unit test-race test-accounting test-radius-local test-all-available test-e2e clean install-hooks

TOLLGATE_RS_DIR := /Users/macbook/src/tollgate-rs

SSH_BINARY := tollgate-auth-ssh
RADIUS_BINARY := tollgate-auth-radius
REMOTE_USER := root
REMOTE_HOST := nodns.shop
REMOTE_PORT := 22
REMOTE_DIR := /opt/cashu-tollgate

build:
	go build -o $(SSH_BINARY) ./cmd/tollgate-auth-ssh

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(SSH_BINARY) ./cmd/tollgate-auth-ssh

build-radius:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(RADIUS_BINARY) ./cmd/tollgate-auth-radius/

build-settle:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o tollgate-settle ./cmd/tollgate-settle/

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
	scp -P $(REMOTE_PORT) config/freeradius/mods-available/eap $(REMOTE_USER)@$(REMOTE_HOST):/etc/freeradius/3.0/mods-available/eap
	scp -P $(REMOTE_PORT) config/freeradius/users $(REMOTE_USER)@$(REMOTE_HOST):/etc/freeradius/3.0/mods-config/files/authorize
	scp -P $(REMOTE_PORT) config/freeradius/clients.conf $(REMOTE_USER)@$(REMOTE_HOST):/etc/freeradius/3.0/clients.conf
	scp -P $(REMOTE_PORT) config/freeradius/sites-available/inner-tunnel $(REMOTE_USER)@$(REMOTE_HOST):/etc/freeradius/3.0/sites-available/inner-tunnel
	scp -P $(REMOTE_PORT) config/freeradius/sites-available/radsec $(REMOTE_USER)@$(REMOTE_HOST):/etc/freeradius/3.0/sites-available/radsec
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'ln -sf ../sites-available/radsec /etc/freeradius/3.0/sites-enabled/radsec 2>/dev/null; \
		 ln -sf ../mods-available/cashu-exec-delegated /etc/freeradius/3.0/mods-enabled/cashu-exec-delegated 2>/dev/null; \
		 mkdir -p /etc/freeradius/3.0/certs/radsec && \
		 CADDY_LE="/var/lib/caddy/.local/share/caddy/certificates/acme-v02.api.letsencrypt.org-directory/nodns.shop" && \
		 cp $$CADDY_LE/nodns.shop.crt /etc/freeradius/3.0/certs/radsec/server.crt && \
		 cp $$CADDY_LE/nodns.shop.key /etc/freeradius/3.0/certs/radsec/server.key && \
		 chown -R root:freerad /etc/freeradius/3.0/certs/radsec && \
		 chmod 640 /etc/freeradius/3.0/certs/radsec/server.key && \
		 chmod 644 /etc/freeradius/3.0/certs/radsec/server.crt && \
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

deploy-jail:
	scp -P $(REMOTE_PORT) scripts/setup-jail.sh $(REMOTE_USER)@$(REMOTE_HOST):/tmp/setup-jail.sh
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'bash /tmp/setup-jail.sh'

deploy-faucet:
	scp -P $(REMOTE_PORT) docs/index.html $(REMOTE_USER)@$(REMOTE_HOST):/tmp/tollgate-faucet.html
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'mkdir -p /var/www/tollgate && \
		 cp /tmp/tollage-faucet.html /var/www/tollgate/index.html'

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
	@echo "All local tests passed. For live tests: make test-e2e"

test-e2e:
	scp -P $(REMOTE_PORT) scripts/test-radius-e2e.sh $(REMOTE_USER)@$(REMOTE_HOST):/tmp/test-radius-e2e.sh
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'bash /tmp/test-radius-e2e.sh'

clean:
	rm -f $(SSH_BINARY) $(RADIUS_BINARY)

install-hooks: ## Install git pre-commit, commit-msg, and pre-push hooks
	ln -sf ../../scripts/git-hooks/pre-commit .git/hooks/pre-commit
	ln -sf ../../scripts/git-hooks/commit-msg .git/hooks/commit-msg
	ln -sf ../../scripts/git-hooks/pre-push .git/hooks/pre-push
	chmod +x scripts/git-hooks/pre-commit scripts/git-hooks/commit-msg scripts/git-hooks/pre-push
	@echo "Git hooks installed:"
	@echo "  pre-commit: gofmt + go vet + secret scan"
	@echo "  pre-push:   go test -race ./..."
	@echo "  commit-msg: ASCII-only, conventional commit format"
