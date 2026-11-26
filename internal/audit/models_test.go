//go:build unit

package audit

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestAuditRecord_JSONSerialization verifies that a fully populated
// AuditRecord marshals to JSON with the exact keys defined in spec §14.
func TestAuditRecord_JSONSerialization(t *testing.T) {
	rec := AuditRecord{
		Timestamp:    time.Date(2026, 11, 25, 20, 0, 0, 0, time.UTC),
		RequestID:    "req-123",
		Tool:         "db_describe_table",
		Target:       "production",
		Operation:    "READ",
		DurationMS:   25,
		Success:      true,
		RowsReturned: 12,
	}

	out, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// The spec §14 example keys must be present.
	wantKeys := []string{
		"timestamp", "request_id", "tool", "target",
		"operation", "duration_ms", "success", "rows_returned",
	}
	for _, k := range wantKeys {
		if _, ok := m[k]; !ok {
			t.Errorf("missing required key %q in output %s", k, out)
		}
	}

	// Optional keys must be omitted when unset (omitempty behavior).
	for _, k := range []string{"error_code", "approval_id", "approver", "params"} {
		if _, ok := m[k]; ok {
			t.Errorf("unexpected key %q present when empty; output: %s", k, out)
		}
	}

	if got, _ := m["tool"].(string); got != "db_describe_table" {
		t.Errorf("tool = %v, want db_describe_table", m["tool"])
	}
	if got, _ := m["duration_ms"].(float64); got != 25 {
		t.Errorf("duration_ms = %v, want 25", m["duration_ms"])
	}
	if got, _ := m["success"].(bool); got != true {
		t.Errorf("success = %v, want true", m["success"])
	}
}

// TestAuditRecord_ApprovalFields verifies that maintenance-operation fields
// (approval_id, approver) are serialized when set.
func TestAuditRecord_ApprovalFields(t *testing.T) {
	rec := AuditRecord{
		Timestamp:    time.Now(),
		RequestID:    "req-1",
		Tool:         "db_drop_index",
		Target:       "staging",
		Operation:    "MAINTENANCE",
		DurationMS:   120,
		Success:      true,
		ApprovalID:   "app-99",
		Approver:     "alice",
		ErrorCode:    "",
		RowsReturned: 0,
	}
	out, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if got, _ := m["approval_id"].(string); got != "app-99" {
		t.Errorf("approval_id = %v, want app-99", m["approval_id"])
	}
	if got, _ := m["approver"].(string); got != "alice" {
		t.Errorf("approver = %v, want alice", m["approver"])
	}
	// error_code should be omitted (empty).
	if _, ok := m["error_code"]; ok {
		t.Errorf("error_code should be omitted when empty; got %s", out)
	}
}

// TestSanitize_MasksSensitiveParams verifies that DSN-like, password-like and
// raw-SQL parameter values are masked so that credentials / SQL never appear
// in the serialized record.
func TestSanitize_MasksSensitiveParams(t *testing.T) {
	cases := []struct {
		name    string
		params  map[string]any
		leaks   []string // substrings that must NOT appear in the marshalled output
		leakKey bool     // check that the specific key's value is masked to ****
	}{
		{
			name:    "dsn value masked",
			params:  map[string]any{"connection": "postgres://admin:secret123@prod-db:5432/mydb?sslmode=verify-full"},
			leaks:   []string{"secret123", "prod-db"},
			leakKey: false,
		},
		{
			name:    "password key masked",
			params:  map[string]any{"password": "hunter2"},
			leaks:   []string{"hunter2"},
			leakKey: true,
		},
		{
			name:    "sql key masked",
			params:  map[string]any{"sql": "SELECT * FROM orders WHERE password='x'"},
			leaks:   []string{"SELECT"},
			leakKey: true,
		},
		{
			name:   "non-sensitive values kept",
			params: map[string]any{"limit": 100, "schema": "public"},
			leaks:  []string{"****"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := AuditRecord{
				Timestamp: time.Now(),
				RequestID: "req",
				Tool:      "db_query_readonly",
				Target:    "development",
				Operation: "READ",
				Params:    tc.params,
			}
			san := rec.Sanitize()
			out, err := json.Marshal(san)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			for _, leak := range tc.leaks {
				if strings.Contains(string(out), leak) {
					t.Errorf("output contains forbidden substring %q: %s", leak, out)
				}
			}
			if tc.leakKey {
				// The sensitive key must now hold the mask value.
				for k, v := range san.Params {
					if isSensitiveKey(k) {
						if got, _ := v.(string); got != "****" {
							t.Errorf("key %q = %v, want ****", k, v)
						}
					}
				}
			}
		})
	}
}

// TestSanitize_DoesNotMutateOriginal verifies Sanitize returns a copy and does
// not modify the receiver's Params map.
func TestSanitize_DoesNotMutateOriginal(t *testing.T) {
	orig := map[string]any{"password": "hunter2"}
	rec := AuditRecord{Params: orig, Timestamp: time.Now()}
	_ = rec.Sanitize()
	if got, _ := orig["password"].(string); got != "hunter2" {
		t.Errorf("original params mutated: password = %v, want hunter2", orig["password"])
	}
}

// TestSanitize_EmptyParams verifies Sanitize handles a nil/empty Params map and
// fills a zero Timestamp.
func TestSanitize_EmptyParams(t *testing.T) {
	var rec AuditRecord
	if rec.Timestamp != (time.Time{}) {
		t.Fatalf("expected zero timestamp")
	}
	before := time.Now()
	got := rec.Sanitize()
	if got.Timestamp.IsZero() {
		t.Error("Sanitize should fill a zero Timestamp with now")
	}
	if got.Timestamp.Before(before) || got.Timestamp.After(time.Now().Add(time.Second)) {
		t.Errorf("Sanitize timestamp out of range: %v", got.Timestamp)
	}
	if got.Params != nil {
		t.Errorf("expected nil Params, got %v", got.Params)
	}
}
