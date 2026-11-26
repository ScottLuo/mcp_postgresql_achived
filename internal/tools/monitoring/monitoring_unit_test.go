//go:build unit

package monitoring

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"postgres-dba-mcp/internal/database"
	"postgres-dba-mcp/internal/mcp"
)

// ---------------------------------------------------------------------------
// Argument validation
// ---------------------------------------------------------------------------

func TestParseArgs(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantErr   bool
		wantLimit int
		wantTgt   string
	}{
		{"missing target", `{}`, true, 0, ""},
		{"empty json", ``, true, 0, ""},
		{"invalid json", `{"target":`, true, 0, ""},
		{"valid target only", `{"target":"development"}`, false, 100, "development"},
		{"default limit applied", `{"target":"development","limit":0}`, false, 100, "development"},
		{"negative limit applied", `{"target":"development","limit":-5}`, false, 100, "development"},
		{"limit clamped to cap", `{"target":"development","limit":500}`, false, 100, "development"},
		{"limit within cap", `{"target":"development","limit":50}`, false, 50, "development"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newTestService(t, nil, true)
			var raw json.RawMessage
			if tc.raw != "" {
				raw = json.RawMessage(tc.raw)
			}
			a, err := svc.parseArgs(raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if a.Limit != tc.wantLimit {
				t.Errorf("limit = %d, want %d", a.Limit, tc.wantLimit)
			}
			if a.Target != tc.wantTgt {
				t.Errorf("target = %q, want %q", a.Target, tc.wantTgt)
			}
		})
	}
}

func TestValidateSchemaTable(t *testing.T) {
	cases := []struct {
		name    string
		schema  string
		table   string
		wantErr bool
	}{
		{"both set", "public", "orders", false},
		{"missing schema", "", "orders", true},
		{"missing table", "public", "", true},
		{"both missing", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSchemaTable(&baseArgs{Schema: tc.schema, Table: tc.table})
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Handler behavior: success, default limit, truncation, error mapping
// ---------------------------------------------------------------------------

func TestGetActivity_DefaultLimitAndSuccess(t *testing.T) {
	runner := &stubRunner{res: &database.ExecResult{
		Columns:      []string{"pid", "usename", "state", "query"},
		Rows:         [][]any{{int32(1), "dba", "active", "SELECT 1"}},
		RowsReturned: 1,
		Truncated:    false,
		Duration:     2 * time.Millisecond,
	}}
	svc, rec := newTestService(t, runner, true)

	result, err := svc.getActivity(context.Background(), json.RawMessage(`{"target":"development"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %+v", result.Error)
	}
	// Default limit (cap=100) must be enforced into the SQL limit parameter.
	if runner.lastLimit != 100 {
		t.Errorf("limit passed to query = %d, want default cap 100", runner.lastLimit)
	}
	if len(resultRows(result)) != 1 {
		t.Errorf("expected 1 row, got %d", len(resultRows(result)))
	}
	if result.Metadata == nil || result.Metadata.RowsReturned != 1 {
		t.Errorf("unexpected metadata: %+v", result.Metadata)
	}
	if rec.count(toolGetActivity) != 1 {
		t.Errorf("expected 1 audit record, got %d", rec.count(toolGetActivity))
	}
}

func TestGetActivity_Truncation(t *testing.T) {
	runner := &stubRunner{res: &database.ExecResult{
		Columns:      []string{"pid"},
		Rows:         [][]any{{int32(1)}, {int32(2)}},
		RowsReturned: 2,
		Truncated:    true,
		Duration:     time.Millisecond,
	}}
	svc, _ := newTestService(t, runner, true)

	result, err := svc.getActivity(context.Background(), json.RawMessage(`{"target":"development"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Metadata == nil || !result.Metadata.Truncated {
		t.Errorf("expected truncated=true, got %+v", result.Metadata)
	}
}

func TestHandler_MissingTarget(t *testing.T) {
	svc, _ := newTestService(t, &stubRunner{}, true)
	result, err := svc.getActivity(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure")
	}
	if result.Error.Code != mcp.ErrCodeSQLInvalid {
		t.Errorf("code = %q, want %q", result.Error.Code, mcp.ErrCodeSQLInvalid)
	}
}

func TestHandler_UnknownTarget(t *testing.T) {
	svc, _ := newTestService(t, &stubRunner{}, true)
	result, err := svc.getLocks(context.Background(), json.RawMessage(`{"target":"nope"}`))
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

func TestHandler_QueryTimeoutMapping(t *testing.T) {
	svc, _ := newTestService(t, &stubRunner{err: database.ErrQueryTimeout}, true)
	result, err := svc.getDatabaseStats(context.Background(), json.RawMessage(`{"target":"development"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error.Code != mcp.ErrCodeQueryTimeout {
		t.Errorf("code = %q, want %q", result.Error.Code, mcp.ErrCodeQueryTimeout)
	}
}

func TestHandler_ResultTooLargeMapping(t *testing.T) {
	svc, _ := newTestService(t, &stubRunner{err: database.ErrResultTooLarge}, true)
	result, err := svc.getConnections(context.Background(), json.RawMessage(`{"target":"development"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error.Code != mcp.ErrCodeResultTooLarge {
		t.Errorf("code = %q, want %q", result.Error.Code, mcp.ErrCodeResultTooLarge)
	}
}

func TestHandler_GenericDBErrorMapping(t *testing.T) {
	// A non-sentinel error must map to DB_EXECUTION_FAILED.
	runner := &stubRunner{err: context.DeadlineExceeded}
	svc, _ := newTestService(t, runner, true)
	result, err := svc.getDeadlocks(context.Background(), json.RawMessage(`{"target":"development"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error.Code != mcp.ErrCodeExecutionFailed {
		t.Errorf("code = %q, want %q", result.Error.Code, mcp.ErrCodeExecutionFailed)
	}
}

// ---------------------------------------------------------------------------
// Feature-unavailable mapping
// ---------------------------------------------------------------------------

func TestGetSlowQueries_FeatureUnavailable(t *testing.T) {
	runner := &stubRunner{}
	svc, _ := newTestService(t, runner, false) // extension not installed
	result, err := svc.getSlowQueries(context.Background(), json.RawMessage(`{"target":"development"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure")
	}
	if result.Error.Code != mcp.ErrCodeFeatureUnavailable {
		t.Errorf("code = %q, want %q", result.Error.Code, mcp.ErrCodeFeatureUnavailable)
	}
	// The query must NOT have been run when the feature is unavailable.
	if runner.lastSQL != "" {
		t.Errorf("expected no query execution, got SQL %q", runner.lastSQL)
	}
}

func TestGetSlowQueries_FeatureAvailable(t *testing.T) {
	runner := &stubRunner{res: &database.ExecResult{
		Columns:      []string{"total_time_ms", "query"},
		Rows:         [][]any{{float64(12.5), "SELECT * FROM big"}},
		RowsReturned: 1,
		Duration:     time.Millisecond,
	}}
	svc, _ := newTestService(t, runner, true)
	result, err := svc.getSlowQueries(context.Background(), json.RawMessage(`{"target":"development"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %+v", result.Error)
	}
	if !strings.Contains(runner.lastSQL, "pg_stat_statements") {
		t.Errorf("expected pg_stat_statements query, got %q", runner.lastSQL)
	}
}

func TestReplicationStatus_NotReplication(t *testing.T) {
	// Primary with no connected standbys: role query returns in_recovery=false,
	// status query returns no rows -> feature unavailable.
	svc, _ := newTestService(t, nil, true)
	svc.runner = &sequentialRunner{results: []sequenceStep{
		{res: &database.ExecResult{Columns: []string{"in_recovery"}, Rows: [][]any{{false}}, RowsReturned: 1}},
		{res: &database.ExecResult{Columns: []string{"application_name"}, Rows: [][]any{}, RowsReturned: 0}},
	}}
	result, err := svc.getReplicationStatus(context.Background(), json.RawMessage(`{"target":"development"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure (feature unavailable)")
	}
	if result.Error.Code != mcp.ErrCodeFeatureUnavailable {
		t.Errorf("code = %q, want %q", result.Error.Code, mcp.ErrCodeFeatureUnavailable)
	}
}

func TestReplicationStatus_PrimaryWithStandby(t *testing.T) {
	svc, _ := newTestService(t, nil, true)
	svc.runner = &sequentialRunner{results: []sequenceStep{
		{res: &database.ExecResult{Columns: []string{"in_recovery"}, Rows: [][]any{{false}}, RowsReturned: 1}},
		{res: &database.ExecResult{
			Columns:      []string{"application_name", "state", "replay_lsn"},
			Rows:         [][]any{{"repl1", "streaming", "0/12345678"}},
			RowsReturned: 1,
		}},
	}}
	result, err := svc.getReplicationStatus(context.Background(), json.RawMessage(`{"target":"development"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %+v", result.Error)
	}
	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map data, got %T", result.Data)
	}
	if data["role"] != "primary" {
		t.Errorf("role = %v, want primary", data["role"])
	}
}

func TestReplicationLag_NotReplication(t *testing.T) {
	svc, _ := newTestService(t, nil, true)
	svc.runner = &sequentialRunner{results: []sequenceStep{
		{res: &database.ExecResult{Columns: []string{"in_recovery"}, Rows: [][]any{{false}}, RowsReturned: 1}},
		{res: &database.ExecResult{Columns: []string{"application_name"}, Rows: [][]any{}, RowsReturned: 0}},
	}}
	result, err := svc.getReplicationLag(context.Background(), json.RawMessage(`{"target":"development"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure (feature unavailable)")
	}
	if result.Error.Code != mcp.ErrCodeFeatureUnavailable {
		t.Errorf("code = %q, want %q", result.Error.Code, mcp.ErrCodeFeatureUnavailable)
	}
}

func TestReplicationLag_PrimaryWithStandby(t *testing.T) {
	svc, _ := newTestService(t, nil, true)
	svc.runner = &sequentialRunner{results: []sequenceStep{
		{res: &database.ExecResult{Columns: []string{"in_recovery"}, Rows: [][]any{{false}}, RowsReturned: 1}},
		{res: &database.ExecResult{
			Columns:      []string{"application_name", "state", "replay_lsn", "lag_lsn"},
			Rows:         [][]any{{"repl1", "streaming", "0/12345678", "12"}},
			RowsReturned: 1,
		}},
	}}
	result, err := svc.getReplicationLag(context.Background(), json.RawMessage(`{"target":"development"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %+v", result.Error)
	}
	if len(resultRows(result)) != 1 {
		t.Errorf("expected 1 row, got %d", len(resultRows(result)))
	}
}

// ---------------------------------------------------------------------------
// Table/index stats require schema+table
// ---------------------------------------------------------------------------

func TestTableStats_MissingSchema(t *testing.T) {
	svc, _ := newTestService(t, &stubRunner{}, true)
	result, err := svc.getTableStats(context.Background(), json.RawMessage(`{"target":"development","table":"orders"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure")
	}
	if result.Error.Code != mcp.ErrCodeSQLInvalid {
		t.Errorf("code = %q, want %q", result.Error.Code, mcp.ErrCodeSQLInvalid)
	}
}

func TestIndexStats_MissingTable(t *testing.T) {
	svc, _ := newTestService(t, &stubRunner{}, true)
	result, err := svc.getIndexStats(context.Background(), json.RawMessage(`{"target":"development","schema":"public"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure")
	}
	if result.Error.Code != mcp.ErrCodeSQLInvalid {
		t.Errorf("code = %q, want %q", result.Error.Code, mcp.ErrCodeSQLInvalid)
	}
}

func TestTableStats_Success(t *testing.T) {
	runner := &stubRunner{res: &database.ExecResult{
		Columns:      []string{"schema", "table", "seq_scan"},
		Rows:         [][]any{{"public", "orders", int32(42)}},
		RowsReturned: 1,
		Duration:     time.Millisecond,
	}}
	svc, rec := newTestService(t, runner, true)
	result, err := svc.getTableStats(context.Background(), json.RawMessage(`{"target":"development","schema":"public","table":"orders"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %+v", result.Error)
	}
	if len(resultRows(result)) != 1 {
		t.Errorf("expected 1 row")
	}
	if rec.count(toolGetTableStats) != 1 {
		t.Errorf("expected 1 audit record, got %d", rec.count(toolGetTableStats))
	}
}

// ---------------------------------------------------------------------------
// Remaining handlers: success paths
// ---------------------------------------------------------------------------

func TestHandler_SuccessPaths(t *testing.T) {
	// Each of these handlers runs a read-only query and returns the rows.
	// Verify they succeed on the success path.
	rows := &database.ExecResult{
		Columns:      []string{"k"},
		Rows:         [][]any{{"v"}},
		RowsReturned: 1,
		Duration:     time.Millisecond,
	}

	cases := []struct {
		name string
		call func(svc *Service, ctx context.Context, raw json.RawMessage) (*mcp.ToolResult, error)
	}{
		{"connections", func(s *Service, ctx context.Context, raw json.RawMessage) (*mcp.ToolResult, error) { return s.getConnections(ctx, raw) }},
		{"locks", func(s *Service, ctx context.Context, raw json.RawMessage) (*mcp.ToolResult, error) { return s.getLocks(ctx, raw) }},
		{"cache_stats", func(s *Service, ctx context.Context, raw json.RawMessage) (*mcp.ToolResult, error) { return s.getCacheStats(ctx, raw) }},
		{"index_stats", func(s *Service, ctx context.Context, raw json.RawMessage) (*mcp.ToolResult, error) { return s.getIndexStats(ctx, raw) }},
		{"deadlocks", func(s *Service, ctx context.Context, raw json.RawMessage) (*mcp.ToolResult, error) { return s.getDeadlocks(ctx, raw) }},
		{"database_stats", func(s *Service, ctx context.Context, raw json.RawMessage) (*mcp.ToolResult, error) { return s.getDatabaseStats(ctx, raw) }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newTestService(t, &stubRunner{res: rows}, true)
			raw := json.RawMessage(`{"target":"development","schema":"public","table":"orders"}`)
			result, err := tc.call(svc, context.Background(), raw)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !result.Success {
				t.Fatalf("expected success, got %+v", result.Error)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

func TestRegister_AllReadOnly(t *testing.T) {
	svc, _ := newTestService(t, &stubRunner{}, true)
	reg := mcp.NewRegistry(slogDiscardLogger(), nil, nil)
	if err := Register(reg, svc); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	if n := len(reg.List()); n != ToolCount() {
		t.Fatalf("registered %d tools, want %d", n, ToolCount())
	}
	if ToolCount() != 11 {
		t.Fatalf("ToolCount() = %d, want 11", ToolCount())
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
		"db_get_activity", "db_get_connections", "db_get_locks",
		"db_get_slow_queries", "db_get_database_stats", "db_get_table_stats",
		"db_get_index_stats", "db_get_cache_stats", "db_get_deadlocks",
		"db_get_replication_status", "db_get_replication_lag",
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
