BINARY     := determined
BIN_DIR    := bin
DOCKERFILE := Dockerfile.build

# Semantic version stamped into the binary. Defaults to the seed in the VERSION
# file; CI overrides it (e.g. `make build VERSION=1.0.42`) so every published
# build carries a unique, monotonic semver.
VERSION ?= $(shell cat VERSION 2>/dev/null || echo dev)
TARGETOS ?=
TARGETARCH ?=
BUILD_ARGS := --build-arg VERSION=$(VERSION)
ifneq ($(TARGETOS),)
BUILD_ARGS += --build-arg TARGETOS=$(TARGETOS)
endif
ifneq ($(TARGETARCH),)
BUILD_ARGS += --build-arg TARGETARCH=$(TARGETARCH)
endif

.PHONY: build clean

# Build the project inside Docker using Dockerfile.build and extract the
# compiled binary to ./$(BIN_DIR)/$(BINARY).
build:
	docker build -f $(DOCKERFILE) --target bin \
		$(BUILD_ARGS) \
		--output type=local,dest=$(BIN_DIR) .

clean:
	rm -rf $(BIN_DIR)
