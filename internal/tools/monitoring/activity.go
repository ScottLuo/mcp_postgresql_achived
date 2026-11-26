package monitoring

import (
	"context"
	"encoding/json"

	"postgres-dba-mcp/internal/mcp"
)

// toolGetActivity is the tool name for the current-activity tool.
const toolGetActivity = "db_get_activity"

// activitySQL queries pg_stat_activity for the most recent sessions.
// It is read-only and capped by the caller-supplied LIMIT ($1).
const activitySQL = `
SELECT
    pid,
    usename,
    application_name,
    client_addr,
    backend_start,
    query_start,
    state,
    wait_event,
    query
FROM pg_stat_activity
ORDER BY backend_start
LIMIT $1`

// getActivity implements the db_get_activity tool (spec §7.2). It returns the
// currently active and idle sessions, capped by the validated limit. A default
// limit is always enforced (see normalizeLimit).
func (s *Service) getActivity(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args)
	if err != nil {
		return s.fail(ctx, toolGetActivity, "", mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	pool, err := s.resolvePool(a.Target)
	if err != nil {
		return s.fail(ctx, toolGetActivity, a.Target, errCodeForErr(err), err.Error(), 0), nil
	}

	out, err := s.runQuery(ctx, pool, activitySQL, []any{a.Limit}, a.Limit)
	if err != nil {
		return s.fail(ctx, toolGetActivity, a.Target, errCodeForErr(err), err.Error(), out.duration), nil
	}

	return s.ok(ctx, toolGetActivity, a.Target, out.rows, out.duration, out.execResult.Truncated, out.execResult.RowsReturned), nil
}

// schemaGetActivity is the JSON Schema for db_get_activity arguments.
func schemaGetActivity() json.RawMessage {
	return json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"target":{"type":"string","description":"Configured database target alias"},"limit":{"type":"integer","minimum":1,"description":"Maximum number of sessions to return (optional; the server enforces a default cap when omitted)"}},"required":["target"],"additionalProperties":false}`)
}

// activityDescription is the user-facing description for db_get_activity.
const activityDescription = "Return currently active and idle database sessions from pg_stat_activity. The result is capped by the 'limit' argument; when omitted the server enforces a default row cap. READ-ONLY."

// registerActivity registers the db_get_activity tool with the registry.
func registerActivity(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolGetActivity,
		Description: activityDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaGetActivity(),
		Handler:     adapt(s.getActivity),
	})
}
