BINARY     := determined
BIN_DIR    := bin
DOCKERFILE := Dockerfile.build

.PHONY: build clean

# Build the project inside Docker using Dockerfile.build and extract the
# compiled binary to ./$(BIN_DIR)/$(BINARY).
build:
	docker build -f $(DOCKERFILE) --target bin --output type=local,dest=$(BIN_DIR) .

clean:
	rm -rf $(BIN_DIR)
