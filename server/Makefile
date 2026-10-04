# Mooch.ai central build targets.
# build makes central and the node binaries for /join.
# run builds, starts TimescaleDB, and then starts central with analytics.
# Set FLAGS for more central flags, for example: make run FLAGS=-details

BINARY := mooch-central
PKG := ./cmd/mooch-central
BINDIR := bin
DISTDIR := dist
ADDR ?= :8080
LOCAL_ADDR ?= 127.0.0.1:8080
FLAGS ?=
# The analytics database. It listens only on loopback, so only central can connect. Change DB_PASSWORD on a shared machine.
DB_PASSWORD ?= mooch
DB_URL ?= postgres://postgres:$(DB_PASSWORD)@127.0.0.1:5432/postgres

.PHONY: help build central nodes start run run-local run-plain db-up db-down test test-db vet fmt fmt-check check clean

help: # Show the targets.
	@grep -E '^[a-z-]+:.*# .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*# "}; {printf "  %-12s %s\n", $$1, $$2}'

build: central nodes # Build central and the node binaries.

central: # Build only the central binary.
	go build -o $(BINDIR)/$(BINARY) $(PKG)

nodes: # Build the node binaries that /join serves.
	./build-nodes.sh

start: db-up # Start the database and central on the Tailscale IPv4 with analytics on 127.0.0.1:3000. Do not build.
	MOOCH_DB_URL=$(DB_URL) ./$(BINDIR)/$(BINARY) -addr $(ADDR) -bin $(DISTDIR) $(FLAGS)

run: build start # Build all, and then start the database and central with analytics.

run-local: central # Start central on loopback for local tests, without the database. Other machines cannot join.
	./$(BINDIR)/$(BINARY) -addr $(LOCAL_ADDR) -bin $(DISTDIR) $(FLAGS)

run-plain: build # Start central with the event lines and without the dashboard or the database.
	./$(BINDIR)/$(BINARY) -addr $(ADDR) -bin $(DISTDIR) -plain $(FLAGS)

db-up: # Start TimescaleDB in Docker on 127.0.0.1:5432. The data stays in the mooch-db volume.
	@docker start mooch-db >/dev/null 2>&1 || docker run -d --name mooch-db -p 127.0.0.1:5432:5432 \
		-e POSTGRES_PASSWORD=$(DB_PASSWORD) -v mooch-db:/var/lib/postgresql/data timescale/timescaledb:latest-pg17
	@until docker exec mooch-db pg_isready -q -U postgres; do sleep 1; done

db-down: # Stop the database. The data stays in the mooch-db volume.
	docker stop mooch-db

test-db: db-up # Run the database tests against the local database.
	MOOCH_TEST_DB_URL=$(DB_URL) go test ./internal/stats -run DB -count=1 -v

test: # Run the test suite.
	go test ./... -count=1

vet: # Run the static checks.
	go vet ./...

fmt: # Format the code.
	gofmt -w .

fmt-check: # Fail when code needs formatting.
	@test -z "$$(gofmt -l .)" || (gofmt -l .; echo "Run make fmt."; exit 1)

check: fmt-check vet test # Run all pre-push checks.

clean: # Remove the build output.
	rm -rf $(BINDIR) $(DISTDIR)
