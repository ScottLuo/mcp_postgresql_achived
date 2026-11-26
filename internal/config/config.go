package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// AccessMode defines the level of database access allowed for a target.
type AccessMode string

// Known access mode values.
const (
	AccessReadOnly          AccessMode = "read_only"
	AccessRestrictedWrite   AccessMode = "restricted_write"
	AccessReadWrite         AccessMode = "read_write"
)

// PoolConfig holds database connection pool settings.
type PoolConfig struct {
	MinConns                int           `json:"min_conns"`
	MaxConns                int           `json:"max_conns"`
	MaxConnectionLifetime   time.Duration `json:"max_connection_lifetime"`
	MaxIdleTime             time.Duration `json:"max_idle_time"`
}

// QueryLimits holds query execution limit settings.
type QueryLimits struct {
	Timeout        time.Duration `json:"timeout"`
	MaxRows        int           `json:"max_rows"`
	MaxResultSize  int           `json:"max_result_size"`  // bytes
	MaxSQLLength   int           `json:"max_sql_length"`   // bytes
}

// Config is the single source of truth for all runtime configuration.
// Downstream modules (database, security, mcp) read values only from this
// struct and never re-parse ENV variables (data-same-source principle).
type Config struct {
	Transport         string      `json:"transport"`
	LogLevel          string      `json:"log_level"`
	AuditEnabled      bool        `json:"audit_enabled"`
	DefaultTarget     string      `json:"default_target"`
	DefaultAccessMode AccessMode  `json:"default_access_mode"`
	Pool              PoolConfig  `json:"pool"`
	QueryLimits       QueryLimits `json:"query_limits"`
	Targets           []Target    `json:"targets"`
}

// defaultPoolConfig returns the default pool configuration.
func defaultPoolConfig() PoolConfig {
	return PoolConfig{
		MinConns:                2,
		MaxConns:                10,
		MaxConnectionLifetime:   time.Hour,
		MaxIdleTime:             15 * time.Minute,
	}
}

// defaultQueryLimits returns the default query limit configuration.
func defaultQueryLimits() QueryLimits {
	return QueryLimits{
		Timeout:       10 * time.Second,
		MaxRows:       1000,
		MaxResultSize: 1048576, // 1 MB
		MaxSQLLength:  65536,   // 64 KB
	}
}

// Load reads all runtime configuration from environment variables and returns
// a fully validated Config. It returns a descriptive error naming the
// missing/invalid variable on failure.
func Load() (*Config, error) {
	// Collect all environment variables into a map for target parsing.
	envVars := make(map[string]string)
	for _, key := range []string{
		"POSTGRES_TARGETS",
		"MCP_TRANSPORT",
		"MCP_LOG_LEVEL",
		"MCP_AUDIT_ENABLED",
		"DB_POOL_MIN_CONNS",
		"DB_POOL_MAX_CONNS",
		"DB_QUERY_TIMEOUT",
		"DB_MAX_ROWS",
		"DB_MAX_RESULT_SIZE",
		"DB_DEFAULT_TARGET",
		"DB_DEFAULT_ACCESS",
	} {
		if val, ok := os.LookupEnv(key); ok {
			envVars[key] = val
		}
	}
	// Add all POSTGRES_*_DSN variables (their names are dynamic, per target alias).
	for _, pair := range os.Environ() {
		name, val, found := strings.Cut(pair, "=")
		if found && strings.HasPrefix(name, "POSTGRES_") && strings.HasSuffix(name, "_DSN") {
			envVars[name] = val
		}
	}

	// --- Required: POSTGRES_TARGETS must be set ---
	if _, err := getRequired("POSTGRES_TARGETS"); err != nil {
		return nil, err
	}

	// --- Parse targets ---
	targets, err := parseTargets(envVars)
	if err != nil {
		return nil, err
	}

	// --- Transport ---
	transport := getOptional("MCP_TRANSPORT", "stdio")
	if err := validateTransport(transport); err != nil {
		return nil, err
	}

	// --- Log level ---
	logLevel := getOptional("MCP_LOG_LEVEL", "info")
	if err := validateLogLevel(logLevel); err != nil {
		return nil, err
	}

	// --- Audit enabled ---
	auditEnabled, err := getOptionalBool("MCP_AUDIT_ENABLED", true)
	if err != nil {
		return nil, err
	}

	// --- Default target ---
	defaultTarget := getOptional("DB_DEFAULT_TARGET", "development")

	// --- Default access mode ---
	defaultAccessStr := getOptional("DB_DEFAULT_ACCESS", "read_only")
	defaultAccess, err := validateAccessMode(defaultAccessStr)
	if err != nil {
		return nil, err
	}

	// --- Pool config ---
	pool := defaultPoolConfig()
	if minConns, err := getOptionalInt("DB_POOL_MIN_CONNS", 0); err != nil {
		return nil, err
	} else if minConns > 0 {
		pool.MinConns = minConns
	}
	if maxConns, err := getOptionalInt("DB_POOL_MAX_CONNS", 0); err != nil {
		return nil, err
	} else if maxConns > 0 {
		pool.MaxConns = maxConns
	}
	if pool.MaxConns < pool.MinConns {
		return nil, fmt.Errorf("DB_POOL_MAX_CONNS (%d) must be >= DB_POOL_MIN_CONNS (%d)", pool.MaxConns, pool.MinConns)
	}

	// --- Query limits ---
	limits := defaultQueryLimits()
	if timeout, err := getOptionalDuration("DB_QUERY_TIMEOUT", 0); err != nil {
		return nil, err
	} else if timeout > 0 {
		limits.Timeout = timeout
	}
	if maxRows, err := getOptionalInt("DB_MAX_ROWS", 0); err != nil {
		return nil, err
	} else if maxRows > 0 {
		limits.MaxRows = maxRows
	}
	if maxResultSize, err := getOptionalInt("DB_MAX_RESULT_SIZE", 0); err != nil {
		return nil, err
	} else if maxResultSize > 0 {
		limits.MaxResultSize = maxResultSize
	}

	// --- Cross-validation: default target must exist in target list ---
	if _, err := ResolveTarget(targets, defaultTarget); err != nil {
		return nil, fmt.Errorf("DB_DEFAULT_TARGET %q: %w", defaultTarget, err)
	}

	return &Config{
		Transport:         transport,
		LogLevel:          logLevel,
		AuditEnabled:      auditEnabled,
		DefaultTarget:     defaultTarget,
		DefaultAccessMode: defaultAccess,
		Pool:              pool,
		QueryLimits:       limits,
		Targets:           targets,
	}, nil
}
