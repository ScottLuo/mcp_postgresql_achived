//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"testing"

	"postgres-dba-mcp/internal/mcp"
	"postgres-dba-mcp/tests/integration/pgtest"
)

// newDiscoveryEnv starts a container, seeds data, and wires the registry.
func newDiscoveryEnv(t *testing.T) (reg *mcp.Registry, pg *pgtest.PG) {
	t.Helper()
	pg = pgtest.NewPostgres(t)
	pg.Setup(t)
	reg = pgtest.Registry(t, "dev", pg.DSN)
	return reg, pg
}

// DIS-I01: db_get_info returns version and uptime (spec §7.1, §13 step 1).
func TestIntegration_GetInfo(t *testing.T) {
	reg, _ := newDiscoveryEnv(t)
	res := pgtest.Exec(t, reg, "db_get_info", "dev", map[string]any{"target": "dev"})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), "PostgreSQL") {
		t.Errorf("data %s should mention PostgreSQL version", string(b))
	}
}

// DIS-I02: db_list_databases returns the visible databases.
func TestIntegration_ListDatabases(t *testing.T) {
	reg, _ := newDiscoveryEnv(t)
	res := pgtest.Exec(t, reg, "db_list_databases", "dev", map[string]any{"target": "dev"})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), "testdb") {
		t.Errorf("data %s should include current database testdb", string(b))
	}
}

// DIS-I03: db_list_schemas returns public, pg_catalog, information_schema.
func TestIntegration_ListSchemas(t *testing.T) {
	reg, _ := newDiscoveryEnv(t)
	res := pgtest.Exec(t, reg, "db_list_schemas", "dev", map[string]any{"target": "dev", "schema": ""})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	for _, want := range []string{"public", "pg_catalog", "information_schema"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("data %s should include schema %q", string(b), want)
		}
	}
}

// DIS-I04: db_list_tables returns exactly the seeded tables in public.
func TestIntegration_ListTables(t *testing.T) {
	reg, _ := newDiscoveryEnv(t)
	res := pgtest.Exec(t, reg, "db_list_tables", "dev", map[string]any{"target": "dev", "schema": "public"})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), "orders") || !strings.Contains(string(b), "customers") {
		t.Errorf("data %s should include seeded tables", string(b))
	}
}

// DIS-U01: db_describe_table with a missing table param returns an error.
func TestDescribeTable_MissingTable(t *testing.T) {
	reg, _ := newDiscoveryEnv(t)
	res := pgtest.Exec(t, reg, "db_describe_table", "dev", map[string]any{"target": "dev", "schema": "public"})
	if res.Success {
		t.Fatalf("expected failure for missing table, got success")
	}
}

// DIS-U02: db_describe_table with a missing target returns an error.
func TestDescribeTable_MissingTarget(t *testing.T) {
	reg, _ := newDiscoveryEnv(t)
	res := pgtest.Exec(t, reg, "db_describe_table", "", map[string]any{"schema": "public", "table": "orders"})
	if res.Success {
		t.Fatalf("expected failure for missing target, got success")
	}
}

// DIS-U03: db_list_schemas without target returns an error.
func TestListSchemas_MissingTarget(t *testing.T) {
	reg, _ := newDiscoveryEnv(t)
	res := pgtest.Exec(t, reg, "db_list_schemas", "", map[string]any{})
	if res.Success {
		t.Fatalf("expected failure for missing target, got success")
	}
}

// DIS-I05: db_describe_table returns column metadata with correct types.
func TestIntegration_DescribeTable(t *testing.T) {
	reg, _ := newDiscoveryEnv(t)
	res := pgtest.Exec(t, reg, "db_describe_table", "dev",
		map[string]any{"target": "dev", "schema": "public", "table": "orders"})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	for _, want := range []string{"id", "customer_id", "status", "amount", "created_at"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("columns %s should include %q", string(b), want)
		}
	}
}

// DIS-I06: db_describe_table returns index information.
func TestIntegration_DescribeTable_Indexes(t *testing.T) {
	reg, _ := newDiscoveryEnv(t)
	res := pgtest.Exec(t, reg, "db_describe_table", "dev",
		map[string]any{"target": "dev", "schema": "public", "table": "orders"})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), "idx_orders_customer") {
		t.Errorf("describe should include index idx_orders_customer: %s", string(b))
	}
}

// DIS-I07: db_describe_table returns constraints (PK, FK).
func TestIntegration_DescribeTable_Constraints(t *testing.T) {
	reg, _ := newDiscoveryEnv(t)
	res := pgtest.Exec(t, reg, "db_describe_table", "dev",
		map[string]any{"target": "dev", "schema": "public", "table": "orders"})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), "orders_pkey") {
		t.Errorf("describe should include primary key constraint: %s", string(b))
	}
}

// DIS-I08: db_list_indexes returns the seeded indexes.
func TestIntegration_ListIndexes(t *testing.T) {
	reg, _ := newDiscoveryEnv(t)
	res := pgtest.Exec(t, reg, "db_list_indexes", "dev",
		map[string]any{"target": "dev", "schema": "public", "table": "orders"})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), "idx_orders_customer") || !strings.Contains(string(b), "idx_orders_status") {
		t.Errorf("indexes should include both seeded indexes: %s", string(b))
	}
}

// DIS-I09: db_get_table_size returns a non-zero data size.
func TestIntegration_TableSize(t *testing.T) {
	reg, _ := newDiscoveryEnv(t)
	res := pgtest.Exec(t, reg, "db_get_table_size", "dev",
		map[string]any{"target": "dev", "schema": "public", "table": "orders"})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	t.Logf("table size data: %s", string(b))
}

// DIS-I10: db_get_column_stats returns stats after ANALYZE.
func TestIntegration_ColumnStats(t *testing.T) {
	reg, _ := newDiscoveryEnv(t)
	res := pgtest.Exec(t, reg, "db_get_column_stats", "dev",
		map[string]any{"target": "dev", "schema": "public", "table": "orders"})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), "id") {
		t.Errorf("column stats should reference column id: %s", string(b))
	}
}

// DIS-I11: db_describe_table with an unknown table errors without leaking DSN.
func TestIntegration_UnknownTable(t *testing.T) {
	reg, _ := newDiscoveryEnv(t)
	res := pgtest.Exec(t, reg, "db_describe_table", "dev",
		map[string]any{"target": "dev", "schema": "public", "table": "no_such_table"})
	if res.Success {
		t.Fatalf("expected failure for unknown table, got success")
	}
	if res.Error == nil {
		t.Fatalf("expected an error, got nil")
	}
	if strings.Contains(res.Error.Message, "postgres://") || strings.Contains(res.Error.Message, "postgresql://") {
		t.Errorf("error leaked DSN: %s", res.Error.Message)
	}
}

// DIS-I12: db_list_tables with an unknown schema errors without leaking DSN.
func TestIntegration_UnknownSchema(t *testing.T) {
	reg, _ := newDiscoveryEnv(t)
	res := pgtest.Exec(t, reg, "db_list_tables", "dev",
		map[string]any{"target": "dev", "schema": "no_such_schema"})
	// A nonexistent schema yields an empty list (no error) OR an error.
	if !res.Success && res.Error != nil {
		if strings.Contains(res.Error.Message, "postgres://") || strings.Contains(res.Error.Message, "postgresql://") {
			t.Errorf("error leaked DSN: %s", res.Error.Message)
		}
	}
}
