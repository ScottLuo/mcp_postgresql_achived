package database

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// HealthPolicy controls whether a target's failure is fatal at startup.
type HealthPolicy int

const (
	// PolicyRequired means a target failure returns an error.
	PolicyRequired HealthPolicy = iota
	// PolicyOptional means a target failure is logged but not fatal.
	PolicyOptional
)

// TargetHealth represents the health status of a single target.
type TargetHealth struct {
	Target string
	OK     bool
	Err    error
}

// targetPolicy maps target names to their health policy.
// All targets are required by default.
type targetPolicy map[string]HealthPolicy

// SetRequired marks the named targets as required for startup.
// All other targets are optional.
func (p targetPolicy) SetRequired(names ...string) {
	for _, n := range names {
		p[n] = PolicyRequired
	}
}

// isRequired returns true if the target is marked as required.
func (p targetPolicy) isRequired(name string) bool {
	if pol, ok := p[name]; ok {
		return pol == PolicyRequired
	}
	// Default: required.
	return true
}

// HealthCheck pings every target's pool and returns an error if any
// required target is unhealthy. Optional target failures are logged as warnings.
// The returned error (if any) lists unhealthy targets without DSN/credentials.
func (m *manager) HealthCheck(ctx context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return errors.New("database: manager is closed")
	}

	var unhealthy []string

	for name, t := range m.targets {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := t.Pool.Ping(pingCtx)
		cancel()

		if err != nil {
			// Never include DSN in the error.
			unhealthy = append(unhealthy, name)
			if m.policy.isRequired(name) {
				slog.Error("health check: required target unhealthy",
					"target", name, "error", err.Error())
			} else {
				slog.Warn("health check: optional target unhealthy",
					"target", name, "error", err.Error())
			}
		}
	}

	// Build error only from required targets.
	var requiredUnhealthy []string
	for _, name := range unhealthy {
		if m.policy.isRequired(name) {
			requiredUnhealthy = append(requiredUnhealthy, name)
		}
	}

	if len(requiredUnhealthy) > 0 {
		return fmt.Errorf("database: health check failed for required target(s): %s",
			strings.Join(requiredUnhealthy, ", "))
	}
	return nil
}
