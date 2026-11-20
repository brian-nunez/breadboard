# Breadboard build. Requires Go; the integration tests also need Docker.
#
#   make build                  bin/bb
#   make tools                  install pinned check tools into .tools/
#   make check                  everything CI runs on every pull request
#   make test                   unit, contract, invariant, and seed-fuzz tests (-race)
#   make test-integration       Docker-backed tests (CI runs them on main)
#   make test-fuzz              run active fuzzing smoke pass across all fuzz targets
#   make test-mutation          run mutation testing (gremlins) on core security/spec packages
#   make test-e2e               run Playwright smoke test against live `bb serve`
#   make guardrail-check        audit diff against deterministic guardrails
#   make release VERSION=vX.Y.Z clean dist/, build every platform and write dist/SHA256SUMS
#
# Releases are made by GitHub Actions (.github/workflows/release.yml), not from a local machine.

BINARY  := bb
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# Release platforms, GOOS/GOARCH. Breadboard supports macOS and Linux hosts.
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: all build install tools check fmt fmt-check vet lint vuln deadcode test test-integration test-fuzz test-mutation test-e2e test-scripts guardrail-check web-check tidy-check release site clean

all: check build

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./bb

install:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(shell go env GOPATH)/bin/$(BINARY) ./bb

tools:
	sh scripts/tools.sh install

check: fmt-check vet lint vuln deadcode test test-scripts web-check tidy-check

fmt:
	sh scripts/tools.sh golangci-lint fmt ./...
	sh scripts/tools.sh golangci-lint run --fix ./...

fmt-check:
	sh scripts/tools.sh golangci-lint fmt --diff ./...

vet:
	go vet ./...
	go vet -tags integration ./...

lint:
	sh scripts/tools.sh golangci-lint run ./...

vuln:
	sh scripts/tools.sh govulncheck ./...

deadcode:
	@out=$$(sh scripts/tools.sh deadcode -test ./...); \
	if [ -n "$$out" ]; then \
		echo "$$out"; \
		echo "deadcode: unreachable functions found" >&2; \
		exit 1; \
	fi

test:
	go test -race ./...

test-integration:
	go test -count=1 -tags integration -timeout 30m ./internal/core/ ./internal/mcp/ ./internal/runtime/

FUZZTIME ?= 3s
test-fuzz:
	go test -fuzz=FuzzParseAndValidate -fuzztime=$(FUZZTIME) ./internal/spec/
	go test -fuzz=FuzzHostCANameConstraints -fuzztime=$(FUZZTIME) ./internal/pki/
	go test -fuzz=FuzzLabCANameConstraints -fuzztime=$(FUZZTIME) ./internal/pki/
	go test -fuzz=FuzzTraefikMergeStatic -fuzztime=$(FUZZTIME) ./internal/catalog/
	go test -fuzz=FuzzSafeNextPath -fuzztime=$(FUZZTIME) ./internal/web/
	go test -fuzz=FuzzOwnHostAndOrigin -fuzztime=$(FUZZTIME) ./internal/web/

test-mutation:
	sh scripts/tools.sh gremlins unleash --timeout-coefficient 20 ./internal/pki
	sh scripts/tools.sh gremlins unleash --timeout-coefficient 20 ./internal/secrets
	sh scripts/tools.sh gremlins unleash --timeout-coefficient 20 ./internal/spec

test-e2e:
	bash scripts/e2e.sh

# Tests for the scripts in scripts/.
test-scripts:
	bash scripts/next-version_test.sh
	bash scripts/guardrail-check_test.sh

guardrail-check:
	bash scripts/guardrail-check.sh

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
