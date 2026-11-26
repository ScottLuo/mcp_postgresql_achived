//go:build unit

package main

import (
	"context"
	"strings"
	"testing"
)

// TestRun_MissingTargets tests that run() returns a clear error when
// POSTGRES_TARGETS is not set.
func TestRun_MissingTargets(t *testing.T) {
	// Clear all relevant env vars.
	for _, k := range []string{
		"POSTGRES_TARGETS", "POSTGRES_DEV_DSN", "POSTGRES_DEVELOPMENT_DSN",
		"POSTGRES_STAGING_DSN", "POSTGRES_PRODUCTION_DSN",
		"DB_POOL_MIN_CONNS", "DB_POOL_MAX_CONNS", "DB_QUERY_TIMEOUT",
		"DB_MAX_ROWS", "DB_MAX_RESULT_SIZE", "DB_DEFAULT_TARGET",
		"DB_DEFAULT_ACCESS", "MCP_TRANSPORT", "MCP_LOG_LEVEL",
		"MCP_AUDIT_ENABLED", "POLICY_FILE",
	} {
		t.Setenv(k, "")
	}
	// Unset the vars (empty string may still be "set" in some Go versions).
	// t.Setenv with empty value is equivalent to setting the var to "".
	// config.Load treats empty POSTGRES_TARGETS as an error.

	err := run(context.Background())
	if err == nil {
		t.Fatal("expected error when POSTGRES_TARGETS is empty, got nil")
	}
	if !strings.Contains(err.Error(), "POSTGRES_TARGETS") {
		t.Errorf("error should mention POSTGRES_TARGETS, got: %v", err)
	}
	if !strings.Contains(err.Error(), appName) {
		t.Errorf("error should mention app name %q, got: %v", appName, err)
	}
}

// TestRun_MissingDSN tests that run() returns a clear error when a target
// is configured but its DSN variable is missing.
func TestRun_MissingDSN(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("POSTGRES_TARGETS", "dev")
	// POSTGRES_DEV_DSN is not set.

	err := run(context.Background())
	if err == nil {
		t.Fatal("expected error when DSN is missing, got nil")
	}
	if !strings.Contains(err.Error(), "POSTGRES_DEV_DSN") {
		t.Errorf("error should mention POSTGRES_DEV_DSN, got: %v", err)
	}
}

// TestRun_InvalidQueryTimeout tests that run() returns a clear error when
// DB_QUERY_TIMEOUT has an invalid duration.
func TestRun_InvalidQueryTimeout(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("POSTGRES_TARGETS", "dev")
	t.Setenv("POSTGRES_DEV_DSN", "postgres://user:pass@localhost:5432/db?sslmode=disable")
	t.Setenv("DB_QUERY_TIMEOUT", "notaduration")

	err := run(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid DB_QUERY_TIMEOUT, got nil")
	}
	if !strings.Contains(err.Error(), "DB_QUERY_TIMEOUT") {
		t.Errorf("error should mention DB_QUERY_TIMEOUT, got: %v", err)
	}
}

// TestRun_InvalidPoolMaxConns tests that run() returns a clear error when
// DB_POOL_MAX_CONNS is not a valid integer.
func TestRun_InvalidPoolMaxConns(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("POSTGRES_TARGETS", "dev")
	t.Setenv("POSTGRES_DEV_DSN", "postgres://user:pass@localhost:5432/db?sslmode=disable")
	t.Setenv("DB_POOL_MAX_CONNS", "notanumber")

	err := run(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid DB_POOL_MAX_CONNS, got nil")
	}
	if !strings.Contains(err.Error(), "DB_POOL_MAX_CONNS") {
		t.Errorf("error should mention DB_POOL_MAX_CONNS, got: %v", err)
	}
}

// TestRun_InvalidAccessMode tests that run() returns a clear error when
// DB_DEFAULT_ACCESS has an unrecognized value.
func TestRun_InvalidAccessMode(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("POSTGRES_TARGETS", "dev")
	t.Setenv("POSTGRES_DEV_DSN", "postgres://user:pass@localhost:5432/db?sslmode=disable")
	t.Setenv("DB_DEFAULT_ACCESS", "unknown_mode")

	err := run(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid DB_DEFAULT_ACCESS, got nil")
	}
	// The error mentions "invalid access mode" and the bad value.
	if !strings.Contains(err.Error(), "access mode") {
		t.Errorf("error should mention access mode, got: %v", err)
	}
	if !strings.Contains(err.Error(), "unknown_mode") {
		t.Errorf("error should mention the invalid value 'unknown_mode', got: %v", err)
	}
}

// TestRun_ValidConfigReachesDatabaseInit tests that with all required ENV
// vars set correctly, run() proceeds past config validation to database
// initialization. Since there's no real database, it will fail at the
// pool init / ping step — but the error should be from the database layer,
// not from config validation.
func TestRun_ValidConfigReachesDatabaseInit(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("POSTGRES_TARGETS", "dev")
	t.Setenv("POSTGRES_DEV_DSN", "postgres://user:pass@localhost:9999/nodb?sslmode=disable&connect_timeout=1")
	t.Setenv("DB_POOL_MIN_CONNS", "1")
	t.Setenv("DB_POOL_MAX_CONNS", "2")
	t.Setenv("DB_QUERY_TIMEOUT", "2s")
	t.Setenv("DB_MAX_ROWS", "100")
	t.Setenv("DB_MAX_RESULT_SIZE", "1048576")
	t.Setenv("DB_DEFAULT_TARGET", "dev")
	t.Setenv("DB_DEFAULT_ACCESS", "read_only")
	t.Setenv("MCP_LOG_LEVEL", "debug")
	t.Setenv("MCP_AUDIT_ENABLED", "false")

	err := run(context.Background())
	if err == nil {
		// If it succeeds (unlikely without a real DB), that's fine too.
		t.Log("run() succeeded (unexpected without real DB)")
		return
	}
	// The error should be from the database layer, not config.
	if strings.Contains(err.Error(), "config:") {
		t.Errorf("error should not be from config layer (all vars set), got: %v", err)
	}
	// It should mention database.
	if !strings.Contains(err.Error(), "database") {
		t.Errorf("error should mention database layer, got: %v", err)
	}
}

// TestNewLogger verifies that newLogger returns non-nil loggers for all
// valid levels.
func TestNewLogger(t *testing.T) {
	tests := []struct {
		name  string
		level string
	}{
		{"info", "info"},
		{"debug", "debug"},
		{"warn", "warn"},
		{"error", "error"},
		{"default (empty)", ""},
		{"invalid level", "verbose"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logger := newLogger(tc.level)
			if logger == nil {
				t.Fatalf("newLogger(%q) returned nil", tc.level)
			}
		})
	}
}

// TestLoadPolicyEngine_MissingFile tests that loadPolicyEngine returns a
// non-nil engine when the policy file does not exist (default-deny mode).
func TestLoadPolicyEngine_MissingFile(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("POLICY_FILE", "/nonexistent/path/policy.yaml")

	engine, err := loadPolicyEngine(context.Background(), nil)
	if err != nil {
		t.Fatalf("loadPolicyEngine with missing file should not error, got: %v", err)
	}
	if engine == nil {
		t.Fatal("loadPolicyEngine returned nil engine")
	}
}

// clearAllEnv resets all environment variables relevant to config.Load.
func clearAllEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"POSTGRES_TARGETS", "POSTGRES_DEV_DSN", "POSTGRES_DEVELOPMENT_DSN",
		"POSTGRES_STAGING_DSN", "POSTGRES_PRODUCTION_DSN",
		"DB_POOL_MIN_CONNS", "DB_POOL_MAX_CONNS", "DB_QUERY_TIMEOUT",
		"DB_MAX_ROWS", "DB_MAX_RESULT_SIZE", "DB_DEFAULT_TARGET",
		"DB_DEFAULT_ACCESS", "MCP_TRANSPORT", "MCP_LOG_LEVEL",
		"MCP_AUDIT_ENABLED", "POLICY_FILE",
	} {
		t.Setenv(k, "")
	}
}

// TestAppVersion_Default verifies that the default version string is "0.1.0".
// This is the value set in main.go that gets overridden by -ldflags at build time.
func TestAppVersion_Default(t *testing.T) {
	if appVersion != "0.1.0" {
		t.Errorf("default appVersion = %q, want %q", appVersion, "0.1.0")
	}
}

// TestAppVersion_Overridable verifies that appVersion is a var (not const),
// so it can be overridden at build time via -ldflags "-X main.appVersion=...".
// The test simulates this by assigning a new value directly.
func TestAppVersion_Overridable(t *testing.T) {
	orig := appVersion
	defer func() { appVersion = orig }()

	appVersion = "0.2.0"
	if appVersion != "0.2.0" {
		t.Errorf("appVersion = %q, want %q (verifying var overridability)", appVersion, "0.2.0")
	}
}

// TestAppName verifies the application name constant.
func TestAppName(t *testing.T) {
	if appName != "postgres-dba-mcp" {
		t.Errorf("appName = %q, want %q", appName, "postgres-dba-mcp")
	}
}
