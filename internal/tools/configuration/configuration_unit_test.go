//go:build unit

package configuration

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"postgres-dba-mcp/internal/audit"
	"postgres-dba-mcp/internal/config"
	"postgres-dba-mcp/internal/database"
	"postgres-dba-mcp/internal/mcp"
	"postgres-dba-mcp/internal/metrics"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// stubManager is a database.DatabaseManager that returns a fixed pool for a
// known target and an ErrTargetNotFound for anything else.
type stubManager struct {
	pool *pgxpool.Pool
}

func (m *stubManager) GetPool(target string) (*pgxpool.Pool, error) {
	if target == "development" {
		return m.pool, nil
	}
	return nil, database.NewTargetNotFound(target)
}
func (m *stubManager) GetTarget(name string) (*database.DatabaseTarget, error) {
	if name == "development" {
		return &database.DatabaseTarget{Name: name}, nil
	}
	return nil, database.NewTargetNotFound(name)
}
func (m *stubManager) ListTargets() []string                { return []string{"development"} }
func (m *stubManager) HealthCheck(ctx context.Context) error { return nil }
func (m *stubManager) Close()                                {}

// stubRunner implements queryRunner. It returns a preset *database.ExecResult
// or error and records the SQL/limit it was asked to run.
type stubRunner struct {
	mu    sync.Mutex
	res   *database.ExecResult
	err   error
	// lastSQL/lastParams/lastLimit capture the most recent invocation.
	lastSQL    string
	lastParams []any
	lastLimit  int
}

func (r *stubRunner) Execute(ctx context.Context, sql string, params []any, limit int) (*database.ExecResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastSQL = sql
	r.lastParams = params
	r.lastLimit = limit
	if r.err != nil {
		return nil, r.err
	}
	if r.res != nil {
		return r.res, nil
	}
	return &database.ExecResult{Columns: []string{"x"}, Rows: [][]any{}, Duration: time.Millisecond}, nil
}

// auditRecorder captures the "operation" values of audit records by acting as
// a slog handler.
type auditRecorder struct {
	mu    sync.Mutex
	tools []string
	ops   []string
}

func (a *auditRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (a *auditRecorder) Handle(_ context.Context, rec slog.Record) error {
	if rec.Message != "mcp_tool_audit" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	rec.Attrs(func(attr slog.Attr) bool {
		switch attr.Key {
		case "tool":
			a.tools = append(a.tools, attr.Value.String())
		case "operation":
			a.ops = append(a.ops, attr.Value.String())
		}
		return true
	})
	return nil
}
func (a *auditRecorder) WithAttrs([]slog.Attr) slog.Handler { return a }
func (a *auditRecorder) WithGroup(string) slog.Handler      { return a }

// count returns how many times the given tool name was recorded.
func (a *auditRecorder) count(tool string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, t := range a.tools {
		if t == tool {
			n++
		}
	}
	return n
}

// lastOp returns the most recent recorded operation category.
func (a *auditRecorder) lastOp() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.ops) == 0 {
		return ""
	}
	return a.ops[len(a.ops)-1]
}

// discardHandler is a no-op slog.Handler used where a logger is required but
// its output is irrelevant to the assertion.
type discardHandler struct{}

func (d discardHandler) Enabled(context.Context, slog.Level) bool { return true }
func (d discardHandler) Handle(context.Context, slog.Record) error {
	return nil
}
func (d discardHandler) WithAttrs([]slog.Attr) slog.Handler { return d }
func (d discardHandler) WithGroup(string) slog.Handler      { return d }

// slogDiscardLogger returns a no-op logger for tests that need a *slog.Logger
// but do not care about its output.
func slogDiscardLogger() *slog.Logger {
	return slog.New(discardHandler{})
}

// newTestService builds a Service with a stub runner and a capturing audit sink.
func newTestService(t *testing.T, runner queryRunner) (*Service, *auditRecorder) {
	t.Helper()
	rec := &auditRecorder{}
	auditLogger := audit.New(true, slog.New(rec))
	svc := NewService(
		&config.Config{QueryLimits: config.QueryLimits{
			Timeout:       10 * time.Second,
			MaxRows:       100,
			MaxResultSize: 1048576,
			MaxSQLLength:  65536,
		}},
		&stubManager{pool: nil},
		auditLogger,
		metrics.New(),
	)
	if runner != nil {
		svc.runner = runner
	}
	return svc, rec
}

// pgSettingsColumns are the columns returned by the config queries.
var pgSettingsColumns = []string{"name", "setting", "unit", "category", "short_desc"}

// ---------------------------------------------------------------------------
// Test cases
// ---------------------------------------------------------------------------

// TestGetConfig_Success verifies a valid allow-listed param returns success.
func TestGetConfig_Success(t *testing.T) {
	runner := &stubRunner{res: &database.ExecResult{
		Columns:      pgSettingsColumns,
		Rows:         [][]any{{"max_connections", "100", "", "Connections", "Maximum number of concurrent connections"}},
		Duration:     time.Millisecond,
		RowsReturned: 1,
	}}
	svc, _ := newTestService(t, runner)
	args, _ := json.Marshal(map[string]string{"target": "development", "name": "max_connections"})

	res, err := svc.getDBConfig(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got %+v", res.Error)
	}
	if res.Error != nil {
		t.Fatalf("expected nil error on success, got %+v", res.Error)
	}
	data, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data is %T, want map[string]any", res.Data)
	}
	if data["name"] != "max_connections" {
		t.Errorf("data[name] = %v, want max_connections", data["name"])
	}
	// Verify the SQL used a parameterized query with the normalized name.
	if runner.lastSQL != getConfigSQL {
		t.Errorf("SQL = %q, want the single-config select", runner.lastSQL)
	}
	if len(runner.lastParams) != 1 || runner.lastParams[0] != "max_connections" {
		t.Errorf("params = %v, want [max_connections]", runner.lastParams)
	}
}

// TestGetConfig_UnknownParam verifies a param not in the allow-list is denied.
func TestGetConfig_UnknownParam(t *testing.T) {
	runner := &stubRunner{}
	svc, _ := newTestService(t, runner)
	args, _ := json.Marshal(map[string]string{"target": "development", "name": "some_unknown_param"})

	res, err := svc.getDBConfig(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Success {
		t.Fatalf("expected failure for unknown param")
	}
	if res.Error.Code != mcp.ErrCodePermissionDenied {
		t.Errorf("error code = %q, want %q", res.Error.Code, mcp.ErrCodePermissionDenied)
	}
	// The runner should never have been invoked (denied before the query).
	if runner.lastSQL != "" {
		t.Errorf("query should not run for unknown param, got %q", runner.lastSQL)
	}
}

// TestGetConfig_SecretParam verifies a secret/sensitive param is rejected.
func TestGetConfig_SecretParam(t *testing.T) {
	for _, secret := range []string{"ssl_cert_file", "password_encryption", "ssl_key_file"} {
		if isAllowedParam(secret) {
			t.Errorf("secret param %q should not be in allow-list", secret)
		}
	}
	runner := &stubRunner{}
	svc, _ := newTestService(t, runner)
	args, _ := json.Marshal(map[string]string{"target": "development", "name": "ssl_cert_file"})
	res, err := svc.getDBConfig(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Error == nil || res.Error.Code != mcp.ErrCodePermissionDenied {
		t.Errorf("expected permission denied for secret param, got %+v", res.Error)
	}
}

// TestGetConfig_InvalidName verifies names with uppercase or special
// characters are validated (identifier rule).
func TestGetConfig_InvalidName(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"Max-Connections", true}, // hyphen
		{"1abc", true},            // leading digit
		{"", true},                // empty
		{"a b", true},             // space
		{"a-b", true},             // hyphen
		{"max_connections", false},
	}
	// A stub runner is provided so the valid-identifier case (max_connections)
	// can complete the flow without a real connection pool.
	runner := &stubRunner{res: &database.ExecResult{
		Columns:      pgSettingsColumns,
		Rows:         [][]any{{"max_connections", "100", "", "Connections", "desc"}},
		Duration:     time.Millisecond,
		RowsReturned: 1,
	}}
	svc, _ := newTestService(t, runner)
	for _, tc := range cases {
		args, _ := json.Marshal(map[string]string{"target": "development", "name": tc.name})
		res, err := svc.getDBConfig(context.Background(), args)
		if err != nil {
			t.Fatalf("name %q: unexpected error: %v", tc.name, err)
		}
		if tc.wantErr {
			if res.Success {
				t.Errorf("name %q: expected failure", tc.name)
			} else if res.Error.Code != mcp.ErrCodeSQLInvalid {
				t.Errorf("name %q: code = %q, want %q", tc.name, res.Error.Code, mcp.ErrCodeSQLInvalid)
			}
		}
	}
}

// TestGetConfig_MissingName verifies missing name is rejected.
func TestGetConfig_MissingName(t *testing.T) {
	svc, _ := newTestService(t, nil)
	args, _ := json.Marshal(map[string]string{"target": "development"})
	res, err := svc.getDBConfig(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Success || res.Error.Code != mcp.ErrCodeSQLInvalid {
		t.Errorf("expected SQL_INVALID for missing name, got %+v", res.Error)
	}
}

// TestGetConfig_MissingTarget verifies missing target is rejected.
func TestGetConfig_MissingTarget(t *testing.T) {
	svc, _ := newTestService(t, nil)
	args, _ := json.Marshal(map[string]string{"name": "max_connections"})
	res, err := svc.getDBConfig(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Success || res.Error.Code != mcp.ErrCodeSQLInvalid {
		t.Errorf("expected SQL_INVALID for missing target, got %+v", res.Error)
	}
}

// TestGetConfig_UnknownTarget verifies an unknown target yields DB_TARGET_NOT_FOUND.
func TestGetConfig_UnknownTarget(t *testing.T) {
	svc, _ := newTestService(t, &stubRunner{})
	args, _ := json.Marshal(map[string]string{"target": "nonexistent", "name": "max_connections"})
	res, err := svc.getDBConfig(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Success || res.Error.Code != mcp.ErrCodeTargetNotFound {
		t.Errorf("expected DB_TARGET_NOT_FOUND, got %+v", res.Error)
	}
}

// TestGetConfig_NotFound verifies an allow-listed param with zero rows yields
// a clean "not found" execution error.
func TestGetConfig_NotFound(t *testing.T) {
	runner := &stubRunner{res: &database.ExecResult{
		Columns:      pgSettingsColumns,
		Rows:         [][]any{},
		Duration:     time.Millisecond,
		RowsReturned: 0,
	}}
	svc, _ := newTestService(t, runner)
	args, _ := json.Marshal(map[string]string{"target": "development", "name": "max_connections"})
	res, err := svc.getDBConfig(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Success || res.Error.Code != mcp.ErrCodeExecutionFailed {
		t.Errorf("expected DB_EXECUTION_FAILED for missing param, got %+v", res.Error)
	}
}

// TestGetConfigList_Success verifies only allow-listed params are returned.
func TestGetConfigList_Success(t *testing.T) {
	runner := &stubRunner{res: &database.ExecResult{
		Columns: pgSettingsColumns,
		Rows: [][]any{
			{"max_connections", "100", "", "Connections", "desc"},
			{"ssl_cert_file", "/path", "", "SSL", "secret-ish"},
			{"work_mem", "4MB", "MB", "Query Planning", "desc"},
			{"some_unknown_param", "x", "", "Other", "desc"},
		},
		Duration:     time.Millisecond,
		RowsReturned: 4,
	}}
	svc, _ := newTestService(t, runner)
	args, _ := json.Marshal(map[string]string{"target": "development"})

	res, err := svc.getDBConfigList(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got %+v", res.Error)
	}
	rows, ok := res.Data.([]map[string]any)
	if !ok {
		t.Fatalf("Data is %T, want []map[string]any", res.Data)
	}
	if len(rows) != 2 {
		t.Fatalf("returned %d rows, want 2 (only allow-listed)", len(rows))
	}
	byName := map[string]bool{}
	for _, r := range rows {
		byName[r["name"].(string)] = true
	}
	if !byName["max_connections"] || !byName["work_mem"] {
		t.Errorf("missing allow-listed params: %v", byName)
	}
	if byName["ssl_cert_file"] || byName["some_unknown_param"] {
		t.Errorf("non-allow-listed params leaked: %v", byName)
	}
}

// TestGetConfigList_MissingTarget verifies missing target is rejected.
func TestGetConfigList_MissingTarget(t *testing.T) {
	svc, _ := newTestService(t, nil)
	res, err := svc.getDBConfigList(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Success || res.Error.Code != mcp.ErrCodeSQLInvalid {
		t.Errorf("expected SQL_INVALID for missing target, got %+v", res.Error)
	}
}

// TestReloadConfig_Success verifies SELECT pg_reload_conf() is issued.
func TestReloadConfig_Success(t *testing.T) {
	runner := &stubRunner{res: &database.ExecResult{
		Columns:      []string{"reloaded"},
		Rows:         [][]any{{true}},
		Duration:     time.Millisecond,
		RowsReturned: 1,
	}}
	svc, rec := newTestService(t, runner)
	args, _ := json.Marshal(map[string]string{"target": "development"})

	res, err := svc.reloadDBConfig(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got %+v", res.Error)
	}
	if runner.lastSQL != reloadConfigSQL {
		t.Errorf("SQL = %q, want reload", runner.lastSQL)
	}
	data, ok := res.Data.(map[string]any)
	if !ok || data["reloaded"] != true {
		t.Errorf("data = %v, want reloaded=true", data)
	}
	// The audit record should carry the MAINTENANCE operation category.
	if rec.lastOp() != operationMaintenance {
		t.Errorf("audit operation = %q, want %q", rec.lastOp(), operationMaintenance)
	}
}

// TestReloadConfig_MissingTarget verifies missing target is rejected.
func TestReloadConfig_MissingTarget(t *testing.T) {
	svc, _ := newTestService(t, nil)
	res, err := svc.reloadDBConfig(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Success || res.Error.Code != mcp.ErrCodeSQLInvalid {
		t.Errorf("expected SQL_INVALID for missing target, got %+v", res.Error)
	}
}

// TestReloadConfig_DefaultDisabled verifies db_reload_config is registered
// with REQUIRES_APPROVAL (so the policy engine denies it by default).
func TestReloadConfig_DefaultDisabled(t *testing.T) {
	svc, _ := newTestService(t, nil)
	reg := mcp.NewRegistry(slogDiscardLogger(), nil, nil)
	if err := Register(reg, svc); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	def, ok := reg.Get(toolReloadConfig)
	if !ok {
		t.Fatalf("tool %s not registered", toolReloadConfig)
	}
	if def.Permission != mcp.PermissionRequiresApproval {
		t.Errorf("db_reload_config permission = %q, want REQUIRES_APPROVAL (default-disabled)", def.Permission)
	}
}

// TestRegister_AllThreeTools verifies Register registers exactly 3 tools with
// correct names and permission levels.
func TestRegister_AllThreeTools(t *testing.T) {
	svc, _ := newTestService(t, &stubRunner{})
	reg := mcp.NewRegistry(slogDiscardLogger(), nil, nil)
	if err := Register(reg, svc); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	if n := len(reg.List()); n != ToolCount() {
		t.Fatalf("registered %d tools, want %d", n, ToolCount())
	}
	if ToolCount() != 3 {
		t.Fatalf("ToolCount() = %d, want 3", ToolCount())
	}

	wantPerm := map[string]mcp.PermissionLevel{
		toolGetConfig:     mcp.PermissionReadOnly,
		toolGetConfigList: mcp.PermissionReadOnly,
		toolReloadConfig:  mcp.PermissionRequiresApproval,
	}
	for _, def := range reg.List() {
		if def.Handler == nil {
			t.Errorf("tool %s has nil handler", def.Name)
		}
		if p, ok := wantPerm[def.Name]; !ok {
			t.Errorf("unexpected tool %s registered", def.Name)
		} else if def.Permission != p {
			t.Errorf("tool %s has permission %q, want %q", def.Name, def.Permission, p)
		}
		// Every schema must require "target".
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
}

// TestToolCount verifies ToolCount() == 3.
func TestToolCount(t *testing.T) {
	if n := ToolCount(); n != 3 {
		t.Errorf("ToolCount() = %d, want 3", n)
	}
}

// TestAllowList_ContainsExpected verifies the allow-list includes key
// operational parameters and excludes secrets.
func TestAllowList_ContainsExpected(t *testing.T) {
	want := []string{"max_connections", "shared_buffers", "work_mem", "autovacuum"}
	for _, p := range want {
		if !isAllowedParam(p) {
			t.Errorf("allow-list missing expected param %q", p)
		}
	}
	for _, secret := range []string{"ssl_cert_file", "password_encryption", "ssl_key_file", "ssl_ca_file"} {
		if isAllowedParam(secret) {
			t.Errorf("allow-list should exclude secret param %q", secret)
		}
	}
}

// TestValidateIdentifier is a table-driven check of identifier validation.
func TestValidateIdentifier(t *testing.T) {
	cases := []struct {
		value   string
		wantErr bool
	}{
		{"max_connections", false},
		{"work_mem", false},
		{"a", false},
		{"_underscore", false},
		{"Max-Connections", true},
		{"1abc", true},
		{"", true},
		{"a b", true},
		{"a-b", true},
		{string(make([]byte, 64)), true}, // 64 chars, over the limit
	}
	for _, tc := range cases {
		err := validateIdentifier("name", tc.value)
		if (err != nil) != tc.wantErr {
			t.Errorf("validateIdentifier(%q) err = %v, wantErr = %v", tc.value, err, tc.wantErr)
		}
		if !errors.Is(err, errInvalidArgs) && tc.wantErr {
			t.Errorf("validateIdentifier(%q): error should wrap errInvalidArgs", tc.value)
		}
	}
}

// TestErrCodeForErr verifies database errors map to standard codes.
func TestErrCodeForErr(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{database.ErrQueryTimeout, mcp.ErrCodeQueryTimeout},
		{database.ErrResultTooLarge, mcp.ErrCodeResultTooLarge},
		{database.ErrSQLTooLong, mcp.ErrCodeSQLInvalid},
		{database.NewTargetNotFound("x"), mcp.ErrCodeTargetNotFound},
		{database.ErrFeatureUnavailable, mcp.ErrCodeFeatureUnavailable},
		{errors.New("boom"), mcp.ErrCodeExecutionFailed},
	}
	for _, tc := range cases {
		if got := errCodeForErr(tc.err); got != tc.want {
			t.Errorf("errCodeForErr(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
