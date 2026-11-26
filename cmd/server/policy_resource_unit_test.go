//go:build unit

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"postgres-dba-mcp/internal/database"
	"postgres-dba-mcp/internal/mcp"
	"postgres-dba-mcp/internal/security"
)

// mockDBManager implements database.DatabaseManager for testing.
type mockDBManager struct {
	targets map[string]*database.DatabaseTarget
}

func (m *mockDBManager) GetPool(target string) (*pgxpool.Pool, error) {
	if t, ok := m.targets[target]; ok {
		return t.Pool, nil
	}
	return nil, database.NewTargetNotFound(target)
}

func (m *mockDBManager) GetTarget(name string) (*database.DatabaseTarget, error) {
	if t, ok := m.targets[name]; ok {
		return t, nil
	}
	return nil, database.NewTargetNotFound(name)
}

func (m *mockDBManager) ListTargets() []string {
	names := make([]string, 0, len(m.targets))
	for name := range m.targets {
		names = append(names, name)
	}
	return names
}

func (m *mockDBManager) HealthCheck(_ context.Context) error { return nil }
func (m *mockDBManager) Close()                               {}

// --- Policy Adapter Tests ---

func TestPolicyAdapter_Allow(t *testing.T) {
	engine := security.NewPolicyEngineFromConfig(security.PolicyConfig{
		Policies: map[string]security.TargetPolicy{
			"dev": {
				DefaultAccess:   "read_only",
				Allowed:         []string{"db_get_info", "db_list_schemas"},
				RequireApproval: []string{},
			},
		},
	})
	adapter := &policyAdapter{engine: engine}

	err := adapter.Check("db_get_info", "dev", mcp.PermissionReadOnly)
	if err != nil {
		t.Errorf("expected nil for allowed tool, got: %v", err)
	}
}

func TestPolicyAdapter_Deny(t *testing.T) {
	engine := security.NewPolicyEngineFromConfig(security.PolicyConfig{
		Policies: map[string]security.TargetPolicy{
			"prod": {
				DefaultAccess:   "read_only",
				Allowed:         []string{"db_get_info"},
				RequireApproval: []string{},
			},
		},
	})
	adapter := &policyAdapter{engine: engine}

	// db_vacuum_table is not in allowed or require_approval for prod.
	err := adapter.Check("db_vacuum_table", "prod", mcp.PermissionControlledMaintenance)
	if err == nil {
		t.Fatal("expected error for denied tool, got nil")
	}
	if !strings.Contains(err.Error(), "not authorized") {
		t.Errorf("expected 'not authorized' in error, got: %v", err)
	}
}

func TestPolicyAdapter_NeedsApproval(t *testing.T) {
	engine := security.NewPolicyEngineFromConfig(security.PolicyConfig{
		Policies: map[string]security.TargetPolicy{
			"dev": {
				DefaultAccess:   "read_only",
				Allowed:         []string{},
				RequireApproval: []string{"db_drop_index"},
			},
		},
	})
	adapter := &policyAdapter{engine: engine}

	// For non-read-only tools with DecisionNeedsApproval, the adapter returns
	// nil (the handler will validate the approval).
	err := adapter.Check("db_drop_index", "dev", mcp.PermissionRequiresApproval)
	if err != nil {
		t.Errorf("expected nil for needs-approval tool, got: %v", err)
	}
}

func TestPolicyAdapter_UnknownTarget(t *testing.T) {
	engine := security.NewPolicyEngineFromConfig(security.PolicyConfig{
		Policies: map[string]security.TargetPolicy{},
	})
	adapter := &policyAdapter{engine: engine}

	err := adapter.Check("db_get_info", "unknown_target", mcp.PermissionReadOnly)
	if err == nil {
		t.Fatal("expected error for unknown target, got nil")
	}
	if !strings.Contains(err.Error(), "not authorized") {
		t.Errorf("expected 'not authorized' in error, got: %v", err)
	}
}

// --- Resource Provider Tests ---

func TestResourceProvider_GetTargets(t *testing.T) {
	mgr := &mockDBManager{
		targets: map[string]*database.DatabaseTarget{
			"dev":  {Name: "dev"},
			"prod": {Name: "prod"},
		},
	}
	p := newDBResourceProvider(mgr, nil)
	targets := p.GetTargets()
	if len(targets) != 2 {
		t.Errorf("expected 2 targets, got %d", len(targets))
	}
}

func TestResourceProvider_GetTargetInfo_UnknownTarget(t *testing.T) {
	mgr := &mockDBManager{
		targets: map[string]*database.DatabaseTarget{},
	}
	p := newDBResourceProvider(mgr, nil)
	_, err := p.GetTargetInfo(context.Background(), "unknown")
	if err == nil {
		t.Fatal("expected error for unknown target")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestResourceProvider_GetSchemas_UnknownTarget(t *testing.T) {
	mgr := &mockDBManager{
		targets: map[string]*database.DatabaseTarget{},
	}
	p := newDBResourceProvider(mgr, nil)
	_, err := p.GetSchemas(context.Background(), "unknown")
	if err == nil {
		t.Fatal("expected error for unknown target")
	}
}

func TestResourceProvider_GetSchemaInfo_UnknownTarget(t *testing.T) {
	mgr := &mockDBManager{
		targets: map[string]*database.DatabaseTarget{},
	}
	p := newDBResourceProvider(mgr, nil)
	_, err := p.GetSchemaInfo(context.Background(), "unknown", "public")
	if err == nil {
		t.Fatal("expected error for unknown target")
	}
}

func TestResourceProvider_GetMonitoringSummary_UnknownTarget(t *testing.T) {
	mgr := &mockDBManager{
		targets: map[string]*database.DatabaseTarget{},
	}
	p := newDBResourceProvider(mgr, nil)
	_, err := p.GetMonitoringSummary(context.Background(), "unknown")
	if err == nil {
		t.Fatal("expected error for unknown target")
	}
}

func TestResourceProvider_GetHealth_UnknownTarget(t *testing.T) {
	mgr := &mockDBManager{
		targets: map[string]*database.DatabaseTarget{},
	}
	p := newDBResourceProvider(mgr, nil)
	_, err := p.GetHealth(context.Background(), "unknown")
	if err == nil {
		t.Fatal("expected error for unknown target")
	}
}

// TestResourceProvider_GetHealth_WithPool tests the health path with a real
// (but unconnected) pool. Ping will fail, so status should be "unhealthy".
func TestResourceProvider_GetHealth_WithPool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, "postgres://user:pass@localhost:1/nonexistent?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	mgr := &mockDBManager{
		targets: map[string]*database.DatabaseTarget{
			"dev": {Name: "dev", Pool: pool},
		},
	}
	p := newDBResourceProvider(mgr, nil)
	data, err := p.GetHealth(ctx, "dev")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(data), "unhealthy") {
		t.Errorf("expected 'unhealthy' in response, got: %s", string(data))
	}
}

// TestResourceProvider_GetTargetInfo_WithPool tests with a real pool.
// The QueryRow will fail since there's no real DB, but the error path is exercised.
func TestResourceProvider_GetTargetInfo_WithPool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, "postgres://user:pass@localhost:1/nonexistent?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	mgr := &mockDBManager{
		targets: map[string]*database.DatabaseTarget{
			"dev": {Name: "dev", Pool: pool},
		},
	}
	p := newDBResourceProvider(mgr, nil)
	_, err = p.GetTargetInfo(ctx, "dev")
	if err == nil {
		// Unlikely without a real DB, but acceptable.
		t.Log("GetTargetInfo succeeded unexpectedly (real DB on port 1?)")
	} else if !strings.Contains(err.Error(), "resource") {
		t.Errorf("expected 'resource' in error prefix, got: %v", err)
	}
}
