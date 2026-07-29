.PHONY: fmt vet test test-race build check

VERSION ?= 0.3.0-dev
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unknown)
LDFLAGS = -X lore/internal/version.Version=$(VERSION) -X lore/internal/version.Commit=$(COMMIT) -X lore/internal/version.BuildDate=$(BUILD_DATE)

fmt:
	gofmt -w $$(find . -name '*.go' -type f)

vet:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o lore ./cmd/lore

check:
	@test -z "$$(gofmt -l .)" || { echo "Go files need formatting"; gofmt -l .; exit 1; }
	go vet ./...
	go test ./...
	go build ./cmd/lore
