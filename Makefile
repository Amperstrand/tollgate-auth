.PHONY: build build-linux deploy clean

BINARY := cashu-tollgate
REMOTE_USER := debian
REMOTE_HOST := npub1mv7l45exqsu5nr5tnefkr33ruhzjj4r8prg6qtcedv4lyf3rzguqptuwm4.nodns.shop
REMOTE_PORT := 2222
REMOTE_DIR := /opt/cashu-tollgate

build:
	go build -o $(BINARY) .

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(BINARY) .

deploy: build-linux
	scp -P $(REMOTE_PORT) $(BINARY) $(REMOTE_USER)@$(REMOTE_HOST):/tmp/$(BINARY)
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'sudo systemctl stop cashu-tollgate && \
		 sudo cp /tmp/$(BINARY) $(REMOTE_DIR)/$(BINARY) && \
		 sudo chmod +x $(REMOTE_DIR)/$(BINARY) && \
		 sudo systemctl start cashu-tollgate && \
		 sleep 2 && \
		 sudo systemctl status cashu-tollgate --no-pager | tail -5'

deploy-faucet:
	scp -P $(REMOTE_PORT) docs/index.html $(REMOTE_USER)@$(REMOTE_HOST):/tmp/tollgate-faucet.html
	ssh -p $(REMOTE_PORT) $(REMOTE_USER)@$(REMOTE_HOST) \
		'sudo mkdir -p /var/www/tollgate && \
		 sudo cp /tmp/tollgate-faucet.html /var/www/tollgate/index.html'

clean:
	rm -f $(BINARY)
