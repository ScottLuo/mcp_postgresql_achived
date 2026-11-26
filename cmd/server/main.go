// Package main is the entrypoint for the PostgreSQL DBA MCP Gateway.
//
// It loads configuration from environment variables, initializes database
// connection pools, registers MCP tools, and starts the stdio or HTTP
// transport. See docs/design.md for the full architectural specification.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Context cancelled on SIGTERM/SIGINT or stdin EOF (handled by SDK).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	logger.Info("postgres-dba-mcp: starting", "version", "0.1.0")

	// Create the MCP server instance.
	srv := mcp.NewServer(
		&mcp.Implementation{
			Name:    "postgres-dba-mcp",
			Version: "0.1.0",
		},
		&mcp.ServerOptions{
			Instructions: "PostgreSQL DBA MCP Gateway - provides database administration capabilities to AI agents.",
			Logger:       logger,
		},
	)

	// Start the server on the stdio transport.
	// Blocks until ctx is cancelled (signal) or stdin is closed (EOF).
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		if ctx.Err() == nil { // not a graceful shutdown
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}

	logger.Info("postgres-dba-mcp: stopped")
}
