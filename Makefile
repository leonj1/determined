BINARY     := determined
BIN_DIR    := bin
DOCKERFILE := Dockerfile.build

# Semantic version stamped into the binary. Defaults to the seed in the VERSION
# file; CI overrides it (e.g. `make build VERSION=1.0.42`) so every published
# build carries a unique, monotonic semver.
VERSION ?= $(shell cat VERSION 2>/dev/null || echo dev)

.PHONY: build clean

# Build the project inside Docker using Dockerfile.build and extract the
# compiled binary to ./$(BIN_DIR)/$(BINARY).
build:
	docker build -f $(DOCKERFILE) --target bin \
		--build-arg VERSION=$(VERSION) \
		--output type=local,dest=$(BIN_DIR) .

clean:
	rm -rf $(BIN_DIR)
