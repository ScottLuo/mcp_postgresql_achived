//go:build unit

package database

import (
	"context"
	"testing"
	"time"
)

func TestTargetPolicy_SetRequired(t *testing.T) {
	p := make(targetPolicy, 2)
	p.SetRequired("dev", "prod")
	if !p.isRequired("dev") {
		t.Error("expected dev to be required")
	}
	if !p.isRequired("prod") {
		t.Error("expected prod to be required")
	}
	// Optional targets are not required when explicitly set.
	p["optional"] = PolicyOptional
	if p.isRequired("optional") {
		t.Error("expected optional to not be required")
	}
}

func TestTargetPolicy_IsRequired_Default(t *testing.T) {
	p := make(targetPolicy, 1)
	p.SetRequired("dev")
	// Unknown targets default to required.
	if !p.isRequired("unknown") {
		t.Error("expected unknown target to default to required")
	}
}

func TestTargetHealth_Struct(t *testing.T) {
	h := TargetHealth{
		Target: "dev",
		OK:     true,
	}
	if h.Target != "dev" {
		t.Errorf("unexpected target: %s", h.Target)
	}
}

func TestHealthCheck_AllHealthy(t *testing.T) {
	// Create a manager with a pool that will fail ping (no real DB).
	// We test the "closed" and "no targets" edge cases here.
	pool := newUnconnectedPool(t)
	m := &manager{
		targets: map[string]*DatabaseTarget{
			"dev": {Name: "dev", Pool: pool},
		},
		policy: make(targetPolicy, 1),
	}
	// Set the policy so "dev" is optional.
	m.policy["dev"] = PolicyOptional

	// HealthCheck will ping the pool which will fail (no real DB on port 1),
	// but since "dev" is optional, no error should be returned.
	err := m.HealthCheck(context.Background())
	if err != nil {
		t.Errorf("expected no error for optional unhealthy target, got: %v", err)
	}
}

func TestHealthCheck_Closed(t *testing.T) {
	m := &manager{
		targets: map[string]*DatabaseTarget{},
		policy:  make(targetPolicy),
		closed:  true,
	}
	err := m.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("expected error for closed manager")
	}
	if !containsStr(err.Error(), "closed") {
		t.Errorf("expected 'closed' in error, got: %v", err)
	}
}

func TestHealthCheck_RequiredUnhealthy(t *testing.T) {
	pool := newUnconnectedPool(t)
	m := &manager{
		targets: map[string]*DatabaseTarget{
			"prod": {Name: "prod", Pool: pool},
		},
		policy: make(targetPolicy, 1),
	}
	// Default: all targets required unless marked optional.
	err := m.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("expected error for required unhealthy target")
	}
	if !containsStr(err.Error(), "prod") {
		t.Errorf("expected 'prod' in error, got: %v", err)
	}
}

func TestHealthPolicy_Values(t *testing.T) {
	if PolicyRequired != 0 {
		t.Errorf("expected PolicyRequired=0, got %d", PolicyRequired)
	}
	if PolicyOptional != 1 {
		t.Errorf("expected PolicyOptional=1, got %d", PolicyOptional)
	}
}

// Ensure the health check timeout doesn't hang.
func TestHealthCheck_Timeout(t *testing.T) {
	pool := newUnconnectedPool(t)
	m := &manager{
		targets: map[string]*DatabaseTarget{
			"dev": {Name: "dev", Pool: pool},
		},
		policy: make(targetPolicy, 1),
	}
	m.policy["dev"] = PolicyOptional

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err := m.HealthCheck(ctx)
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Errorf("health check took too long: %v", elapsed)
	}
	_ = err // May be nil (optional target) or non-nil, both acceptable here.
}
