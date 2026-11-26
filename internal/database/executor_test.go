//go:build unit

package database

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// quoteIdentifier tests
// ---------------------------------------------------------------------------

func TestQuoteIdentifier(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple identifier", "public", `"public"`},
		{"empty string", "", `""`},
		{"embedded double quote", `he"llo`, `"he""llo"`},
		{"multiple embedded quotes", `a"b"c`, `"a""b""c"`},
		{"spaces", "my schema", `"my schema"`},
		{"special chars", "my-table", `"my-table"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := quoteIdentifier(tt.input)
			if got != tt.expected {
				t.Errorf("quoteIdentifier(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// qualifiedIdentifier tests
// ---------------------------------------------------------------------------

func TestQualifiedIdentifier(t *testing.T) {
	tests := []struct {
		name     string
		schema   string
		table    string
		expected string
	}{
		{"standard", "public", "orders", `"public"."orders"`},
		{"schema with quotes", `my"schema`, "t", `"my""schema"."t"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := qualifiedIdentifier(tt.schema, tt.table)
			if got != tt.expected {
				t.Errorf("qualifiedIdentifier(%q, %q) = %q, want %q",
					tt.schema, tt.table, got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ErrTargetNotFound / errors.Is tests
// ---------------------------------------------------------------------------

func TestTargetNotFoundError(t *testing.T) {
	err := NewTargetNotFound("production")

	if !errors.Is(err, ErrTargetNotFound) {
		t.Error("errors.Is(err, ErrTargetNotFound) = false, want true")
	}
	if !strings.Contains(err.Error(), "production") {
		t.Errorf("error %q does not contain target name", err.Error())
	}
}

func TestManagerGetTargetUnknown(t *testing.T) {
	m := &manager{
		targets: map[string]*DatabaseTarget{},
	}
	_, err := m.GetTarget("unknown")
	if !errors.Is(err, ErrTargetNotFound) {
		t.Errorf("GetTarget(unknown) error = %v, want ErrTargetNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// maskError tests (DSN masking in error strings)
// ---------------------------------------------------------------------------

func TestMaskError(t *testing.T) {
	tests := []struct {
		name     string
		input    error
		expected string
	}{
		{"nil error", nil, ""},
		{"no DSN", errors.New("connection refused"), "connection refused"},
		{
			"DSN with password",
			errors.New("failed to connect postgres://user:secret@localhost:5432/db"),
			"failed to connect postgres://user:****@localhost:5432/db",
		},
		{
			"DSN without password",
			errors.New("connect postgres://localhost:5432/db"),
			"connect postgres://localhost:5432/db",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maskError(tt.input)
			if got != tt.expected {
				t.Errorf("maskError() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// joinErrors tests
// ---------------------------------------------------------------------------

func TestJoinErrors(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected string
	}{
		{"nil slice", nil, ""},
		{"single element", []string{"error1"}, "error1"},
		{"multiple elements", []string{"a", "b", "c"}, "a; b; c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := joinErrors(tt.input)
			if got != tt.expected {
				t.Errorf("joinErrors(%v) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// containsDSNPattern / maskDSN tests
// ---------------------------------------------------------------------------

func TestContainsDSNPattern(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"hello world", false},
		{"user:pass@host", true},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := containsDSNPattern(tt.input); got != tt.expected {
				t.Errorf("containsDSNPattern(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestMaskDSN(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"postgres://user:secret@localhost:5432/db", "postgres://user:****@localhost:5432/db"},
		{"no_protocol_here", "no_protocol_here"},
		{"postgres://localhost:5432/db", "postgres://localhost:5432/db"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := maskDSN(tt.input); got != tt.expected {
				t.Errorf("maskDSN(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Executor max-SQL-length enforcement
//
// The length check in Execute() runs before any pool interaction, so we can
// test it with a nil pool. Over-limit SQL returns ErrSQLTooLong; under-limit
// SQL will panic on nil pool (expected, caught by recover).
// ---------------------------------------------------------------------------

func TestExecutorSQLLengthEnforcement(t *testing.T) {
	tests := []struct {
		name    string
		sqlLen  int
		maxLen  int
		wantErr bool
	}{
		{"sql under limit", 10, 100, false},
		{"sql at limit", 10, 10, false},
		{"sql over limit", 11, 10, true},
		{"empty sql", 0, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := NewExecutor(nil, 10*time.Second, 100, 1024, tt.maxLen)
			sql := strings.Repeat("a", tt.sqlLen)

			var result *ExecResult
			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						// Expected for under-limit with nil pool.
						result = nil
					}
				}()
				result, err = exec.Execute(context.Background(), sql, nil, 0)
			}()

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected ErrSQLTooLong, got nil err")
				}
				if !errors.Is(err, ErrSQLTooLong) {
					t.Errorf("errors.Is(err, ErrSQLTooLong) = false, err=%v", err)
				}
			} else {
				if err != nil && errors.Is(err, ErrSQLTooLong) {
					t.Errorf("unexpected ErrSQLTooLong for sql len %d, max %d", tt.sqlLen, tt.maxLen)
				}
				_ = result
			}
		})
	}
}

// ---------------------------------------------------------------------------
// classifyError tests
// ---------------------------------------------------------------------------

func TestClassifyError(t *testing.T) {
	ctx := context.Background()
	exec := &Executor{timeout: 10 * time.Second, maxRows: 100, maxResultSize: 1024, maxSQLLength: 65536}

	tests := []struct {
		name    string
		err     error
		wantSub string
	}{
		{"deadline exceeded", context.DeadlineExceeded, "db query timeout"},
		{"generic error", errors.New("connection refused"), "db execution failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := exec.classifyError(ctx, tt.err)
			if got == nil {
				t.Fatal("classifyError returned nil")
			}
			if !strings.Contains(got.Error(), tt.wantSub) {
				t.Errorf("classifyError() = %q, want substring %q", got.Error(), tt.wantSub)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ErrFeatureUnavailable sentinel test
// ---------------------------------------------------------------------------

func TestErrFeatureUnavailable(t *testing.T) {
	err := errors.New("db feature unavailable: no column statistics")
	// Verify the sentinel is accessible and has the expected message.
	if ErrFeatureUnavailable.Error() != "db feature unavailable" {
		t.Errorf("unexpected sentinel message: %q", ErrFeatureUnavailable.Error())
	}
	_ = err
}
