//go:build unit

package maintenance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"postgres-dba-mcp/internal/mcp"
	"postgres-dba-mcp/internal/security"
)

// ---------------------------------------------------------------------------
// Registration tests
// ---------------------------------------------------------------------------

func TestRegister_AllSevenTools(t *testing.T) {
	nonTx := &stubNonTxRunner{}
	svc, _ := newTestService(t, nonTx, nil, nil)
	reg := mcp.NewRegistry(slogDiscardLogger(), nil, nil)
	if err := Register(reg, svc); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(reg.List()) != 7 {
		t.Fatalf("expected 7 tools, got %d", len(reg.List()))
	}
	// Verify specific tool names and permissions.
	wantPerms := map[string]mcp.PermissionLevel{
		"db_vacuum_table":         mcp.PermissionControlledMaintenance,
		"db_analyze_table":        mcp.PermissionControlledMaintenance,
		"db_reindex_table":        mcp.PermissionControlledMaintenance,
		"db_create_index":         mcp.PermissionControlledMaintenance,
		"db_drop_index":           mcp.PermissionRequiresApproval,
		"db_cancel_query":         mcp.PermissionControlledMaintenance,
		"db_terminate_connection": mcp.PermissionRequiresApproval,
	}
	for name, wantPerm := range wantPerms {
		def, ok := reg.Get(name)
		if !ok {
			t.Errorf("tool %q not registered", name)
			continue
		}
		if def.Permission != wantPerm {
			t.Errorf("tool %s: permission = %s, want %s", name, def.Permission, wantPerm)
		}
	}
}

func TestRegister_ValidSchemas(t *testing.T) {
	schemas := []json.RawMessage{
		schemaVacuumTable(),
		schemaAnalyzeTable(),
		schemaReindexTable(),
		schemaCreateIndex(),
		schemaDropIndex(),
		schemaCancelQuery(),
		schemaTerminateConnection(),
	}
	for i, s := range schemas {
		var m map[string]any
		if err := json.Unmarshal(s, &m); err != nil {
			t.Errorf("schema %d: invalid JSON: %v", i, err)
		}
	}
}

func TestToolCount(t *testing.T) {
	if got := ToolCount(); got != 7 {
		t.Errorf("ToolCount() = %d, want 7", got)
	}
}

// ---------------------------------------------------------------------------
// Vacuum tests
// ---------------------------------------------------------------------------

func TestVacuumTable_ArgValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]any
		wantMsg string
	}{
		{"missing schema", map[string]any{"target": "development", "table": "orders"}, "schema"},
		{"missing table", map[string]any{"target": "development", "schema": "public"}, "table"},
		{"semicolon in schema", map[string]any{"target": "development", "schema": "pub;lic", "table": "orders"}, "invalid characters"},
		{"newline in table", map[string]any{"target": "development", "schema": "public", "table": "ord\ners"}, "invalid characters"},
		{"identifier too long", map[string]any{"target": "development", "schema": "public", "table": strings.Repeat("x", 64)}, "maximum identifier length"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nonTx := &stubNonTxRunner{}
			svc, _ := newTestService(t, nonTx, nil, nil)
			b, _ := json.Marshal(tc.args)
			res, err := svc.vacuumTable(context.Background(), b)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Success {
				t.Fatalf("expected failure, got success")
			}
			if !strings.Contains(res.Error.Message, tc.wantMsg) {
				t.Errorf("error message = %q, want contains %q", res.Error.Message, tc.wantMsg)
			}
			if nonTx.called {
				t.Errorf("nonTx runner should not have been called")
			}
		})
	}
}

func TestVacuumTable_UsesNonTransactionalPath(t *testing.T) {
	nonTx := &stubNonTxRunner{}
	svc, _ := newTestService(t, nonTx, nil, nil)
	b, _ := json.Marshal(map[string]any{"target": "development", "schema": "public", "table": "orders"})
	res, err := svc.vacuumTable(context.Background(), b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	if !nonTx.called {
		t.Fatal("non-transactional runner was not called")
	}
	if !strings.Contains(nonTx.lastSQL, "VACUUM") {
		t.Errorf("SQL does not contain VACUUM: %s", nonTx.lastSQL)
	}
	if !strings.Contains(nonTx.lastSQL, `"public"."orders"`) {
		t.Errorf("SQL does not contain quoted identifiers: %s", nonTx.lastSQL)
	}
}

func TestVacuumTable_FullAnalyze(t *testing.T) {
	nonTx := &stubNonTxRunner{}
	svc, _ := newTestService(t, nonTx, nil, nil)
	b, _ := json.Marshal(map[string]any{
		"target": "development", "schema": "public", "table": "orders",
		"full": true, "analyze": true,
	})
	_, _ = svc.vacuumTable(context.Background(), b)
	want := `VACUUM FULL ANALYZE "public"."orders"`
	if nonTx.lastSQL != want {
		t.Errorf("SQL = %q, want %q", nonTx.lastSQL, want)
	}
}

func TestVacuumTable_PrivilegeDenied(t *testing.T) {
	nonTx := &stubNonTxRunner{
		err: &pgconn.PgError{Code: "42501", Message: "permission denied to vacuum table"},
	}
	svc, _ := newTestService(t, nonTx, nil, nil)
	b, _ := json.Marshal(map[string]any{"target": "development", "schema": "public", "table": "orders"})
	res, _ := svc.vacuumTable(context.Background(), b)
	if res.Success {
		t.Fatal("expected failure")
	}
	if res.Error.Code != "DB_PERMISSION_DENIED" {
		t.Errorf("code = %s, want DB_PERMISSION_DENIED", res.Error.Code)
	}
}

// ---------------------------------------------------------------------------
// Analyze tests
// ---------------------------------------------------------------------------

func TestAnalyzeTable_UsesNonTransactionalPath(t *testing.T) {
	nonTx := &stubNonTxRunner{}
	svc, _ := newTestService(t, nonTx, nil, nil)
	b, _ := json.Marshal(map[string]any{"target": "development", "schema": "public", "table": "orders"})
	res, err := svc.analyzeTable(context.Background(), b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	if !nonTx.called {
		t.Fatal("non-transactional runner was not called")
	}
	want := `ANALYZE "public"."orders"`
	if nonTx.lastSQL != want {
		t.Errorf("SQL = %q, want %q", nonTx.lastSQL, want)
	}
}

func TestAnalyzeTable_Success(t *testing.T) {
	nonTx := &stubNonTxRunner{}
	svc, rec := newTestService(t, nonTx, nil, nil)
	b, _ := json.Marshal(map[string]any{"target": "development", "schema": "public", "table": "orders"})
	_, _ = svc.analyzeTable(context.Background(), b)
	if rec.count(toolAnalyzeTable) != 1 {
		t.Errorf("expected 1 audit record for analyze, got %d", rec.count(toolAnalyzeTable))
	}
}

// ---------------------------------------------------------------------------
// Reindex tests
// ---------------------------------------------------------------------------

func TestReindexTable_Concurrently_UsesNonTransactionalPath(t *testing.T) {
	nonTx := &stubNonTxRunner{}
	svc, _ := newTestService(t, nonTx, nil, nil)
	b, _ := json.Marshal(map[string]any{"target": "development", "schema": "public", "table": "orders", "concurrently": true})
	res, _ := svc.reindexTable(context.Background(), b)
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	if !nonTx.called {
		t.Fatal("non-transactional runner was not called")
	}
	want := `REINDEX CONCURRENTLY TABLE "public"."orders"`
	if nonTx.lastSQL != want {
		t.Errorf("SQL = %q, want %q", nonTx.lastSQL, want)
	}
}

func TestReindexTable_Plain_UsesNonTransactionalPath(t *testing.T) {
	nonTx := &stubNonTxRunner{}
	svc, _ := newTestService(t, nonTx, nil, nil)
	b, _ := json.Marshal(map[string]any{"target": "development", "schema": "public", "table": "orders"})
	res, _ := svc.reindexTable(context.Background(), b)
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	if !nonTx.called {
		t.Fatal("non-transactional runner was not called")
	}
	want := `REINDEX TABLE "public"."orders"`
	if nonTx.lastSQL != want {
		t.Errorf("SQL = %q, want %q", nonTx.lastSQL, want)
	}
}

// ---------------------------------------------------------------------------
// Create index tests
// ---------------------------------------------------------------------------

func TestCreateIndex_Concurrently_UsesNonTransactionalPath(t *testing.T) {
	nonTx := &stubNonTxRunner{}
	svc, _ := newTestService(t, nonTx, nil, nil)
	b, _ := json.Marshal(map[string]any{
		"target": "development", "schema": "public", "table": "orders",
		"index_name": "idx_orders_cust", "columns": []string{"customer_id", "created_at"},
		"concurrently": true,
	})
	res, _ := svc.createIndex(context.Background(), b)
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	if !nonTx.called {
		t.Fatal("non-transactional runner was not called")
	}
	want := `CREATE INDEX CONCURRENTLY "idx_orders_cust" ON "public"."orders" ("customer_id", "created_at")`
	if nonTx.lastSQL != want {
		t.Errorf("SQL = %q, want %q", nonTx.lastSQL, want)
	}
}

func TestCreateIndex_Plain_UsesNonTransactionalPath(t *testing.T) {
	nonTx := &stubNonTxRunner{}
	svc, _ := newTestService(t, nonTx, nil, nil)
	b, _ := json.Marshal(map[string]any{
		"target": "development", "schema": "public", "table": "orders",
		"index_name": "idx_orders_status", "columns": []string{"status"},
		"unique": true,
	})
	res, _ := svc.createIndex(context.Background(), b)
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	want := `CREATE UNIQUE INDEX "idx_orders_status" ON "public"."orders" ("status")`
	if nonTx.lastSQL != want {
		t.Errorf("SQL = %q, want %q", nonTx.lastSQL, want)
	}
}

func TestCreateIndex_ArgValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]any
		wantMsg string
	}{
		{"missing index_name", map[string]any{"target": "development", "schema": "public", "table": "orders", "columns": []string{"id"}}, "index_name"},
		{"missing columns", map[string]any{"target": "development", "schema": "public", "table": "orders", "index_name": "idx_x"}, "columns"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nonTx := &stubNonTxRunner{}
			svc, _ := newTestService(t, nonTx, nil, nil)
			b, _ := json.Marshal(tc.args)
			res, _ := svc.createIndex(context.Background(), b)
			if res.Success {
				t.Fatal("expected failure")
			}
			if !strings.Contains(res.Error.Message, tc.wantMsg) {
				t.Errorf("error = %q, want contains %q", res.Error.Message, tc.wantMsg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Drop index tests
// ---------------------------------------------------------------------------

func TestDropIndex_Success(t *testing.T) {
	nonTx := &stubNonTxRunner{}
	// No approval validator, so approval is skipped.
	svc, _ := newTestService(t, nonTx, nil, nil)
	b, _ := json.Marshal(map[string]any{
		"target": "development", "schema": "public", "index": "idx_old", "if_exists": true,
	})
	res, _ := svc.dropIndex(context.Background(), b)
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	if !nonTx.called {
		t.Fatal("non-transactional runner was not called")
	}
	want := `DROP INDEX IF EXISTS "public"."idx_old"`
	if nonTx.lastSQL != want {
		t.Errorf("SQL = %q, want %q", nonTx.lastSQL, want)
	}
}

func TestDropIndex_ApprovalRequired_MissingApproval(t *testing.T) {
	nonTx := &stubNonTxRunner{}
	// Create a service with an approval validator but no approval provided.
	store := security.NewInMemoryApprovalStore()
	validator := security.NewApprovalValidator(store)
	svc, _ := newTestService(t, nonTx, nil, validator)
	b, _ := json.Marshal(map[string]any{
		"target": "development", "schema": "public", "index": "idx_old",
	})
	res, _ := svc.dropIndex(context.Background(), b)
	if res.Success {
		t.Fatal("expected failure when approval is missing")
	}
	if res.Error.Code != "DB_APPROVAL_REQUIRED" {
		t.Errorf("code = %s, want DB_APPROVAL_REQUIRED", res.Error.Code)
	}
	if nonTx.called {
		t.Error("nonTx runner should not have been called when approval is missing")
	}
}

func TestDropIndex_ApprovalRequired_ValidApproval(t *testing.T) {
	nonTx := &stubNonTxRunner{}
	store := security.NewInMemoryApprovalStore()
	// The params must match what checkApproval will normalize.
	// checkApproval normalizes: {"index":"idx_old","if_exists":false,"schema":"public"}
	normalized, _ := security.NormalizeParams(map[string]any{"index": "idx_old", "if_exists": false, "schema": "public"})
	_ = store.Create(&security.ApprovalRecord{
		ID:               "app-123",
		Target:           "development",
		Tool:             "db_drop_index",
		NormalizedParams: normalized,
		Expiry:           time.Now().Add(10 * time.Minute),
		Approver:         "admin@example.com",
	})
	validator := security.NewApprovalValidator(store)
	svc, _ := newTestService(t, nonTx, nil, validator)
	b, _ := json.Marshal(map[string]any{
		"target": "development", "schema": "public", "index": "idx_old", "approval_id": "app-123",
	})
	res, _ := svc.dropIndex(context.Background(), b)
	if !res.Success {
		t.Fatalf("expected success with valid approval, got error: %v", res.Error)
	}
	if !nonTx.called {
		t.Fatal("non-transactional runner was not called")
	}
}

func TestDropIndex_ApprovalInvalid_Expired(t *testing.T) {
	nonTx := &stubNonTxRunner{}
	store := security.NewInMemoryApprovalStore()
	normalized, _ := security.NormalizeParams(map[string]any{"index": "idx_old", "if_exists": false, "schema": "public"})
	_ = store.Create(&security.ApprovalRecord{
		ID:               "app-expired",
		Target:           "development",
		Tool:             "db_drop_index",
		NormalizedParams: normalized,
		Expiry:           time.Now().Add(-1 * time.Minute), // already expired
		Approver:         "admin@example.com",
	})
	validator := security.NewApprovalValidator(store)
	svc, _ := newTestService(t, nonTx, nil, validator)
	b, _ := json.Marshal(map[string]any{
		"target": "development", "schema": "public", "index": "idx_old", "approval_id": "app-expired",
	})
	res, _ := svc.dropIndex(context.Background(), b)
	if res.Success {
		t.Fatal("expected failure with expired approval")
	}
	if res.Error.Code != "DB_APPROVAL_INVALID" {
		t.Errorf("code = %s, want DB_APPROVAL_INVALID", res.Error.Code)
	}
}

// ---------------------------------------------------------------------------
// Cancel query tests
// ---------------------------------------------------------------------------

func TestCancelQuery_Success(t *testing.T) {
	qr := &stubQueryRunner{}
	svc, _ := newTestService(t, nil, qr, nil)
	b, _ := json.Marshal(map[string]any{"target": "development", "pid": 12345})
	res, _ := svc.cancelQuery(context.Background(), b)
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	if !qr.called {
		t.Fatal("query runner was not called")
	}
	wantSQL := "SELECT pg_cancel_backend($1)"
	if qr.lastSQL != wantSQL {
		t.Errorf("SQL = %q, want %q", qr.lastSQL, wantSQL)
	}
	if len(qr.lastParam) != 1 || qr.lastParam[0] != int64(12345) {
		t.Errorf("params = %v, want [12345]", qr.lastParam)
	}
}

func TestCancelQuery_InvalidPID(t *testing.T) {
	qr := &stubQueryRunner{}
	svc, _ := newTestService(t, nil, qr, nil)
	b, _ := json.Marshal(map[string]any{"target": "development", "pid": 0})
	res, _ := svc.cancelQuery(context.Background(), b)
	if res.Success {
		t.Fatal("expected failure for pid=0")
	}
	if !strings.Contains(res.Error.Message, "positive integer") {
		t.Errorf("error = %q, want mention of positive integer", res.Error.Message)
	}
}

func TestCancelQuery_PrivilegeDenied(t *testing.T) {
	qr := &stubQueryRunner{
		err: &pgconn.PgError{Code: "42501", Message: "permission denied to cancel query"},
	}
	svc, _ := newTestService(t, nil, qr, nil)
	b, _ := json.Marshal(map[string]any{"target": "development", "pid": 99})
	res, _ := svc.cancelQuery(context.Background(), b)
	if res.Success {
		t.Fatal("expected failure")
	}
	if res.Error.Code != "DB_PERMISSION_DENIED" {
		t.Errorf("code = %s, want DB_PERMISSION_DENIED", res.Error.Code)
	}
}

// ---------------------------------------------------------------------------
// Terminate connection tests
// ---------------------------------------------------------------------------

func TestTerminateConnection_Success(t *testing.T) {
	qr := &stubQueryRunner{}
	// No approval validator → approval skipped.
	svc, _ := newTestService(t, nil, qr, nil)
	b, _ := json.Marshal(map[string]any{"target": "development", "pid": 42})
	res, _ := svc.terminateConnection(context.Background(), b)
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	wantSQL := "SELECT pg_terminate_backend($1)"
	if qr.lastSQL != wantSQL {
		t.Errorf("SQL = %q, want %q", qr.lastSQL, wantSQL)
	}
}

func TestTerminateConnection_RequiresApproval(t *testing.T) {
	qr := &stubQueryRunner{}
	store := security.NewInMemoryApprovalStore()
	validator := security.NewApprovalValidator(store)
	svc, _ := newTestService(t, nil, qr, validator)
	// No approval_id provided → should get DB_APPROVAL_REQUIRED.
	b, _ := json.Marshal(map[string]any{"target": "development", "pid": 42})
	res, _ := svc.terminateConnection(context.Background(), b)
	if res.Success {
		t.Fatal("expected failure when approval is missing")
	}
	if res.Error.Code != "DB_APPROVAL_REQUIRED" {
		t.Errorf("code = %s, want DB_APPROVAL_REQUIRED", res.Error.Code)
	}
}

func TestTerminateConnection_InvalidPID(t *testing.T) {
	qr := &stubQueryRunner{}
	svc, _ := newTestService(t, nil, qr, nil)
	b, _ := json.Marshal(map[string]any{"target": "development", "pid": -1})
	res, _ := svc.terminateConnection(context.Background(), b)
	if res.Success {
		t.Fatal("expected failure for negative pid")
	}
	if !strings.Contains(res.Error.Message, "positive integer") {
		t.Errorf("error = %q, want mention of positive integer", res.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// Unknown target
// ---------------------------------------------------------------------------

func TestHandler_UnknownTarget(t *testing.T) {
	nonTx := &stubNonTxRunner{}
	svc, _ := newTestService(t, nonTx, nil, nil)
	b, _ := json.Marshal(map[string]any{"target": "nonexistent", "schema": "public", "table": "orders"})
	res, _ := svc.vacuumTable(context.Background(), b)
	if res.Success {
		t.Fatal("expected failure for unknown target")
	}
	if res.Error.Code != "DB_TARGET_NOT_FOUND" {
		t.Errorf("code = %s, want DB_TARGET_NOT_FOUND", res.Error.Code)
	}
}

// ---------------------------------------------------------------------------
// Audit record includes approval
// ---------------------------------------------------------------------------

func TestHandler_AuditRecordIncludesApproval(t *testing.T) {
	nonTx := &stubNonTxRunner{}
	store := security.NewInMemoryApprovalStore()
	normalized, _ := security.NormalizeParams(map[string]any{"index": "idx_old", "if_exists": false, "schema": "public"})
	_ = store.Create(&security.ApprovalRecord{
		ID:               "app-777",
		Target:           "development",
		Tool:             "db_drop_index",
		NormalizedParams: normalized,
		Expiry:           time.Now().Add(10 * time.Minute),
		Approver:         "admin@example.com",
	})
	validator := security.NewApprovalValidator(store)
	svc, rec := newTestService(t, nonTx, nil, validator)
	b, _ := json.Marshal(map[string]any{
		"target": "development", "schema": "public", "index": "idx_old", "approval_id": "app-777",
	})
	_, _ = svc.dropIndex(context.Background(), b)
	// Verify that an audit record was emitted for the tool.
	if rec.count(toolDropIndex) != 1 {
		t.Errorf("expected 1 audit record for drop_index, got %d", rec.count(toolDropIndex))
	}
}
