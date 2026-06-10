.PHONY: build build-linux deploy deploy-jail deploy-faucet clean

SSH_BINARY := tollgate-auth-ssh
REMOTE_USER := debian
REMOTE_HOST := npub1mv7l45exqsu5nr5tnefkr33ruhzjj4r8prg6qtcedv4lyf3rzguqptuwm4.nodns.shop
REMOTE_PORT := 2222
REMOTE_DIR := /opt/cashu-tollgate

build:
	go build -o $(SSH_BINARY) ./cmd/tollgate-auth-ssh

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(SSH_BINARY) ./cmd/tollgate-auth-ssh

deploy: build-linux
	scp -P $(REMOTE_PORT) $(SSH_BINARY) $(REMOTE_USER)@$(REMOTE_HOST):/tmp/$(SSH_BINARY)
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'sudo systemctl stop cashu-tollgate && \
		 sudo cp /tmp/$(SSH_BINARY) $(REMOTE_DIR)/$(SSH_BINARY) && \
		 sudo chmod +x $(REMOTE_DIR)/$(SSH_BINARY) && \
		 sudo mkdir -p $(REMOTE_DIR)/sessions && \
		 sudo systemctl start cashu-tollgate && \
		 sleep 2 && \
		 sudo systemctl status cashu-tollgate --no-pager | tail -5'

deploy-jail:
	scp -P $(REMOTE_PORT) scripts/setup-jail.sh $(REMOTE_USER)@$(REMOTE_HOST):/tmp/setup-jail.sh
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'sudo bash /tmp/setup-jail.sh'

deploy-faucet:
	scp -P $(REMOTE_PORT) docs/index.html $(REMOTE_USER)@$(REMOTE_HOST):/tmp/tollgate-faucet.html
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'sudo mkdir -p /var/www/tollgate && \
		 sudo cp /tmp/tollgate-faucet.html /var/www/tollgate/index.html'

clean:
	rm -f $(SSH_BINARY)
