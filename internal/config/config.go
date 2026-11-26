// Package config provides runtime configuration loaded from environment variables.
// It is the single source of truth for pool settings, query limits, and target definitions.
package config

import (
	"errors"
	"fmt"
	"time"
)

// PoolConfig holds connection pool settings.
type PoolConfig struct {
	MinConns            int           `json:"min_conns"`
	MaxConns            int           `json:"max_conns"`
	MaxConnLifetime     time.Duration `json:"max_conn_lifetime"`
	MaxIdleTime         time.Duration `json:"max_idle_time"`
}

// QueryConfig holds query execution limits.
type QueryConfig struct {
	Timeout        time.Duration `json:"timeout"`
	MaxRows        int           `json:"max_rows"`
	MaxResultSize  int           `json:"max_result_size"`
	MaxSQLLength   int           `json:"max_sql_length"`
}

// Config is the top-level application configuration.
type Config struct {
	Transport      string       `json:"transport"`
	Pool           PoolConfig   `json:"pool"`
	Query          QueryConfig  `json:"query"`
	LogLevel       string       `json:"log_level"`
	AuditEnabled   bool         `json:"audit_enabled"`
	DefaultTarget  string       `json:"default_target"`
	DefaultAccess  AccessMode   `json:"default_access"`
}

// AccessMode defines the privilege level for a target.
type AccessMode string

const (
	// AccessReadOnly allows only SELECT queries.
	AccessReadOnly AccessMode = "read_only"
	// AccessRestrictedWrite allows SELECT and limited writes.
	AccessRestrictedWrite AccessMode = "restricted_write"
	// AccessReadWrite allows full read/write access.
	AccessReadWrite AccessMode = "read_write"
)

// Default values as specified in the design document.
const (
	DefaultPoolMinConns          = 2
	DefaultPoolMaxConns          = 10
	DefaultMaxConnLifetime       = 1 * time.Hour
	DefaultMaxIdleTime           = 15 * time.Minute
	DefaultQueryTimeout          = 10 * time.Second
	DefaultMaxRows               = 1000
	DefaultMaxResultSize         = 1048576 // 1 MB
	DefaultMaxSQLLength          = 65536   // 64 KB
	DefaultTransport             = "stdio"
	DefaultLogLevel              = "info"
	DefaultAuditEnabled          = true
	DefaultTarget                = "development"
	DefaultAccess                = AccessReadOnly
)

// Load reads configuration from environment variables and returns a validated Config.
// Missing required variables produce a descriptive error naming the variable.
func Load() (*Config, error) {
	cfg := &Config{
		Transport:     envGetString("MCP_TRANSPORT", DefaultTransport),
		LogLevel:      envGetString("MCP_LOG_LEVEL", DefaultLogLevel),
		AuditEnabled:  envGetBool("MCP_AUDIT_ENABLED", DefaultAuditEnabled),
		DefaultTarget: envGetString("DB_DEFAULT_TARGET", DefaultTarget),
		DefaultAccess: AccessMode(envGetString("DB_DEFAULT_ACCESS", string(DefaultAccess))),
	}

	// Pool settings
	cfg.Pool = PoolConfig{
		MinConns:          envGetInt("DB_POOL_MIN_CONNS", DefaultPoolMinConns),
		MaxConns:          envGetInt("DB_POOL_MAX_CONNS", DefaultPoolMaxConns),
		MaxConnLifetime:   envGetDuration("DB_POOL_MAX_LIFETIME", DefaultMaxConnLifetime),
		MaxIdleTime:       envGetDuration("DB_POOL_MAX_IDLE", DefaultMaxIdleTime),
	}

	// Query limits
	cfg.Query = QueryConfig{
		Timeout:       envGetDuration("DB_QUERY_TIMEOUT", DefaultQueryTimeout),
		MaxRows:       envGetInt("DB_MAX_ROWS", DefaultMaxRows),
		MaxResultSize: envGetInt("DB_MAX_RESULT_SIZE", DefaultMaxResultSize),
		MaxSQLLength:  envGetInt("DB_MAX_SQL_LENGTH", DefaultMaxSQLLength),
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks internal consistency of the configuration.
func (c *Config) Validate() error {
	if c.Pool.MinConns < 1 {
		return errors.New("config: DB_POOL_MIN_CONNS must be >= 1")
	}
	if c.Pool.MaxConns < c.Pool.MinConns {
		return fmt.Errorf("config: DB_POOL_MAX_CONNS (%d) must be >= DB_POOL_MIN_CONNS (%d)",
			c.Pool.MaxConns, c.Pool.MinConns)
	}
	if c.Query.Timeout <= 0 {
		return errors.New("config: DB_QUERY_TIMEOUT must be > 0")
	}
	if c.Query.MaxRows < 1 {
		return errors.New("config: DB_MAX_ROWS must be >= 1")
	}
	if c.Query.MaxResultSize < 1 {
		return errors.New("config: DB_MAX_RESULT_SIZE must be >= 1")
	}
	if c.Query.MaxSQLLength < 1 {
		return errors.New("config: DB_MAX_SQL_LENGTH must be >= 1")
	}
	switch c.DefaultAccess {
	case AccessReadOnly, AccessRestrictedWrite, AccessReadWrite:
	default:
		return fmt.Errorf("config: DB_DEFAULT_ACCESS must be one of %q, %q, %q; got %q",
			AccessReadOnly, AccessRestrictedWrite, AccessReadWrite, c.DefaultAccess)
	}
	return nil
}
