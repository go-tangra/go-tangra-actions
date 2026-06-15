GOFLAGS ?= -mod=mod
export GOFLAGS

.PHONY: all build cli test race cover vet fmt tidy clean

all: fmt vet test

build:
	go build ./...

# Build the CLI runner into ./bin.
cli:
	go build -o bin/tangra-actions ./cmd/tangra-actions

test:
	go test ./...

race:
	go test -race ./...

cover:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -1

cover-html: cover
	go tool cover -html=coverage.out -o coverage.html

vet:
	go vet ./...

fmt:
	gofmt -w -s .

tidy:
	go mod tidy

clean:
	rm -f coverage.out coverage.html
	go clean ./...
