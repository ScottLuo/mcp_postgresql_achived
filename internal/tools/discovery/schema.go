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

const toolListSchemas = "db_list_schemas"

// ---------------------------------------------------------------------------
// Tool description
// ---------------------------------------------------------------------------

const listSchemasDescription = "Return all schemas in the current database, including name, owner, and whether it is a system schema. READ-ONLY."

// ---------------------------------------------------------------------------
// JSON Schema
// ---------------------------------------------------------------------------

// schemaListSchemas returns the JSON Schema for db_list_schemas.
func schemaListSchemas() json.RawMessage {
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
// Handler
// ---------------------------------------------------------------------------

// listSchemas implements db_list_schemas (spec §7.1).
func (s *Service) listSchemas(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args, false, false)
	if err != nil {
		return s.fail(ctx, toolListSchemas, "", mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	start := time.Now()
	schemas, err := s.meta.ListSchemas(ctx, a.Target)
	duration := time.Since(start)

	if err != nil {
		return s.fail(ctx, toolListSchemas, a.Target, errCodeForErr(err), err.Error(), duration), nil
	}
	return s.ok(ctx, toolListSchemas, a.Target, schemas, duration, len(schemas)), nil
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

func registerListSchemas(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolListSchemas,
		Description: listSchemasDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaListSchemas(),
		Handler:     adapt(s.listSchemas),
	})
}
