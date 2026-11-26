package discovery

import (
	"context"
	"encoding/json"
	"time"

	"postgres-dba-mcp/internal/mcp"
)

// ---------------------------------------------------------------------------
// Tool name constant
// ---------------------------------------------------------------------------

const toolListIndexes = "db_list_indexes"

// ---------------------------------------------------------------------------
// Tool description
// ---------------------------------------------------------------------------

const listIndexesDescription = "Return all indexes on a table: name, definition, uniqueness, validity, usage statistics (scan count, tuple reads), and size. READ-ONLY."

// ---------------------------------------------------------------------------
// JSON Schema
// ---------------------------------------------------------------------------

// schemaListIndexes returns the JSON Schema for db_list_indexes.
func schemaListIndexes() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"target": {
				"type": "string",
				"description": "The database target alias to query."
			},
			"schema": {
				"type": "string",
				"description": "The schema name."
			},
			"table": {
				"type": "string",
				"description": "The table name."
			}
		},
		"required": ["target", "schema", "table"]
	}`)
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// listIndexes implements db_list_indexes (spec §7.1).
func (s *Service) listIndexes(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args, true, true)
	if err != nil {
		return s.fail(ctx, toolListIndexes, aOrEmpty(a), mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	start := time.Now()
	indexes, err := s.meta.ListIndexes(ctx, a.Target, a.Schema, a.Table)
	duration := time.Since(start)

	if err != nil {
		return s.fail(ctx, toolListIndexes, a.Target, errCodeForErr(err), err.Error(), duration), nil
	}
	return s.ok(ctx, toolListIndexes, a.Target, indexes, duration, len(indexes)), nil
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

func registerListIndexes(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolListIndexes,
		Description: listIndexesDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaListIndexes(),
		Handler:     adapt(s.listIndexes),
	})
}
