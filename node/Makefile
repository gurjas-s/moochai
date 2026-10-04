# Mooch.ai node build targets.
# Standard mode runs with request logs.
# Dev mode runs with debug logs.
# Both binaries use the same optimized build.

BINARY := mooch-node
DEV_BINARY := mooch-node-dev
PKG := ./cmd/mooch-node
BINDIR := bin
CONFIG ?= ./mooch-node.yaml
LOG_FORMAT ?= text

.PHONY: help build build-dev start start-dev test vet fmt fmt-check check clean

help: # Show the targets.
	@grep -E '^[a-z-]+:.*# .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*# "}; {printf "  %-12s %s\n", $$1, $$2}'

build: # Build the standard binary.
	go build -o $(BINDIR)/$(BINARY) $(PKG)

build-dev: # Build the dev binary.
	go build -o $(BINDIR)/$(DEV_BINARY) $(PKG)

start: build # Start the standard binary with request logs.
	./$(BINDIR)/$(BINARY) --config $(CONFIG) --log-mode requests --log-format $(LOG_FORMAT)

start-dev: build-dev # Start the dev binary with dev logs.
	./$(BINDIR)/$(DEV_BINARY) --config $(CONFIG) --log-mode dev --log-format $(LOG_FORMAT)

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
	rm -rf $(BINDIR)
