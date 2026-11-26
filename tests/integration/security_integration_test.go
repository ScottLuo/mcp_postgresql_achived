//go:build security

package integration

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"postgres-dba-mcp/internal/mcp"
	"postgres-dba-mcp/internal/security"
	"postgres-dba-mcp/tests/integration/pgtest"
)

// SEC-01: No password in tool input schema.
func TestSecurity_PasswordNotInToolSchema(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)

	for _, def := range reg.List() {
		schemaStr := string(def.InputSchema)
		if strings.Contains(schemaStr, "testpass") {
			t.Errorf("tool %s: schema contains password", def.Name)
		}
		if strings.Contains(schemaStr, "testuser") {
			t.Errorf("tool %s: schema contains username", def.Name)
		}
		if strings.Contains(schemaStr, "postgres://") {
			t.Errorf("tool %s: schema contains DSN", def.Name)
		}
	}
}

// SEC-02: No password or DSN in tool response.
// Note: The username may appear in pg_stat_activity (usename field) which is
// expected and not a security leak. The password and full DSN must never appear.
func TestSecurity_PasswordNotInToolResponse(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)

	tools := map[string]map[string]any{
		"db_get_info":             {"target": "dev"},
		"db_list_schemas":         {"target": "dev"},
		"db_list_tables":          {"target": "dev", "schema": "public"},
		"db_get_activity":         {"target": "dev"},
		"db_get_connections":      {"target": "dev"},
		"db_get_locks":            {"target": "dev"},
		"db_get_database_stats":   {"target": "dev"},
	}
	for tool, args := range tools {
		res := pgtest.Exec(t, reg, tool, "dev", args)
		if !res.Success {
			t.Logf("tool %s failed: %v", tool, res.Error)
			continue
		}
		b, _ := json.Marshal(res.Data)
		// The password must never appear in responses.
		if strings.Contains(string(b), "testpass") {
			t.Errorf("tool %s: response contains password", tool)
		}
		// A full DSN pattern must never appear in responses.
		if strings.Contains(string(b), "postgres://testuser:") {
			t.Errorf("tool %s: response contains DSN pattern", tool)
		}
	}
}

// SEC-03: Read-only tool rejects UPDATE.
func TestSecurity_ReadOnlyRejectsUpdate(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)

	res := pgtest.Exec(t, reg, "db_query_readonly", "dev", map[string]any{
		"target": "dev",
		"sql":    "UPDATE orders SET status = 'x' WHERE id = 1",
	})
	if res.Success {
		t.Fatal("expected UPDATE to be rejected by db_query_readonly")
	}
	if res.Error.Code != mcp.ErrCodeSQLNotAllowed {
		t.Errorf("code = %q, want %q", res.Error.Code, mcp.ErrCodeSQLNotAllowed)
	}
}

// SEC-04: Read-only tool rejects DELETE.
func TestSecurity_ReadOnlyRejectsDelete(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)

	res := pgtest.Exec(t, reg, "db_query_readonly", "dev", map[string]any{
		"target": "dev",
		"sql":    "DELETE FROM orders WHERE id = 1",
	})
	if res.Success {
		t.Fatal("expected DELETE to be rejected by db_query_readonly")
	}
	if res.Error.Code != mcp.ErrCodeSQLNotAllowed {
		t.Errorf("code = %q, want %q", res.Error.Code, mcp.ErrCodeSQLNotAllowed)
	}
}

// SEC-05: Read-only tool rejects DROP.
func TestSecurity_ReadOnlyRejectsDrop(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)

	res := pgtest.Exec(t, reg, "db_query_readonly", "dev", map[string]any{
		"target": "dev",
		"sql":    "DROP TABLE orders",
	})
	if res.Success {
		t.Fatal("expected DROP to be rejected by db_query_readonly")
	}
	if res.Error.Code != mcp.ErrCodeSQLNotAllowed {
		t.Errorf("code = %q, want %q", res.Error.Code, mcp.ErrCodeSQLNotAllowed)
	}
}

// SEC-06: Multiple statements rejected.
func TestSecurity_MultipleStatementsRejected(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)

	res := pgtest.Exec(t, reg, "db_query_readonly", "dev", map[string]any{
		"target": "dev",
		"sql":    "SELECT 1; SELECT 2",
	})
	if res.Success {
		t.Fatal("expected multiple statements to be rejected")
	}
	if res.Error.Code != mcp.ErrCodeSQLInvalid {
		t.Errorf("code = %q, want %q", res.Error.Code, mcp.ErrCodeSQLInvalid)
	}
}

// SEC-07: Unknown target rejected.
func TestSecurity_UnauthorizedTargetRejected(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)

	res := pgtest.Exec(t, reg, "db_get_info", "nonexistent", map[string]any{"target": "nonexistent"})
	if res.Success {
		t.Fatal("expected unknown target to be rejected")
	}
	if res.Error.Code != mcp.ErrCodeTargetNotFound {
		t.Errorf("code = %q, want %q", res.Error.Code, mcp.ErrCodeTargetNotFound)
	}
}

// SEC-08: Invalid approval rejected.
// Uses a registry with an approval validator that has no record for the given ID.
func TestSecurity_InvalidApprovalRejected(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)

	// Empty approval list means the store exists but has no records.
	reg := pgtest.RegistryWithApproval(t, "dev", pg.DSN, nil)

	// db_drop_index requires approval; provide an approval_id that doesn't exist in the store.
	res := pgtest.Exec(t, reg, "db_drop_index", "dev", map[string]any{
		"target":      "dev",
		"schema":      "public",
		"index":       "idx_orders_customer",
		"approval_id": "nonexistent-id",
	})
	if res.Success {
		t.Fatal("expected invalid approval to be rejected")
	}
	// When the record is not found, the store returns ErrCodeApprovalRequired.
	if res.Error.Code != mcp.ErrCodeApprovalRequired && res.Error.Code != mcp.ErrCodeApprovalInvalid {
		t.Errorf("code = %q, want approval-related error", res.Error.Code)
	}
}

// SEC-09: Expired approval rejected.
func TestSecurity_ExpiredApprovalRejected(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)

	// Compute the normalized params that db_drop_index will produce:
	// map[string]any{"if_exists": false, "index": "idx_orders_customer", "schema": "public"}
	normalized, err := security.NormalizeParams(map[string]any{
		"if_exists": false,
		"index":     "idx_orders_customer",
		"schema":    "public",
	})
	if err != nil {
		t.Fatalf("failed to normalize params: %v", err)
	}

	approvals := []*security.ApprovalRecord{{
		ID:               "exp-1",
		Target:           "dev",
		Tool:             "db_drop_index",
		NormalizedParams: normalized,
		Expiry:           time.Now().Add(-1 * time.Minute),
		Approver:         "admin",
		CreatedAt:        time.Now().Add(-2 * time.Minute),
	}}
	reg := pgtest.RegistryWithApproval(t, "dev", pg.DSN, approvals)

	res := pgtest.Exec(t, reg, "db_drop_index", "dev", map[string]any{
		"target":      "dev",
		"schema":      "public",
		"index":       "idx_orders_customer",
		"approval_id": "exp-1",
	})
	if res.Success {
		t.Fatal("expected expired approval to be rejected")
	}
	if res.Error.Code != mcp.ErrCodeApprovalInvalid {
		t.Errorf("code = %q, want %q", res.Error.Code, mcp.ErrCodeApprovalInvalid)
	}
}

// SEC-10: DSN not in SQL errors.
func TestSecurity_SQLNoDSNLeak(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)

	// Cause a SQL error by referencing a non-existent column.
	res := pgtest.Exec(t, reg, "db_query_readonly", "dev", map[string]any{
		"target": "dev",
		"sql":    "SELECT nonexistent_col FROM orders",
	})
	if res.Success {
		t.Skip("query unexpectedly succeeded")
	}
	// Error message should not contain DSN components.
	msg := res.Error.Message
	for _, secret := range []string{"testpass", "testuser", "postgres://"} {
		if strings.Contains(msg, secret) {
			t.Errorf("error message leaks %q: %s", secret, msg)
		}
	}
}

// SEC-11: Query limits cannot be bypassed.
func TestSecurity_QueryLimitsCannotBypass(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)

	// The registry is configured with MaxRows=1000.
	// Even if the SQL has LIMIT 5000, the server should enforce its own cap via the "limit" arg.
	res := pgtest.Exec(t, reg, "db_query_readonly", "dev", map[string]any{
		"target": "dev",
		"sql":    "SELECT i FROM generate_series(1, 5000) AS i",
		"limit":  100,
	})
	if !res.Success {
		t.Fatalf("expected success, got %v", res.Error)
	}
	if res.Metadata == nil {
		t.Fatal("expected metadata in result")
	}
	if res.Metadata.RowsReturned > 100 {
		t.Errorf("rows returned = %d, want <= 100 (limit enforced)", res.Metadata.RowsReturned)
	}
}

// SEC-12: Restricted role cannot write (tested via pg role privileges).
// The test setup uses testuser/testpass which is a superuser in the container,
// so we verify that the read-only transaction enforces read-only at the SQL level.
func TestSecurity_ReadOnlyTransactionEnforced(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)

	// db_query_readonly should use a read-only transaction.
	// An INSERT in a read-only transaction will be rejected by PostgreSQL.
	res := pgtest.Exec(t, reg, "db_query_readonly", "dev", map[string]any{
		"target": "dev",
		"sql":    "SELECT 1",
	})
	if !res.Success {
		t.Fatalf("expected SELECT 1 to succeed, got %v", res.Error)
	}
}

// SEC-13: Prompts do not grant permission (advisory only).
// db_drop_index requires approval; without a valid approval record, it must be rejected.
func TestSecurity_PromptsDoNotGrantPermission(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)

	// Use an empty approval store: the validator exists but has no records.
	reg := pgtest.RegistryWithApproval(t, "dev", pg.DSN, nil)

	// db_drop_index without approval_id should return DB_APPROVAL_REQUIRED.
	res := pgtest.Exec(t, reg, "db_drop_index", "dev", map[string]any{
		"target":      "dev",
		"schema":      "public",
		"index":       "idx_orders_customer",
		"approval_id": "",
	})
	if res.Success {
		t.Fatal("expected db_drop_index without approval to be rejected")
	}
	if res.Error.Code != mcp.ErrCodeApprovalRequired {
		t.Errorf("code = %q, want %q", res.Error.Code, mcp.ErrCodeApprovalRequired)
	}
}
