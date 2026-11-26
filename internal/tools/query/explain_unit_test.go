//go:build unit

package query

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"postgres-dba-mcp/internal/mcp"
)

// ---- db_explain_query ----

func TestExplainQuery_PlanOnly(t *testing.T) {
	d, _ := newTestDeps(t)
	plan := `[{"Plan":{"Node Type":"Seq Scan","Startup Cost":0.00,"Total Cost":1.01,"Plan Rows":100}}]`
	fake := &fakeSQLExec{res: explainJSONResult(plan)}
	d.Exec = fake

	h := NewExplainQueryHandler(d)
	args, _ := json.Marshal(map[string]any{"sql": "SELECT * FROM orders"})
	res, err := h.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got %+v", res.Error)
	}
	// EXPLAIN (FORMAT JSON) must NOT execute the original query: the SQL sent to
	// the executor is the wrapped EXPLAIN, not the bare SELECT.
	if fake.sql != "EXPLAIN (FORMAT JSON) SELECT * FROM orders" {
		t.Errorf("executed sql = %q, want %q", fake.sql, "EXPLAIN (FORMAT JSON) SELECT * FROM orders")
	}
	data, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("data is not a map: %T", res.Data)
	}
	if data["executed"] != false {
		t.Errorf("executed flag = %v, want false", data["executed"])
	}
	// The plan must be parsed back into structured JSON, not a raw string.
	if _, ok := data["plan"].([]any); !ok {
		t.Errorf("plan is not a parsed JSON array: %T", data["plan"])
	}
}

func TestExplainQuery_RejectsNonSelect(t *testing.T) {
	d, _ := newTestDeps(t)
	fake := &fakeSQLExec{}
	d.Exec = fake
	h := NewExplainQueryHandler(d)

	// A data-modifying statement must be rejected; the executor is not called.
	for _, sql := range []string{"UPDATE t SET x=1", "DELETE FROM t", "DROP TABLE t", "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d"} {
		args, _ := json.Marshal(map[string]any{"sql": sql})
		res, _ := h.Execute(context.Background(), args)
		if res.Success {
			t.Fatalf("sql %q: expected rejection, got success", sql)
		}
		if res.Error.Code != mcp.ErrCodeSQLNotAllowed {
			t.Errorf("sql %q: code = %q, want %q", sql, res.Error.Code, mcp.ErrCodeSQLNotAllowed)
		}
		if fake.sql != "" {
			t.Errorf("sql %q: executor should not be called", sql)
		}
	}
}

// ---- db_explain_analyze ----

func TestExplainAnalyze_Executes(t *testing.T) {
	d, _ := newTestDeps(t)
	plan := `[{"Plan":{"Node Type":"Seq Scan","Actual Rows":10,"Execution Time":1.2}}]`
	fake := &fakeSQLExec{res: explainJSONResult(plan)}
	d.Exec = fake

	h := NewExplainAnalyzeHandler(d)
	args, _ := json.Marshal(map[string]any{"sql": "SELECT * FROM orders"})
	res, err := h.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got %+v", res.Error)
	}
	if fake.sql != "EXPLAIN (ANALYZE, FORMAT JSON) SELECT * FROM orders" {
		t.Errorf("executed sql = %q, want ANALYZE variant", fake.sql)
	}
	data := res.Data.(map[string]any)
	if data["executed"] != true {
		t.Errorf("executed flag = %v, want true", data["executed"])
	}
}

func TestExplainAnalyze_RejectsDestructive(t *testing.T) {
	d, _ := newTestDeps(t)
	fake := &fakeSQLExec{}
	d.Exec = fake
	h := NewExplainAnalyzeHandler(d)

	// db_explain_analyze executes the query, so it must reject anything that is
	// not a single read-only SELECT (expensive/destructive queries). DML/DDL/
	// data-modifying CTEs map to DB_SQL_NOT_ALLOWED; a multi-statement input
	// maps to DB_SQL_INVALID.
	cases := []struct {
		sql      string
		wantCode string
	}{
		{sql: "DELETE FROM orders", wantCode: mcp.ErrCodeSQLNotAllowed},
		{sql: "TRUNCATE orders", wantCode: mcp.ErrCodeSQLNotAllowed},
		{sql: "SELECT 1; SELECT 2", wantCode: mcp.ErrCodeSQLInvalid},
		{sql: "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", wantCode: mcp.ErrCodeSQLNotAllowed},
	}
	for _, tc := range cases {
		args, _ := json.Marshal(map[string]any{"sql": tc.sql})
		res, _ := h.Execute(context.Background(), args)
		if res.Success {
			t.Fatalf("sql %q: expected rejection", tc.sql)
		}
		if res.Error.Code != tc.wantCode {
			t.Errorf("sql %q: code = %q, want %q", tc.sql, res.Error.Code, tc.wantCode)
		}
		if fake.sql != "" {
			t.Errorf("sql %q: executor should not be called for rejected SQL", tc.sql)
		}
	}
}

// ---- db_estimate_query_cost ----

func TestCostEstimate_ExtractsCosts(t *testing.T) {
	d, _ := newTestDeps(t)
	plan := `[{"Plan":{"Node Type":"Seq Scan","Startup Cost":0.00,"Total Cost":12.5,"Plan Rows":100}}]`
	fake := &fakeSQLExec{res: explainJSONResult(plan)}
	d.Exec = fake

	h := NewCostEstimateHandler(d)
	args, _ := json.Marshal(map[string]any{"sql": "SELECT * FROM orders"})
	res, err := h.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got %+v", res.Error)
	}
	// Cost estimation must not execute the query (no ANALYZE).
	if fake.sql != "EXPLAIN (FORMAT JSON) SELECT * FROM orders" {
		t.Errorf("executed sql = %q, want plain FORMAT JSON (no ANALYZE)", fake.sql)
	}
	data := res.Data.(map[string]any)
	if data["total_cost"] != 12.5 {
		t.Errorf("total_cost = %v, want 12.5", data["total_cost"])
	}
	if data["startup_cost"] != 0.0 {
		t.Errorf("startup_cost = %v, want 0.0", data["startup_cost"])
	}
}

func TestCostEstimate_RejectsNonSelect(t *testing.T) {
	d, _ := newTestDeps(t)
	fake := &fakeSQLExec{}
	d.Exec = fake
	h := NewCostEstimateHandler(d)
	args, _ := json.Marshal(map[string]any{"sql": "UPDATE t SET x=1"})
	res, _ := h.Execute(context.Background(), args)
	if res.Success {
		t.Fatal("expected rejection for non-SELECT")
	}
	if res.Error.Code != mcp.ErrCodeSQLNotAllowed {
		t.Errorf("code = %q, want %q", res.Error.Code, mcp.ErrCodeSQLNotAllowed)
	}
}

// ---- shared helpers ----

func TestParseExplainSQL(t *testing.T) {
	cases := []struct{ format, sql, want string }{
		{"FORMAT JSON", "SELECT 1", "EXPLAIN (FORMAT JSON) SELECT 1"},
		{"ANALYZE, FORMAT JSON", "SELECT 2", "EXPLAIN (ANALYZE, FORMAT JSON) SELECT 2"},
	}
	for _, c := range cases {
		if got := parseExplainSQL(c.format, c.sql); got != c.want {
			t.Errorf("parseExplainSQL(%q,%q) = %q, want %q", c.format, c.sql, got, c.want)
		}
	}
}

func TestExtractPlan_NonJSON(t *testing.T) {
	// A non-JSON plan value is returned verbatim as a string.
	res := explainJSONResult("not-json")
	got := extractPlan(res)
	if s, ok := got.(string); !ok || s != "not-json" {
		t.Errorf("extractPlan = %v, want string \"not-json\"", got)
	}
}

func TestExtractCost_NilAndEmpty(t *testing.T) {
	if c := extractCost(nil); c != (planCosts{}) {
		t.Errorf("extractCost(nil) = %+v", c)
	}
	if c := extractCost([]any{}); c != (planCosts{}) {
		t.Errorf("extractCost(empty slice) = %+v", c)
	}
}

// TestRegister_AllFourTools verifies Register wires all four tools with the
// correct permission levels and that the handlers are attached.
func TestRegister_AllFourTools(t *testing.T) {
	d, _ := newTestDeps(t)
	reg := mcp.NewRegistry(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	if err := Register(reg, d); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	want := map[string]mcp.PermissionLevel{
		"db_query_readonly":      mcp.PermissionReadOnly,
		"db_explain_query":       mcp.PermissionReadOnly,
		"db_explain_analyze":     mcp.PermissionControlledMaintenance,
		"db_estimate_query_cost": mcp.PermissionReadOnly,
	}
	for name, perm := range want {
		def, ok := reg.Get(name)
		if !ok {
			t.Fatalf("tool %q not registered", name)
		}
		if def.Permission != perm {
			t.Errorf("%s permission = %v, want %v", name, def.Permission, perm)
		}
		if def.Handler == nil {
			t.Errorf("%s has nil handler", name)
		}
		if !strings.Contains(string(def.InputSchema), "sql") {
			t.Errorf("%s input schema missing sql property", name)
		}
	}
}
