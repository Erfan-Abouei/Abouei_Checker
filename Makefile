SHELL := /bin/sh
.DEFAULT_GOAL := help

BINARY := abouei-checker
MODULE := $(shell go list -m)
GO ?= go
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
COVERAGE_MIN ?= 75

LDFLAGS := -s -w \
	-X $(MODULE)/internal/buildinfo.Version=$(VERSION) \
	-X $(MODULE)/internal/buildinfo.Commit=$(COMMIT) \
	-X $(MODULE)/internal/buildinfo.BuildDate=$(BUILD_DATE)

.PHONY: help build fmt fmt-check vet lint test test-race cover cover-check vuln cross-build docker-build check verify clean

help: ## Show available commands
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the local binary with version metadata
	@mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/$(BINARY)

fmt: ## Format Go source files
	$(GO) fmt ./...
	gofmt -w cmd internal

fmt-check: ## Fail if Go source files are not formatted
	@files="$$(gofmt -l cmd internal)"; \
	if [ -n "$$files" ]; then \
		echo "The following files need gofmt:"; \
		echo "$$files"; \
		exit 1; \
	fi

vet: ## Run go vet
	$(GO) vet ./...

lint: ## Run golangci-lint (requires golangci-lint v2.15.0)
	golangci-lint run ./...

test: ## Run the unit test suite
	$(GO) test ./...

test-race: ## Run the test suite with the race detector
	$(GO) test -race ./...

cover: ## Generate a coverage profile
	$(GO) test -race -covermode=atomic -coverprofile=coverage.out ./...

cover-check: cover ## Enforce the coverage threshold
	COVERAGE_MIN=$(COVERAGE_MIN) ./scripts/check-coverage.sh coverage.out

vuln: ## Scan dependencies and the standard library for known vulnerabilities
	govulncheck ./...

cross-build: ## Compile release targets into dist/
	@VERSION="$(VERSION)" COMMIT="$(COMMIT)" BUILD_DATE="$(BUILD_DATE)" ./scripts/cross-build.sh

docker-build: ## Build the container image locally
	docker build \
		--build-arg VERSION="$(VERSION)" \
		--build-arg COMMIT="$(COMMIT)" \
		--build-arg BUILD_DATE="$(BUILD_DATE)" \
		-t $(BINARY):dev .

check: fmt-check vet test-race cover-check ## Run local checks that do not need extra tools

verify: check lint vuln cross-build ## Run the full local verification suite

clean: ## Remove local build and coverage artifacts
	rm -rf bin dist coverage.out
