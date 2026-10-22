VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint install

build:
	go build -ldflags "-s -w -X main.version=$(VERSION)" -o herd ./cmd/herd

test:
	go test -race ./...

lint:
	test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	go vet ./...

install:
	go install -ldflags "-s -w -X main.version=$(VERSION)" ./cmd/herd
