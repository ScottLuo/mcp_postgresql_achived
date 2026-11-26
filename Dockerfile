# PostgreSQL DBA MCP Gateway - Dockerfile
#
# Multi-stage build.
# Stage 1: Build the Go binary with CGO enabled (required by pg_query_go).
# Stage 2: Slim runtime image with non-root user.

# ─── Stage 1: Build ───────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

# Install CGO build dependencies (gcc + musl headers for static linking).
RUN apk add --no-cache gcc musl-dev

WORKDIR /app

# Copy go.mod/go.sum first for layer caching.
COPY go.mod go.sum ./
RUN go mod download

# Copy source.
COPY . .

# Version injection via ldflags (set by CI/CD at release time).
ARG VERSION=dev

# Build static linux/amd64 binary.
# CGO_ENABLED=1 is required by pg_query_go (cgo-based PostgreSQL SQL parser).
# -ldflags "-linkmode external -extldflags '-static'" produces a static binary
# that works on both glibc and musl (Alpine) systems.
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
    go build -ldflags "-linkmode external -extldflags '-static' -X main.appVersion=${VERSION}" \
    -o /app/postgres-dba-mcp ./cmd/server/

# ─── Stage 2: Runtime ─────────────────────────────────────────────────────────
FROM alpine:3.19

# Create non-root user (UID 65532 = standard "nobody" UID for containers).
RUN adduser -D -u 65532 -h /app mcp

WORKDIR /app

# Copy the compiled binary and configuration files.
COPY --from=builder /app/postgres-dba-mcp /app/postgres-dba-mcp
COPY --from=builder /app/tool.yaml /app/tool.yaml
COPY --from=builder /app/configs/ /app/configs/

# Metrics/health port for HTTP mode (exposed but not required for stdio mode).
EXPOSE 9090

USER mcp

ENTRYPOINT ["/app/postgres-dba-mcp"]
CMD []
