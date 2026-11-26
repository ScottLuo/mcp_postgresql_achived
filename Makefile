# PostgreSQL DBA MCP Gateway - Build & Deploy
#
# Note: pg_query_go requires CGO. The build target uses CGO_ENABLED=1
# for the local build; the Docker build uses a multi-stage approach with
# a CGO toolchain in the build stage.

BINARY     := postgres-dba-mcp
BUILD_DIR  := build
PKG        := $(BUILD_DIR)/$(BINARY).tar.gz
VERSION    ?= 0.1.0
LDFLAGS    := -X main.appVersion=$(VERSION)

.PHONY: all build test test-integration lint package docker clean

# Default target: build the linux/amd64 binary.
all: build

# build: cross-compile for linux/amd64.
# CGO_ENABLED=1 is required by pg_query_go (cgo-based SQL parser).
# Version is injected via ldflags; override with: make build VERSION=0.2.0
build:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/server/

# test: run unit tests.
test:
	go test -tags=unit -v ./... --timeout 20s

# test-integration: run integration tests (requires Docker/testcontainers).
test-integration:
	go test -tags=integration -v ./... --timeout 120s

# lint: run static analysis.
lint:
	golangci-lint run

# package: produce a tar.gz containing the compiled binary + tool.yaml.
# The tarball contains: postgres-dba-mcp (exec bit) and tool.yaml at top level.
package: build
	@chmod +x $(BUILD_DIR)/$(BINARY)
	@rm -rf /tmp/mcp-pkg-staging && mkdir -p /tmp/mcp-pkg-staging
	@cp $(BUILD_DIR)/$(BINARY) /tmp/mcp-pkg-staging/
	@cp tool.yaml /tmp/mcp-pkg-staging/
	@tar -czf $(PKG) -C /tmp/mcp-pkg-staging $(BINARY) tool.yaml
	@rm -rf /tmp/mcp-pkg-staging
	@echo "Packaged: $(PKG)"

# docker: build the Docker image.
docker:
	docker build -t $(BINARY) .

# clean: remove build artifacts.
clean:
	rm -rf $(BUILD_DIR)
