VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.version=$(VERSION) -s -w"
TARGET  := music-service
INSTALL := /data/music-service
SERVICE := music-service.service

.PHONY: build cross clean deploy install-alias vet

build:
	go build $(LDFLAGS) -o $(TARGET) .

cross:
	GOOS=linux GOARCH=arm GOARM=7 go build $(LDFLAGS) -o $(TARGET) .

vet:
	go vet ./...

clean:
	rm -f $(TARGET)

# Deploy to a running scooter.
# Usage: make deploy HOST=192.168.7.1
deploy: cross
	ssh root@$(HOST) "mkdir -p $(INSTALL)"
	scp $(TARGET)   root@$(HOST):$(INSTALL)/
	scp $(SERVICE)  root@$(HOST):$(INSTALL)/$(SERVICE)
	scp $(SERVICE)  root@$(HOST):/etc/systemd/system/
	ssh root@$(HOST) " \
	  chmod +x $(INSTALL)/$(TARGET) && \
	  test -f $(INSTALL)/config.json || cp $(INSTALL)/config.json.example $(INSTALL)/config.json 2>/dev/null || true && \
	  grep -qF 'music-service/$(SERVICE)' /data/onboot.sh 2>/dev/null \
	    || printf '\n# music-service systemd unit (re-installed each boot: /etc is wiped on OS updates)\ncp $(INSTALL)/$(SERVICE) /etc/systemd/system/$(SERVICE)\nsystemctl daemon-reload\nsystemctl enable music-service\nsystemctl --no-block start music-service\n' >> /data/onboot.sh && \
	  chmod +x /data/onboot.sh && \
	  systemctl daemon-reload && \
	  systemctl enable --now $(SERVICE)"

# Set up the 'music' alias and tab-completion on the scooter.
# Usage: make install-alias HOST=192.168.7.1
install-alias:
	scp music-alias.sh       root@$(HOST):$(INSTALL)/music-alias.sh
	scp music-completion.bash root@$(HOST):$(INSTALL)/music-completion.bash
	ssh root@$(HOST) " \
	  mkdir -p /etc/bash_completion.d && \
	  cp $(INSTALL)/music-alias.sh /etc/profile.d/music-alias.sh && \
	  cp $(INSTALL)/music-completion.bash /etc/bash_completion.d/music && \
	  grep -qF 'music-service/music-alias.sh' /data/onboot.sh 2>/dev/null \
	    || printf '\n# music alias + completion (re-installed each boot: /etc is wiped on OS updates)\nmkdir -p /etc/bash_completion.d\ncp $(INSTALL)/music-alias.sh /etc/profile.d/music-alias.sh\ncp $(INSTALL)/music-completion.bash /etc/bash_completion.d/music\n' >> /data/onboot.sh && \
	  chmod +x /data/onboot.sh"
