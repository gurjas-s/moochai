# Mooch.ai central build targets.
# start builds central, starts TimescaleDB, and then starts central with analytics.
# Set ADDR to change the listen address, for example: make start ADDR=127.0.0.1:8080
# Set FLAGS for more central flags, for example: make start FLAGS=-plain

BINARY := mooch-central
BINDIR := bin
DISTDIR := dist
ADDR ?= :8080
FLAGS ?=
# The analytics database. It listens only on loopback, so only central can connect. Change DB_PASSWORD on a shared machine.
DB_PASSWORD ?= mooch
DB_URL ?= postgres://postgres:$(DB_PASSWORD)@127.0.0.1:5432/postgres

.PHONY: help build start db-up db-down test-db fmt check clean

help: # Show the targets.
	@grep -E '^[a-z-]+:.*# .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*# "}; {printf "  %-12s %s\n", $$1, $$2}'

build: # Build central and the node binaries that /join serves.
	go build -o $(BINDIR)/$(BINARY) ./cmd/mooch-central
	./build-nodes.sh

start: db-up # Build central, and then start it on the Tailscale IPv4 and 127.0.0.1, with analytics on 127.0.0.1:3000.
	go build -o $(BINDIR)/$(BINARY) ./cmd/mooch-central
	MOOCH_DB_URL=$(DB_URL) ./$(BINDIR)/$(BINARY) -addr $(ADDR) -bin $(DISTDIR) $(FLAGS)

db-up: # Start TimescaleDB in Docker on 127.0.0.1:5432. The data stays in the mooch-db volume.
	@docker start mooch-db >/dev/null 2>&1 || docker run -d --name mooch-db -p 127.0.0.1:5432:5432 \
		-e POSTGRES_PASSWORD=$(DB_PASSWORD) -v mooch-db:/var/lib/postgresql/data timescale/timescaledb:latest-pg17
	@until docker exec mooch-db pg_isready -q -U postgres; do sleep 1; done

db-down: # Stop the database. The data stays in the mooch-db volume.
	docker stop mooch-db

test-db: db-up # Run the database tests against the local database.
	MOOCH_TEST_DB_URL=$(DB_URL) go test ./internal/stats -run DB -count=1 -v

fmt: # Format the code.
	gofmt -w .

check: # Run all pre-push checks: format, vet, and tests.
	@test -z "$$(gofmt -l .)" || (gofmt -l .; echo "Run make fmt."; exit 1)
	go vet ./...
	go test ./... -count=1

clean: # Remove the build output.
	rm -rf $(BINDIR) $(DISTDIR)
