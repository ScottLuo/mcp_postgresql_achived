package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"postgres-dba-mcp/internal/database"
)

// dbResourceProvider implements mcp.ResourceProvider by delegating to the
// database.DatabaseManager. All returned JSON is safe for MCP resource
// responses — no DSNs, usernames, or passwords are included.
type dbResourceProvider struct {
	mgr    database.DatabaseManager
	logger *slog.Logger
}

// newDBResourceProvider creates a dbResourceProvider backed by the given
// DatabaseManager.
func newDBResourceProvider(mgr database.DatabaseManager, logger *slog.Logger) *dbResourceProvider {
	return &dbResourceProvider{mgr: mgr, logger: logger}
}

// GetTargets returns a JSON array of configured target aliases.
func (p *dbResourceProvider) GetTargets() []string {
	return p.mgr.ListTargets()
}

// GetTargetInfo returns a JSON object with database version and basic info.
func (p *dbResourceProvider) GetTargetInfo(ctx context.Context, target string) (json.RawMessage, error) {
	pool, err := p.mgr.GetPool(target)
	if err != nil {
		return nil, err
	}

	var version string
	err = pool.QueryRow(ctx, "SELECT version()").Scan(&version)
	if err != nil {
		return nil, fmt.Errorf("resource: get target info: %w", err)
	}

	data, _ := json.Marshal(map[string]string{
		"target":  target,
		"version": version,
	})
	return data, nil
}

// GetSchemas returns a JSON array of non-system schema names.
func (p *dbResourceProvider) GetSchemas(ctx context.Context, target string) (json.RawMessage, error) {
	pool, err := p.mgr.GetPool(target)
	if err != nil {
		return nil, err
	}

	rows, err := pool.Query(ctx,
		`SELECT schema_name FROM information_schema.schemata
		 WHERE schema_name NOT LIKE 'pg_%' AND schema_name != 'information_schema'
		 ORDER BY schema_name`)
	if err != nil {
		return nil, fmt.Errorf("resource: list schemas: %w", err)
	}
	defer rows.Close()

	schemas := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("resource: scan schema: %w", err)
		}
		schemas = append(schemas, name)
	}

	data, _ := json.Marshal(map[string]any{
		"target":  target,
		"schemas": schemas,
	})
	return data, nil
}

// GetSchemaInfo returns a JSON object with table and index counts for a schema.
func (p *dbResourceProvider) GetSchemaInfo(ctx context.Context, target, schema string) (json.RawMessage, error) {
	pool, err := p.mgr.GetPool(target)
	if err != nil {
		return nil, err
	}

	var tableCount, indexCount int
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM pg_tables WHERE schemaname = $1`, schema,
	).Scan(&tableCount)
	if err != nil {
		return nil, fmt.Errorf("resource: table count: %w", err)
	}

	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM pg_indexes WHERE schemaname = $1`, schema,
	).Scan(&indexCount)
	if err != nil {
		return nil, fmt.Errorf("resource: index count: %w", err)
	}

	data, _ := json.Marshal(map[string]any{
		"target":      target,
		"schema":      schema,
		"table_count": tableCount,
		"index_count": indexCount,
	})
	return data, nil
}

// GetMonitoringSummary returns a JSON object with connection count and active sessions.
func (p *dbResourceProvider) GetMonitoringSummary(ctx context.Context, target string) (json.RawMessage, error) {
	pool, err := p.mgr.GetPool(target)
	if err != nil {
		return nil, err
	}

	var totalConns, activeConns int
	err = pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()`,
	).Scan(&totalConns)
	if err != nil {
		return nil, fmt.Errorf("resource: total connections: %w", err)
	}

	err = pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND state = 'active'`,
	).Scan(&activeConns)
	if err != nil {
		return nil, fmt.Errorf("resource: active connections: %w", err)
	}

	data, _ := json.Marshal(map[string]any{
		"target":                target,
		"total_connections":     totalConns,
		"active_connections":    activeConns,
	})
	return data, nil
}

// GetHealth returns a JSON object with the health status of the target.
func (p *dbResourceProvider) GetHealth(ctx context.Context, target string) (json.RawMessage, error) {
	pool, err := p.mgr.GetPool(target)
	if err != nil {
		return nil, err
	}

	status := "healthy"
	if err := pool.Ping(ctx); err != nil {
		status = "unhealthy"
	}

	data, _ := json.Marshal(map[string]any{
		"target": target,
		"status": status,
	})
	return data, nil
}

// Ensure dbResourceProvider satisfies the mcp.ResourceProvider interface at
// compile time.
var _ interface {
	GetTargets() []string
	GetTargetInfo(ctx context.Context, target string) (json.RawMessage, error)
	GetSchemas(ctx context.Context, target string) (json.RawMessage, error)
	GetSchemaInfo(ctx context.Context, target, schema string) (json.RawMessage, error)
	GetMonitoringSummary(ctx context.Context, target string) (json.RawMessage, error)
	GetHealth(ctx context.Context, target string) (json.RawMessage, error)
} = (*dbResourceProvider)(nil)
