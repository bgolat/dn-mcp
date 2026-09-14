VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.DEFAULT_GOAL := build

.PHONY: build
build:
	go build -ldflags "-X main.version=$(VERSION)" -o dn-mcp .

.PHONY: install
install:
	go install -ldflags "-X main.version=$(VERSION)" .

.PHONY: test
test:
	go test ./...

.PHONY: fmt
fmt:
	gofmt -w .

.PHONY: vet
vet:
	go vet ./...

.PHONY: ready
ready: fmt vet test build
