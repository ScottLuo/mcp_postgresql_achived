//go:build unit

package discovery

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"postgres-dba-mcp/internal/database"
	"postgres-dba-mcp/internal/mcp"
)

// ---------------------------------------------------------------------------
// Argument validation (table-driven)
// ---------------------------------------------------------------------------

func TestParseArgs_MissingTarget(t *testing.T) {
	cases := []struct {
		name    string
		tool    string
		raw     json.RawMessage
		reqSchema bool
		reqTable  bool
	}{
		{"get_info", toolGetInfo, json.RawMessage(`{}`), false, false},
		{"list_databases", toolListDatabases, json.RawMessage(`{}`), false, false},
		{"list_schemas", toolListSchemas, json.RawMessage(`{}`), false, false},
		{"list_tables", toolListTables, json.RawMessage(`{"schema":"public"}`), true, false},
		{"describe_table", toolDescribeTable, json.RawMessage(`{"schema":"public","table":"orders"}`), true, true},
		{"list_indexes", toolListIndexes, json.RawMessage(`{"schema":"public","table":"orders"}`), true, true},
		{"list_constraints", toolListConstraints, json.RawMessage(`{"schema":"public","table":"orders"}`), true, true},
		{"table_size", toolGetTableSize, json.RawMessage(`{"schema":"public","table":"orders"}`), true, true},
		{"column_stats", toolGetColumnStats, json.RawMessage(`{"schema":"public","table":"orders"}`), true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newTestService(t, nil)
			a, err := svc.parseArgs(tc.raw, tc.reqSchema, tc.reqTable)
			if err == nil {
				t.Fatalf("expected error for missing target, got args=%+v", a)
			}
			if !containsErr(err, "target") {
				t.Errorf("error should mention 'target', got: %v", err)
			}
		})
	}
}

func TestParseArgs_MissingSchema(t *testing.T) {
	svc, _ := newTestService(t, nil)

	cases := []struct {
		name string
		raw  json.RawMessage
	}{
		{"list_tables", json.RawMessage(`{"target":"development"}`)},
		{"describe_table", json.RawMessage(`{"target":"development","table":"orders"}`)},
		{"list_indexes", json.RawMessage(`{"target":"development","table":"orders"}`)},
		{"list_constraints", json.RawMessage(`{"target":"development","table":"orders"}`)},
		{"table_size", json.RawMessage(`{"target":"development","table":"orders"}`)},
		{"column_stats", json.RawMessage(`{"target":"development","table":"orders"}`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := svc.parseArgs(tc.raw, true, true)
			if err == nil {
				t.Fatalf("expected error for missing schema, got args=%+v", a)
			}
			if !containsErr(err, "schema") {
				t.Errorf("error should mention 'schema', got: %v", err)
			}
		})
	}
}

func TestParseArgs_MissingTable(t *testing.T) {
	svc, _ := newTestService(t, nil)

	cases := []struct {
		name string
		raw  json.RawMessage
	}{
		{"describe_table", json.RawMessage(`{"target":"development","schema":"public"}`)},
		{"list_indexes", json.RawMessage(`{"target":"development","schema":"public"}`)},
		{"list_constraints", json.RawMessage(`{"target":"development","schema":"public"}`)},
		{"table_size", json.RawMessage(`{"target":"development","schema":"public"}`)},
		{"column_stats", json.RawMessage(`{"target":"development","schema":"public"}`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := svc.parseArgs(tc.raw, true, true)
			if err == nil {
				t.Fatalf("expected error for missing table, got args=%+v", a)
			}
			if !containsErr(err, "table") {
				t.Errorf("error should mention 'table', got: %v", err)
			}
		})
	}
}

func TestParseArgs_InvalidSchema(t *testing.T) {
	svc, _ := newTestService(t, nil)

	cases := []struct {
		name   string
		schema string
	}{
		{"semicolon", "public;DROP TABLE orders"},
		{"newline", "public\n"},
		{"carriage_return", "public\r"},
		{"too_long", string(make([]byte, 64))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(`{"target":"development","schema":"` + tc.schema + `","table":"orders"}`)
			_, err := svc.parseArgs(raw, true, true)
			if err == nil {
				t.Fatalf("expected error for invalid schema %q", tc.schema)
			}
		})
	}
}

func TestParseArgs_InvalidTable(t *testing.T) {
	svc, _ := newTestService(t, nil)

	cases := []struct {
		name  string
		table string
	}{
		{"semicolon", "orders;SELECT 1"},
		{"newline", "orders\n"},
		{"carriage_return", "orders\r"},
		{"too_long", string(make([]byte, 64))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(`{"target":"development","schema":"public","table":"` + tc.table + `"}`)
			_, err := svc.parseArgs(raw, true, true)
			if err == nil {
				t.Fatalf("expected error for invalid table %q", tc.table)
			}
		})
	}
}

func TestParseArgs_Valid(t *testing.T) {
	svc, _ := newTestService(t, nil)

	// target-only tools.
	a, err := svc.parseArgs(json.RawMessage(`{"target":"development"}`), false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Target != "development" {
		t.Errorf("target = %q, want development", a.Target)
	}

	// target+schema tools.
	a, err = svc.parseArgs(json.RawMessage(`{"target":"development","schema":"public"}`), true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Schema != "public" {
		t.Errorf("schema = %q, want public", a.Schema)
	}

	// target+schema+table tools.
	a, err = svc.parseArgs(json.RawMessage(`{"target":"development","schema":"public","table":"orders"}`), true, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Table != "orders" {
		t.Errorf("table = %q, want orders", a.Table)
	}
}

// ---------------------------------------------------------------------------
// Handler behavior: success paths
// ---------------------------------------------------------------------------

func TestGetDBInfo_Success(t *testing.T) {
	meta := &stubMeta{info: &database.ServerInfo{Version: "PostgreSQL 16.0", Database: "testdb"}}
	svc, rec := newTestService(t, meta)

	result, err := svc.getDBInfo(context.Background(), json.RawMessage(`{"target":"development"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %+v", result.Error)
	}
	info, ok := result.Data.(*database.ServerInfo)
	if !ok {
		t.Fatalf("expected *ServerInfo, got %T", result.Data)
	}
	if info.Version != "PostgreSQL 16.0" {
		t.Errorf("version = %q, want PostgreSQL 16.0", info.Version)
	}
	if rec.count(toolGetInfo) != 1 {
		t.Errorf("expected 1 audit record, got %d", rec.count(toolGetInfo))
	}
}

func TestListDatabases_Success(t *testing.T) {
	meta := &stubMeta{dbs: []database.DatabaseInfo{{Name: "postgres"}, {Name: "testdb"}}}
	svc, rec := newTestService(t, meta)

	result, err := svc.listDatabases(context.Background(), json.RawMessage(`{"target":"development"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %+v", result.Error)
	}
	if result.Metadata == nil || result.Metadata.RowsReturned != 2 {
		t.Errorf("unexpected metadata: %+v", result.Metadata)
	}
	if rec.count(toolListDatabases) != 1 {
		t.Errorf("expected 1 audit record, got %d", rec.count(toolListDatabases))
	}
}

func TestListSchemas_Success(t *testing.T) {
	meta := &stubMeta{schemas: []database.SchemaInfo{{Name: "public"}, {Name: "pg_catalog"}}}
	svc, rec := newTestService(t, meta)

	result, err := svc.listSchemas(context.Background(), json.RawMessage(`{"target":"development"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %+v", result.Error)
	}
	if result.Metadata == nil || result.Metadata.RowsReturned != 2 {
		t.Errorf("unexpected metadata: %+v", result.Metadata)
	}
	if rec.count(toolListSchemas) != 1 {
		t.Errorf("expected 1 audit record, got %d", rec.count(toolListSchemas))
	}
}

func TestListTables_Success(t *testing.T) {
	meta := &stubMeta{tables: []database.TableInfo{{Schema: "public", Name: "orders"}}}
	svc, rec := newTestService(t, meta)

	result, err := svc.listTables(context.Background(), json.RawMessage(`{"target":"development","schema":"public"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %+v", result.Error)
	}
	if rec.count(toolListTables) != 1 {
		t.Errorf("expected 1 audit record, got %d", rec.count(toolListTables))
	}
}

func TestDescribeTable_Success(t *testing.T) {
	meta := &stubMeta{
		columns:     []database.ColumnInfo{{Name: "id", Type: "bigint", IsPrimaryKey: true}},
		indexes:     []database.IndexInfo{{Name: "idx_id"}},
		constraints: []database.ConstraintInfo{{Name: "pk", Type: "PRIMARY KEY"}},
	}
	svc, rec := newTestService(t, meta)

	result, err := svc.describeTable(context.Background(), json.RawMessage(`{"target":"development","schema":"public","table":"orders"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %+v", result.Error)
	}

	// Verify the spec §7.1 shape.
	data, ok := result.Data.(map[string]any)
	if !ok {
		// The anonymous struct marshals as a map when we JSON-encode it,
		// but as a Go struct when returned directly. Check via type assertion
		// to the anonymous struct type by encoding to JSON and back.
		encoded, jsonErr := json.Marshal(result.Data)
		if jsonErr != nil {
			t.Fatalf("failed to marshal data: %v", jsonErr)
		}
		var m map[string]any
		if jsonErr := json.Unmarshal(encoded, &m); jsonErr != nil {
			t.Fatalf("failed to unmarshal: %v", jsonErr)
		}
		data = m
	}

	// Verify schema and table are present.
	if data["schema"] != "public" {
		t.Errorf("schema = %v, want public", data["schema"])
	}
	if data["table"] != "orders" {
		t.Errorf("table = %v, want orders", data["table"])
	}

	// Verify columns are present.
	cols, ok := data["columns"].([]any)
	if !ok {
		t.Fatalf("expected columns array, got %T", data["columns"])
	}
	if len(cols) != 1 {
		t.Fatalf("expected 1 column, got %d", len(cols))
	}

	if rec.count(toolDescribeTable) != 1 {
		t.Errorf("expected 1 audit record, got %d", rec.count(toolDescribeTable))
	}
}

func TestListIndexes_Success(t *testing.T) {
	meta := &stubMeta{indexes: []database.IndexInfo{{Name: "idx_orders_id"}}}
	svc, rec := newTestService(t, meta)

	result, err := svc.listIndexes(context.Background(), json.RawMessage(`{"target":"development","schema":"public","table":"orders"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %+v", result.Error)
	}
	if rec.count(toolListIndexes) != 1 {
		t.Errorf("expected 1 audit record, got %d", rec.count(toolListIndexes))
	}
}

func TestListConstraints_Success(t *testing.T) {
	meta := &stubMeta{constraints: []database.ConstraintInfo{{Name: "orders_pk", Type: "PRIMARY KEY"}}}
	svc, rec := newTestService(t, meta)

	result, err := svc.listConstraints(context.Background(), json.RawMessage(`{"target":"development","schema":"public","table":"orders"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %+v", result.Error)
	}
	if rec.count(toolListConstraints) != 1 {
		t.Errorf("expected 1 audit record, got %d", rec.count(toolListConstraints))
	}
}

func TestGetTableSize_Success(t *testing.T) {
	meta := &stubMeta{size: &database.TableSizeInfo{Schema: "public", Table: "orders", TotalSize: "64 kB"}}
	svc, rec := newTestService(t, meta)

	result, err := svc.getTableSize(context.Background(), json.RawMessage(`{"target":"development","schema":"public","table":"orders"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %+v", result.Error)
	}
	if rec.count(toolGetTableSize) != 1 {
		t.Errorf("expected 1 audit record, got %d", rec.count(toolGetTableSize))
	}
}

func TestGetColumnStats_Success(t *testing.T) {
	meta := &stubMeta{stats: []database.ColumnStats{{Column: "id", NumDistinct: 42}}}
	svc, rec := newTestService(t, meta)

	result, err := svc.getColumnStats(context.Background(), json.RawMessage(`{"target":"development","schema":"public","table":"orders"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %+v", result.Error)
	}
	if rec.count(toolGetColumnStats) != 1 {
		t.Errorf("expected 1 audit record, got %d", rec.count(toolGetColumnStats))
	}
}

// ---------------------------------------------------------------------------
// Handler behavior: error mapping
// ---------------------------------------------------------------------------

func TestHandler_UnknownTarget(t *testing.T) {
	meta := &stubMeta{infoErr: database.NewTargetNotFound("nope")}
	svc, _ := newTestService(t, meta)

	result, err := svc.getDBInfo(context.Background(), json.RawMessage(`{"target":"nope"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure")
	}
	if result.Error.Code != mcp.ErrCodeTargetNotFound {
		t.Errorf("code = %q, want %q", result.Error.Code, mcp.ErrCodeTargetNotFound)
	}
}

func TestHandler_FeatureUnavailable(t *testing.T) {
	meta := &stubMeta{statsErr: database.ErrFeatureUnavailable}
	svc, _ := newTestService(t, meta)

	result, err := svc.getColumnStats(context.Background(), json.RawMessage(`{"target":"development","schema":"public","table":"orders"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure")
	}
	if result.Error.Code != mcp.ErrCodeFeatureUnavailable {
		t.Errorf("code = %q, want %q", result.Error.Code, mcp.ErrCodeFeatureUnavailable)
	}
}

func TestHandler_QueryTimeoutMapping(t *testing.T) {
	meta := &stubMeta{schemasErr: database.ErrQueryTimeout}
	svc, _ := newTestService(t, meta)

	result, err := svc.listSchemas(context.Background(), json.RawMessage(`{"target":"development"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error.Code != mcp.ErrCodeQueryTimeout {
		t.Errorf("code = %q, want %q", result.Error.Code, mcp.ErrCodeQueryTimeout)
	}
}

func TestHandler_GenericDBErrorMapping(t *testing.T) {
	meta := &stubMeta{tablesErr: context.DeadlineExceeded}
	svc, _ := newTestService(t, meta)

	result, err := svc.listTables(context.Background(), json.RawMessage(`{"target":"development","schema":"public"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error.Code != mcp.ErrCodeExecutionFailed {
		t.Errorf("code = %q, want %q", result.Error.Code, mcp.ErrCodeExecutionFailed)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// containsErr reports whether err's message contains the substring s.
func containsErr(err error, s string) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), s)
}
