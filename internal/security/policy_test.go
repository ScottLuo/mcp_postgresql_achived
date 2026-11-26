//go:build unit

package security

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// testPolicyConfig returns a PolicyConfig that mirrors configs/policy.yaml for testing.
func testPolicyConfig() PolicyConfig {
	return PolicyConfig{
		Policies: map[string]TargetPolicy{
			"development": {
				DefaultAccess:   "read_only",
				Allowed:         []string{"db_query_readonly", "db_explain_query", "db_analyze_table", "db_vacuum_table", "db_create_index"},
				RequireApproval: []string{"db_drop_index", "db_reindex_table"},
			},
			"staging": {
				DefaultAccess:   "read_only",
				Allowed:         []string{"db_query_readonly", "db_explain_query", "db_analyze_table"},
				RequireApproval: []string{"db_create_index", "db_drop_index", "db_vacuum_table"},
			},
			"production": {
				DefaultAccess:   "read_only",
				Allowed:         []string{"db_query_readonly", "db_explain_query", "db_get_activity", "db_get_locks"},
				RequireApproval: []string{"db_analyze_table", "db_vacuum_table", "db_create_index", "db_drop_index"},
			},
		},
	}
}

func TestPolicyCheck_AllowedTool(t *testing.T) {
	// Arrange
	engine := NewPolicyEngineFromConfig(testPolicyConfig())
	ctx := context.Background()

	// Table-driven test cases
	tests := []struct {
		name     string
		target   string
		tool     string
		expected Decision
	}{
		{"db_query_readonly on development", "development", "db_query_readonly", DecisionAllow},
		{"db_explain_query on development", "development", "db_explain_query", DecisionAllow},
		{"db_analyze_table on development", "development", "db_analyze_table", DecisionAllow},
		{"db_vacuum_table on development", "development", "db_vacuum_table", DecisionAllow},
		{"db_create_index on development", "development", "db_create_index", DecisionAllow},
		{"db_query_readonly on staging", "staging", "db_query_readonly", DecisionAllow},
		{"db_explain_query on staging", "staging", "db_explain_query", DecisionAllow},
		{"db_analyze_table on staging", "staging", "db_analyze_table", DecisionAllow},
		{"db_query_readonly on production", "production", "db_query_readonly", DecisionAllow},
		{"db_explain_query on production", "production", "db_explain_query", DecisionAllow},
		{"db_get_activity on production", "production", "db_get_activity", DecisionAllow},
		{"db_get_locks on production", "production", "db_get_locks", DecisionAllow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			decision, err := engine.Check(ctx, tt.target, tt.tool)

			// Assert
			if err != nil {
				t.Errorf("expected no error, got: %v", err)
			}
			if decision != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, decision)
			}
		})
	}
}

func TestPolicyCheck_RequireApprovalTool(t *testing.T) {
	// Arrange
	engine := NewPolicyEngineFromConfig(testPolicyConfig())
	ctx := context.Background()

	tests := []struct {
		name     string
		target   string
		tool     string
		expected Decision
	}{
		{"db_drop_index on development", "development", "db_drop_index", DecisionNeedsApproval},
		{"db_reindex_table on development", "development", "db_reindex_table", DecisionNeedsApproval},
		{"db_create_index on staging", "staging", "db_create_index", DecisionNeedsApproval},
		{"db_drop_index on staging", "staging", "db_drop_index", DecisionNeedsApproval},
		{"db_vacuum_table on staging", "staging", "db_vacuum_table", DecisionNeedsApproval},
		{"db_analyze_table on production", "production", "db_analyze_table", DecisionNeedsApproval},
		{"db_vacuum_table on production", "production", "db_vacuum_table", DecisionNeedsApproval},
		{"db_create_index on production", "production", "db_create_index", DecisionNeedsApproval},
		{"db_drop_index on production", "production", "db_drop_index", DecisionNeedsApproval},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			decision, err := engine.Check(ctx, tt.target, tt.tool)

			// Assert
			if err != nil {
				t.Errorf("expected no error, got: %v", err)
			}
			if decision != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, decision)
			}
		})
	}
}

func TestPolicyCheck_DenyUnknownTool(t *testing.T) {
	// Arrange
	engine := NewPolicyEngineFromConfig(testPolicyConfig())
	ctx := context.Background()

	// Act
	decision, err := engine.Check(ctx, "development", "db_unknown_tool")

	// Assert
	if err == nil {
		t.Error("expected an error for unknown tool, got nil")
	}
	if decision != DecisionDeny {
		t.Errorf("expected DecisionDeny, got %s", decision)
	}

	// Verify error message contains tool name and target
	if err != nil {
		if !strings.Contains(err.Error(), "db_unknown_tool") {
			t.Errorf("error should mention the tool name, got: %s", err.Error())
		}
		if !strings.Contains(err.Error(), "development") {
			t.Errorf("error should mention the target name, got: %s", err.Error())
		}
	}
}

func TestPolicyCheck_DenyUnknownTarget(t *testing.T) {
	// Arrange
	engine := NewPolicyEngineFromConfig(testPolicyConfig())
	ctx := context.Background()

	// Act
	decision, err := engine.Check(ctx, "nonexistent_target", "db_query_readonly")

	// Assert
	if err == nil {
		t.Error("expected an error for unknown target, got nil")
	}
	if decision != DecisionDeny {
		t.Errorf("expected DecisionDeny, got %s", decision)
	}
	if err != nil && !strings.Contains(err.Error(), "nonexistent_target") {
		t.Errorf("error should mention the target name, got: %s", err.Error())
	}
}

func TestPolicyCheck_EmptyPolicies(t *testing.T) {
	// Arrange: empty policies map
	engine := NewPolicyEngineFromConfig(PolicyConfig{Policies: map[string]TargetPolicy{}})
	ctx := context.Background()

	// Act
	decision, err := engine.Check(ctx, "development", "db_query_readonly")

	// Assert
	if err == nil {
		t.Error("expected an error for target not found in empty policies, got nil")
	}
	if decision != DecisionDeny {
		t.Errorf("expected DecisionDeny, got %s", decision)
	}
}

func TestPolicyCheck_TargetWithNoAllowedList(t *testing.T) {
	// Arrange: target with only require_approval entries
	cfg := PolicyConfig{
		Policies: map[string]TargetPolicy{
			"restricted": {
				DefaultAccess:   "read_only",
				Allowed:         []string{},
				RequireApproval: []string{"db_vacuum_table"},
			},
		},
	}
	engine := NewPolicyEngineFromConfig(cfg)
	ctx := context.Background()

	// Act: tool in require_approval
	decision, err := engine.Check(ctx, "restricted", "db_vacuum_table")
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if decision != DecisionNeedsApproval {
		t.Errorf("expected DecisionNeedsApproval, got %s", decision)
	}

	// Act: tool not in any list
	decision, err = engine.Check(ctx, "restricted", "db_query_readonly")
	if err == nil {
		t.Error("expected an error for tool not in any list, got nil")
	}
	if decision != DecisionDeny {
		t.Errorf("expected DecisionDeny, got %s", decision)
	}
}

func TestNewPolicyEngine_ValidFile(t *testing.T) {
	// Arrange: use the actual configs/policy.yaml
	policyPath := filepath.Join("..", "..", "configs", "policy.yaml")

	// Act
	engine, err := NewPolicyEngine(policyPath)

	// Assert
	if err != nil {
		t.Fatalf("unexpected error loading policy file: %v", err)
	}
	if engine == nil {
		t.Fatal("expected non-nil engine")
	}

	// Verify the engine works with the loaded config
	ctx := context.Background()

	// Test that a known allowed tool works
	decision, err := engine.Check(ctx, "development", "db_query_readonly")
	if err != nil {
		t.Errorf("expected no error for allowed tool, got: %v", err)
	}
	if decision != DecisionAllow {
		t.Errorf("expected DecisionAllow, got %s", decision)
	}

	// Test that a require_approval tool works
	decision, err = engine.Check(ctx, "production", "db_vacuum_table")
	if err != nil {
		t.Errorf("expected no error for require_approval tool, got: %v", err)
	}
	if decision != DecisionNeedsApproval {
		t.Errorf("expected DecisionNeedsApproval, got %s", decision)
	}
}

func TestNewPolicyEngine_FileNotFound(t *testing.T) {
	// Arrange
	nonExistentPath := filepath.Join(t.TempDir(), "nonexistent", "policy.yaml")

	// Act
	engine, err := NewPolicyEngine(nonExistentPath)

	// Assert
	if err == nil {
		t.Fatal("expected an error for non-existent file, got nil")
	}
	if engine != nil {
		t.Error("expected nil engine on error, got non-nil")
	}
	if !strings.Contains(err.Error(), nonExistentPath) {
		t.Errorf("error should mention the file path %q, got: %s", nonExistentPath, err.Error())
	}
}

func TestNewPolicyEngine_MalformedYAML(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	invalidFile := filepath.Join(dir, "policy.yaml")
	content := []byte("policies: {invalid: [unclosed\n")
	if err := os.WriteFile(invalidFile, content, 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Act
	engine, err := NewPolicyEngine(invalidFile)

	// Assert
	if err == nil {
		t.Fatal("expected an error for malformed YAML, got nil")
	}
	if engine != nil {
		t.Error("expected nil engine on error, got non-nil")
	}
	if !strings.Contains(err.Error(), invalidFile) {
		t.Errorf("error should mention the file path, got: %s", err.Error())
	}
}

func TestNewPolicyEngine_ValidYAMLStructure(t *testing.T) {
	// Arrange: write a valid YAML to temp dir and verify full schema compatibility
	dir := t.TempDir()
	policyFile := filepath.Join(dir, "policy.yaml")

	// Use the same content as configs/policy.yaml
	yamlContent := `policies:
  development:
    default_access: read_only
    allowed:
      - db_query_readonly
      - db_explain_query
      - db_analyze_table
      - db_vacuum_table
      - db_create_index
    require_approval:
      - db_drop_index
      - db_reindex_table
  staging:
    default_access: read_only
    allowed:
      - db_query_readonly
      - db_explain_query
      - db_analyze_table
    require_approval:
      - db_create_index
      - db_drop_index
      - db_vacuum_table
  production:
    default_access: read_only
    allowed:
      - db_query_readonly
      - db_explain_query
      - db_get_activity
      - db_get_locks
    require_approval:
      - db_analyze_table
      - db_vacuum_table
      - db_create_index
      - db_drop_index
`
	if err := os.WriteFile(policyFile, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Act
	engine, err := NewPolicyEngine(policyFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Assert: verify the schema is correctly parsed
	expectedCfg := PolicyConfig{
		Policies: map[string]TargetPolicy{
			"development": {
				DefaultAccess:   "read_only",
				Allowed:         []string{"db_query_readonly", "db_explain_query", "db_analyze_table", "db_vacuum_table", "db_create_index"},
				RequireApproval: []string{"db_drop_index", "db_reindex_table"},
			},
			"staging": {
				DefaultAccess:   "read_only",
				Allowed:         []string{"db_query_readonly", "db_explain_query", "db_analyze_table"},
				RequireApproval: []string{"db_create_index", "db_drop_index", "db_vacuum_table"},
			},
			"production": {
				DefaultAccess:   "read_only",
				Allowed:         []string{"db_query_readonly", "db_explain_query", "db_get_activity", "db_get_locks"},
				RequireApproval: []string{"db_analyze_table", "db_vacuum_table", "db_create_index", "db_drop_index"},
			},
		},
	}

	// Verify development target
	dev, ok := engine.cfg.Policies["development"]
	if !ok {
		t.Fatal("development target not found")
	}
	if dev.DefaultAccess != "read_only" {
		t.Errorf("development default_access: expected 'read_only', got '%s'", dev.DefaultAccess)
	}
	if len(dev.Allowed) != 5 {
		t.Errorf("development allowed: expected 5 tools, got %d", len(dev.Allowed))
	}
	if len(dev.RequireApproval) != 2 {
		t.Errorf("development require_approval: expected 2 tools, got %d", len(dev.RequireApproval))
	}

	// Verify production target
	prod, ok := engine.cfg.Policies["production"]
	if !ok {
		t.Fatal("production target not found")
	}
	if prod.DefaultAccess != "read_only" {
		t.Errorf("production default_access: expected 'read_only', got '%s'", prod.DefaultAccess)
	}
	if len(prod.Allowed) != 4 {
		t.Errorf("production allowed: expected 4 tools, got %d", len(prod.Allowed))
	}
	if len(prod.RequireApproval) != 4 {
		t.Errorf("production require_approval: expected 4 tools, got %d", len(prod.RequireApproval))
	}

	// Verify expectedCfg is also valid YAML (round-trip)
	data, err := yaml.Marshal(expectedCfg)
	if err != nil {
		t.Fatalf("failed to marshal expected config: %v", err)
	}
	var roundTrip PolicyConfig
	if err := yaml.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("round-trip unmarshal failed: %v", err)
	}
}

func TestPolicyCheck_ReindexTableOnProduction(t *testing.T) {
	// db_reindex_table is in development's require_approval but NOT in production's
	// allowed or require_approval lists, so it should be denied on production.
	engine := NewPolicyEngineFromConfig(testPolicyConfig())
	ctx := context.Background()

	decision, err := engine.Check(ctx, "production", "db_reindex_table")
	if err == nil {
		t.Error("expected an error for tool not in either list on production, got nil")
	}
	if decision != DecisionDeny {
		t.Errorf("expected DecisionDeny, got %s", decision)
	}
}
