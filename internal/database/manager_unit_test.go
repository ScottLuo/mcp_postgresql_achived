//go:build unit

package database

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"postgres-dba-mcp/internal/config"
)

// helper creates a pool that is created but not connected (lazy connection).
// Ping will fail when the server is not actually reachable.
func newUnconnectedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, "postgres://user:pass@localhost:1/nonexistent?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("failed to create unconnected pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestNewManager_NoTargets(t *testing.T) {
	cfg := &config.Config{}
	_, err := NewManager(context.Background(), cfg, nil)
	if err == nil {
		t.Fatal("expected error with no targets, got nil")
	}
	if !errors.Is(err, errors.New("database: no targets provided")) && err.Error() != "database: no targets provided" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNewManager_AllDisabledTargets(t *testing.T) {
	cfg := &config.Config{}
	targets := []config.Target{
		{Name: "dev", Enabled: false, DSN: "postgres://u:p@localhost/db"},
	}
	_, err := NewManager(context.Background(), cfg, targets)
	if err == nil {
		t.Fatal("expected error when all targets disabled, got nil")
	}
	if !containsStr(err.Error(), "all targets failed") {
		t.Errorf("expected 'all targets failed' in error, got: %v", err)
	}
}

func TestNewManager_EmptyDSN(t *testing.T) {
	cfg := &config.Config{}
	targets := []config.Target{
		{Name: "dev", Enabled: true, DSN: ""},
	}
	_, err := NewManager(context.Background(), cfg, targets)
	if err == nil {
		t.Fatal("expected error with empty DSN, got nil")
	}
	if !containsStr(err.Error(), "empty DSN") {
		t.Errorf("expected 'empty DSN' in error, got: %v", err)
	}
}

func TestNewManager_InvalidDSN(t *testing.T) {
	cfg := &config.Config{}
	targets := []config.Target{
		{Name: "dev", Enabled: true, DSN: "not_a_valid_dsn"},
	}
	_, err := NewManager(context.Background(), cfg, targets)
	if err == nil {
		t.Fatal("expected error with invalid DSN, got nil")
	}
	if !containsStr(err.Error(), "all targets failed") {
		t.Errorf("expected 'all targets failed' in error, got: %v", err)
	}
}

func TestManager_GetPool_Valid(t *testing.T) {
	pool := newUnconnectedPool(t)
	m := &manager{
		targets: map[string]*DatabaseTarget{
			"dev": {Name: "dev", Pool: pool},
		},
	}
	p, err := m.GetPool("dev")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != pool {
		t.Error("expected same pool pointer")
	}
}

func TestManager_GetPool_Unknown(t *testing.T) {
	m := &manager{
		targets: map[string]*DatabaseTarget{},
	}
	_, err := m.GetPool("unknown")
	if err == nil {
		t.Fatal("expected error for unknown target")
	}
	var tnf *TargetNotFoundError
	if !errors.As(err, &tnf) {
		t.Errorf("expected *TargetNotFoundError, got: %T (%v)", err, err)
	}
}

func TestManager_GetTarget_Closed(t *testing.T) {
	pool := newUnconnectedPool(t)
	m := &manager{
		targets: map[string]*DatabaseTarget{
			"dev": {Name: "dev", Pool: pool},
		},
		closed: true,
	}
	_, err := m.GetTarget("dev")
	if err == nil {
		t.Fatal("expected error for closed manager")
	}
	if !containsStr(err.Error(), "closed") {
		t.Errorf("expected 'closed' in error, got: %v", err)
	}
}

func TestManager_ListTargets(t *testing.T) {
	pool1 := newUnconnectedPool(t)
	pool2 := newUnconnectedPool(t)
	m := &manager{
		targets: map[string]*DatabaseTarget{
			"zeta":    {Name: "zeta", Pool: pool1},
			"alpha":   {Name: "alpha", Pool: pool2},
			"mid":     {Name: "mid", Pool: pool1},
		},
	}
	names := m.ListTargets()
	if len(names) != 3 {
		t.Fatalf("expected 3 targets, got %d", len(names))
	}
	// Should be sorted.
	if names[0] != "alpha" || names[1] != "mid" || names[2] != "zeta" {
		t.Errorf("expected sorted order, got: %v", names)
	}
}

func TestManager_ListTargets_Empty(t *testing.T) {
	m := &manager{
		targets: map[string]*DatabaseTarget{},
	}
	names := m.ListTargets()
	if len(names) != 0 {
		t.Errorf("expected empty, got %v", names)
	}
}

func TestManager_Close_Idempotent(t *testing.T) {
	pool := newUnconnectedPool(t)
	m := &manager{
		targets: map[string]*DatabaseTarget{
			"dev": {Name: "dev", Pool: pool},
		},
	}
	// First close.
	m.Close()
	if !m.closed {
		t.Error("expected closed=true after Close()")
	}
	// Second close should not panic.
	m.Close()
}

func TestManager_Close_NilTargets(t *testing.T) {
	m := &manager{
		targets: map[string]*DatabaseTarget{},
	}
	m.Close()
	if !m.closed {
		t.Error("expected closed=true")
	}
	// Access after close.
	_, err := m.GetTarget("dev")
	if err == nil {
		t.Error("expected error after close")
	}
}

func containsStr(s, substr string) bool {
	return strings.Contains(s, substr)
}
