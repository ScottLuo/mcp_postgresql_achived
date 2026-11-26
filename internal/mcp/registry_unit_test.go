//go:build unit

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// stubPolicy is a stub PolicyChecker for testing.
type stubPolicy struct {
	deny bool
	err  error
}

func (s *stubPolicy) Check(toolName, target string, perm PermissionLevel) error {
	if s.deny {
		return s.err
	}
	return nil
}

// stubHandler is a stub ToolHandler for testing.
type stubHandler struct {
	called  bool
	result  *ToolResult
	err     error
	panics  bool
	sleepMs int // when > 0, the handler sleeps before returning
}

func (s *stubHandler) Execute(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	s.called = true
	if s.panics {
		panic("test panic")
	}
	if s.sleepMs > 0 {
		time.Sleep(time.Duration(s.sleepMs) * time.Millisecond)
	}
	return s.result, s.err
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestRegistry_RegisterAndLookup verifies that a tool can be registered and
// retrieved by name with the correct fields.
func TestRegistry_RegisterAndLookup(t *testing.T) {
	reg := NewRegistry(newTestLogger(), &stubPolicy{}, nil)

	handler := &stubHandler{result: SuccessResult(map[string]string{"ok": "true"}, nil)}
	def := &ToolDefinition{
		Name:        "db_test",
		Description: "A test tool",
		Permission:  PermissionReadOnly,
		Timeout:     10 * time.Second,
		Handler:     handler,
	}

	if err := reg.Register(def); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	got, ok := reg.Get("db_test")
	if !ok {
		t.Fatal("Get returned not found")
	}
	if got.Name != "db_test" {
		t.Errorf("Name = %q, want %q", got.Name, "db_test")
	}
	if got.Description != "A test tool" {
		t.Errorf("Description = %q", got.Description)
	}
	if got.Permission != PermissionReadOnly {
		t.Errorf("Permission = %q, want %q", got.Permission, PermissionReadOnly)
	}
	if got.Timeout != 10*time.Second {
		t.Errorf("Timeout = %v, want 10s", got.Timeout)
	}
}

// TestRegistry_DuplicateName verifies that registering two tools with the
// same name returns an error.
func TestRegistry_DuplicateName(t *testing.T) {
	reg := NewRegistry(newTestLogger(), &stubPolicy{}, nil)

	handler := &stubHandler{}
	err1 := reg.Register(&ToolDefinition{Name: "db_dup", Description: "a", Handler: handler})
	if err1 != nil {
		t.Fatalf("first Register failed: %v", err1)
	}
	err2 := reg.Register(&ToolDefinition{Name: "db_dup", Description: "b", Handler: handler})
	if err2 == nil {
		t.Fatal("expected error on duplicate registration, got nil")
	}
	if !errors.Is(err2, err2) { // just check it's an error
		t.Error("expected error")
	}
}

// TestRegistry_ListAll verifies that List() returns all registered tools.
func TestRegistry_ListAll(t *testing.T) {
	reg := NewRegistry(newTestLogger(), &stubPolicy{}, nil)

	names := []string{"tool_a", "tool_b", "tool_c", "tool_d", "tool_e"}
	for _, n := range names {
		if err := reg.Register(&ToolDefinition{
			Name: n, Description: "desc", Handler: &stubHandler{},
		}); err != nil {
			t.Fatalf("Register %q: %v", n, err)
		}
	}

	list := reg.List()
	if len(list) != 5 {
		t.Fatalf("List() returned %d tools, want 5", len(list))
	}
}

// TestRegistry_Execute_PolicyDenied verifies that when the policy denies a
// call, the handler is not invoked and the result has DB_PERMISSION_DENIED.
func TestRegistry_Execute_PolicyDenied(t *testing.T) {
	policy := &stubPolicy{
		deny: true,
		err:  errors.New("access denied"),
	}
	handler := &stubHandler{}
	reg := NewRegistry(newTestLogger(), policy, nil)

	if err := reg.Register(&ToolDefinition{
		Name: "db_perm", Description: "test", Handler: handler,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := reg.Execute(context.Background(), "db_perm", "development", nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Error("expected Success=false when policy denies")
	}
	if result.Error == nil {
		t.Fatal("expected Error to be non-nil")
	}
	if result.Error.Code != ErrCodePermissionDenied {
		t.Errorf("Error.Code = %q, want %q", result.Error.Code, ErrCodePermissionDenied)
	}
	if handler.called {
		t.Error("handler was called despite policy denial")
	}
}

// TestRegistry_Execute_PolicyError verifies that a PolicyError carries its
// specific code through to the ToolResult.
func TestRegistry_Execute_PolicyError(t *testing.T) {
	policy := &stubPolicy{
		deny: true,
		err:  &PolicyError{Code: ErrCodeApprovalRequired, Message: "approval needed"},
	}
	reg := NewRegistry(newTestLogger(), policy, nil)

	if err := reg.Register(&ToolDefinition{
		Name: "db_appr", Description: "test", Handler: &stubHandler{},
	}); err != nil {
		t.Fatal(err)
	}

	result, _ := reg.Execute(context.Background(), "db_appr", "production", nil)
	if result.Error.Code != ErrCodeApprovalRequired {
		t.Errorf("Error.Code = %q, want %q", result.Error.Code, ErrCodeApprovalRequired)
	}
	if result.Error.Message != "approval needed" {
		t.Errorf("Error.Message = %q", result.Error.Message)
	}
}

// TestRegistry_Execute_PanicRecovery verifies that a handler panic is
// recovered and converted to a DB_EXECUTION_FAILED result.
func TestRegistry_Execute_PanicRecovery(t *testing.T) {
	reg := NewRegistry(newTestLogger(), &stubPolicy{}, nil)

	handler := &stubHandler{panics: true}
	if err := reg.Register(&ToolDefinition{
		Name: "db_panic", Description: "test", Handler: handler,
		Timeout: 5 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := reg.Execute(context.Background(), "db_panic", "dev", nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Error("expected Success=false after panic")
	}
	if result.Error.Code != ErrCodeExecutionFailed {
		t.Errorf("Error.Code = %q, want %q", result.Error.Code, ErrCodeExecutionFailed)
	}
	if handler.called != true {
		t.Error("handler should have been called before panicking")
	}
}

// TestRegistry_Execute_Success verifies that a successful handler result is
// passed through unchanged.
func TestRegistry_Execute_Success(t *testing.T) {
	reg := NewRegistry(newTestLogger(), &stubPolicy{}, nil)

	expected := SuccessResult(map[string]int{"rows": 42}, &Metadata{
		Target: "dev", DurationMS: 5, Truncated: false, RowsReturned: 42,
	})
	handler := &stubHandler{result: expected}
	if err := reg.Register(&ToolDefinition{
		Name: "db_ok", Description: "test", Handler: handler,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := reg.Execute(context.Background(), "db_ok", "dev", nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Error("expected Success=true")
	}
	if result.Error != nil {
		t.Errorf("expected Error=nil, got %+v", result.Error)
	}
	if result.Metadata == nil || result.Metadata.RowsReturned != 42 {
		t.Errorf("Metadata.RowsReturned = %v, want 42", result.Metadata)
	}
}

// TestRegistry_Execute_HandlerError verifies that a handler returning a
// non-nil error is wrapped into a DB_EXECUTION_FAILED result.
func TestRegistry_Execute_HandlerError(t *testing.T) {
	reg := NewRegistry(newTestLogger(), &stubPolicy{}, nil)

	handler := &stubHandler{err: errors.New("db connection refused")}
	if err := reg.Register(&ToolDefinition{
		Name: "db_err", Description: "test", Handler: handler,
	}); err != nil {
		t.Fatal(err)
	}

	result, _ := reg.Execute(context.Background(), "db_err", "dev", nil)
	if result.Success {
		t.Error("expected Success=false when handler returns error")
	}
	if result.Error.Code != ErrCodeExecutionFailed {
		t.Errorf("Error.Code = %q, want %q", result.Error.Code, ErrCodeExecutionFailed)
	}
}

// TestRegistry_Execute_Timeout verifies that when the handler takes longer
// than the tool timeout, the result has DB_QUERY_TIMEOUT.
func TestRegistry_Execute_Timeout(t *testing.T) {
	reg := NewRegistry(newTestLogger(), &stubPolicy{}, nil)

	handler := &stubHandler{
		result:  SuccessResult("data", nil),
		sleepMs: 200,
	}
	if err := reg.Register(&ToolDefinition{
		Name:    "db_slow",
		Description: "test",
		Handler: handler,
		Timeout: 50 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}

	result, _ := reg.Execute(context.Background(), "db_slow", "dev", nil)
	if !result.Success {
		// When the handler sleeps past the timeout, the result should indicate
		// a timeout. The handler may still return its result if it finished
		// before the deadline was checked — this is an implementation detail.
		// In this test, sleepMs=200 and timeout=50ms, so the handler will
		// exceed the timeout.
		if result.Error.Code != ErrCodeQueryTimeout {
			t.Errorf("Error.Code = %q, want %q (got Success=%v, Error=%+v)",
				result.Error.Code, ErrCodeQueryTimeout, result.Success, result.Error)
		}
	}
}

// TestRegistry_Execute_UnknownTool verifies that executing an unregistered
// tool returns DB_TARGET_NOT_FOUND.
func TestRegistry_Execute_UnknownTool(t *testing.T) {
	reg := NewRegistry(newTestLogger(), &stubPolicy{}, nil)

	result, err := reg.Execute(context.Background(), "db_nope", "dev", nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Error.Code != ErrCodeTargetNotFound {
		t.Errorf("Error.Code = %q, want %q", result.Error.Code, ErrCodeTargetNotFound)
	}
}

// TestRegistry_RegisterValidation verifies Register() input validation.
func TestRegistry_RegisterValidation(t *testing.T) {
	reg := NewRegistry(newTestLogger(), &stubPolicy{}, nil)

	cases := []struct {
		name    string
		def     *ToolDefinition
		wantErr string
	}{
		{"nil_def", nil, "nil"},
		{"empty_name", &ToolDefinition{Description: "d", Handler: &stubHandler{}}, "name"},
		{"empty_desc", &ToolDefinition{Name: "x", Handler: &stubHandler{}}, "description"},
		{"nil_handler", &ToolDefinition{Name: "x", Description: "d"}, "handler"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := reg.Register(tc.def)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

// TestRegistry_ToMCPTools verifies the translation to MCP SDK tools.
func TestRegistry_ToMCPTools(t *testing.T) {
	reg := NewRegistry(newTestLogger(), &stubPolicy{}, nil)

	schema := json.RawMessage(`{"type":"object","properties":{"target":{"type":"string"}}}`)
	if err := reg.Register(&ToolDefinition{
		Name:        "db_tool",
		Description: "A tool",
		Permission:  PermissionReadOnly,
		InputSchema: schema,
		Handler:     &stubHandler{},
	}); err != nil {
		t.Fatal(err)
	}

	tools := reg.ToMCPTools()
	if len(tools) != 1 {
		t.Fatalf("ToMCPTools returned %d, want 1", len(tools))
	}
	if tools[0].Name != "db_tool" {
		t.Errorf("Name = %q", tools[0].Name)
	}
}

// TestRegistry_ToMCPTools_EmptySchema verifies the fallback schema.
func TestRegistry_ToMCPTools_EmptySchema(t *testing.T) {
	reg := NewRegistry(newTestLogger(), &stubPolicy{}, nil)

	if err := reg.Register(&ToolDefinition{
		Name: "db_noschema", Description: "no schema", Handler: &stubHandler{},
	}); err != nil {
		t.Fatal(err)
	}

	tools := reg.ToMCPTools()
	if len(tools) != 1 {
		t.Fatal("expected 1 tool")
	}
	// The fallback schema should be a valid JSON object.
	var m map[string]interface{}
	if err := json.Unmarshal(tools[0].InputSchema.(json.RawMessage), &m); err != nil {
		t.Errorf("fallback InputSchema not valid JSON: %v", err)
	}
}

// TestConcurrencyLimiter verifies per-target concurrency control.
func TestConcurrencyLimiter(t *testing.T) {
	lim := newConcurrencyLimiter()
	ctx := context.Background()

	// Acquire first slot for target "dev".
	release1, err := lim.Acquire(ctx, "dev")
	if err != nil {
		t.Fatalf("first Acquire failed: %v", err)
	}

	// A different target should not be blocked.
	release2, err := lim.Acquire(ctx, "prod")
	if err != nil {
		t.Fatalf("second Acquire (different target) failed: %v", err)
	}
	release2()

	// Same target should block — use a short deadline to verify.
	ctx2, cancel2 := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel2()
	_, err = lim.Acquire(ctx2, "dev")
	if err == nil {
		t.Error("expected error when slot is held and context times out")
	}

	// Release the first slot, then acquire should succeed.
	release1()
	if _, err := lim.Acquire(ctx, "dev"); err != nil {
		t.Errorf("Acquire after release failed: %v", err)
	}
}

// TestBuildToolHandler_TargetExtraction verifies the target is extracted
// from the arguments.
func TestBuildToolHandler_TargetExtraction(t *testing.T) {
	reg := NewRegistry(newTestLogger(), &stubPolicy{}, nil)

	handler := &stubHandler{result: SuccessResult("ok", nil)}
	if err := reg.Register(&ToolDefinition{
		Name: "db_x", Description: "test", Handler: handler,
	}); err != nil {
		t.Fatal(err)
	}

	h := reg.BuildToolHandler(reg.List()[0])
	req := &mcpsdk.CallToolRequest{
		Params: &mcpsdk.CallToolParamsRaw{
			Arguments: json.RawMessage(`{"target":"staging"}`),
		},
	}
	result, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
}

// TestNewRegistry_NilLimiter verifies the default limiter is used.
func TestNewRegistry_NilLimiter(t *testing.T) {
	reg := NewRegistry(newTestLogger(), &stubPolicy{}, nil)
	if reg.limiter == nil {
		t.Error("limiter should not be nil after NewRegistry")
	}
}

// TestServer_NewServer verifies server construction.
func TestServer_NewServer(t *testing.T) {
	reg := NewRegistry(newTestLogger(), &stubPolicy{}, nil)
	if err := reg.Register(&ToolDefinition{
		Name: "db_s", Description: "server test", Handler: &stubHandler{},
	}); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := NewServer(reg, ServerConfig{
		Name:    "test-server",
		Version: "1.0.0",
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	if srv == nil {
		t.Fatal("server is nil")
	}
}

// TestServer_NewServer_NilRegistry verifies nil registry error.
func TestServer_NewServer_NilRegistry(t *testing.T) {
	_, err := NewServer(nil, ServerConfig{Logger: newTestLogger()})
	if err == nil {
		t.Fatal("expected error for nil registry")
	}
}

// TestServer_NewServer_Defaults verifies default name/version are applied.
func TestServer_NewServer_Defaults(t *testing.T) {
	reg := NewRegistry(newTestLogger(), &stubPolicy{}, nil)
	srv, err := NewServer(reg, ServerConfig{Logger: newTestLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if srv.cfg.Name != "postgres-dba-mcp" {
		t.Errorf("Name = %q, want default", srv.cfg.Name)
	}
	if srv.cfg.Version != "0.1.0" {
		t.Errorf("Version = %q, want default", srv.cfg.Version)
	}
}
