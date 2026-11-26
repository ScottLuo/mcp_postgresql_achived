package monitoring

import (
	"context"
	"encoding/json"

	"postgres-dba-mcp/internal/mcp"
)

// toolGetConnections is the tool name for the connection-statistics tool.
const toolGetConnections = "db_get_connections"

// connectionStatsSQL computes total/max connections and a per-state breakdown
// from pg_stat_activity.
const connectionStatsSQL = `
WITH states AS (
    SELECT
        count(*) AS total,
        count(*) FILTER (WHERE state = 'active') AS active,
        count(*) FILTER (WHERE state = 'idle') AS idle,
        count(*) FILTER (WHERE state = 'idle in transaction') AS idle_in_transaction,
        count(*) FILTER (WHERE state = 'idle in transaction (aborted)') AS idle_in_transaction_aborted,
        count(*) FILTER (WHERE state = 'disabled') AS disabled
    FROM pg_stat_activity
)
SELECT
    s.total,
    s.active,
    s.idle,
    s.idle_in_transaction,
    s.idle_in_transaction_aborted,
    s.disabled,
    (SELECT setting::int FROM pg_settings WHERE name = 'max_connections') AS max_connections
FROM states s`

// getConnections implements the db_get_connections tool (spec §7.2). It returns
// connection counts, the configured max, and a per-state breakdown.
func (s *Service) getConnections(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args)
	if err != nil {
		return s.fail(ctx, toolGetConnections, "", mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	pool, err := s.resolvePool(a.Target)
	if err != nil {
		return s.fail(ctx, toolGetConnections, a.Target, errCodeForErr(err), err.Error(), 0), nil
	}

	out, err := s.runQuery(ctx, pool, connectionStatsSQL, nil, 1)
	if err != nil {
		return s.fail(ctx, toolGetConnections, a.Target, errCodeForErr(err), err.Error(), out.duration), nil
	}

	data := map[string]any{"connections": out.rows}
	if len(out.rows) > 0 {
		// Surface the key fields at the top level for convenience.
		if row := out.rows[0]; row != nil {
			for _, key := range []string{"total", "active", "idle", "idle_in_transaction", "max_connections"} {
				if v, ok := row[key]; ok {
					data[key] = v
				}
			}
		}
	}

	return s.ok(ctx, toolGetConnections, a.Target, data, out.duration, false, out.execResult.RowsReturned), nil
}

// schemaGetConnections is the JSON Schema for db_get_connections arguments.
func schemaGetConnections() json.RawMessage {
	return json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"target":{"type":"string","description":"Configured database target alias"}},"required":["target"],"additionalProperties":false}`)
}

// connectionsDescription is the user-facing description for db_get_connections.
const connectionsDescription = "Return connection statistics for a target: total connections, per-state breakdown (active, idle, idle-in-transaction) and the configured max_connections. READ-ONLY."

// registerConnections registers the db_get_connections tool with the registry.
func registerConnections(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolGetConnections,
		Description: connectionsDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaGetConnections(),
		Handler:     adapt(s.getConnections),
	})
}
