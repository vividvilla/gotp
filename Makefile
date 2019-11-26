LAST_COMMIT := $(shell git rev-parse --short HEAD)
LAST_COMMIT_DATE := $(shell git show -s --format=%ci ${LAST_COMMIT})
VERSION := $(shell git describe)
BUILDSTR := ${VERSION} (${LAST_COMMIT} $(shell date -u +"%Y-%m-%dT%H:%M:%S%z"))
STATIC := assets/index.html
BIN := gotp

.PHONY: build
build:
	go build -o ${BIN} -ldflags="-X 'main.buildVersion=${VERSION}' -X 'main.buildDate=${BUILD_DATE}'" cmd/*.go
	- stuffbin -a stuff -in ${BIN} -out ${BIN} ./assets/index.html

.PHONY: test
test:
	go test ./...

clean:
	go clean
	- rm -f ${BIN}

.PHONY: deps
deps:
	go get -u github.com/knadh/stuffbin/...

# pack-releases runns stuffbin packing on a given list of
# binaries. This is used with goreleaser for packing
# release builds for cross-build targets.
.PHONY: pack-releases
pack-releases:
	$(foreach var,$(RELEASE_BUILDS),stuffbin -a stuff -in ${var} -out ${var} ${STATIC};)
