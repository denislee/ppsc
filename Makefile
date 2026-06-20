# ppsc — common developer tasks.
# Override the host/port or db path: `make run ADDR=127.0.0.1:9000 DB=/tmp/ppsc.db`

APP   := ppsc
# Bind to all interfaces by default so others on your LAN can reach the UI.
# Override to loopback-only with `make run ADDR=127.0.0.1:8080`.
ADDR  ?= 0.0.0.0:8080
DB    ?= ppsc.db
# Port parsed out of ADDR (the part after the last ':') for the browse URLs.
PORT  := $(lastword $(subst :, ,$(ADDR)))
# The browser/health-check always targets loopback even when bound to 0.0.0.0.
URL   := http://127.0.0.1:$(PORT)/

# Best-effort LAN address to hand out to other devices on the network.
ifeq ($(shell uname),Darwin)
LAN_IP := $(shell ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null)
else
LAN_IP := $(shell hostname -I 2>/dev/null | awk '{print $$1}' || true)
LAN_IP := $(or $(LAN_IP),$(shell ip route get 1.1.1.1 2>/dev/null | grep -oP 'src \K\S+'))
endif

# `make run` enables verbose debug logging by default. Turn it off with DEBUG=0.
DEBUG ?= 1
ifeq ($(DEBUG),1)
DEBUG_FLAG := -debug
endif

# Pick the platform's default-browser opener.
ifeq ($(shell uname),Darwin)
OPEN := open
else
OPEN := xdg-open
endif

.PHONY: run build test vet tidy clean help

## run: run locally (debug logging) and open the web UI once it's ready
run:
	@echo "Starting ppsc (bound to $(ADDR)) — waiting for it to come up…"
	@echo "  local:   $(URL)"
	@if [ -n "$(LAN_IP)" ]; then echo "  network: http://$(LAN_IP):$(PORT)/  (share this with others on your LAN)"; fi
	@( for i in $$(seq 1 100); do \
		if curl -fs -o /dev/null "$(URL)" 2>/dev/null; then \
			echo "ppsc is up — opening $(URL)"; \
			$(OPEN) "$(URL)" >/dev/null 2>&1 || echo "(open your browser at $(URL))"; \
			exit 0; \
		fi; \
		sleep 0.2; \
	done; echo "(timed out waiting for $(URL); open it manually)" ) &
	@go run . -addr $(ADDR) -db $(DB) $(DEBUG_FLAG)

## build: compile the binary to ./$(APP)
build:
	go build -o $(APP) .

## test: run the test suite
test:
	go test ./...

## vet: run go vet
vet:
	go vet ./...

## tidy: tidy go.mod/go.sum
tidy:
	go mod tidy

## clean: remove the binary and local database files
clean:
	rm -f $(APP) $(DB) $(DB)-shm $(DB)-wal

## help: list available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'
