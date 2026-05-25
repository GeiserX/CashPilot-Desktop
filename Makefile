WAILS ?= $(shell go env GOPATH)/bin/wails

.PHONY: dev build test generate

dev:
	$(WAILS) dev

build:
	$(WAILS) build

test:
	go test ./...

generate:
	$(WAILS) generate module
