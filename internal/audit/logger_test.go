//go:build unit

package audit

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// newCapturingLogger builds an AuditLogger that writes JSON to buf.
func newCapturingLogger(enabled bool, buf *bytes.Buffer) *AuditLogger {
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	return New(enabled, slog.New(h))
}

// TestLogger_Disabled verifies that when audit is disabled
// (MCP_AUDIT_ENABLED = false), Log writes nothing.
func TestLogger_Disabled(t *testing.T) {
	var buf bytes.Buffer
	l := newCapturingLogger(false, &buf)
	if l.Enabled() {
		t.Fatal("expected logger to be disabled")
	}
	rec := &AuditRecord{
		Timestamp: time.Now(),
		RequestID: "req-1",
		Tool:      "db_get_info",
		Target:    "development",
		Operation: "READ",
		DurationMS: 5,
		Success:   true,
	}
	l.Log(context.Background(), rec)
	if buf.Len() != 0 {
		t.Errorf("disabled logger wrote output: %q", buf.String())
	}
}

// TestLogger_Enabled verifies that when audit is enabled
// (MCP_AUDIT_ENABLED = true), Log writes a structured record containing the
// expected fields.
func TestLogger_Enabled(t *testing.T) {
	var buf bytes.Buffer
	l := newCapturingLogger(true, &buf)
	if !l.Enabled() {
		t.Fatal("expected logger to be enabled")
	}
	rec := &AuditRecord{
		Timestamp:    time.Date(2026, 11, 25, 20, 0, 0, 0, time.UTC),
		RequestID:    "req-123",
		Tool:         "db_describe_table",
		Target:       "production",
		Operation:    "READ",
		DurationMS:   25,
		Success:      true,
		RowsReturned: 12,
	}
	l.Log(context.Background(), rec)

	out := buf.String()
	if out == "" {
		t.Fatal("enabled logger produced no output")
	}
	// The message tag and key fields must appear in the JSON output.
	for _, want := range []string{
		"mcp_tool_audit",
		"db_describe_table",
		"production",
		"req-123",
		"READ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got: %s", want, out)
		}
	}
}

// TestLogger_Enabled_MasksSensitive verifies that sensitive params are masked
// in the emitted output (DSN-like value and password key).
func TestLogger_Enabled_MasksSensitive(t *testing.T) {
	var buf bytes.Buffer
	l := newCapturingLogger(true, &buf)
	rec := &AuditRecord{
		Timestamp: time.Now(),
		RequestID: "req",
		Tool:      "db_query_readonly",
		Target:    "development",
		Operation: "READ",
		Params: map[string]any{
			"connection": "postgres://admin:secret123@prod-db:5432/mydb",
			"password":   "hunter2",
		},
	}
	l.Log(context.Background(), rec)
	out := buf.String()
	// None of the sensitive substrings should appear in the emitted JSON.
	for _, leak := range []string{"secret123", "hunter2", "prod-db", "mydb"} {
		if strings.Contains(out, leak) {
			t.Errorf("output leaked %q: %s", leak, out)
		}
	}
}

// TestLogger_NilSafe verifies that Log is safe on a nil receiver and a nil
// record (no panic, no output).
func TestLogger_NilSafe(t *testing.T) {
	var l *AuditLogger
	l.Log(context.Background(), nil) // nil receiver + nil record must not panic

	var buf bytes.Buffer
	l2 := newCapturingLogger(true, &buf)
	l2.Log(context.Background(), nil) // nil record must not write
	if buf.Len() != 0 {
		t.Errorf("nil record produced output: %q", buf.String())
	}
}
