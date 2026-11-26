// Package main is the entrypoint for the PostgreSQL DBA MCP Gateway.
//
// Startup sequence (spec §5.3):
//  1. Load configuration from ENV (config.Load)
//  2. Initialize slog logger to stderr (stdout reserved for MCP protocol)
//  3. Build DatabaseManager (per-target pgxpool pools)
//  4. Run startup health-check / ping per configured policy
//  5. Load Policy Engine from configs/policy.yaml
//  6. Create ApprovalValidator
//  7. Create Audit Logger
//  8. Create Prometheus metrics
//  9. Build Tool Registry (with policy adapter + concurrency limiter)
// 10. Register all tool packages (discovery, monitoring, query, maintenance, configuration)
// 11. Create ResourceProvider
// 12. Build mcp.Server and start stdio transport
// 13. Graceful shutdown on SIGTERM/SIGINT/EOF: close pools, flush audit
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"postgres-dba-mcp/internal/audit"
	"postgres-dba-mcp/internal/config"
	"postgres-dba-mcp/internal/database"
	"postgres-dba-mcp/internal/mcp"
	"postgres-dba-mcp/internal/metrics"
	"postgres-dba-mcp/internal/security"
	"postgres-dba-mcp/internal/tools/configuration"
	"postgres-dba-mcp/internal/tools/discovery"
	"postgres-dba-mcp/internal/tools/maintenance"
	"postgres-dba-mcp/internal/tools/monitoring"
	"postgres-dba-mcp/internal/tools/query"
)

const appName = "postgres-dba-mcp"

// appVersion is the default version string; overridden at build time via
// -ldflags "-X main.appVersion=<version>".
var appVersion = "0.1.0"

func main() {
	if err := run(context.Background()); err != nil {
		// Fallback logger to stderr in case the structured logger was not yet set up.
		slog.Error(appName+": startup or runtime error", "error", err.Error())
		os.Exit(1)
	}
}

// run is the main orchestration function. It is extracted for testability:
// unit tests can call run() directly with a pre-set context to test the
// config→validation→error path without a real database.
func run(ctx context.Context) error {
	// 1. Load configuration from environment variables.
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("%s: config: %w", appName, err)
	}

	// 2. Initialize structured logger to stderr.
	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)
	logger.Info(appName+" starting", "version", appVersion, "targets", len(cfg.Targets))

	// 3. Build DatabaseManager (pgxpool pools per target).
	dbMgr, err := database.NewManager(ctx, cfg, cfg.Targets)
	if err != nil {
		return fmt.Errorf("%s: database init: %w", appName, err)
	}
	defer dbMgr.Close()
	logger.Info("database pools initialized", "targets", len(cfg.Targets))

	// 4. Run startup health-check.
	if err := dbMgr.HealthCheck(ctx); err != nil {
		return fmt.Errorf("%s: health check: %w", appName, err)
	}
	logger.Info("startup health check passed")

	// 5. Load Policy Engine.
	policyEngine, err := loadPolicyEngine(ctx, cfg)
	if err != nil {
		return fmt.Errorf("%s: policy: %w", appName, err)
	}

	// 6. Create ApprovalValidator.
	approvalStore := security.NewInMemoryApprovalStore()
	approver := security.NewApprovalValidator(approvalStore)

	// 7. Create Audit Logger.
	auditLogger := audit.New(cfg.AuditEnabled, logger.With("component", "audit"))

	// 8. Create Prometheus metrics.
	m := metrics.New()

	// 9. Build Tool Registry with policy adapter and concurrency limiter.
	policyAdapter := &policyAdapter{engine: policyEngine}
	registry := mcp.NewRegistry(logger, policyAdapter, nil)

	// 10. Register all tool packages.
	if err := registerAllTools(registry, cfg, dbMgr, auditLogger, m, approver, logger); err != nil {
		return fmt.Errorf("%s: tool registration: %w", appName, err)
	}
	logger.Info("tools registered", "count", len(registry.List()))

	// 11. Create ResourceProvider.
	rp := newDBResourceProvider(dbMgr, logger)

	// 12. Build MCP server.
	server, err := mcp.NewServer(registry, mcp.ServerConfig{
		Name:             appName,
		Version:          appVersion,
		Instructions:     "PostgreSQL DBA MCP Gateway - provides database administration capabilities to AI agents.",
		Logger:           logger,
		ResourceProvider: rp,
	})
	if err != nil {
		return fmt.Errorf("%s: mcp server: %w", appName, err)
	}

	// 13. Start stdio transport. Blocks until SIGTERM/SIGINT/EOF.
	logger.Info("starting stdio transport")
	runErr := server.Run(ctx)
	if runErr != nil {
		return fmt.Errorf("%s: server: %w", appName, runErr)
	}

	// 14. Graceful shutdown cleanup (deferred dbMgr.Close() already ran).
	logger.Info("postgres-dba-mcp: stopped")
	return nil
}

// registerAllTools wires and registers all five tool packages with the registry.
func registerAllTools(
	registry *mcp.Registry,
	cfg *config.Config,
	dbMgr database.DatabaseManager,
	auditLogger *audit.AuditLogger,
	m *metrics.Metrics,
	approver *security.ApprovalValidator,
	logger *slog.Logger,
) error {
	// Discovery tools (all READ_ONLY).
	discSvc := discovery.NewService(cfg, dbMgr, auditLogger, m)
	if err := discovery.Register(registry, discSvc); err != nil {
		return fmt.Errorf("discovery: %w", err)
	}

	// Monitoring tools (all READ_ONLY).
	monSvc := monitoring.NewService(cfg, dbMgr, auditLogger, m)
	if err := monitoring.Register(registry, monSvc); err != nil {
		return fmt.Errorf("monitoring: %w", err)
	}

	// Query analysis tools.
	qDeps := query.NewDeps(dbMgr, cfg, auditLogger, m, logger)
	if err := query.Register(registry, qDeps); err != nil {
		return fmt.Errorf("query: %w", err)
	}

	// Maintenance tools (CONTROLLED_MAINTENANCE + REQUIRES_APPROVAL).
	maintSvc := maintenance.NewService(cfg, dbMgr, auditLogger, m, approver, logger)
	if err := maintenance.Register(registry, maintSvc); err != nil {
		return fmt.Errorf("maintenance: %w", err)
	}

	// Configuration tools.
	cfgSvc := configuration.NewService(cfg, dbMgr, auditLogger, m)
	if err := configuration.Register(registry, cfgSvc); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}

	return nil
}

// loadPolicyEngine loads the policy engine from the configured path.
// If the POLICY_FILE environment variable is set, it overrides the default
// "configs/policy.yaml" path. If the file does not exist, a default-deny
// empty policy engine is used (all tools denied until policy is configured).
func loadPolicyEngine(_ context.Context, _ *config.Config) (*security.PolicyEngine, error) {
	path := "configs/policy.yaml"
	if override := os.Getenv("POLICY_FILE"); override != "" {
		path = override
	}

	engine, err := security.NewPolicyEngine(path)
	if err != nil {
		// If the policy file is missing, log a warning and use an empty engine
		// (default-deny). This allows the server to start for read-only use
		// without a policy file.
		slog.Warn("policy file not found, using default-deny policy", "path", path, "error", err.Error())
		return security.NewPolicyEngineFromConfig(security.PolicyConfig{Policies: map[string]security.TargetPolicy{}}), nil
	}
	return engine, nil
}

// newLogger creates a slog.Logger that writes to stderr at the given level.
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
