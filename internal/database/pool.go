package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"postgres-dba-mcp/internal/config"
)

// Privilege levels for connection pools.
// The default pool is read-only.
const (
	// PrivilegeReader is the default read-only pool.
	PrivilegeReader = "reader"
	// PrivilegeMonitor allows reading pg_stat_* views.
	PrivilegeMonitor = "monitor"
	// PrivilegeMaintenance allows VACUUM, ANALYZE, and index maintenance.
	PrivilegeMaintenance = "maintenance"
	// PrivilegeAdmin allows restricted administrative operations.
	PrivilegeAdmin = "admin"
)

// newPool creates a pgxpool.Pool from the DSN and pool configuration.
// The access mode determines whether the pool defaults to read-only transactions.
func newPool(ctx context.Context, cfg *config.Config, dsn string, accessMode config.AccessMode) (*pgxpool.Pool, error) {
	// Parse DSN into pgx config.
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// Sanitize: pgx may embed the DSN in error messages.
		return nil, sanitizePoolError("parse config", err)
	}

	// Apply pool settings from config (data-same-source).
	poolCfg.MinConns = int32(cfg.Pool.MinConns)
	poolCfg.MaxConns = int32(cfg.Pool.MaxConns)
	poolCfg.MaxConnLifetime = cfg.Pool.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.Pool.MaxIdleTime

	// Validate pool settings.
	if poolCfg.MaxConns < poolCfg.MinConns {
		return nil, fmt.Errorf("pool: max_conns (%d) must be >= min_conns (%d)",
			poolCfg.MaxConns, poolCfg.MinConns)
	}
	if poolCfg.MinConns < 0 {
		return nil, errors.New("pool: min_conns must be >= 0")
	}

	// Apply read-only default for read_only access mode.
	// This sets default_transaction_read_only = on on every new connection,
	// providing defense-in-depth alongside the PostgreSQL role.
	if accessMode == config.AccessReadOnly {
		poolCfg.AfterConnect = func(c context.Context, conn *pgx.Conn) error {
			_, err := conn.Exec(c, "SET default_transaction_read_only = on")
			return err
		}
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, sanitizePoolError("create pool", err)
	}
	return pool, nil
}

// sanitizePoolError masks the DSN from pool creation errors.
// pgx errors may embed the DSN string which contains credentials.
func sanitizePoolError(operation string, err error) error {
	// pgx errors that contain a DSN pattern (user:pass@) are masked.
	msg := err.Error()
	if containsDSNPattern(msg) {
		return fmt.Errorf("pool: %s failed: %s", operation, maskDSN(msg))
	}
	return fmt.Errorf("pool: %s failed: %s", operation, msg)
}

// containsDSNPattern returns true if the message likely contains a DSN.
func containsDSNPattern(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '@' {
			return true
		}
	}
	return false
}

// maskDSN replaces the user:password portion of a DSN-like string with "user:****".
func maskDSN(s string) string {
	// Find "://" prefix.
	protoIdx := indexOf(s, "://")
	if protoIdx < 0 {
		return s
	}
	rest := s[protoIdx+3:]
	atIdx := indexOf(rest, "@")
	if atIdx < 0 {
		return s
	}
	creds := rest[:atIdx]
	colonIdx := indexOf(creds, ":")
	if colonIdx < 0 {
		return s
	}
	user := creds[:colonIdx]
	return s[:protoIdx] + "://" + user + ":****" + rest[atIdx:]
}

// indexOf returns the index of sub in s, or -1.
func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Compile-time check that pgx.Conn is used.
var _ = (*pgx.Conn)(nil)
