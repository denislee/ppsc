# ppsc — common developer tasks.
# Override the host/port or db path: `make run ADDR=127.0.0.1:9000 DB=/tmp/ppsc.db`

APP   := ppsc
ADDR  ?= 127.0.0.1:8080
DB    ?= ppsc.db
URL   := http://$(ADDR)/

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
	@echo "Starting ppsc on $(URL) — waiting for it to come up…"
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
