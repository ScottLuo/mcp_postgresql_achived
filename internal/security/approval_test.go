//go:build unit

package security

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// mustNormalize is a test helper that normalizes params, failing the test on error.
func mustNormalize(t *testing.T, params map[string]any) string {
	t.Helper()
	s, err := NormalizeParams(params)
	if err != nil {
		t.Fatalf("NormalizeParams(%v) error: %v", params, err)
	}
	return s
}

// TestApprovalValidator_ValidApproval verifies a fresh, fully-matching approval
// passes validation.
func TestApprovalValidator_ValidApproval(t *testing.T) {
	store := NewInMemoryApprovalStore()
	params := mustNormalize(t, map[string]any{"index": "idx_1"})
	record := &ApprovalRecord{
		ID:               "app-001",
		Target:           "production",
		Tool:             "db_drop_index",
		NormalizedParams: params,
		Expiry:           time.Now().Add(10 * time.Minute),
		Approver:         "alice",
		CreatedAt:        time.Now(),
	}
	if err := store.Create(record); err != nil {
		t.Fatalf("Create: %v", err)
	}

	v := NewApprovalValidator(store)
	if err := v.Validate(context.Background(), "app-001", "production", "db_drop_index", params); err != nil {
		t.Fatalf("Validate valid approval: %v", err)
	}
}

// TestApprovalValidator_Expired verifies an expired approval is rejected with
// ErrCodeApprovalInvalid.
func TestApprovalValidator_Expired(t *testing.T) {
	store := NewInMemoryApprovalStore()
	params := mustNormalize(t, map[string]any{"index": "idx_1"})
	record := &ApprovalRecord{
		ID:               "app-exp",
		Target:           "production",
		Tool:             "db_drop_index",
		NormalizedParams: params,
		Expiry:           time.Now().Add(-1 * time.Second),
		Approver:         "alice",
		CreatedAt:        time.Now().Add(-10 * time.Minute),
	}
	if err := store.Create(record); err != nil {
		t.Fatalf("Create: %v", err)
	}

	v := NewApprovalValidator(store)
	err := v.Validate(context.Background(), "app-exp", "production", "db_drop_index", params)
	if err == nil {
		t.Fatal("Validate expired approval: want error, got nil")
	}
	if !strings.Contains(err.Error(), ErrCodeApprovalInvalid) {
		t.Fatalf("Validate expired approval: error = %q, want code %s", err, ErrCodeApprovalInvalid)
	}
}

// TestApprovalValidator_WrongTarget verifies a target mismatch is rejected with
// ErrCodeApprovalInvalid.
func TestApprovalValidator_WrongTarget(t *testing.T) {
	store := NewInMemoryApprovalStore()
	params := mustNormalize(t, map[string]any{"index": "idx_1"})
	record := &ApprovalRecord{
		ID:               "app-target",
		Target:           "development",
		Tool:             "db_drop_index",
		NormalizedParams: params,
		Expiry:           time.Now().Add(10 * time.Minute),
		Approver:         "alice",
		CreatedAt:        time.Now(),
	}
	if err := store.Create(record); err != nil {
		t.Fatalf("Create: %v", err)
	}

	v := NewApprovalValidator(store)
	err := v.Validate(context.Background(), "app-target", "production", "db_drop_index", params)
	if err == nil {
		t.Fatal("Validate wrong target: want error, got nil")
	}
	if !strings.Contains(err.Error(), ErrCodeApprovalInvalid) {
		t.Fatalf("Validate wrong target: error = %q, want code %s", err, ErrCodeApprovalInvalid)
	}
}

// TestApprovalValidator_WrongTool verifies a tool mismatch is rejected with
// ErrCodeApprovalInvalid.
func TestApprovalValidator_WrongTool(t *testing.T) {
	store := NewInMemoryApprovalStore()
	params := mustNormalize(t, map[string]any{"index": "idx_1"})
	record := &ApprovalRecord{
		ID:               "app-tool",
		Target:           "production",
		Tool:             "db_vacuum_table",
		NormalizedParams: params,
		Expiry:           time.Now().Add(10 * time.Minute),
		Approver:         "alice",
		CreatedAt:        time.Now(),
	}
	if err := store.Create(record); err != nil {
		t.Fatalf("Create: %v", err)
	}

	v := NewApprovalValidator(store)
	err := v.Validate(context.Background(), "app-tool", "production", "db_drop_index", params)
	if err == nil {
		t.Fatal("Validate wrong tool: want error, got nil")
	}
	if !strings.Contains(err.Error(), ErrCodeApprovalInvalid) {
		t.Fatalf("Validate wrong tool: error = %q, want code %s", err, ErrCodeApprovalInvalid)
	}
}

// TestApprovalValidator_ParamsChanged verifies that changing the operation
// parameters invalidates an approval.
func TestApprovalValidator_ParamsChanged(t *testing.T) {
	store := NewInMemoryApprovalStore()
	originalParams := mustNormalize(t, map[string]any{"index": "idx_1"})
	changedParams := mustNormalize(t, map[string]any{"index": "idx_2"})
	record := &ApprovalRecord{
		ID:               "app-params",
		Target:           "production",
		Tool:             "db_drop_index",
		NormalizedParams: originalParams,
		Expiry:           time.Now().Add(10 * time.Minute),
		Approver:         "alice",
		CreatedAt:        time.Now(),
	}
	if err := store.Create(record); err != nil {
		t.Fatalf("Create: %v", err)
	}

	v := NewApprovalValidator(store)
	err := v.Validate(context.Background(), "app-params", "production", "db_drop_index", changedParams)
	if err == nil {
		t.Fatal("Validate changed params: want error, got nil")
	}
	if !strings.Contains(err.Error(), ErrCodeApprovalInvalid) {
		t.Fatalf("Validate changed params: error = %q, want code %s", err, ErrCodeApprovalInvalid)
	}
}

// TestApprovalValidator_ParamsKeyOrder verifies that key order does not affect
// normalization: two maps with the same keys but inserted in different orders
// normalize to identical strings.
func TestApprovalValidator_ParamsKeyOrder(t *testing.T) {
	a := mustNormalize(t, map[string]any{"a": "1", "b": "2"})
	b := mustNormalize(t, map[string]any{"b": "2", "a": "1"})
	if a != b {
		t.Fatalf("NormalizeParams key order not deterministic: %q != %q", a, b)
	}
}

// TestApprovalValidator_MissingApproval verifies a non-existent approval ID is
// rejected with ErrCodeApprovalRequired.
func TestApprovalValidator_MissingApproval(t *testing.T) {
	store := NewInMemoryApprovalStore()
	v := NewApprovalValidator(store)

	params := mustNormalize(t, map[string]any{"index": "idx_1"})
	err := v.Validate(context.Background(), "nonexistent-id", "production", "db_drop_index", params)
	if err == nil {
		t.Fatal("Validate missing approval: want error, got nil")
	}
	if !strings.Contains(err.Error(), ErrCodeApprovalRequired) {
		t.Fatalf("Validate missing approval: error = %q, want code %s", err, ErrCodeApprovalRequired)
	}
}

// TestApprovalValidator_Invalidated verifies an invalidated (deleted) approval is
// rejected with ErrCodeApprovalRequired (treated as not found).
func TestApprovalValidator_Invalidated(t *testing.T) {
	store := NewInMemoryApprovalStore()
	params := mustNormalize(t, map[string]any{"index": "idx_1"})
	record := &ApprovalRecord{
		ID:               "app-inval",
		Target:           "production",
		Tool:             "db_drop_index",
		NormalizedParams: params,
		Expiry:           time.Now().Add(10 * time.Minute),
		Approver:         "alice",
		CreatedAt:        time.Now(),
	}
	if err := store.Create(record); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Invalidate("app-inval"); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}

	v := NewApprovalValidator(store)
	err := v.Validate(context.Background(), "app-inval", "production", "db_drop_index", params)
	if err == nil {
		t.Fatal("Validate invalidated approval: want error, got nil")
	}
	if !strings.Contains(err.Error(), ErrCodeApprovalRequired) {
		t.Fatalf("Validate invalidated approval: error = %q, want code %s", err, ErrCodeApprovalRequired)
	}
}

// TestApprovalValidator_ContextCancelled verifies that a cancelled context is
// propagated before the store is consulted.
func TestApprovalValidator_ContextCancelled(t *testing.T) {
	store := NewInMemoryApprovalStore()
	v := NewApprovalValidator(store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	params := mustNormalize(t, map[string]any{"index": "idx_1"})
	err := v.Validate(ctx, "app-001", "production", "db_drop_index", params)
	if err == nil {
		t.Fatal("Validate cancelled context: want error, got nil")
	}
	if !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("Validate cancelled context: error = %q, want context canceled", err)
	}
}

// TestNormalizeParams verifies deterministic JSON serialization of params,
// including edge cases.
func TestNormalizeParams(t *testing.T) {
	// nil and empty maps both normalize to "{}".
	emptyStr, err := NormalizeParams(nil)
	if err != nil {
		t.Fatalf("NormalizeParams(nil): %v", err)
	}
	if emptyStr != "{}" {
		t.Fatalf("NormalizeParams(nil) = %q, want {}", emptyStr)
	}

	emptyMapStr, err := NormalizeParams(map[string]any{})
	if err != nil {
		t.Fatalf("NormalizeParams(empty): %v", err)
	}
	if emptyMapStr != "{}" {
		t.Fatalf("NormalizeParams(empty) = %q, want {}", emptyMapStr)
	}

	// Nested maps are also sorted deterministically.
	nested, err := NormalizeParams(map[string]any{"outer": map[string]any{"z": "1", "a": "2"}})
	if err != nil {
		t.Fatalf("NormalizeParams(nested): %v", err)
	}
	// Go sorts top-level and nested object keys, so this is deterministic.
	if nested != `{"outer":{"a":"2","z":"1"}}` {
		t.Fatalf("NormalizeParams(nested) = %q, want %q", nested, `{"outer":{"a":"2","z":"1"}}`)
	}

	// Determinism across repeated calls.
	first, _ := NormalizeParams(map[string]any{"target": "prod", "tool": "db_drop_index"})
	second, _ := NormalizeParams(map[string]any{"tool": "db_drop_index", "target": "prod"})
	if first != second {
		t.Fatalf("NormalizeParams not deterministic: %q != %q", first, second)
	}
}

// TestInMemoryApprovalStore_Concurrent exercises the store under concurrent
// read/write access. Run with -race to detect data races.
func TestInMemoryApprovalStore_Concurrent(t *testing.T) {
	store := NewInMemoryApprovalStore()

	// Pre-populate some records.
	for i := 0; i < 100; i++ {
		id := string(rune('A' + i%26)) + string(rune('a'+i%26)) + string(rune('0'+i%10))
		_ = store.Create(&ApprovalRecord{
			ID:               id,
			Target:           "production",
			Tool:             "db_drop_index",
			NormalizedParams: "{}",
			Expiry:           time.Now().Add(time.Hour),
			Approver:         "alice",
			CreatedAt:        time.Now(),
		})
	}

	var wg sync.WaitGroup
	// 10 writers.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				id := "write-" + string(rune('0'+i)) + string(rune('0'+j%10))
				_ = store.Create(&ApprovalRecord{
					ID:               id,
					Target:           "production",
					Tool:             "db_drop_index",
					NormalizedParams: "{}",
					Expiry:           time.Now().Add(time.Hour),
					Approver:         "alice",
					CreatedAt:        time.Now(),
				})
			}
		}(i)
	}
	// 10 readers.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = store.Get(string(rune('A' + i%26)) + string(rune('a'+i%26)) + string(rune('0'+j%10)))
				_ = store.Invalidate("write-" + string(rune('0'+i)) + string(rune('0'+j%10)))
			}
		}(i)
	}
	wg.Wait()
}

// TestInMemoryApprovalStore_GetMissing verifies Get returns an error for a
// missing ID.
func TestInMemoryApprovalStore_GetMissing(t *testing.T) {
	store := NewInMemoryApprovalStore()
	_, err := store.Get("no-such-id")
	if err == nil {
		t.Fatal("Get missing id: want error, got nil")
	}
	if !strings.Contains(err.Error(), ErrCodeApprovalRequired) {
		t.Fatalf("Get missing id: error = %q, want code %s", err, ErrCodeApprovalRequired)
	}
}

// TestInMemoryApprovalStore_CreateInvalid verifies Create rejects nil records
// and empty IDs.
func TestInMemoryApprovalStore_CreateInvalid(t *testing.T) {
	store := NewInMemoryApprovalStore()
	if err := store.Create(nil); err == nil {
		t.Fatal("Create(nil): want error, got nil")
	}
	if err := store.Create(&ApprovalRecord{ID: ""}); err == nil {
		t.Fatal("Create(empty id): want error, got nil")
	}
}

// TestInMemoryApprovalStore_OverwriteSameID verifies that creating a record with
// the same ID overwrites the prior one.
func TestInMemoryApprovalStore_OverwriteSameID(t *testing.T) {
	store := NewInMemoryApprovalStore()
	if err := store.Create(&ApprovalRecord{ID: "x", Target: "dev", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Create(&ApprovalRecord{ID: "x", Target: "prod", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("Create overwrite: %v", err)
	}
	got, err := store.Get("x")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Target != "prod" {
		t.Fatalf("Get after overwrite: target = %q, want prod", got.Target)
	}
}

// TestInMemoryApprovalStore_StoreIsolation verifies that the stored record is a
// copy: mutating the caller's record after Create does not affect the store.
func TestInMemoryApprovalStore_StoreIsolation(t *testing.T) {
	store := NewInMemoryApprovalStore()
	original := &ApprovalRecord{ID: "iso", Target: "dev", Expiry: time.Now().Add(time.Hour)}
	if err := store.Create(original); err != nil {
		t.Fatalf("Create: %v", err)
	}
	original.Target = "mutated"
	got, err := store.Get("iso")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Target != "dev" {
		t.Fatalf("stored record not isolated: target = %q, want dev", got.Target)
	}
}
