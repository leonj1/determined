BINARY     := determined
BIN_DIR    := bin
DOCKERFILE := Dockerfile.build

# Semantic version stamped into the binary. Defaults to the seed in the VERSION
# file; CI overrides it (e.g. `make build VERSION=1.0.42`) so every published
# build carries a unique, monotonic semver.
VERSION ?= $(shell cat VERSION 2>/dev/null || echo dev)
# Default to the host machine's OS/arch so `make build` produces a binary that
# runs on this machine; override for cross-compilation (e.g. TARGETARCH=arm64).
TARGETOS ?= $(shell uname -s | tr '[:upper:]' '[:lower:]')
TARGETARCH ?= $(shell uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
BUILD_ARGS := --build-arg VERSION=$(VERSION) \
	--build-arg TARGETOS=$(TARGETOS) \
	--build-arg TARGETARCH=$(TARGETARCH)

.PHONY: build clean test

# Build the project inside Docker using Dockerfile.build and extract the
# compiled binary to ./$(BIN_DIR)/$(BINARY).
build:
	docker build -f $(DOCKERFILE) --target bin \
		$(BUILD_ARGS) \
		--output type=local,dest=$(BIN_DIR) .

test:
	go test -cover ./...

clean:
	rm -rf $(BIN_DIR)
