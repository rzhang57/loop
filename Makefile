.PHONY: build test install

build:
	go build ./...

test:
	go vet ./...
	go test ./...

install:
	GOBIN=$(HOME)/.local/bin go install ./cmd/loop
