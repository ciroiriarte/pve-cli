# pve-cli Makefile. The binary is `pc`.
GO        ?= go
BINARY    := pc
PKG       := github.com/ciroiriarte/pve-cli
CMD       := ./cmd/pc
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE      ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS   := -s -w \
	-X $(PKG)/internal/version.Version=$(VERSION) \
	-X $(PKG)/internal/version.Commit=$(COMMIT) \
	-X $(PKG)/internal/version.Date=$(DATE)

.PHONY: all build test vet fmt tidy clean run docs

all: build

# Generate man pages, shell completions, and the markdown command reference
# into dist/ (consumed by packaging). Regenerate in CI to catch drift.
docs:
	$(GO) run ./cmd/gendocs dist

build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) $(CMD)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

tidy:
	$(GO) mod tidy

clean:
	rm -f $(BINARY)
	rm -rf dist

run: build
	./$(BINARY) $(ARGS)
