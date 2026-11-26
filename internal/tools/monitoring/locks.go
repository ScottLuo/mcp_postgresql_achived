package monitoring

import (
	"context"
	"encoding/json"

	"postgres-dba-mcp/internal/mcp"
)

// toolGetLocks is the tool name for the lock-information tool.
const toolGetLocks = "db_get_locks"

// locksSQL joins pg_locks with pg_stat_activity and pg_class to report
// per-lock detail including the blocking relationship.
//
// Note: pg_locks.waiting was removed in PostgreSQL 13; we report the
// modern wait_event / wait_event_type columns instead (available since 9.6).
const locksSQL = `
SELECT
    l.pid,
    a.usename,
    l.locktype,
    l.mode,
    l.granted,
    a.wait_event,
    a.wait_event_type,
    c.relname          AS relation,
    a.application_name,
    a.query_start,
    a.query
FROM pg_locks l
LEFT JOIN pg_stat_activity a ON a.pid = l.pid
LEFT JOIN pg_class c ON c.oid = l.relation
WHERE l.pid <> pg_backend_pid()
ORDER BY l.granted DESC, l.pid`

// getLocks implements the db_get_locks tool (spec §7.2). It returns current
// lock information joined with the owning session.
func (s *Service) getLocks(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args)
	if err != nil {
		return s.fail(ctx, toolGetLocks, "", mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	pool, err := s.resolvePool(a.Target)
	if err != nil {
		return s.fail(ctx, toolGetLocks, a.Target, errCodeForErr(err), err.Error(), 0), nil
	}

	out, err := s.runQuery(ctx, pool, locksSQL, nil, a.Limit)
	if err != nil {
		return s.fail(ctx, toolGetLocks, a.Target, errCodeForErr(err), err.Error(), out.duration), nil
	}

	return s.ok(ctx, toolGetLocks, a.Target, out.rows, out.duration, out.execResult.Truncated, out.execResult.RowsReturned), nil
}

// schemaGetLocks is the JSON Schema for db_get_locks arguments.
func schemaGetLocks() json.RawMessage {
	return json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"target":{"type":"string","description":"Configured database target alias"},"limit":{"type":"integer","minimum":1,"description":"Maximum number of lock rows to return (optional)"}},"required":["target"],"additionalProperties":false}`)
}

// locksDescription is the user-facing description for db_get_locks.
const locksDescription = "Return current lock information from pg_locks joined with pg_stat_activity. Includes lock type, mode, granted/waiting status and the associated query. READ-ONLY."

// registerLocks registers the db_get_locks tool with the registry.
func registerLocks(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolGetLocks,
		Description: locksDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaGetLocks(),
		Handler:     adapt(s.getLocks),
	})
}
