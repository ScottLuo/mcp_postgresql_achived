//go:build integration || security

package pgtest

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

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

// PG holds the running PostgreSQL container and its resolved DSN.
type PG struct {
	Container testcontainers.Container
	DSN       string
	t         *testing.T
}

// NewPostgres starts a PostgreSQL 16 container and returns a PG handle.
func NewPostgres(t *testing.T) *PG {
	t.Helper()

	ctx := context.Background()

	container, err := postgres.RunContainer(ctx,
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		postgres.WithDatabase("testdb"),
	)
	if err != nil {
		t.Fatalf("failed to start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(ctx); err != nil {
			t.Logf("warning: failed to terminate container: %v", err)
		}
	})

	connStr, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("failed to get connection string: %v", err)
	}

	// Wait for readiness: retry ping.
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("failed to create probe pool: %v", err)
	}
	var lastErr error
	for i := 0; i < 30; i++ {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		lastErr = pool.Ping(pingCtx)
		cancel()
		if lastErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	pool.Close()
	if lastErr != nil {
		t.Fatalf("pool ping failed after retries: %v", lastErr)
	}

	return &PG{Container: container, DSN: connStr, t: t}
}

// EnableStatStatements installs the pg_stat_statements extension.
func (pg *PG) EnableStatStatements() {
	pg.t.Helper()
	pool := pg.Pool(pg.t, pg.DSN)
	defer pool.Close()
	_, err := pool.Exec(context.Background(), "CREATE EXTENSION IF NOT EXISTS pg_stat_statements")
	if err != nil {
		pg.t.Logf("warning: pg_stat_statements not available: %v", err)
	}
}

// Pool creates a pgxpool for the given DSN.
func (pg *PG) Pool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("failed to parse DSN: %v", err)
	}
	poolCfg.MaxConns = 10
	poolCfg.MinConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	return pool
}

// Setup creates test tables and data in the database.
func (pg *PG) Setup(t *testing.T) {
	t.Helper()
	pool := pg.Pool(t, pg.DSN)
	defer pool.Close()
	ctx := context.Background()

	statements := []string{
		`DROP TABLE IF EXISTS orders CASCADE`,
		`DROP TABLE IF EXISTS customers CASCADE`,
		`CREATE TABLE customers (
			id BIGINT PRIMARY KEY,
			name VARCHAR(100) NOT NULL,
			email VARCHAR(200) UNIQUE NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE orders (
			id BIGINT PRIMARY KEY,
			customer_id BIGINT NOT NULL REFERENCES customers(id),
			status VARCHAR(20) NOT NULL DEFAULT 'pending',
			amount NUMERIC(10,2) NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`INSERT INTO customers (id, name, email) VALUES
			(1, 'Alice', 'alice@example.com'),
			(2, 'Bob', 'bob@example.com'),
			(3, 'Charlie', 'charlie@example.com')`,
		`INSERT INTO orders (id, customer_id, status, amount)
			SELECT i, (i % 3) + 1, CASE WHEN i % 5 = 0 THEN 'shipped' ELSE 'pending' END, i * 10.5
			FROM generate_series(1, 100) AS i`,
		`CREATE INDEX idx_orders_customer ON orders (customer_id)`,
		`CREATE INDEX idx_orders_status ON orders (status)`,
		`ANALYZE orders`,
		`ANALYZE customers`,
	}
	for _, stmt := range statements {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("setup failed: %v\nstmt: %s", err, stmt)
		}
	}
}

// Registry wires all tool services into an MCP registry with the given target
// name and DSN. The database manager and all pools are closed on test cleanup.
func Registry(t *testing.T, targetName, dsn string) *mcp.Registry {
	t.Helper()
	cfg, dbm := buildManager(t, targetName, dsn)
	reg := wireTools(t, cfg, dbm, nil)
	return reg
}

// RegistryWithApproval creates a registry with an in-memory approval store
// preloaded with the provided approval records.
func RegistryWithApproval(t *testing.T, targetName, dsn string, approvals []*security.ApprovalRecord) *mcp.Registry {
	t.Helper()
	cfg, dbm := buildManager(t, targetName, dsn)
	store := security.NewInMemoryApprovalStore()
	ctx := context.Background()
	for _, rec := range approvals {
		if err := store.Create(rec); err != nil {
			t.Fatalf("failed to seed approval %q: %v", rec.ID, err)
		}
	}
	_ = ctx
	validator := security.NewApprovalValidator(store)
	reg := wireTools(t, cfg, dbm, validator)
	return reg
}

func buildManager(t *testing.T, targetName, dsn string) (*config.Config, database.DatabaseManager) {
	t.Helper()
	cfg := &config.Config{
		Transport:         "stdio",
		LogLevel:          "debug",
		AuditEnabled:      false,
		DefaultTarget:     targetName,
		DefaultAccessMode: config.AccessReadWrite,
		Pool: config.PoolConfig{
			MinConns:                1,
			MaxConns:                10,
			MaxConnectionLifetime:   time.Hour,
			MaxIdleTime:             15 * time.Minute,
		},
		QueryLimits: config.QueryLimits{
			Timeout:       10 * time.Second,
			MaxRows:       1000,
			MaxResultSize: 1048576,
			MaxSQLLength:  65536,
		},
		Targets: []config.Target{
			{Name: targetName, Enabled: true, AccessMode: config.AccessReadWrite, DSN: dsn},
		},
	}
	dbm, err := database.NewManager(context.Background(), cfg, cfg.Targets)
	if err != nil {
		t.Fatalf("failed to create database manager: %v", err)
	}
	t.Cleanup(func() { dbm.Close() })
	return cfg, dbm
}

func wireTools(t *testing.T, cfg *config.Config, dbm database.DatabaseManager, approver *security.ApprovalValidator) *mcp.Registry {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(&discardWriter{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	auditLogger := audit.New(false, logger)
	met := metrics.New()

	reg := mcp.NewRegistry(logger, nil, nil)

	disc := discovery.NewService(cfg, dbm, auditLogger, met)
	if err := discovery.Register(reg, disc); err != nil {
		t.Fatalf("failed to register discovery: %v", err)
	}

	mon := monitoring.NewService(cfg, dbm, auditLogger, met)
	if err := monitoring.Register(reg, mon); err != nil {
		t.Fatalf("failed to register monitoring: %v", err)
	}

	qdeps := query.NewDeps(dbm, cfg, auditLogger, met, logger)
	if err := query.Register(reg, qdeps); err != nil {
		t.Fatalf("failed to register query: %v", err)
	}

	maint := maintenance.NewService(cfg, dbm, auditLogger, met, approver, logger)
	if err := maintenance.Register(reg, maint); err != nil {
		t.Fatalf("failed to register maintenance: %v", err)
	}

	cfgsvc := configuration.NewService(cfg, dbm, auditLogger, met)
	if err := configuration.Register(reg, cfgsvc); err != nil {
		t.Fatalf("failed to register configuration: %v", err)
	}

	return reg
}

// discardWriter swallows log output so tests stay quiet.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// Exec runs a registered tool by name and returns the ToolResult.
func Exec(t *testing.T, reg *mcp.Registry, tool, target string, args map[string]any) *mcp.ToolResult {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("failed to marshal args for %s: %v", tool, err)
	}
	result, err := reg.Execute(context.Background(), tool, target, b)
	if err != nil {
		t.Fatalf("registry.Execute(%q) error: %v", tool, err)
	}
	return result
}
