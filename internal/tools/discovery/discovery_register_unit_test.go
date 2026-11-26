//go:build unit

package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"postgres-dba-mcp/internal/database"
	"postgres-dba-mcp/internal/mcp"
)

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

func TestRegister_AllReadOnly(t *testing.T) {
	svc, _ := newTestService(t, nil)
	reg := mcp.NewRegistry(slogDiscardLogger(), nil, nil)
	if err := Register(reg, svc); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	if n := len(reg.List()); n != ToolCount() {
		t.Fatalf("registered %d tools, want %d", n, ToolCount())
	}
	if ToolCount() != 9 {
		t.Fatalf("ToolCount() = %d, want 9", ToolCount())
	}

	for _, def := range reg.List() {
		if def.Permission != mcp.PermissionReadOnly {
			t.Errorf("tool %s has permission %q, want READ_ONLY", def.Name, def.Permission)
		}
		if def.Handler == nil {
			t.Errorf("tool %s has nil handler", def.Name)
		}
		// Input schema must be valid JSON with a "target" requirement.
		var schema map[string]any
		if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
			t.Errorf("tool %s: invalid input schema JSON: %v", def.Name, err)
			continue
		}
		req, _ := schema["required"].([]any)
		found := false
		for _, r := range req {
			if r == "target" {
				found = true
			}
		}
		if !found {
			t.Errorf("tool %s: schema does not require target", def.Name)
		}
	}

	// Every expected tool name must be present.
	want := []string{
		"db_get_info", "db_list_databases", "db_list_schemas",
		"db_list_tables", "db_describe_table", "db_list_indexes",
		"db_list_constraints", "db_get_table_size", "db_get_column_stats",
	}
	seen := make(map[string]bool, len(want))
	for _, def := range reg.List() {
		seen[def.Name] = true
	}
	for _, name := range want {
		if !seen[name] {
			t.Errorf("tool %s not registered", name)
		}
	}
}

func TestRegister_ValidSchemas(t *testing.T) {
	svc, _ := newTestService(t, nil)
	reg := mcp.NewRegistry(slogDiscardLogger(), nil, nil)
	if err := Register(reg, svc); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	// Verify that tools requiring schema also list it in "required".
	schemaTools := map[string]bool{
		toolListTables:      true,
		toolDescribeTable:   true,
		toolListIndexes:     true,
		toolListConstraints: true,
		toolGetTableSize:    true,
		toolGetColumnStats:  true,
	}
	for name := range schemaTools {
		def, ok := reg.Get(name)
		if !ok {
			t.Fatalf("tool %s not found in registry", name)
		}
		var schema map[string]any
		if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
			t.Fatalf("tool %s: invalid schema JSON: %v", name, err)
		}
		req, _ := schema["required"].([]any)
		hasSchema := false
		for _, r := range req {
			if r == "schema" {
				hasSchema = true
			}
		}
		if !hasSchema {
			t.Errorf("tool %s: schema does not require 'schema'", name)
		}
	}

	// Verify that tools requiring table also list it in "required".
	tableTools := map[string]bool{
		toolDescribeTable:   true,
		toolListIndexes:     true,
		toolListConstraints: true,
		toolGetTableSize:    true,
		toolGetColumnStats:  true,
	}
	for name := range tableTools {
		def, ok := reg.Get(name)
		if !ok {
			t.Fatalf("tool %s not found in registry", name)
		}
		var schema map[string]any
		if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
			t.Fatalf("tool %s: invalid schema JSON: %v", name, err)
		}
		req, _ := schema["required"].([]any)
		hasTable := false
		for _, r := range req {
			if r == "table" {
				hasTable = true
			}
		}
		if !hasTable {
			t.Errorf("tool %s: schema does not require 'table'", name)
		}
	}
}

func TestToolCount(t *testing.T) {
	if n := ToolCount(); n != 9 {
		t.Errorf("ToolCount() = %d, want 9", n)
	}
}

// ---------------------------------------------------------------------------
// realMetaExecutor: error propagation through pool resolution
// ---------------------------------------------------------------------------

// stubManager is a database.DatabaseManager stub that returns an error from
// GetPool, exercising the error branch in each realMetaExecutor method.
type stubManager struct {
	getErr error
}

func (m *stubManager) GetPool(target string) (*pgxpool.Pool, error) {
	return nil, m.getErr
}

func (m *stubManager) GetTarget(name string) (*database.DatabaseTarget, error) {
	return nil, m.getErr
}

func (m *stubManager) ListTargets() []string { return nil }

func (m *stubManager) HealthCheck(ctx context.Context) error {
	return m.getErr
}

func (m *stubManager) Close() {}

func TestRealMetaExecutor_PoolResolutionError(t *testing.T) {
	ctx := context.Background()
	err := errors.New("pool down")
	mgr := &stubManager{getErr: err}
	exec := &realMetaExecutor{manager: mgr}

	cases := []struct {
		name string
		call func() error
	}{
		{"GetInfo", func() error { _, e := exec.GetInfo(ctx, "t"); return e }},
		{"ListDatabases", func() error { _, e := exec.ListDatabases(ctx, "t"); return e }},
		{"ListSchemas", func() error { _, e := exec.ListSchemas(ctx, "t"); return e }},
		{"ListTables", func() error { _, e := exec.ListTables(ctx, "t", "s"); return e }},
		{"DescribeTable", func() error { _, e := exec.DescribeTable(ctx, "t", "s", "tb"); return e }},
		{"ListIndexes", func() error { _, e := exec.ListIndexes(ctx, "t", "s", "tb"); return e }},
		{"ListConstraints", func() error { _, e := exec.ListConstraints(ctx, "t", "s", "tb"); return e }},
		{"GetTableSize", func() error { _, e := exec.GetTableSize(ctx, "t", "s", "tb"); return e }},
		{"GetColumnStats", func() error { _, e := exec.GetColumnStats(ctx, "t", "s", "tb"); return e }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.call()
			if got != err {
				t.Errorf("expected %v, got %v", err, got)
			}
		})
	}
}
