.PHONY: build build-linux build-radius deploy deploy-radius deploy-jail deploy-faucet deploy-radius-config deploy-certs test-e2e clean

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

deploy-radius: build-radius
	scp -P $(REMOTE_PORT) $(RADIUS_BINARY) $(REMOTE_USER)@$(REMOTE_HOST):/usr/local/bin/$(RADIUS_BINARY)
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'systemctl restart freeradius && \
		 sleep 2 && \
		 systemctl status freeradius --no-pager | tail -5'

deploy-radius-config:
	scp -P $(REMOTE_PORT) config/freeradius/mods-available/cashu-exec $(REMOTE_USER)@$(REMOTE_HOST):/etc/freeradius/3.0/mods-available/cashu-exec
	scp -P $(REMOTE_PORT) config/freeradius/mods-available/eap $(REMOTE_USER)@$(REMOTE_HOST):/etc/freeradius/3.0/mods-available/eap
	scp -P $(REMOTE_PORT) config/freeradius/users $(REMOTE_USER)@$(REMOTE_HOST):/etc/freeradius/3.0/mods-config/files/authorize
	scp -P $(REMOTE_PORT) config/freeradius/clients.conf $(REMOTE_USER)@$(REMOTE_HOST):/etc/freeradius/3.0/clients.conf
	scp -P $(REMOTE_PORT) config/freeradius/sites-available/inner-tunnel $(REMOTE_USER)@$(REMOTE_HOST):/etc/freeradius/3.0/sites-available/inner-tunnel
	scp -P $(REMOTE_PORT) config/freeradius/sites-available/radsec $(REMOTE_USER)@$(REMOTE_HOST):/etc/freeradius/3.0/sites-available/radsec
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'ln -sf ../sites-available/radsec /etc/freeradius/3.0/sites-enabled/radsec 2>/dev/null; \
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

test-e2e:
	scp -P $(REMOTE_PORT) scripts/test-radius-e2e.sh $(REMOTE_USER)@$(REMOTE_HOST):/tmp/test-radius-e2e.sh
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'bash /tmp/test-radius-e2e.sh'

clean:
	rm -f $(SSH_BINARY) $(RADIUS_BINARY)
