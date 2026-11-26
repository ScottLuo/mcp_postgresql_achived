//go:build unit

package security

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSecurity_PolicyPlusPermission_Consistent verifies that the permission
// level mapping and the policy configuration agree on a known tool.
// Spec: db_drop_index should be REQUIRES_APPROVAL at the permission level
// AND in the require_approval list for production in configs/policy.yaml.
func TestSecurity_PolicyPlusPermission_Consistent(t *testing.T) {
	// Verify permission level mapping.
	level := GetPermissionLevel("db_drop_index")
	if level != PermRequiresApproval {
		t.Fatalf("GetPermissionLevel(db_drop_index) = %q, want %q", level, PermRequiresApproval)
	}

	// Load the actual policy file and verify db_drop_index is in production's require_approval.
	engine, err := NewPolicyEngine(filepath.Join("..", "..", "configs", "policy.yaml"))
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}
	ctx := context.Background()
	decision, err := engine.Check(ctx, "production", "db_drop_index")
	if err != nil {
		t.Fatalf("Check(production, db_drop_index): %v", err)
	}
	if decision != DecisionNeedsApproval {
		t.Fatalf("Check(production, db_drop_index) = %q, want %q", decision, DecisionNeedsApproval)
	}
}

// TestSecurity_SQLValidatorPlusPolicy_EndToEnd simulates the full check path:
// policy allows → SQL validates for SELECT, rejects for UPDATE.
func TestSecurity_SQLValidatorPlusPolicy_EndToEnd(t *testing.T) {
	// 1. Policy allows db_query_readonly on development.
	engine, err := NewPolicyEngine(filepath.Join("..", "..", "configs", "policy.yaml"))
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}
	ctx := context.Background()
	decision, err := engine.Check(ctx, "development", "db_query_readonly")
	if err != nil {
		t.Fatalf("Check(development, db_query_readonly): %v", err)
	}
	if decision != DecisionAllow {
		t.Fatalf("Check = %q, want %q", decision, DecisionAllow)
	}

	// 2. ValidateReadOnly("SELECT 1") returns true.
	v := NewSQLValidator(65536)
	ok, err := v.ValidateReadOnly("SELECT 1")
	if err != nil {
		t.Fatalf("ValidateReadOnly(SELECT 1): unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ValidateReadOnly(SELECT 1) = false, want true")
	}

	// 3. ValidateReadOnly("UPDATE t SET x=1") returns false.
	ok, err = v.ValidateReadOnly("UPDATE t SET x=1")
	if ok {
		t.Fatal("ValidateReadOnly(UPDATE) = true, want false")
	}
	if err == nil {
		t.Fatal("ValidateReadOnly(UPDATE) error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), ErrCodeSQLNotAllowed) {
		t.Fatalf("ValidateReadOnly(UPDATE) error = %q, want code %s", err, ErrCodeSQLNotAllowed)
	}
}

// TestSecurity_ApprovalPlusPolicy verifies that approval is enforced when
// the policy says NeedsApproval, and that a valid approval passes while a
// missing one returns DB_APPROVAL_REQUIRED.
func TestSecurity_ApprovalPlusPolicy(t *testing.T) {
	// 1. Policy says db_drop_index on production requires approval.
	engine, err := NewPolicyEngine(filepath.Join("..", "..", "configs", "policy.yaml"))
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}
	ctx := context.Background()
	decision, err := engine.Check(ctx, "production", "db_drop_index")
	if err != nil {
		t.Fatalf("Check(production, db_drop_index): %v", err)
	}
	if decision != DecisionNeedsApproval {
		t.Fatalf("Check = %q, want %q", decision, DecisionNeedsApproval)
	}

	// 2. A valid approval passes.
	store := NewInMemoryApprovalStore()
	params, err := NormalizeParams(map[string]any{"index": "idx_1"})
	if err != nil {
		t.Fatalf("NormalizeParams: %v", err)
	}
	record := &ApprovalRecord{
		ID:               "app-cross",
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

	validator := NewApprovalValidator(store)
	if err := validator.Validate(ctx, "app-cross", "production", "db_drop_index", params); err != nil {
		t.Fatalf("Validate valid approval: %v", err)
	}

	// 3. No approval → DB_APPROVAL_REQUIRED.
	err = validator.Validate(ctx, "nonexistent-id", "production", "db_drop_index", params)
	if err == nil {
		t.Fatal("Validate missing approval: want error, got nil")
	}
	if !strings.Contains(err.Error(), ErrCodeApprovalRequired) {
		t.Fatalf("Validate missing approval: error = %q, want code %s", err, ErrCodeApprovalRequired)
	}
}
