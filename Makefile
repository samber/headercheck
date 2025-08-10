# Project Makefile for github.com/samber/headercheck

SHELL := /bin/sh
.DEFAULT_GOAL := help

# --- Configuration -----------------------------------------------------------
GO           ?= go
PKG          := github.com/samber/headercheck
CMD_PKG      := $(PKG)/cmd/headercheck
PLUGIN_PKG   := $(PKG)/plugin/headercheck
BIN_DIR      := bin
BIN_NAME     := headercheck
PLUGIN_NAME  := headercheck.so

# Version metadata (not embedded by default; kept for future use)
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT       := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
DATE         := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Build flags
GOFLAGS     ?=
GCFLAGS     ?=
ASMFLAGS    ?=
LDFLAGS     ?= -s -w

# Toggle plugin build in CI where plugin may not be supported
BUILD_PLUGIN ?= 1

.PHONY: help all build plugin install fmt vet lint lint-custom test cover tidy modverify clean ci tools

## Show this help
help:
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage: make <target>\n\nTargets:\n"} /^[a-zA-Z0-9_.-]+:.*##/ {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

## Build CLI and (optionally) plugin
all: build $(if $(filter 1,$(BUILD_PLUGIN)),plugin,)

## Build CLI binary at bin/headercheck
build: $(BIN_DIR)/$(BIN_NAME)

$(BIN_DIR)/$(BIN_NAME):
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -trimpath -gcflags='$(GCFLAGS)' -asmflags='$(ASMFLAGS)' -ldflags='$(LDFLAGS)' -o $@ ./cmd/headercheck

## Build Go plugin at bin/headercheck.so (requires CGO and platform plugin support)
plugin: $(BIN_DIR)/$(PLUGIN_NAME)

$(BIN_DIR)/$(PLUGIN_NAME):
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 $(GO) build $(GOFLAGS) -trimpath -buildmode=plugin -gcflags='$(GCFLAGS)' -asmflags='$(ASMFLAGS)' -ldflags='$(LDFLAGS)' -o $@ $(PLUGIN_PKG)

## Install CLI to GOPATH/bin or GOBIN
install:
	CGO_ENABLED=0 $(GO) install $(GOFLAGS) -trimpath -ldflags='$(LDFLAGS)' $(CMD_PKG)

## Format sources
fmt:
	$(GO) fmt ./...

## Vet static checks
vet:
	$(GO) vet ./...

## Lint with golangci-lint if available
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not found. Install via: brew install golangci-lint || go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; exit 1; }
	golangci-lint run -v

## Lint-fix with golangci-lint if available
lint-fix:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not found. Install via: brew install golangci-lint || go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; exit 1; }
	golangci-lint run -v --fix

## Build custom golangci binary from .custom-gcl.yml and run it
lint-custom: tools
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not found"; exit 1; }
	golangci-lint custom -c .custom-gcl.yml
	./custom-gcl run -v

## Run tests with race detector and coverage
test:
	$(GO) test -race -covermode=atomic -coverprofile=coverage.out ./...

## Open coverage report (requires go tool cover)
cover: test
	@$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## Ensure go.mod/go.sum are tidy
tidy:
	$(GO) mod tidy

## Verify modules
modverify:
	$(GO) mod verify

## Install local dev tools (best-effort)
tools:
	@command -v golangci-lint >/dev/null 2>&1 || GO111MODULE=on $(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

## Remove build artifacts
clean:
	rm -rf $(BIN_DIR) coverage.out coverage.html custom-gcl

## Run typical CI pipeline: tidy, fmt, vet, lint, test, build
ci: tidy fmt vet lint test build


