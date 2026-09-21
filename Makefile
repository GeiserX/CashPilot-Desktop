WAILS ?= $(shell go env GOPATH)/bin/wails

.PHONY: dev build test test-race e2e check generate catalog-sync catalog-check

dev:
	$(WAILS) dev

build:
	$(WAILS) build

test:
	go test ./...

test-race:
	go test -race ./...

# The end-to-end suite: the real app engine against a fake Docker daemon and fake
# earning platforms, over loopback TCP. No container runtime, no accounts, no
# network, and the same run on linux, macOS and windows. The root package is
# included because the App-level tests live there -- package main cannot be
# imported, so they cannot move under e2e/.
e2e:
	go test -count=1 ./e2e/... .

# Everything CI checks on a platform, in one command.
check:
	go build ./...
	go vet ./...
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then echo "not gofmt'd:"; echo "$$unformatted"; exit 1; fi
	go test -race ./...

generate:
	$(WAILS) generate module

# services/ is a vendored copy of the CashPilot web catalog; catalog-overlay/ holds
# the Desktop-only deltas. Set SRC=../CashPilot to use a local checkout.
catalog-sync:
	go run ./cmd/catalogsync $(if $(SRC),-src $(SRC),)

catalog-check:
	go run ./cmd/catalogsync -check $(if $(SRC),-src $(SRC),)
