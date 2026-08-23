LAST_COMMIT := $(shell git rev-parse --short HEAD)
LAST_COMMIT_DATE := $(shell git show -s --format=%ci ${LAST_COMMIT})
TAG := $(shell git describe --tags 2>/dev/null || echo dev)
BUILDSTR := ${TAG} (${LAST_COMMIT} ${LAST_COMMIT_DATE})
BIN := gotp

.PHONY: build
build:
	go build -o ${BIN} -ldflags="-s -w -X 'main.buildString=${BUILDSTR}'" ./cmd

.PHONY: test
test:
	go test -race ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: check
check: vet test

.PHONY: clean
clean:
	go clean
	- rm -f ${BIN}

.PHONY: deps
deps:
	go mod download
