//go:build unit

package security

import (
	"path/filepath"
	"sort"
	"testing"
)

// containsAll verifies that list contains all elements of want.
func containsAll(list, want []string) bool {
	set := make(map[string]struct{}, len(list))
	for _, item := range list {
		set[item] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; !ok {
			return false
		}
	}
	return true
}

// TestPolicyYAML_DevelopmentTarget verifies development target structure in
// configs/policy.yaml matches spec §8.2.
func TestPolicyYAML_DevelopmentTarget(t *testing.T) {
	engine, err := NewPolicyEngine(filepath.Join("..", "..", "configs", "policy.yaml"))
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}

	dev, ok := engine.cfg.Policies["development"]
	if !ok {
		t.Fatal("development target not found in configs/policy.yaml")
	}

	if dev.DefaultAccess != "read_only" {
		t.Errorf("development.default_access = %q, want read_only", dev.DefaultAccess)
	}

	wantAllowed := []string{"db_query_readonly", "db_explain_query", "db_analyze_table", "db_vacuum_table", "db_create_index"}
	if !containsAll(dev.Allowed, wantAllowed) {
		t.Errorf("development.allowed = %v, want to contain %v", dev.Allowed, wantAllowed)
	}

	wantRequire := []string{"db_drop_index", "db_reindex_table"}
	if !containsAll(dev.RequireApproval, wantRequire) {
		t.Errorf("development.require_approval = %v, want to contain %v", dev.RequireApproval, wantRequire)
	}
}

// TestPolicyYAML_StagingTarget verifies staging target structure in
// configs/policy.yaml matches spec §8.2.
func TestPolicyYAML_StagingTarget(t *testing.T) {
	engine, err := NewPolicyEngine(filepath.Join("..", "..", "configs", "policy.yaml"))
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}

	stg, ok := engine.cfg.Policies["staging"]
	if !ok {
		t.Fatal("staging target not found in configs/policy.yaml")
	}

	if stg.DefaultAccess != "read_only" {
		t.Errorf("staging.default_access = %q, want read_only", stg.DefaultAccess)
	}

	wantAllowed := []string{"db_query_readonly", "db_explain_query", "db_analyze_table"}
	if !containsAll(stg.Allowed, wantAllowed) {
		t.Errorf("staging.allowed = %v, want to contain %v", stg.Allowed, wantAllowed)
	}

	wantRequire := []string{"db_create_index", "db_drop_index", "db_vacuum_table"}
	if !containsAll(stg.RequireApproval, wantRequire) {
		t.Errorf("staging.require_approval = %v, want to contain %v", stg.RequireApproval, wantRequire)
	}
}

// TestPolicyYAML_ProductionTarget verifies production target structure in
// configs/policy.yaml matches spec §8.2.
func TestPolicyYAML_ProductionTarget(t *testing.T) {
	engine, err := NewPolicyEngine(filepath.Join("..", "..", "configs", "policy.yaml"))
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}

	prod, ok := engine.cfg.Policies["production"]
	if !ok {
		t.Fatal("production target not found in configs/policy.yaml")
	}

	if prod.DefaultAccess != "read_only" {
		t.Errorf("production.default_access = %q, want read_only", prod.DefaultAccess)
	}

	wantAllowed := []string{"db_query_readonly", "db_explain_query", "db_get_activity", "db_get_locks"}
	if !containsAll(prod.Allowed, wantAllowed) {
		t.Errorf("production.allowed = %v, want to contain %v", prod.Allowed, wantAllowed)
	}

	wantRequire := []string{"db_analyze_table", "db_vacuum_table", "db_create_index", "db_drop_index"}
	if !containsAll(prod.RequireApproval, wantRequire) {
		t.Errorf("production.require_approval = %v, want to contain %v", prod.RequireApproval, wantRequire)
	}
}

// TestPolicyYAML_ValidYAML verifies the file is parseable and contains the
// expected set of targets.
func TestPolicyYAML_ValidYAML(t *testing.T) {
	engine, err := NewPolicyEngine(filepath.Join("..", "..", "configs", "policy.yaml"))
	if err != nil {
		t.Fatalf("NewPolicyEngine (valid YAML check): %v", err)
	}

	// Verify all three expected targets are present.
	expectedTargets := []string{"development", "staging", "production"}
	var got []string
	for name := range engine.cfg.Policies {
		got = append(got, name)
	}
	sort.Strings(got)
	if len(got) != len(expectedTargets) {
		t.Errorf("configs/policy.yaml targets = %v, want exactly %v (got %d)", got, expectedTargets, len(got))
	}
	for _, target := range expectedTargets {
		if _, ok := engine.cfg.Policies[target]; !ok {
			t.Errorf("configs/policy.yaml missing target %q", target)
		}
	}
}
