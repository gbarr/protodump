GO ?= go
GOLANGCI_LINT ?= golangci-lint
BIN := bin

.PHONY: all build test vet lint fmt tidy update-testdata install clean help

all: vet lint test build

## build: build proto-extract into bin/
build:
	$(GO) build -o $(BIN)/proto-extract ./cmd/proto-extract

## test: run all tests
test:
	$(GO) test ./...

## vet: run go vet
vet:
	$(GO) vet ./...

## lint: run golangci-lint
lint:
	$(GOLANGCI_LINT) run ./...

## fmt: format the code
fmt:
	$(GO) fmt ./...

## tidy: tidy go.mod and go.sum
tidy:
	$(GO) mod tidy

## update-testdata: regenerate testdata/descriptors.txtpb (requires protoc)
update-testdata:
	$(GO) test -run TestDumpTestdata -update .

## install: install proto-extract into GOBIN
install:
	$(GO) install ./cmd/proto-extract

## clean: remove build output
clean:
	rm -rf $(BIN)

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
