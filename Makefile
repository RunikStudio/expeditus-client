.PHONY: all build build-linux build-windows clean help

BINARY_NAME=expeditus-web
DIST_DIR=dist

all: build

build:
	@mkdir -p $(DIST_DIR)
	@echo "Building $(BINARY_NAME)..."
	go build -o $(DIST_DIR)/$(BINARY_NAME) ./cmd/expeditus-web/
	@echo "Build complete: $(DIST_DIR)/$(BINARY_NAME)"

build-linux:
	@mkdir -p $(DIST_DIR)
	@echo "Building $(BINARY_NAME) for Linux..."
	GOOS=linux GOARCH=amd64 go build -o $(DIST_DIR)/$(BINARY_NAME)-linux-amd64 ./cmd/expeditus-web/
	@echo "Build complete: $(DIST_DIR)/$(BINARY_NAME)-linux-amd64"

build-windows:
	@mkdir -p $(DIST_DIR)
	@echo "Building $(BINARY_NAME) for Windows..."
	GOOS=windows GOARCH=amd64 go build -o $(DIST_DIR)/$(BINARY_NAME)-windows-amd64.exe ./cmd/expeditus-web/
	@echo "Build complete: $(DIST_DIR)/$(BINARY_NAME)-windows-amd64.exe"

build-all: build-linux build-windows
	@echo "All builds complete"

clean:
	rm -rf $(DIST_DIR)

help:
	@echo "Available targets:"
	@echo "  build         - Build for current platform"
	@echo "  build-linux   - Build for Linux AMD64"
	@echo "  build-windows - Build for Windows AMD64"
	@echo "  build-all     - Build for all platforms"
	@echo "  clean         - Remove build artifacts"
	@echo "  help          - Show this help message"
