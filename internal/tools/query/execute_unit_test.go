//go:build unit

package query

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"postgres-dba-mcp/internal/audit"
	"postgres-dba-mcp/internal/config"
	"postgres-dba-mcp/internal/database"
	"postgres-dba-mcp/internal/metrics"
)

// fakeReadonlyExec implements readonlyExecutor and records the arguments it is
// called with, returning a preset result or error.
type fakeReadonlyExec struct {
	sql    string
	params []any
	limit  int
	res    *database.ExecResult
	err    error
}

func (f *fakeReadonlyExec) ExecuteReadOnly(_ context.Context, sql string, params []any, limit int) (*database.ExecResult, error) {
	f.sql, f.params, f.limit = sql, params, limit
	return f.res, f.err
}

// fakeSQLExec implements sqlExecutor and records the arguments it is called with.
type fakeSQLExec struct {
	sql    string
	params []any
	limit  int
	res    *database.ExecResult
	err    error
}

func (f *fakeSQLExec) Execute(_ context.Context, sql string, params []any, limit int) (*database.ExecResult, error) {
	f.sql, f.params, f.limit = sql, params, limit
	return f.res, f.err
}

// fakeDBM implements database.DatabaseManager. When notFound is true, GetPool
// returns a *database.TargetNotFoundError.
type fakeDBM struct {
	pool     *pgxpool.Pool
	notFound bool
}

func (f *fakeDBM) GetPool(target string) (*pgxpool.Pool, error) {
	if f.notFound {
		return nil, database.NewTargetNotFound(target)
	}
	if f.pool == nil {
		f.pool = &pgxpool.Pool{}
	}
	return f.pool, nil
}

func (f *fakeDBM) GetTarget(name string) (*database.DatabaseTarget, error) {
	if f.notFound {
		return nil, database.NewTargetNotFound(name)
	}
	return &database.DatabaseTarget{Name: name, Pool: f.pool}, nil
}

func (f *fakeDBM) ListTargets() []string { return []string{"development"} }

func (f *fakeDBM) HealthCheck(_ context.Context) error { return nil }

func (f *fakeDBM) Close()                               {}

// newTestDeps builds a Deps with an enabled-false audit logger and a fresh
// metrics registry, wiring the given executor fakes. It returns the Deps plus
// the executor fakes for post-call assertions.
func newTestDeps(t *testing.T) (*Deps, *fakeDBM) {
	t.Helper()
	cfg := &config.Config{
		DefaultTarget: "development",
		QueryLimits: config.QueryLimits{
			Timeout:       10 * time.Second,
			MaxRows:       1000,
			MaxResultSize: 1048576,
			MaxSQLLength:  65536,
		},
	}
	dbm := &fakeDBM{pool: &pgxpool.Pool{}}
	aud := audit.New(false, slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})))
	met := metrics.New()
	d := NewDeps(dbm, cfg, aud, met, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return d, dbm
}
