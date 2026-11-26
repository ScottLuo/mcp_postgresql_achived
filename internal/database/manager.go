// Package database provides connection pool management, SQL execution,
// transaction handling, catalog metadata access, and health checking
// for multiple PostgreSQL targets.
package database

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"postgres-dba-mcp/internal/config"
)

// DatabaseTarget represents a configured PostgreSQL connection target.
// The DSN is not stored after pool creation to prevent credential leakage.
type DatabaseTarget struct {
	Name       string
	AccessMode config.AccessMode
	Pool       *pgxpool.Pool
}

// ErrTargetNotFound is a sentinel error returned when a target alias is unknown.
var ErrTargetNotFound = errors.New("db target not found")

// TargetNotFoundError wraps ErrTargetNotFound with the target name.
type TargetNotFoundError struct {
	Target string
}

// Error implements the error interface.
func (e *TargetNotFoundError) Error() string {
	return fmt.Sprintf("db target not found: %q", e.Target)
}

// Is allows errors.Is to match both the specific type and the sentinel.
func (e *TargetNotFoundError) Is(target error) bool {
	return target == ErrTargetNotFound
}

// NewTargetNotFound creates a TargetNotFoundError for the given target name.
func NewTargetNotFound(name string) *TargetNotFoundError {
	return &TargetNotFoundError{Target: name}
}

// DatabaseManager defines the interface for managing database connections.
type DatabaseManager interface {
	// GetPool returns the connection pool for the named target.
	// Returns *TargetNotFoundError if the target is unknown.
	GetPool(target string) (*pgxpool.Pool, error)

	// GetTarget returns the DatabaseTarget for the named alias.
	// Returns *TargetNotFoundError if the target is unknown.
	GetTarget(name string) (*DatabaseTarget, error)

	// ListTargets returns all configured target aliases in stable order.
	ListTargets() []string

	// HealthCheck pings all targets and returns an error if any
	// required target is unhealthy.
	HealthCheck(ctx context.Context) error

	// Close shuts down all connection pools. Idempotent.
	Close()
}

// manager is the concrete implementation of DatabaseManager.
type manager struct {
	mu           sync.RWMutex
	targets      map[string]*DatabaseTarget
	policy       targetPolicy
	closed       bool
}

// NewManager creates a DatabaseManager by initializing a connection pool
// for each enabled target. Targets are resolved via config.Target.
//
// If all enabled targets fail to ping, an error is returned.
// Partial failures log warnings and continue with the healthy targets.
func NewManager(ctx context.Context, cfg *config.Config, targets []config.Target) (DatabaseManager, error) {
	if len(targets) == 0 {
		return nil, errors.New("database: no targets provided")
	}

	m := &manager{
		targets: make(map[string]*DatabaseTarget, len(targets)),
		policy:  make(targetPolicy, len(targets)),
	}

	var failures []string
	healthyCount := 0

	for _, t := range targets {
		if !t.Enabled {
			continue
		}

		dsn := t.DSN
		if dsn == "" {
			failures = append(failures, fmt.Sprintf("%s: empty DSN", t.Name))
			continue
		}

		pool, err := newPool(ctx, cfg, dsn, t.AccessMode)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: pool init: %v", t.Name, err))
			continue
		}

		// Ping with a short timeout to verify connectivity.
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = pool.Ping(pingCtx)
		cancel()
		if err != nil {
			pool.Close()
			failures = append(failures, fmt.Sprintf("%s: ping: %v", t.Name, err))
			continue
		}

		m.targets[t.Name] = &DatabaseTarget{
			Name:       t.Name,
			AccessMode: t.AccessMode,
			Pool:       pool,
		}
		healthyCount++
	}

	if healthyCount == 0 {
		// Close all pools before returning.
		m.Close()
		return nil, fmt.Errorf("database: all targets failed initialization: %s", joinErrors(failures))
	}

	for _, f := range failures {
		slog.Warn("target initialization failed", "detail", f)
	}

	return m, nil
}

// GetPool implements DatabaseManager.
func (m *manager) GetPool(target string) (*pgxpool.Pool, error) {
	dt, err := m.GetTarget(target)
	if err != nil {
		return nil, err
	}
	return dt.Pool, nil
}

// GetTarget implements DatabaseManager.
func (m *manager) GetTarget(name string) (*DatabaseTarget, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, errors.New("database: manager is closed")
	}
	t, ok := m.targets[name]
	if !ok {
		return nil, NewTargetNotFound(name)
	}
	return t, nil
}

// ListTargets implements DatabaseManager.
func (m *manager) ListTargets() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.targets))
	for name := range m.targets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// HealthCheck is implemented in health.go.

// Close implements DatabaseManager.
func (m *manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return
	}
	m.closed = true

	for name, t := range m.targets {
		slog.Debug("closing pool", "target", name)
		t.Pool.Close()
	}
	m.targets = nil
}

// joinErrors joins a slice of error strings with "; ".
func joinErrors(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for i := 1; i < len(parts); i++ {
		result += "; " + parts[i]
	}
	return result
}
