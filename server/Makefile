# Mooch.ai central build targets.
# run builds central and the node binaries for /join, and then starts central.
# Set FLAGS for more central flags, for example: make run FLAGS=-details

BINARY := mooch-central
PKG := ./cmd/mooch-central
BINDIR := bin
DISTDIR := dist
ADDR ?= :8080
LOCAL_ADDR ?= 127.0.0.1:8080
FLAGS ?=

.PHONY: help build nodes all run run-local run-plain test vet fmt fmt-check check clean

help: # Show the targets.
	@grep -E '^[a-z-]+:.*# .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*# "}; {printf "  %-12s %s\n", $$1, $$2}'

build: # Build the central binary.
	go build -o $(BINDIR)/$(BINARY) $(PKG)

nodes: # Build the node binaries that /join serves.
	./build-nodes.sh

all: build nodes # Build central and the node binaries.

run: all # Start central on the Tailscale IPv4 with the dashboard.
	./$(BINDIR)/$(BINARY) -addr $(ADDR) -bin $(DISTDIR) $(FLAGS)

run-local: build # Start central on loopback for local tests. Other machines cannot join.
	./$(BINDIR)/$(BINARY) -addr $(LOCAL_ADDR) -bin $(DISTDIR) $(FLAGS)

run-plain: all # Start central with the event lines and without the dashboard.
	./$(BINDIR)/$(BINARY) -addr $(ADDR) -bin $(DISTDIR) -plain $(FLAGS)

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
