.DEFAULT_GOAL=help

# Required for globs to work correctly
SHELL:=/bin/bash

BUILD_TIME	= $(shell date +%FT%T%z)
BUILD_DIR	= $(CURDIR)/build
BINARIES	= chatops chatops-mcp
PKG_PREFIX	= github.com/hangxie/chatops
REL_TARGET	= \
				darwin-arm64 \
				linux-amd64 linux-arm linux-arm64
VERSION		= $(shell git describe --tags --always)

PAGES_DIR		= $(BUILD_DIR)/pages
COVERAGE_CSV	?= $(BUILD_DIR)/coverage.csv
COLLECT_ARGS	?=

# go option
CGO_ENABLED := 0
GO			?= go
PYTHON		?= python3
GOBIN		= $(shell $(GO) env GOPATH)/bin
GOFLAGS		:= -trimpath
GOSOURCES	:= $(shell find . -type f -name '*.go')
LDFLAGS		:= -w -s \
				-extldflags "-static" \
				-X $(PKG_PREFIX)/internal/version.version=$(VERSION) \
				-X $(PKG_PREFIX)/internal/version.build=$(BUILD_TIME) \
				-X $(PKG_PREFIX)/internal/version.source=Makefile

.EXPORT_ALL_VARIABLES:

.PHONY: all
all: deps tools format lint test build  ## Build all common targets

.PHONY: format
format: tools  ## Format source codes
	@echo "==> Formatting source codes"
	@$(GOBIN)/gofumpt -w -extra $(GOSOURCES)
	@$(GOBIN)/goimports -w -local $(PKG_PREFIX) $(GOSOURCES)

.PHONY: lint
lint: tools  ## Run static code analysis
	@echo "==> Running static code analysis"
	@$(GOBIN)/golangci-lint cache clean
	@$(GOBIN)/golangci-lint run ./... --timeout 5m --enable gocognit

.PHONY: deps
deps:  ## Install prerequisite for build
	@echo "==> Installing prerequisite for build"
	@$(GO) mod tidy

.PHONY: tools
tools:  ## Install build tools
	@echo "==> Installing build tools"
	@(cd /tmp; \
		$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest; \
		$(GO) install mvdan.cc/gofumpt@latest; \
		$(GO) install golang.org/x/tools/cmd/goimports@latest; \
	)

.PHONY: build
build: deps  ## Build all binaries for local os/arch into $(BUILD_DIR) in ./
	@echo "==> Building executable"
	@mkdir -p $(BUILD_DIR)
	@CGO_ENABLED=$(CGO_ENABLED) \
		$(GO) build $(GOFLAGS) \
			-ldflags '$(LDFLAGS)' \
			-o $(BUILD_DIR)/ ./cmd/...

.PHONY: clean
clean:  ## Clean up the build dirs
	@echo "==> Cleaning up build dirs"
	@rm -rf $(BUILD_DIR) vendor

.PHONY: docker-build
docker-build:  ## Build docker image for local test
	@echo "==> Building docker image"
	@mkdir -p $(BUILD_DIR)/release/
	@package/scripts/build-bin.sh
	@sleep 2 # to address podman volume issue on MacOS
	@docker build . -f package/container/Dockerfile -t chatops:local

.PHONY: test
test: deps  ## Run unit tests
	@echo "==> Running unit tests"
	@mkdir -p $(BUILD_DIR)/test
	@set -euo pipefail ; \
		cd $(BUILD_DIR)/test; \
		CGO_ENABLED=1 $(GO) test -parallel 4 -race -count 1 -trimpath -coverprofile=coverage.out $(CURDIR)/... ; \
		$(GO) tool cover -html=coverage.out -o coverage.html ; \
		$(GO) tool cover -func=coverage.out -o coverage.txt ; \
		cat coverage.txt

.PHONY: pages
pages: pages-coverage  ## Generate all GitHub Pages content to build/pages/

.PHONY: pages-coverage
pages-coverage: deps  ## Collect coverage and generate chart, report and badge (COLLECT_ARGS="--start 2026-01-01 --end 2026-06-01")
	@echo "==> Generating coverage history page"
	@mkdir -p $(PAGES_DIR)
	@$(PYTHON) scripts/coverage-history.py $(COLLECT_ARGS) $(PAGES_DIR)/coverage-history.html $(COVERAGE_CSV)
	@echo "==> Generating Go coverage report"
	@mkdir -p $(BUILD_DIR)/test
	@set -euo pipefail ; \
		CGO_ENABLED=1 $(GO) test -parallel 4 -count 1 -trimpath \
			-coverprofile=$(BUILD_DIR)/test/coverage.out ./... ; \
		$(GO) tool cover -html=$(BUILD_DIR)/test/coverage.out -o $(PAGES_DIR)/coverage.html

.PHONY: release-build
release-build: deps ## Build release binaries
	@echo "==> Building release binaries"
	@mkdir -p $(BUILD_DIR)/release/
	@package/scripts/build-bin.sh

	@echo "==> generate RPM and deb packages"
	@package/scripts/build-rpm.sh
	@package/scripts/build-deb.sh

	@echo "==> generate build meta data"
	@package/scripts/gen-meta.sh

	@echo "==> release info"
	@cat $(BUILD_DIR)/release/checksum-sha512.txt
	@echo
	@cat $(BUILD_DIR)/CHANGELOG

.PHONY: help
help:  ## Print list of Makefile targets
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  cut -d ":" -f1- | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'
