LAST_COMMIT := $(shell git rev-parse --short HEAD)
LAST_COMMIT_DATE := $(shell git show -s --format=%ci ${LAST_COMMIT})
TAG := $(shell git describe --tags)
BUILDSTR := ${TAG} (${LAST_COMMIT} ${LAST_COMMIT_DATE})
STATIC := assets/index.html
BIN := gotp

.PHONY: build
build:
	go build -o ${BIN} -ldflags="-X 'main.buildString=${BUILDSTR}'" cmd/*.go
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
