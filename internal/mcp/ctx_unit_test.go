//go:build unit

package mcp

import (
	"context"
	"reflect"
	"testing"
)

// TestToolContext_Fields verifies all four documented fields are present and
// set correctly by NewToolContext.
func TestToolContext_Fields(t *testing.T) {
	base := context.Background()
	tc := NewToolContext(base, "production", "req-123", "op-456")

	if tc.Context != base {
		t.Error("Context not set to the passed context")
	}
	if tc.Target != "production" {
		t.Errorf("Target = %q, want %q", tc.Target, "production")
	}
	if tc.RequestID != "req-123" {
		t.Errorf("RequestID = %q, want %q", tc.RequestID, "req-123")
	}
	if tc.OperationID != "op-456" {
		t.Errorf("OperationID = %q, want %q", tc.OperationID, "op-456")
	}
}

// TestToolContext_NoSecrets uses reflection to assert the ToolContext struct
// contains only the four documented fields and no field that could carry a
// password, DSN, or credential.
func TestToolContext_NoSecrets(t *testing.T) {
	typ := reflect.TypeOf(ToolContext{})
	if typ.Kind() != reflect.Struct {
		t.Fatalf("ToolContext is not a struct: %s", typ.Kind())
	}

	want := map[string]bool{
		"Context":     true,
		"Target":      true,
		"RequestID":   true,
		"OperationID": true,
	}
	// Any field not in `want` is unexpected and could leak secrets.
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !want[f.Name] {
			t.Errorf("ToolContext has unexpected field %q (could carry secrets)", f.Name)
		}
	}
	for name := range want {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("ToolContext missing documented field %q", name)
		}
	}
}

// TestToolContext_WithAndFromContext verifies the context round-trip.
func TestToolContext_WithAndFromContext(t *testing.T) {
	base := context.Background()
	tc := NewToolContext(base, "staging", "req-1", "op-1")
	ctx := WithToolContext(base, tc)

	got := FromContext(ctx)
	if got != tc {
		t.Fatalf("FromContext returned %p, want %p", got, tc)
	}
	if got.Target != "staging" {
		t.Errorf("Target = %q", got.Target)
	}
}

// TestToolContext_FromContext_Missing verifies FromContext returns nil when
// no ToolContext is stored.
func TestToolContext_FromContext_Missing(t *testing.T) {
	if got := FromContext(context.Background()); got != nil {
		t.Errorf("FromContext on plain context = %v, want nil", got)
	}
}
