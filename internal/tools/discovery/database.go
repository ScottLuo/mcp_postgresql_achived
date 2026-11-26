package discovery

import (
	"context"
	"encoding/json"
	"time"

	"postgres-dba-mcp/internal/mcp"
)

// ---------------------------------------------------------------------------
// Tool name constants
// ---------------------------------------------------------------------------

const (
	toolGetInfo       = "db_get_info"
	toolListDatabases = "db_list_databases"
)

// ---------------------------------------------------------------------------
// Tool descriptions
// ---------------------------------------------------------------------------

const getInfoDescription = "Return PostgreSQL server information: version string, database name, and server start time. READ-ONLY."

const listDatabasesDescription = "Return all databases visible to the configured database role, including name, encoding, owner, size, and whether it is the current database. READ-ONLY."

// ---------------------------------------------------------------------------
// JSON Schemas
// ---------------------------------------------------------------------------

// schemaGetInfo returns the JSON Schema for db_get_info.
func schemaGetInfo() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"target": {
				"type": "string",
				"description": "The database target alias to query."
			}
		},
		"required": ["target"]
	}`)
}

// schemaListDatabases returns the JSON Schema for db_list_databases.
func schemaListDatabases() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"target": {
				"type": "string",
				"description": "The database target alias to query."
			}
		},
		"required": ["target"]
	}`)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// getDBInfo implements db_get_info (spec §7.1).
func (s *Service) getDBInfo(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args, false, false)
	if err != nil {
		return s.fail(ctx, toolGetInfo, "", mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	start := time.Now()
	info, err := s.meta.GetInfo(ctx, a.Target)
	duration := time.Since(start)

	if err != nil {
		return s.fail(ctx, toolGetInfo, a.Target, errCodeForErr(err), err.Error(), duration), nil
	}
	return s.ok(ctx, toolGetInfo, a.Target, info, duration, 1), nil
}

// listDatabases implements db_list_databases (spec §7.1).
func (s *Service) listDatabases(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args, false, false)
	if err != nil {
		return s.fail(ctx, toolListDatabases, "", mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	start := time.Now()
	dbs, err := s.meta.ListDatabases(ctx, a.Target)
	duration := time.Since(start)

	if err != nil {
		return s.fail(ctx, toolListDatabases, a.Target, errCodeForErr(err), err.Error(), duration), nil
	}
	return s.ok(ctx, toolListDatabases, a.Target, dbs, duration, len(dbs)), nil
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

func registerGetInfo(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolGetInfo,
		Description: getInfoDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaGetInfo(),
		Handler:     adapt(s.getDBInfo),
	})
}

func registerListDatabases(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolListDatabases,
		Description: listDatabasesDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaListDatabases(),
		Handler:     adapt(s.listDatabases),
	})
}
