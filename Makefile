.PHONY: build test

build:
	GOMAXPROCS=2 GOFLAGS=-p=1 CGO_ENABLED=0 go build -o bin/manager ./cmd/manager

test:
	GOMAXPROCS=2 GOFLAGS=-p=1 go test ./...
