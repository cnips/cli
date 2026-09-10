.PHONY: test build install

test:
	go test ./...

build:
	go build -o bin/cnips ./cmd/cnips

install:
	go install ./cmd/cnips
