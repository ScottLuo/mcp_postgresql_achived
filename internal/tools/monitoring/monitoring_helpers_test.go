//go:build unit

package monitoring

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"postgres-dba-mcp/internal/audit"
	"postgres-dba-mcp/internal/config"
	"postgres-dba-mcp/internal/database"
	"postgres-dba-mcp/internal/mcp"
	"postgres-dba-mcp/internal/metrics"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// stubManager is a database.DatabaseManager that returns a fixed pool for a
// known target and an ErrTargetNotFound for anything else.
type stubManager struct {
	pool *pgxpool.Pool
}

func (m *stubManager) GetPool(target string) (*pgxpool.Pool, error) {
	if target == "development" {
		return m.pool, nil
	}
	return nil, database.NewTargetNotFound(target)
}
func (m *stubManager) GetTarget(name string) (*database.DatabaseTarget, error) {
	if name == "development" {
		return &database.DatabaseTarget{Name: name}, nil
	}
	return nil, database.NewTargetNotFound(name)
}
func (m *stubManager) ListTargets() []string                { return []string{"development"} }
func (m *stubManager) HealthCheck(ctx context.Context) error { return nil }
func (m *stubManager) Close()                                {}

// stubRunner implements queryRunner. It returns a preset *database.ExecResult
// or error and records the SQL/limit it was asked to run.
type stubRunner struct {
	mu    sync.Mutex
	res   *database.ExecResult
	err   error
	// lastSQL/lastLimit capture the most recent invocation for assertions.
	lastSQL   string
	lastLimit int
}

func (r *stubRunner) Execute(ctx context.Context, sql string, params []any, limit int) (*database.ExecResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastSQL = sql
	r.lastLimit = limit
	if r.err != nil {
		return nil, r.err
	}
	if r.res != nil {
		return r.res, nil
	}
	return &database.ExecResult{Columns: []string{"x"}, Rows: [][]any{}, Duration: time.Millisecond}, nil
}

// sequentialRunner returns a different ExecResult on each consecutive call,
// used to test multi-step handlers (role detection + status query).
type sequentialRunner struct {
	results []sequenceStep
	idx     int
	mu      sync.Mutex
}

type sequenceStep struct {
	res *database.ExecResult
	err error
}

func (r *sequentialRunner) Execute(ctx context.Context, sql string, params []any, limit int) (*database.ExecResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.idx >= len(r.results) {
		return &database.ExecResult{Columns: []string{"x"}, Rows: [][]any{}}, nil
	}
	step := r.results[r.idx]
	r.idx++
	if step.err != nil {
		return nil, step.err
	}
	if step.res != nil {
		return step.res, nil
	}
	return &database.ExecResult{Columns: []string{"x"}, Rows: [][]any{}}, nil
}

// auditRecorder captures audit "tool" values by acting as a slog handler.
type auditRecorder struct {
	mu    sync.Mutex
	tools []string
}

func (a *auditRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (a *auditRecorder) Handle(_ context.Context, rec slog.Record) error {
	if rec.Message != "mcp_tool_audit" {
		return nil
	}
	rec.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "tool" {
			a.mu.Lock()
			a.tools = append(a.tools, attr.Value.String())
			a.mu.Unlock()
			return false
		}
		return true
	})
	return nil
}
func (a *auditRecorder) WithAttrs([]slog.Attr) slog.Handler { return a }
func (a *auditRecorder) WithGroup(string) slog.Handler      { return a }

// count returns how many times the given tool name was recorded.
func (a *auditRecorder) count(tool string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, t := range a.tools {
		if t == tool {
			n++
		}
	}
	return n
}

// discardHandler is a no-op slog.Handler used where a logger is required but
// its output is irrelevant to the assertion.
type discardHandler struct{}

func (d discardHandler) Enabled(context.Context, slog.Level) bool { return true }
func (d discardHandler) Handle(context.Context, slog.Record) error {
	return nil
}
func (d discardHandler) WithAttrs([]slog.Attr) slog.Handler { return d }
func (d discardHandler) WithGroup(string) slog.Handler      { return d }

// slogDiscardLogger returns a no-op logger for tests that need a *slog.Logger
// but do not care about its output.
func slogDiscardLogger() *slog.Logger {
	return slog.New(discardHandler{})
}

// newTestService builds a Service with a stub runner and a capturing audit sink.
func newTestService(t *testing.T, runner queryRunner, featureAvailable bool) (*Service, *auditRecorder) {
	t.Helper()
	rec := &auditRecorder{}
	auditLogger := audit.New(true, slog.New(rec))
	svc := NewService(
		&config.Config{QueryLimits: config.QueryLimits{
			Timeout:       10 * time.Second,
			MaxRows:       100,
			MaxResultSize: 1048576,
			MaxSQLLength:  65536,
		}},
		&stubManager{pool: nil},
		auditLogger,
		metrics.New(),
	)
	if runner != nil {
		svc.runner = runner
	}
	svc.featureCheck = func(ctx context.Context, pool *pgxpool.Pool, ext string) bool {
		return featureAvailable
	}
	return svc, rec
}

// resultRows returns the Data of a ToolResult cast to []map[string]any when
// possible.
func resultRows(result *mcp.ToolResult) []map[string]any {
	if result == nil || result.Data == nil {
		return nil
	}
	if rows, ok := result.Data.([]map[string]any); ok {
		return rows
	}
	return nil
}
