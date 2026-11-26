//go:build unit

package maintenance

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
	"postgres-dba-mcp/internal/metrics"
	"postgres-dba-mcp/internal/security"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// stubManager is a database.DatabaseManager that returns a fixed pool for
// "development" and an ErrTargetNotFound for anything else.
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
func (m *stubManager) ListTargets() []string { return []string{"development"} }
func (m *stubManager) HealthCheck(ctx context.Context) error {
	return nil
}
func (m *stubManager) Close() {}

// stubNonTxRunner implements nonTxRunner and records the SQL it was asked to run.
type stubNonTxRunner struct {
	mu        sync.Mutex
	res       *database.ExecResult
	err       error
	lastSQL   string
	called    bool
}

func (r *stubNonTxRunner) ExecuteNonTransactional(ctx context.Context, sql string, params []any) (*database.ExecResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastSQL = sql
	r.called = true
	if r.err != nil {
		return nil, r.err
	}
	if r.res != nil {
		return r.res, nil
	}
	return &database.ExecResult{Columns: []string{"x"}, Rows: [][]any{}, Duration: time.Millisecond}, nil
}

// stubQueryRunner implements queryRunner and records the SQL it was asked to run.
type stubQueryRunner struct {
	mu        sync.Mutex
	res       *database.ExecResult
	err       error
	lastSQL   string
	lastParam []any
	called    bool
}

func (r *stubQueryRunner) Execute(ctx context.Context, sql string, params []any, limit int) (*database.ExecResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastSQL = sql
	r.lastParam = params
	r.called = true
	if r.err != nil {
		return nil, r.err
	}
	if r.res != nil {
		return r.res, nil
	}
	return &database.ExecResult{Columns: []string{"x"}, Rows: [][]any{}, Duration: time.Millisecond}, nil
}

// auditRecorder captures audit tool names by acting as a slog handler.
type auditRecorder struct {
	mu      sync.Mutex
	tools   []string
	approvals []string
}

func (a *auditRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (a *auditRecorder) Handle(_ context.Context, rec slog.Record) error {
	if rec.Message != "mcp_tool_audit" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	rec.Attrs(func(attr slog.Attr) bool {
		switch attr.Key {
		case "tool":
			a.tools = append(a.tools, attr.Value.String())
		case "approval_id":
			a.approvals = append(a.approvals, attr.Value.String())
		}
		return true
	})
	return nil
}
func (a *auditRecorder) WithAttrs([]slog.Attr) slog.Handler { return a }
func (a *auditRecorder) WithGroup(string) slog.Handler      { return a }

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

func (a *auditRecorder) approvalCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.approvals)
}

// discardHandler is a no-op slog.Handler.
type discardHandler struct{}

func (d discardHandler) Enabled(context.Context, slog.Level) bool { return true }
func (d discardHandler) Handle(context.Context, slog.Record) error {
	return nil
}
func (d discardHandler) WithAttrs([]slog.Attr) slog.Handler { return d }
func (d discardHandler) WithGroup(string) slog.Handler      { return d }

// slogDiscardLogger returns a no-op logger.
func slogDiscardLogger() *slog.Logger {
	return slog.New(discardHandler{})
}

// newTestService builds a maintenance Service with stubs.
func newTestService(t *testing.T, nonTx *stubNonTxRunner, query *stubQueryRunner, approval *security.ApprovalValidator) (*Service, *auditRecorder) {
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
		approval,
		slogDiscardLogger(),
	)
	if nonTx != nil {
		svc.nonTxRunner = nonTx
	}
	if query != nil {
		svc.queryRunner = query
	}
	return svc, rec
}

// newTestServiceWithApproval builds a Service with a pre-populated approval store.
func newTestServiceWithApproval(t *testing.T, nonTx *stubNonTxRunner, query *stubQueryRunner, approvals ...*security.ApprovalRecord) (*Service, *auditRecorder) {
	t.Helper()
	store := security.NewInMemoryApprovalStore()
	for _, rec := range approvals {
		if err := store.Create(rec); err != nil {
			t.Fatalf("create approval: %v", err)
		}
	}
	validator := security.NewApprovalValidator(store)
	return newTestService(t, nonTx, query, validator)
}
