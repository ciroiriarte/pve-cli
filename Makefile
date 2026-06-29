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

.PHONY: all build test vet fmt fmtcheck check tidy clean run docs coverage changelog

all: build

# Run the same gates as CI in one shot — use before committing/pushing.
check: fmtcheck vet test build

# Generate man pages, shell completions, and the markdown command reference
# into dist/ (consumed by packaging). Regenerate in CI to catch drift.
docs:
	$(GO) run ./cmd/gendocs dist

# Regenerate the API coverage matrix (docs/coverage.md) from the schemas.
coverage:
	$(GO) run ./cmd/coverage docs/coverage.md

# Regenerate the OBS packaging changelogs from CHANGELOG.md + git tags. CI
# fails on drift, so a release that bumps CHANGELOG.md must regenerate these.
changelog:
	$(GO) run ./cmd/genchangelog

build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) $(CMD)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

# Fail if any non-vendored file isn't gofmt-clean (mirrors the CI gofmt gate).
fmtcheck:
	@unformatted=$$(gofmt -l . | grep -v '^vendor/' || true); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed (run 'make fmt'):"; echo "$$unformatted"; exit 1; \
	fi

tidy:
	$(GO) mod tidy

clean:
	rm -f $(BINARY)
	rm -rf dist

run: build
	./$(BINARY) $(ARGS)
