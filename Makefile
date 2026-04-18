# port-manager Makefile.
# Targets mirror the harness quality-contract invocations in
# .tenet/harness/current.md.

BINARY          := port-manager
CMD             := ./cmd/port-manager
GOLANGCI_VERSION := v1.62.2
GOLANGCI        := $(shell go env GOPATH)/bin/golangci-lint

# Version string baked into the binary via -ldflags. Override with
# `make release VERSION=v1.2.3`. The default reads `git describe`; if
# that fails (clean checkout without git, CI shallow clone), fall back
# to a date-stamped dev tag so the binary still self-identifies.
VERSION         ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev-$$(date +%Y%m%d)")
LDFLAGS_VERSION := -X main.version=$(VERSION)

GO_BUILD_FLAGS  := -ldflags "$(LDFLAGS_VERSION)"
GO_RELEASE_FLAGS := -trimpath -ldflags "-s -w $(LDFLAGS_VERSION)"
RELEASE_GOOS    ?= darwin
RELEASE_GOARCH  ?= arm64
RELEASE_BINARY  := dist/$(BINARY)-$(RELEASE_GOOS)-$(RELEASE_GOARCH)

export CGO_ENABLED ?= 1

.PHONY: build test lint smoke e2e release clean tools version

build:
	go build $(GO_BUILD_FLAGS) -o $(BINARY) $(CMD)

test:
	go test -race -shuffle=on ./...

tools:
	@command -v $(GOLANGCI) >/dev/null 2>&1 || \
		go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_VERSION)

lint: tools
	gofmt -s -l . | (! grep .)
	go vet ./...
	$(GOLANGCI) run --timeout=60s

smoke:
	./scripts/smoke.sh

e2e: build
	cd tests/acceptance/e2e && npm ci && npx playwright install --with-deps chromium && npx playwright test

release:
	mkdir -p dist
	GOOS=$(RELEASE_GOOS) GOARCH=$(RELEASE_GOARCH) \
		go build $(GO_RELEASE_FLAGS) -o $(RELEASE_BINARY) $(CMD)
	@echo "release: built $(RELEASE_BINARY) (version=$(VERSION))"

version:
	@echo "$(VERSION)"

clean:
	rm -f $(BINARY) .smoke.pid .smoke.log
	rm -rf dist
