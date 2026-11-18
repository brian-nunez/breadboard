# Breadboard build. Requires Go; the integration tests also need Docker.
#
#   make build                  bin/bb
#   make check                  everything CI runs on every pull request
#   make test                   unit tests
#   make test-integration       Docker-backed tests (CI runs them on main)
#   make release VERSION=vX.Y.Z clean dist/, build every platform and write dist/SHA256SUMS
#
# Releases are made by GitHub Actions (.github/workflows/release.yml), not from a local machine.

BINARY  := bb
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# Release platforms, GOOS/GOARCH. Breadboard supports macOS and Linux hosts.
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: all build install check fmt fmt-check vet lint test test-integration test-scripts web-check tidy-check release site clean

all: check build

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./bb

install:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(shell go env GOPATH)/bin/$(BINARY) ./bb

check: fmt-check vet lint test test-scripts web-check tidy-check

fmt:
	golangci-lint fmt ./...
	golangci-lint run --fix ./...

fmt-check:
	golangci-lint fmt --diff ./...

vet:
	go vet ./...
	go vet -tags integration ./...

lint:
	golangci-lint run ./...

test:
	go test ./...

test-integration:
	go test -count=1 -tags integration -timeout 30m ./internal/core/ ./internal/mcp/ ./internal/runtime/

# Tests for the release scripts in scripts/.
test-scripts:
	bash scripts/next-version_test.sh

# Generated web files (templ output, compiled CSS) match their sources.
web-check:
	sh scripts/web.sh check

tidy-check:
	go mod tidy -diff

# Release files in dist/: one binary per platform, named bb-<os>-<arch>, plus SHA256SUMS.
release:
	rm -rf dist
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		out=dist/$(BINARY)-$$os-$$arch; echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o $$out ./bb || exit 1; \
	done
	cd dist && shasum -a 256 $(BINARY)-* > SHA256SUMS
	@cat dist/SHA256SUMS

# The docs, starters and landing page as a static site in dist/site.
site:
	rm -rf dist/site
	go run ./internal/web/site dist/site

clean:
	rm -rf bin dist
