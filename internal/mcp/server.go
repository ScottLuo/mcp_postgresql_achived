package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerConfig configures the MCP server.
type ServerConfig struct {
	// Name is the MCP server implementation name.
	Name string
	// Version is the MCP server implementation version.
	Version string
	// Instructions is an optional human-readable instruction string shown to
	// connected clients.
	Instructions string
	// Logger receives all server log output. It must write to stderr because
	// stdout is reserved for the MCP protocol.
	Logger *slog.Logger
	// ResourceProvider backs the MCP resources. If nil, resources are not
	// registered.
	ResourceProvider ResourceProvider
}

// Server wraps the MCP SDK server together with the tool Registry. It
// registers all tools, resources, and prompts, and manages the stdio
// transport lifecycle.
type Server struct {
	server *mcpsdk.Server
	reg    *Registry
	cfg    ServerConfig
}

// NewServer builds an MCP SDK server from the registry and configuration.
//
// It registers every tool in reg (via Registry.BuildToolHandler), all
// resources (when cfg.ResourceProvider is non-nil), and all DBA workflow
// prompts.
//
// logger must be non-nil.
func NewServer(reg *Registry, cfg ServerConfig) (*Server, error) {
	if reg == nil {
		return nil, errors.New("mcp: registry must be non-nil")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if cfg.Name == "" {
		cfg.Name = "postgres-dba-mcp"
	}
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}

	srv := mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: cfg.Name, Version: cfg.Version},
		&mcpsdk.ServerOptions{
			Instructions: cfg.Instructions,
			Logger:       cfg.Logger,
		},
	)

	// Register all tools.
	for _, def := range reg.List() {
		srv.AddTool(
			&mcpsdk.Tool{
				Name:        def.Name,
				Description: def.Description,
				InputSchema: inputSchemaFor(def),
			},
			reg.BuildToolHandler(def),
		)
	}

	// Register resources.
	if cfg.ResourceProvider != nil {
		registerResources(srv, cfg.ResourceProvider, cfg.Logger)
	}

	// Register DBA workflow prompts.
	registerPrompts(srv)

	return &Server{server: srv, reg: reg, cfg: cfg}, nil
}

// inputSchemaFor returns the InputSchema JSON for a tool definition, falling
// back to a valid empty-object schema.
func inputSchemaFor(def *ToolDefinition) interface{} {
	if len(def.InputSchema) > 0 {
		return def.InputSchema
	}
	return map[string]interface{}{"type": "object"}
}

// Run starts the stdio transport and blocks until the context is cancelled
// (via SIGTERM/SIGINT or caller cancellation) or stdin is closed (EOF).
//
// It installs SIGTERM/SIGINT handlers that cancel the internal context so
// the server shuts down gracefully. All log output goes to stderr.
func (s *Server) Run(ctx context.Context) error {
	// Derive a context that is also cancelled on SIGTERM/SIGINT.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	s.cfg.Logger.Info("mcp server starting on stdio",
		"name", s.cfg.Name, "version", s.cfg.Version,
		"tools", len(s.reg.List()))

	err := s.server.Run(ctx, &mcpsdk.StdioTransport{})

	// EOF on stdin is a normal shutdown for stdio; report it as nil so the
	// entrypoint can exit with code 0.
	if errors.Is(err, io.EOF) {
		s.cfg.Logger.Info("mcp server shutting down (stdin closed)")
		return nil
	}
	if err != nil {
		s.cfg.Logger.Error("mcp server stopped", "error", err.Error())
		return fmt.Errorf("mcp: server run: %w", err)
	}
	s.cfg.Logger.Info("mcp server stopped")
	return nil
}
