//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"postgres-dba-mcp/internal/security"
	"postgres-dba-mcp/tests/integration/pgtest"
)

// createApproval builds a valid approval record bound to the given tool/params.
func createApproval(target, tool string, params map[string]any) *security.ApprovalRecord {
	normalized, _ := security.NormalizeParams(params)
	return &security.ApprovalRecord{
		ID:               "appr-test-1",
		Target:           target,
		Tool:             tool,
		NormalizedParams: normalized,
		Expiry:           time.Now().Add(5 * time.Minute),
		Approver:         "tester",
		CreatedAt:        time.Now(),
	}
}

// MAI-I01: db_vacuum_table executes VACUUM outside a transaction.
func TestIntegration_VacuumTable(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)
	res := pgtest.Exec(t, reg, "db_vacuum_table", "dev",
		map[string]any{"target": "dev", "schema": "public", "table": "orders"})
	if !res.Success {
		t.Fatalf("expected vacuum success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	t.Logf("vacuum data: %s", string(b))
}

// MAI-I02: db_vacuum_table on an unknown table errors without leaking DSN.
func TestIntegration_VacuumTable_MissingTable(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)
	res := pgtest.Exec(t, reg, "db_vacuum_table", "dev",
		map[string]any{"target": "dev", "schema": "public", "table": "no_such_table"})
	if res.Success {
		t.Fatalf("expected failure for unknown table, got success")
	}
	if res.Error != nil && (strings.Contains(res.Error.Message, "postgres://") || strings.Contains(res.Error.Message, "postgresql://")) {
		t.Errorf("error leaked DSN: %s", res.Error.Message)
	}
}

// MAI-I03: db_analyze_table executes ANALYZE.
func TestIntegration_AnalyzeTable(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)
	res := pgtest.Exec(t, reg, "db_analyze_table", "dev",
		map[string]any{"target": "dev", "schema": "public", "table": "orders"})
	if !res.Success {
		t.Fatalf("expected analyze success, got error: %v", res.Error)
	}
}

// MAI-I04: db_reindex_table rebuilds indexes.
func TestIntegration_ReindexTable(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)
	res := pgtest.Exec(t, reg, "db_reindex_table", "dev",
		map[string]any{"target": "dev", "schema": "public", "table": "orders"})
	if !res.Success {
		t.Fatalf("expected reindex success, got error: %v", res.Error)
	}
}

// MAI-I05: db_create_index creates a new index.
func TestIntegration_CreateIndex(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)
	res := pgtest.Exec(t, reg, "db_create_index", "dev", map[string]any{
		"target":     "dev",
		"schema":     "public",
		"table":      "orders",
		"index_name": "idx_orders_amount",
		"columns":    []any{"amount"},
	})
	if !res.Success {
		t.Fatalf("expected create index success, got error: %v", res.Error)
	}
	// Verify the index actually exists.
	pool := pg.Pool(t, pg.DSN)
	defer pool.Close()
	var exists bool
	err := pool.QueryRow(t.Context(),
		"SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE indexname='idx_orders_amount' AND schemaname='public')").Scan(&exists)
	if err != nil {
		t.Fatalf("failed to check index: %v", err)
	}
	if !exists {
		t.Errorf("expected index idx_orders_amount to exist after create")
	}
}

// MAI-I06: db_create_index with CONCURRENTLY succeeds (outside transaction).
func TestIntegration_CreateIndex_Concurrently(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)
	res := pgtest.Exec(t, reg, "db_create_index", "dev", map[string]any{
		"target":       "dev",
		"schema":       "public",
		"table":        "orders",
		"index_name":   "idx_orders_amount_cc",
		"columns":      []any{"amount"},
		"concurrently": true,
	})
	if !res.Success {
		t.Fatalf("expected create index concurrently success, got error: %v", res.Error)
	}
}

// MAI-I07: db_create_index with an invalid identifier is rejected.
func TestIntegration_CreateIndex_InvalidDefinition(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)
	res := pgtest.Exec(t, reg, "db_create_index", "dev", map[string]any{
		"target":     "dev",
		"schema":     "public",
		"table":      "orders",
		"index_name": "bad name with space",
		"columns":    []any{"amount"},
	})
	if res.Success {
		t.Fatalf("expected failure for invalid identifier, got success")
	}
}

// MAI-I08: db_drop_index with a valid approval removes the index.
func TestIntegration_DropIndex(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)

	// Seed an index to drop.
	pool := pg.Pool(t, pg.DSN)
	_, _ = pool.Exec(t.Context(), "CREATE INDEX idx_drop_me ON orders (amount)")
	pool.Close()

	appr := createApproval("dev", "db_drop_index", map[string]any{
		"schema":    "public",
		"index":     "idx_drop_me",
		"if_exists": false,
	})
	reg := pgtest.RegistryWithApproval(t, "dev", pg.DSN, []*security.ApprovalRecord{appr})
	res := pgtest.Exec(t, reg, "db_drop_index", "dev", map[string]any{
		"target":      "dev",
		"schema":      "public",
		"index":       "idx_drop_me",
		"approval_id": "appr-test-1",
	})
	if !res.Success {
		t.Fatalf("expected drop index success, got error: %v", res.Error)
	}
}

// MAI-I09: db_drop_index on a missing index (if_exists) succeeds.
func TestIntegration_DropIndex_Missing(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)

	appr := createApproval("dev", "db_drop_index", map[string]any{
		"schema":    "public",
		"index":     "no_such_index",
		"if_exists": true,
	})
	reg := pgtest.RegistryWithApproval(t, "dev", pg.DSN, []*security.ApprovalRecord{appr})
	res := pgtest.Exec(t, reg, "db_drop_index", "dev", map[string]any{
		"target":      "dev",
		"schema":      "public",
		"index":       "no_such_index",
		"if_exists":   true,
		"approval_id": "appr-test-1",
	})
	if !res.Success {
		t.Fatalf("expected drop index (if_exists) success, got error: %v", res.Error)
	}
}

// MAI-I10: db_cancel_query cancels a running query.
func TestIntegration_CancelQuery(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)

	// Start a long-running query in a separate session.
	pool := pg.Pool(t, pg.DSN)
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatalf("failed to acquire conn: %v", err)
	}
	go func() {
		// Release the pool connection once the query finishes (on cancellation),
		// so pool.Close() does not block on a held connection.
		defer conn.Release()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		_, _ = conn.Query(ctx, "SELECT pg_sleep(30)")
	}()
	time.Sleep(500 * time.Millisecond)

	var pid int
	err = pool.QueryRow(t.Context(), "SELECT pid FROM pg_stat_activity WHERE query LIKE '%pg_sleep(30)%' LIMIT 1").Scan(&pid)
	if err != nil {
		conn.Release()
		pool.Close()
		t.Fatalf("failed to find sleep pid: %v", err)
	}

	reg := pgtest.Registry(t, "dev", pg.DSN)
	res := pgtest.Exec(t, reg, "db_cancel_query", "dev", map[string]any{
		"target": "dev",
		"pid":    pid,
	})
	if !res.Success {
		t.Fatalf("expected cancel query success, got error: %v", res.Error)
	}
	_ = conn
	pool.Close()
}

// MAI-I11: db_terminate_connection terminates a backend.
func TestIntegration_TerminateConnection(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)

	pool := pg.Pool(t, pg.DSN)
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatalf("failed to acquire conn: %v", err)
	}
	go func() {
		// Release the pool connection once the query finishes (which happens
		// immediately when the backend is terminated), so pool.Close() does
		// not block forever on a held, dead connection.
		defer conn.Release()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		_, _ = conn.Query(ctx, "SELECT pg_sleep(30)")
	}()
	time.Sleep(500 * time.Millisecond)

	var pid int
	if err := pool.QueryRow(t.Context(), "SELECT pid FROM pg_stat_activity WHERE query LIKE '%pg_sleep(30)%' LIMIT 1").Scan(&pid); err != nil {
		conn.Release()
		pool.Close()
		t.Fatalf("failed to find sleep pid: %v", err)
	}

	reg := pgtest.Registry(t, "dev", pg.DSN)
	res := pgtest.Exec(t, reg, "db_terminate_connection", "dev", map[string]any{
		"target": "dev",
		"pid":    pid,
	})
	if !res.Success {
		t.Fatalf("expected terminate connection success, got error: %v", res.Error)
	}
	pool.Close()
}

// MAI-I12: db_drop_index without approval returns DB_APPROVAL_REQUIRED.
func TestIntegration_Maintenance_RequiresApproval(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	// Registry with approval validator but no seeded approvals.
	reg := pgtest.RegistryWithApproval(t, "dev", pg.DSN, nil)
	res := pgtest.Exec(t, reg, "db_drop_index", "dev", map[string]any{
		"target": "dev",
		"schema": "public",
		"index":  "idx_orders_customer",
	})
	if res.Success {
		t.Fatalf("expected approval required, got success")
	}
	if res.Error == nil || res.Error.Code != "DB_APPROVAL_REQUIRED" {
		t.Errorf("expected DB_APPROVAL_REQUIRED, got %v", res.Error)
	}
}

// MAI-I13: db_drop_index with a valid approval succeeds.
func TestIntegration_Maintenance_WithApproval(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	pool := pg.Pool(t, pg.DSN)
	_, _ = pool.Exec(t.Context(), "CREATE INDEX idx_with_appr ON orders (amount)")
	pool.Close()

	appr := createApproval("dev", "db_drop_index", map[string]any{
		"schema":    "public",
		"index":     "idx_with_appr",
		"if_exists": false,
	})
	reg := pgtest.RegistryWithApproval(t, "dev", pg.DSN, []*security.ApprovalRecord{appr})
	res := pgtest.Exec(t, reg, "db_drop_index", "dev", map[string]any{
		"target":      "dev",
		"schema":      "public",
		"index":       "idx_with_appr",
		"approval_id": "appr-test-1",
	})
	if !res.Success {
		t.Fatalf("expected drop index with approval success, got error: %v", res.Error)
	}
}
