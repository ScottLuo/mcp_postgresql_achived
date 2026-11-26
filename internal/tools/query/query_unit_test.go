//go:build unit

package query

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"postgres-dba-mcp/internal/database"
	"postgres-dba-mcp/internal/mcp"
)

// mustExecOK builds a successful ExecResult with a single row.
func okResult() *database.ExecResult {
	return &database.ExecResult{
		Columns: []string{"id"},
		Rows:    [][]any{{int64(1)}},
		RowsReturned: 1,
		Duration:     time.Millisecond,
	}
}

// explainJSONResult builds an ExecResult mimicking EXPLAIN (FORMAT JSON): a
// single row whose first column is the JSON plan ([]byte).
func explainJSONResult(plan string) *database.ExecResult {
	return &database.ExecResult{
		Columns:      []string{"QUERY PLAN"},
		Rows:         [][]any{{[]byte(plan)}},
		RowsReturned: 1,
		Duration:     5 * time.Millisecond,
	}
}

// ---- db_query_readonly ----

func TestQueryReadonly_Success(t *testing.T) {
	d, _ := newTestDeps(t)
	fake := &fakeReadonlyExec{res: okResult()}
	d.ReadonlyExec = fake

	h := NewQueryReadonlyHandler(d)
	args, _ := json.Marshal(map[string]any{
		"sql":    "SELECT id FROM orders WHERE customer_id = $1",
		"params": []any{1001},
	})
	res, err := h.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %+v", res.Error)
	}
	// The read-only executor must receive the original SQL and the limit.
	if fake.sql != "SELECT id FROM orders WHERE customer_id = $1" {
		t.Errorf("sql = %q", fake.sql)
	}
	if len(fake.params) != 1 {
		t.Fatalf("params length = %d, want 1", len(fake.params))
	}
	// json.Unmarshal decodes JSON numbers as float64.
	if p, ok := fake.params[0].(float64); !ok || p != 1001 {
		t.Errorf("params[0] = %v (%T), want 1001", fake.params[0], fake.params[0])
	}
	if res.Metadata == nil || res.Metadata.RowsReturned != 1 {
		t.Errorf("unexpected metadata: %+v", res.Metadata)
	}
	data, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("data is not a map: %T", res.Data)
	}
	if data["rows_returned"] != int64(1) && data["rows_returned"] != 1 {
		// rows_returned is an int in the map; encoding/json keeps it as int.
		t.Errorf("rows_returned = %v", data["rows_returned"])
	}
}

func TestQueryReadonly_LimitEnforced(t *testing.T) {
	d, _ := newTestDeps(t)
	fake := &fakeReadonlyExec{res: okResult()}
	d.ReadonlyExec = fake

	h := NewQueryReadonlyHandler(d)
	args, _ := json.Marshal(map[string]any{"sql": "SELECT 1", "limit": 42})
	if _, err := h.Execute(context.Background(), args); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The caller-supplied limit is passed through to the executor.
	if fake.limit != 42 {
		t.Errorf("limit = %d, want 42 (caller limit is passed to the read-only executor)", fake.limit)
	}
}

// TestQueryReadonly_SQLValidation is the read-only SQL validation matrix:
// valid SELECTs pass, DML/DDL/multi-statement/data-modifying CTEs are rejected
// with the standard error code, and the executor is not invoked when rejected.
func TestQueryReadonly_SQLValidation(t *testing.T) {
	cases := []struct {
		name     string
		sql      string
		wantErr  bool
		wantCode string
	}{
		{name: "valid select", sql: "SELECT id FROM orders"},
		{name: "parameterized select", sql: "SELECT $1::int"},
		{name: "read-only cte", sql: "WITH c AS (SELECT 1 AS x) SELECT * FROM c"},
		{name: "subquery select", sql: "SELECT * FROM (SELECT id FROM orders) s"},
		{name: "join select", sql: "SELECT a.id FROM a JOIN b ON a.id = b.aid"},
		{name: "update rejected", sql: "UPDATE orders SET status = 'x' WHERE id = 1", wantErr: true, wantCode: mcp.ErrCodeSQLNotAllowed},
		{name: "delete rejected", sql: "DELETE FROM orders WHERE id = 1", wantErr: true, wantCode: mcp.ErrCodeSQLNotAllowed},
		{name: "insert rejected", sql: "INSERT INTO orders VALUES (1)", wantErr: true, wantCode: mcp.ErrCodeSQLNotAllowed},
		{name: "create table rejected", sql: "CREATE TABLE t (id INT)", wantErr: true, wantCode: mcp.ErrCodeSQLNotAllowed},
		{name: "drop table rejected", sql: "DROP TABLE orders", wantErr: true, wantCode: mcp.ErrCodeSQLNotAllowed},
		{name: "alter table rejected", sql: "ALTER TABLE orders ADD COLUMN x INT", wantErr: true, wantCode: mcp.ErrCodeSQLNotAllowed},
		{name: "truncated select", sql: "SELECT FROM WHERE", wantErr: true, wantCode: mcp.ErrCodeSQLInvalid},
		{name: "data-modifying cte", sql: "WITH d AS (DELETE FROM orders WHERE id=1 RETURNING *) SELECT * FROM d", wantErr: true, wantCode: mcp.ErrCodeSQLNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := newTestDeps(t)
			fake := &fakeReadonlyExec{res: okResult()}
			d.ReadonlyExec = fake
			h := NewQueryReadonlyHandler(d)
			args, _ := json.Marshal(map[string]any{"sql": tc.sql})
			res, err := h.Execute(context.Background(), args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr {
				if res.Success {
					t.Fatalf("expected error result, got success")
				}
				if res.Error.Code != tc.wantCode {
					t.Errorf("error code = %q, want %q", res.Error.Code, tc.wantCode)
				}
				if fake.sql != "" {
					t.Errorf("executor should not be invoked for rejected SQL, but was called with %q", fake.sql)
				}
			} else {
				if !res.Success {
					t.Fatalf("expected success, got %+v", res.Error)
				}
				if !strings.HasPrefix(fake.sql, "SELECT") && !strings.HasPrefix(fake.sql, "WITH") {
					t.Errorf("executor sql = %q, expected a SELECT/WITH statement", fake.sql)
				}
			}
		})
	}
}

// TestQueryReadonly_Limits enforces the server-side limits that the AI agent
// cannot bypass (spec §9.3).
func TestQueryReadonly_Limits(t *testing.T) {
	// SQL length limit: a >64KB SQL string is rejected before any DB I/O.
	t.Run("sql too long", func(t *testing.T) {
		d, _ := newTestDeps(t)
		d.Cfg.QueryLimits.MaxSQLLength = 10
		fake := &fakeReadonlyExec{res: okResult()}
		d.ReadonlyExec = fake
		h := NewQueryReadonlyHandler(d)
		args, _ := json.Marshal(map[string]any{"sql": "SELECT 12345"})
		res, _ := h.Execute(context.Background(), args)
		if res.Success {
			t.Fatal("expected failure for over-length SQL")
		}
		if res.Error.Code != mcp.ErrCodeSQLInvalid {
			t.Errorf("code = %q, want %q", res.Error.Code, mcp.ErrCodeSQLInvalid)
		}
		if fake.sql != "" {
			t.Error("executor should not be called when SQL is over the length limit")
		}
	})

	// Error mapping: the executor's sentinel errors map to standard codes.
	errCases := []struct {
		name    string
		execErr error
		want    string
	}{
		{name: "timeout", execErr: database.ErrQueryTimeout, want: mcp.ErrCodeQueryTimeout},
		{name: "result too large", execErr: database.ErrResultTooLarge, want: mcp.ErrCodeResultTooLarge},
		{name: "sql too long (executor)", execErr: database.ErrSQLTooLong, want: mcp.ErrCodeSQLInvalid},
		{name: "generic failure", execErr: database.NewTargetNotFound("x"), want: mcp.ErrCodeExecutionFailed},
	}
	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := newTestDeps(t)
			fake := &fakeReadonlyExec{err: tc.execErr}
			d.ReadonlyExec = fake
			h := NewQueryReadonlyHandler(d)
			args, _ := json.Marshal(map[string]any{"sql": "SELECT 1"})
			res, _ := h.Execute(context.Background(), args)
			if res.Success {
				t.Fatal("expected error result")
			}
			if res.Error.Code != tc.want {
				t.Errorf("code = %q, want %q", res.Error.Code, tc.want)
			}
		})
	}
}

func TestQueryReadonly_MissingSQL(t *testing.T) {
	d, _ := newTestDeps(t)
	d.ReadonlyExec = &fakeReadonlyExec{res: okResult()}
	h := NewQueryReadonlyHandler(d)
	args, _ := json.Marshal(map[string]any{})
	res, _ := h.Execute(context.Background(), args)
	if res.Success {
		t.Fatal("expected error for missing sql")
	}
	if res.Error.Code != mcp.ErrCodeSQLInvalid {
		t.Errorf("code = %q, want %q", res.Error.Code, mcp.ErrCodeSQLInvalid)
	}
}

func TestQueryReadonly_TargetNotFound(t *testing.T) {
	d, dbm := newTestDeps(t)
	dbm.notFound = true
	d.ReadonlyExec = &fakeReadonlyExec{res: okResult()}
	h := NewQueryReadonlyHandler(d)
	args, _ := json.Marshal(map[string]any{"sql": "SELECT 1", "target": "nope"})
	res, _ := h.Execute(context.Background(), args)
	if res.Success {
		t.Fatal("expected error for unknown target")
	}
	if res.Error.Code != mcp.ErrCodeTargetNotFound {
		t.Errorf("code = %q, want %q", res.Error.Code, mcp.ErrCodeTargetNotFound)
	}
}

func TestQueryReadonly_DefaultTarget(t *testing.T) {
	d, _ := newTestDeps(t) // DefaultTarget = "development"
	fake := &fakeReadonlyExec{res: okResult()}
	d.ReadonlyExec = fake
	h := NewQueryReadonlyHandler(d)
	args, _ := json.Marshal(map[string]any{"sql": "SELECT 1"})
	res, _ := h.Execute(context.Background(), args)
	if !res.Success {
		t.Fatalf("expected success, got %+v", res.Error)
	}
	if res.Metadata.Target != "development" {
		t.Errorf("target = %q, want default target %q", res.Metadata.Target, "development")
	}
}
