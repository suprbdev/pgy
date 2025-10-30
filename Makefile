APP := pgy
PKG := github.com/suprbdev/pgy
VERSION ?= 0.1.0
BUILDTIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X $(PKG)/internal/cli.version=$(VERSION)
PREFIX ?= /usr/local

.PHONY: all build test clean install

all: build

build:
	GO111MODULE=on go build -ldflags "$(LDFLAGS)" -o bin/$(APP) ./cmd/pgy

test:
	go test ./...

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


