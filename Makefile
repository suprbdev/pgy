APP := pgy
PKG := github.com/suprbdev/pgy
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILDTIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X $(PKG)/internal/cli.version=$(VERSION)
PREFIX ?= /usr/local

.PHONY: all help build test test-integration test-integration-up test-integration-down lint clean install release

all: build

help: ## Show this help
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-16s\033[0m %s\n", $$1, $$2}'

build: ## Build the binary into bin/
	GO111MODULE=on go build -ldflags "$(LDFLAGS)" -o bin/$(APP) ./cmd/pgy

test: ## Run tests
	go test ./...

lint: ## Run golangci-lint (falls back to go vet)
	golangci-lint run ./... || go vet ./...

test-integration-up:
	docker compose up -d --wait

test-integration-down:
	docker compose down -v

test-integration: test-integration-up
	PGY_TEST_DSN=postgres://pgy:pgy@localhost:5433/pgytest go test ./internal/integration/... -v -count=1
	$(MAKE) test-integration-down

clean:
	rm -rf bin
	rm -f ./.pgy.buffer.sql

install: build
	@dest="$(HOME)/go/bin"; \
	if [ ! -d "$$dest" ]; then \
	  dest="$(DESTDIR)$(PREFIX)/bin"; \
	fi; \
	printf "Installing to %s\n" "$$dest"; \
	install -d "$$dest"; \
	install -m 0755 bin/$(APP) "$$dest/$(APP)"

# Verifies, tags, and pushes; the Release workflow (release.yaml) then builds
# the binaries and publishes the GitHub release with generated notes.
release: ## Cut a new release (prompts for version, tags, pushes)
	@set -e; \
	git diff --quiet && git diff --cached --quiet || { echo "error: working tree dirty — commit or stash first"; exit 1; }; \
	current=$$(git describe --tags --abbrev=0 2>/dev/null || echo "(none)"); \
	echo "Current version: $$current"; \
	printf "New version (vX.Y.Z): "; read -r version; \
	echo "$$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$$' || { echo "error: invalid version '$$version' (expected vX.Y.Z)"; exit 1; }; \
	if git rev-parse -q --verify "refs/tags/$$version" >/dev/null; then echo "error: tag $$version already exists"; exit 1; fi; \
	$(MAKE) test lint; \
	git tag -a "$$version" -m "$$version"; \
	git push origin HEAD "$$version"; \
	url=$$(git remote get-url origin | sed -E 's#^git@github\.com:#https://github.com/#; s#\.git$$##'); \
	echo "Pushed $$version — the Release workflow is publishing it:"; \
	echo "  $$url/actions/workflows/release.yaml"


