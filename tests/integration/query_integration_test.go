//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"testing"

	"postgres-dba-mcp/internal/mcp"
	"postgres-dba-mcp/tests/integration/pgtest"
)

func newQueryEnv(t *testing.T) *mcp.Registry {
	t.Helper()
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	return pgtest.Registry(t, "dev", pg.DSN)
}

// Q-I01: basic read-only SELECT succeeds.
func TestIntegration_QueryReadonly_Success(t *testing.T) {
	reg := newQueryEnv(t)
	res := pgtest.Exec(t, reg, "db_query_readonly", "dev", map[string]any{
		"target": "dev",
		"sql":    "SELECT 1 AS test",
	})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), "test") {
		t.Errorf("data should include column 'test': %s", string(b))
	}
}

// Q-I02: parameterized query.
func TestIntegration_QueryReadonly_Parameters(t *testing.T) {
	reg := newQueryEnv(t)
	res := pgtest.Exec(t, reg, "db_query_readonly", "dev", map[string]any{
		"target": "dev",
		"sql":    "SELECT $1::int AS val",
		"params": []any{42},
	})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), "42") {
		t.Errorf("data should include value 42: %s", string(b))
	}
}

// Q-I03: row limit applied — table has 100 rows, limit=5.
func TestIntegration_QueryReadonly_Limit(t *testing.T) {
	reg := newQueryEnv(t)
	res := pgtest.Exec(t, reg, "db_query_readonly", "dev", map[string]any{
		"target": "dev",
		"sql":    "SELECT id FROM orders",
		"limit":  5,
	})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	if res.Metadata == nil {
		t.Fatalf("expected metadata to be present")
	}
	if res.Metadata.RowsReturned != 5 {
		t.Errorf("expected 5 rows, got %d", res.Metadata.RowsReturned)
	}
}

// Q-I04: query timeout — pg_sleep(15) with 3s timeout.
func TestIntegration_QueryReadonly_Timeout(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	// Build a registry with a short timeout (3s).
	reg := pgtest.Registry(t, "dev", pg.DSN)
	res := pgtest.Exec(t, reg, "db_query_readonly", "dev", map[string]any{
		"target": "dev",
		"sql":    "SELECT pg_sleep(15)",
	})
	// With the default 10s limit the query would still succeed (15>10 → timeout).
	// A 15s sleep against the default 10s query timeout must time out.
	if res.Success {
		t.Fatalf("expected timeout failure, got success")
	}
	if res.Error == nil {
		t.Fatalf("expected an error, got nil")
	}
	if res.Error.Code != mcp.ErrCodeQueryTimeout {
		t.Errorf("expected %s, got %s (%s)", mcp.ErrCodeQueryTimeout, res.Error.Code, res.Error.Message)
	}
}

// Q-I05: UPDATE rejected in read-only mode.
func TestIntegration_QueryReadonly_RejectUpdate(t *testing.T) {
	reg := newQueryEnv(t)
	res := pgtest.Exec(t, reg, "db_query_readonly", "dev", map[string]any{
		"target": "dev",
		"sql":    "UPDATE orders SET status='x' WHERE id=1",
	})
	if res.Success {
		t.Fatalf("expected failure for UPDATE, got success")
	}
}

// Q-I06: multiple statements rejected.
func TestIntegration_QueryReadonly_RejectMultipleStatements(t *testing.T) {
	reg := newQueryEnv(t)
	res := pgtest.Exec(t, reg, "db_query_readonly", "dev", map[string]any{
		"target": "dev",
		"sql":    "SELECT 1; SELECT 2",
	})
	if res.Success {
		t.Fatalf("expected failure for multiple statements, got success")
	}
}

// Q-I07: data-modifying CTE rejected.
func TestIntegration_QueryReadonly_RejectCTEWithModify(t *testing.T) {
	reg := newQueryEnv(t)
	res := pgtest.Exec(t, reg, "db_query_readonly", "dev", map[string]any{
		"target": "dev",
		"sql":    "WITH del AS (DELETE FROM orders WHERE id=1 RETURNING *) SELECT * FROM del",
	})
	if res.Success {
		t.Fatalf("expected failure for data-modifying CTE, got success")
	}
}

// Q-I08: valid read-only CTE succeeds.
func TestIntegration_QueryReadonly_ValidCTE(t *testing.T) {
	reg := newQueryEnv(t)
	res := pgtest.Exec(t, reg, "db_query_readonly", "dev", map[string]any{
		"target": "dev",
		"sql":    "WITH c AS (SELECT id FROM orders WHERE id < 5) SELECT count(*) AS n FROM c",
	})
	if !res.Success {
		t.Fatalf("expected success for read-only CTE, got error: %v", res.Error)
	}
}

// Q-I09: EXPLAIN without execution returns a JSON plan.
func TestIntegration_ExplainQuery(t *testing.T) {
	reg := newQueryEnv(t)
	res := pgtest.Exec(t, reg, "db_explain_query", "dev", map[string]any{
		"target": "dev",
		"sql":    "SELECT * FROM orders",
	})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), "Plan") && !strings.Contains(string(b), "plan") {
		t.Errorf("data should contain an explain plan: %s", string(b))
	}
}

// Q-U02: db_explain_query missing SQL param errors.
func TestExplainQuery_MissingSQL(t *testing.T) {
	reg := newQueryEnv(t)
	res := pgtest.Exec(t, reg, "db_explain_query", "dev", map[string]any{"target": "dev"})
	if res.Success {
		t.Fatalf("expected failure for missing sql, got success")
	}
}

// Q-I11: EXPLAIN ANALYZE executes the query and returns a plan with timing.
func TestIntegration_ExplainAnalyze(t *testing.T) {
	reg := newQueryEnv(t)
	res := pgtest.Exec(t, reg, "db_explain_analyze", "dev", map[string]any{
		"target": "dev",
		"sql":    "SELECT * FROM orders LIMIT 5",
	})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), "Execution Time") && !strings.Contains(string(b), "execution") {
		t.Errorf("analyze plan should include execution timing: %s", string(b))
	}
}

// Q-I13: cost estimation returns startup/total cost.
func TestIntegration_EstimateQueryCost(t *testing.T) {
	reg := newQueryEnv(t)
	res := pgtest.Exec(t, reg, "db_estimate_query_cost", "dev", map[string]any{
		"target": "dev",
		"sql":    "SELECT * FROM orders",
	})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), "cost") {
		t.Errorf("cost estimate should include 'cost': %s", string(b))
	}
}

// Q-I14: SQL longer than the 64KB limit is rejected.
func TestIntegration_QueryReadonly_SqlLengthLimit(t *testing.T) {
	reg := newQueryEnv(t)
	// Build a >64KB SQL string using a large IN list.
	var sb strings.Builder
	sb.WriteString("SELECT id FROM orders WHERE id IN (")
	for i := 0; i < 40000; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("0")
	}
	sb.WriteString(")")
	sql := sb.String()
	if len(sql) <= 65536 {
		t.Fatalf("test SQL not long enough: %d", len(sql))
	}
	res := pgtest.Exec(t, reg, "db_query_readonly", "dev", map[string]any{
		"target": "dev",
		"sql":    sql,
	})
	if res.Success {
		t.Fatalf("expected failure for oversized SQL, got success")
	}
}

// Q-U01: db_query_readonly missing SQL param errors.
func TestQueryReadonly_MissingSQL(t *testing.T) {
	reg := newQueryEnv(t)
	res := pgtest.Exec(t, reg, "db_query_readonly", "dev", map[string]any{"target": "dev"})
	if res.Success {
		t.Fatalf("expected failure for missing sql, got success")
	}
}

// Q-U03: empty params array is valid.
func TestQueryReadonly_EmptyParams(t *testing.T) {
	reg := newQueryEnv(t)
	res := pgtest.Exec(t, reg, "db_query_readonly", "dev", map[string]any{
		"target": "dev",
		"sql":    "SELECT 1 AS one",
		"params": []any{},
	})
	if !res.Success {
		t.Fatalf("expected success with empty params, got error: %v", res.Error)
	}
}
