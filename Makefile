.PHONY: fmt vet test test-race build check

fmt:
	gofmt -w $$(find . -name '*.go' -type f)

vet:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

build:
	go build -o lore ./cmd/lore

check:
	@test -z "$$(gofmt -l .)" || { echo "Go files need formatting"; gofmt -l .; exit 1; }
	go vet ./...
	go test ./...
	go build ./cmd/lore
