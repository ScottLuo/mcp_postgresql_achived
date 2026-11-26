//go:build unit

package config

import (
	"strings"
	"testing"
	"time"
)

// clearPostgresDSNs clears all known POSTGRES_*_DSN vars to prevent cross-test leaks.
func clearPostgresDSNs(t *testing.T) {
	t.Helper()
	for _, d := range []string{
		"POSTGRES_DEVELOPMENT_DSN",
		"POSTGRES_STAGING_DSN",
		"POSTGRES_PRODUCTION_DSN",
	} {
		t.Setenv(d, "")
	}
}

// TestLoad_AllEnvVarsSet (C-01) verifies full config loading when all ENV vars are set.
func TestLoad_AllEnvVarsSet(t *testing.T) {
	clearPostgresDSNs(t)
	t.Setenv("MCP_TRANSPORT", "stdio")
	t.Setenv("POSTGRES_TARGETS", "development,staging")
	t.Setenv("POSTGRES_DEVELOPMENT_DSN", "postgres://dba:secret@localhost:5432/dev?sslmode=disable")
	t.Setenv("POSTGRES_STAGING_DSN", "postgres://dba:secret@staging:5432/stg?sslmode=require")
	t.Setenv("DB_POOL_MIN_CONNS", "3")
	t.Setenv("DB_POOL_MAX_CONNS", "20")
	t.Setenv("DB_QUERY_TIMEOUT", "15s")
	t.Setenv("DB_MAX_ROWS", "500")
	t.Setenv("DB_MAX_RESULT_SIZE", "2097152")
	t.Setenv("MCP_LOG_LEVEL", "debug")
	t.Setenv("MCP_AUDIT_ENABLED", "false")
	t.Setenv("DB_DEFAULT_TARGET", "staging")
	t.Setenv("DB_DEFAULT_ACCESS", "read_write")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.Transport != "stdio" {
		t.Errorf("Transport = %q, want %q", cfg.Transport, "stdio")
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
	if cfg.AuditEnabled {
		t.Errorf("AuditEnabled = true, want false")
	}
	if cfg.DefaultTarget != "staging" {
		t.Errorf("DefaultTarget = %q, want %q", cfg.DefaultTarget, "staging")
	}
	if cfg.DefaultAccessMode != AccessReadWrite {
		t.Errorf("DefaultAccessMode = %q, want %q", cfg.DefaultAccessMode, AccessReadWrite)
	}
	if cfg.Pool.MinConns != 3 {
		t.Errorf("Pool.MinConns = %d, want 3", cfg.Pool.MinConns)
	}
	if cfg.Pool.MaxConns != 20 {
		t.Errorf("Pool.MaxConns = %d, want 20", cfg.Pool.MaxConns)
	}
	if cfg.QueryLimits.Timeout != 15*time.Second {
		t.Errorf("QueryLimits.Timeout = %v, want 15s", cfg.QueryLimits.Timeout)
	}
	if cfg.QueryLimits.MaxRows != 500 {
		t.Errorf("QueryLimits.MaxRows = %d, want 500", cfg.QueryLimits.MaxRows)
	}
	if cfg.QueryLimits.MaxResultSize != 2097152 {
		t.Errorf("QueryLimits.MaxResultSize = %d, want 2097152", cfg.QueryLimits.MaxResultSize)
	}
	if len(cfg.Targets) != 2 {
		t.Fatalf("len(Targets) = %d, want 2", len(cfg.Targets))
	}
}

// TestLoad_MissingRequiredDSN (C-02) verifies a missing target DSN produces a descriptive error.
func TestLoad_MissingRequiredDSN(t *testing.T) {
	clearPostgresDSNs(t)
	t.Setenv("POSTGRES_TARGETS", "development")
	// POSTGRES_DEVELOPMENT_DSN is set to "" by clearPostgresDSNs

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "POSTGRES_DEVELOPMENT_DSN") {
		t.Errorf("error should name the missing variable, got: %v", err)
	}
}

// TestLoad_MissingRequiredTargets verifies POSTGRES_TARGETS being empty produces an error.
func TestLoad_MissingRequiredTargets(t *testing.T) {
	clearPostgresDSNs(t)
	t.Setenv("POSTGRES_TARGETS", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "POSTGRES_TARGETS") {
		t.Errorf("error should name POSTGRES_TARGETS, got: %v", err)
	}
}

// TestLoad_DefaultsApplied (C-03) verifies defaults apply when optional vars are absent.
func TestLoad_DefaultsApplied(t *testing.T) {
	clearPostgresDSNs(t)
	t.Setenv("POSTGRES_TARGETS", "development")
	t.Setenv("POSTGRES_DEVELOPMENT_DSN", "postgres://dba:secret@localhost:5432/dev?sslmode=disable")
	t.Setenv("DB_DEFAULT_TARGET", "development")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.Transport != "stdio" {
		t.Errorf("default Transport = %q, want %q", cfg.Transport, "stdio")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("default LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
	if !cfg.AuditEnabled {
		t.Errorf("default AuditEnabled = false, want true")
	}
	if cfg.DefaultAccessMode != AccessReadOnly {
		t.Errorf("default DefaultAccessMode = %q, want %q", cfg.DefaultAccessMode, AccessReadOnly)
	}
	if cfg.Pool.MinConns != 2 {
		t.Errorf("default Pool.MinConns = %d, want 2", cfg.Pool.MinConns)
	}
	if cfg.Pool.MaxConns != 10 {
		t.Errorf("default Pool.MaxConns = %d, want 10", cfg.Pool.MaxConns)
	}
	if cfg.Pool.MaxConnectionLifetime != time.Hour {
		t.Errorf("default Pool.MaxConnectionLifetime = %v, want 1h", cfg.Pool.MaxConnectionLifetime)
	}
	if cfg.Pool.MaxIdleTime != 15*time.Minute {
		t.Errorf("default Pool.MaxIdleTime = %v, want 15m", cfg.Pool.MaxIdleTime)
	}
	if cfg.QueryLimits.Timeout != 10*time.Second {
		t.Errorf("default QueryLimits.Timeout = %v, want 10s", cfg.QueryLimits.Timeout)
	}
	if cfg.QueryLimits.MaxRows != 1000 {
		t.Errorf("default QueryLimits.MaxRows = %d, want 1000", cfg.QueryLimits.MaxRows)
	}
	if cfg.QueryLimits.MaxResultSize != 1048576 {
		t.Errorf("default QueryLimits.MaxResultSize = %d, want 1048576", cfg.QueryLimits.MaxResultSize)
	}
	if cfg.QueryLimits.MaxSQLLength != 65536 {
		t.Errorf("default QueryLimits.MaxSQLLength = %d, want 65536", cfg.QueryLimits.MaxSQLLength)
	}
}

// TestLoad_InvalidDuration (C-04) verifies duration parse failure is reported.
func TestLoad_InvalidDuration(t *testing.T) {
	clearPostgresDSNs(t)
	t.Setenv("POSTGRES_TARGETS", "development")
	t.Setenv("POSTGRES_DEVELOPMENT_DSN", "postgres://dba:secret@localhost:5432/dev")
	t.Setenv("DB_QUERY_TIMEOUT", "abc")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "DB_QUERY_TIMEOUT") {
		t.Errorf("error should name DB_QUERY_TIMEOUT, got: %v", err)
	}
}

// TestLoad_InvalidInt (C-05) verifies integer parse failure is reported.
func TestLoad_InvalidInt(t *testing.T) {
	clearPostgresDSNs(t)
	t.Setenv("POSTGRES_TARGETS", "development")
	t.Setenv("POSTGRES_DEVELOPMENT_DSN", "postgres://dba:secret@localhost:5432/dev")
	t.Setenv("DB_POOL_MAX_CONNS", "notanumber")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "DB_POOL_MAX_CONNS") {
		t.Errorf("error should name DB_POOL_MAX_CONNS, got: %v", err)
	}
}

// TestLoad_InvalidBool verifies boolean parse failure is reported.
func TestLoad_InvalidBool(t *testing.T) {
	clearPostgresDSNs(t)
	t.Setenv("POSTGRES_TARGETS", "development")
	t.Setenv("POSTGRES_DEVELOPMENT_DSN", "postgres://dba:secret@localhost:5432/dev")
	t.Setenv("MCP_AUDIT_ENABLED", "notabool")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "MCP_AUDIT_ENABLED") {
		t.Errorf("error should name MCP_AUDIT_ENABLED, got: %v", err)
	}
}

// TestLoad_TargetsParser_MultiTarget (C-06) verifies multi-target resolution.
func TestLoad_TargetsParser_MultiTarget(t *testing.T) {
	clearPostgresDSNs(t)
	t.Setenv("POSTGRES_TARGETS", "development,staging,production")
	t.Setenv("POSTGRES_DEVELOPMENT_DSN", "postgres://dba:secret@localhost:5432/dev")
	t.Setenv("POSTGRES_STAGING_DSN", "postgres://dba:secret@staging:5432/stg")
	t.Setenv("POSTGRES_PRODUCTION_DSN", "postgres://dba:secret@prod:5432/prod")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if len(cfg.Targets) != 3 {
		t.Fatalf("len(Targets) = %d, want 3", len(cfg.Targets))
	}
	names := make(map[string]bool)
	for _, tg := range cfg.Targets {
		names[tg.Name] = true
	}
	for _, want := range []string{"development", "staging", "production"} {
		if !names[want] {
			t.Errorf("missing target %q", want)
		}
	}
}

// TestLoad_TargetsParser_SingleTarget (C-07) verifies single target resolution.
func TestLoad_TargetsParser_SingleTarget(t *testing.T) {
	clearPostgresDSNs(t)
	t.Setenv("POSTGRES_TARGETS", "development")
	t.Setenv("POSTGRES_DEVELOPMENT_DSN", "postgres://dba:secret@localhost:5432/dev")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if len(cfg.Targets) != 1 {
		t.Fatalf("len(Targets) = %d, want 1", len(cfg.Targets))
	}
	if cfg.Targets[0].Name != "development" {
		t.Errorf("Target name = %q, want development", cfg.Targets[0].Name)
	}
}

// TestLoad_TargetsParser_EmptyList (C-08) verifies empty target list error.
func TestLoad_TargetsParser_EmptyList(t *testing.T) {
	clearPostgresDSNs(t)
	t.Setenv("POSTGRES_TARGETS", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
}

// TestLoad_InvalidAccessMode (C-09) verifies access mode validation.
func TestLoad_InvalidAccessMode(t *testing.T) {
	clearPostgresDSNs(t)
	t.Setenv("POSTGRES_TARGETS", "development")
	t.Setenv("POSTGRES_DEVELOPMENT_DSN", "postgres://dba:secret@localhost:5432/dev")
	t.Setenv("DB_DEFAULT_ACCESS", "unknown_mode")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown_mode") {
		t.Errorf("error should name the invalid mode, got: %v", err)
	}
}

// TestLoad_InvalidTransport verifies transport validation.
func TestLoad_InvalidTransport(t *testing.T) {
	clearPostgresDSNs(t)
	t.Setenv("POSTGRES_TARGETS", "development")
	t.Setenv("POSTGRES_DEVELOPMENT_DSN", "postgres://dba:secret@localhost:5432/dev")
	t.Setenv("MCP_TRANSPORT", "websocket")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "MCP_TRANSPORT") {
		t.Errorf("error should name MCP_TRANSPORT, got: %v", err)
	}
}

// TestLoad_InvalidLogLevel verifies log level validation.
func TestLoad_InvalidLogLevel(t *testing.T) {
	clearPostgresDSNs(t)
	t.Setenv("POSTGRES_TARGETS", "development")
	t.Setenv("POSTGRES_DEVELOPMENT_DSN", "postgres://dba:secret@localhost:5432/dev")
	t.Setenv("MCP_LOG_LEVEL", "verbose")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "MCP_LOG_LEVEL") {
		t.Errorf("error should name MCP_LOG_LEVEL, got: %v", err)
	}
}

// TestLoad_PoolMinGreaterThanMax verifies pool sanity check.
func TestLoad_PoolMinGreaterThanMax(t *testing.T) {
	clearPostgresDSNs(t)
	t.Setenv("POSTGRES_TARGETS", "development")
	t.Setenv("POSTGRES_DEVELOPMENT_DSN", "postgres://dba:secret@localhost:5432/dev")
	t.Setenv("DB_POOL_MIN_CONNS", "10")
	t.Setenv("DB_POOL_MAX_CONNS", "5")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "DB_POOL_MAX_CONNS") {
		t.Errorf("error should mention pool config, got: %v", err)
	}
}

// TestLoad_DefaultTargetNotInList verifies cross-validation of the default target.
func TestLoad_DefaultTargetNotInList(t *testing.T) {
	clearPostgresDSNs(t)
	t.Setenv("POSTGRES_TARGETS", "development")
	t.Setenv("POSTGRES_DEVELOPMENT_DSN", "postgres://dba:secret@localhost:5432/dev")
	t.Setenv("DB_DEFAULT_TARGET", "staging")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "DB_DEFAULT_TARGET") {
		t.Errorf("error should name DB_DEFAULT_TARGET, got: %v", err)
	}
}

// TestLoad_TargetWithWhitespace verifies target aliases with whitespace are trimmed.
func TestLoad_TargetWithWhitespace(t *testing.T) {
	clearPostgresDSNs(t)
	t.Setenv("POSTGRES_TARGETS", " development , staging ")
	t.Setenv("POSTGRES_DEVELOPMENT_DSN", "postgres://dba:secret@localhost:5432/dev")
	t.Setenv("POSTGRES_STAGING_DSN", "postgres://dba:secret@staging:5432/stg")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if len(cfg.Targets) != 2 {
		t.Fatalf("len(Targets) = %d, want 2", len(cfg.Targets))
	}
	if cfg.Targets[0].Name != "development" {
		t.Errorf("first target name = %q, want development", cfg.Targets[0].Name)
	}
	if cfg.Targets[1].Name != "staging" {
		t.Errorf("second target name = %q, want staging", cfg.Targets[1].Name)
	}
}

// TestLoad_TrailingCommaInTargets verifies error on trailing comma.
func TestLoad_TrailingCommaInTargets(t *testing.T) {
	clearPostgresDSNs(t)
	t.Setenv("POSTGRES_TARGETS", "development,")
	t.Setenv("POSTGRES_DEVELOPMENT_DSN", "postgres://dba:secret@localhost:5432/dev")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
}

// TestLoad_InvalidTargetName verifies invalid alias characters are rejected.
func TestLoad_InvalidTargetName(t *testing.T) {
	clearPostgresDSNs(t)
	t.Setenv("POSTGRES_TARGETS", "dev-prod")
	t.Setenv("POSTGRES_DEV_PROD_DSN", "postgres://dba:secret@localhost:5432/dev")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "dev-prod") {
		t.Errorf("error should name the invalid alias, got: %v", err)
	}
}
