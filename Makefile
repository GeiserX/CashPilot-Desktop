WAILS ?= $(shell go env GOPATH)/bin/wails

.PHONY: dev build test generate catalog-sync catalog-check

dev:
	$(WAILS) dev

build:
	$(WAILS) build

test:
	go test ./...

generate:
	$(WAILS) generate module

# services/ is a vendored copy of the CashPilot web catalog; catalog-overlay/ holds
# the Desktop-only deltas. Set SRC=../CashPilot to use a local checkout.
catalog-sync:
	go run ./cmd/catalogsync $(if $(SRC),-src $(SRC),)

catalog-check:
	go run ./cmd/catalogsync -check $(if $(SRC),-src $(SRC),)
