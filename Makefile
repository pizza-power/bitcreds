.PHONY: all build scan server clean test

all: build

build: scan server

scan:
	go build -o bin/bitcreds-scan ./cmd/bitcreds-scan

server:
	go build -o bin/bitcreds-server ./cmd/bitcreds-server

clean:
	rm -rf bin/

test:
	go test ./...

tidy:
	go mod tidy

.DEFAULT_GOAL := all
