//go:build unit

package discovery

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"postgres-dba-mcp/internal/audit"
	"postgres-dba-mcp/internal/config"
	"postgres-dba-mcp/internal/database"
	"postgres-dba-mcp/internal/metrics"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// stubMeta is a metaExecutor that returns preset values or errors.
type stubMeta struct {
	mu         sync.Mutex
	info       *database.ServerInfo
	infoErr    error
	dbs        []database.DatabaseInfo
	dbsErr     error
	schemas    []database.SchemaInfo
	schemasErr error
	tables     []database.TableInfo
	tablesErr  error
	columns    []database.ColumnInfo
	columnsErr error
	indexes    []database.IndexInfo
	indexesErr error
	constraints []database.ConstraintInfo
	constraintsErr error
	size       *database.TableSizeInfo
	sizeErr    error
	stats      []database.ColumnStats
	statsErr   error
}

func (m *stubMeta) GetInfo(ctx context.Context, target string) (*database.ServerInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.infoErr != nil {
		return nil, m.infoErr
	}
	if m.info != nil {
		return m.info, nil
	}
	return &database.ServerInfo{Version: "16.0"}, nil
}

func (m *stubMeta) ListDatabases(ctx context.Context, target string) ([]database.DatabaseInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dbsErr != nil {
		return nil, m.dbsErr
	}
	if m.dbs != nil {
		return m.dbs, nil
	}
	return []database.DatabaseInfo{{Name: "postgres"}}, nil
}

func (m *stubMeta) ListSchemas(ctx context.Context, target string) ([]database.SchemaInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.schemasErr != nil {
		return nil, m.schemasErr
	}
	if m.schemas != nil {
		return m.schemas, nil
	}
	return []database.SchemaInfo{{Name: "public"}}, nil
}

func (m *stubMeta) ListTables(ctx context.Context, target, schema string) ([]database.TableInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tablesErr != nil {
		return nil, m.tablesErr
	}
	if m.tables != nil {
		return m.tables, nil
	}
	return []database.TableInfo{{Schema: schema, Name: "orders"}}, nil
}

func (m *stubMeta) DescribeTable(ctx context.Context, target, schema, table string) ([]database.ColumnInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.columnsErr != nil {
		return nil, m.columnsErr
	}
	if m.columns != nil {
		return m.columns, nil
	}
	return []database.ColumnInfo{{Name: "id", Type: "bigint", IsPrimaryKey: true}}, nil
}

func (m *stubMeta) ListIndexes(ctx context.Context, target, schema, table string) ([]database.IndexInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.indexesErr != nil {
		return nil, m.indexesErr
	}
	if m.indexes != nil {
		return m.indexes, nil
	}
	return []database.IndexInfo{}, nil
}

func (m *stubMeta) ListConstraints(ctx context.Context, target, schema, table string) ([]database.ConstraintInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.constraintsErr != nil {
		return nil, m.constraintsErr
	}
	if m.constraints != nil {
		return m.constraints, nil
	}
	return []database.ConstraintInfo{}, nil
}

func (m *stubMeta) GetTableSize(ctx context.Context, target, schema, table string) (*database.TableSizeInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sizeErr != nil {
		return nil, m.sizeErr
	}
	if m.size != nil {
		return m.size, nil
	}
	return &database.TableSizeInfo{Schema: schema, Table: table, TotalSize: "16 kB"}, nil
}

func (m *stubMeta) GetColumnStats(ctx context.Context, target, schema, table string) ([]database.ColumnStats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.statsErr != nil {
		return nil, m.statsErr
	}
	if m.stats != nil {
		return m.stats, nil
	}
	return []database.ColumnStats{{Column: "id", NumDistinct: 100}}, nil
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

// newTestService builds a discovery Service with a stub metaExecutor.
func newTestService(t *testing.T, meta *stubMeta) (*Service, *auditRecorder) {
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
		nil, // manager not needed when meta is overridden
		auditLogger,
		metrics.New(),
	)
	if meta == nil {
		meta = &stubMeta{}
	}
	svc.meta = meta
	return svc, rec
}
