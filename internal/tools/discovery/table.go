package discovery

import (
	"context"
	"encoding/json"
	"time"

	"postgres-dba-mcp/internal/database"
	"postgres-dba-mcp/internal/mcp"
)

// ---------------------------------------------------------------------------
// Tool name constants
// ---------------------------------------------------------------------------

const (
	toolListTables      = "db_list_tables"
	toolDescribeTable   = "db_describe_table"
	toolListConstraints = "db_list_constraints"
	toolGetTableSize    = "db_get_table_size"
	toolGetColumnStats  = "db_get_column_stats"
)

// ---------------------------------------------------------------------------
// Tool descriptions
// ---------------------------------------------------------------------------

const listTablesDescription = "Return all tables, views, and materialized views in the specified schema, including owner, estimated row count, and whether the table has indexes or foreign keys. READ-ONLY."

const describeTableDescription = "Return the full structure of a table: column definitions (name, type, nullable, primary key), indexes, and constraints. READ-ONLY."

const listConstraintsDescription = "Return all constraints on a table: primary keys, foreign keys, unique constraints, and check constraints, with their SQL definitions. READ-ONLY."

const getTableSizeDescription = "Return the disk size breakdown for a table: total size (table + indexes + TOAST), table-only size, index size, and TOAST size. READ-ONLY."

const getColumnStatsDescription = "Return column statistics from PostgreSQL's catalog (pg_statistic): number of distinct values and most common values. Returns DB_FEATURE_UNAVAILABLE if ANALYZE has not been run on the table. READ-ONLY."

// ---------------------------------------------------------------------------
// JSON Schemas
// ---------------------------------------------------------------------------

// schemaListTables returns the JSON Schema for db_list_tables.
func schemaListTables() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"target": {
				"type": "string",
				"description": "The database target alias to query."
			},
			"schema": {
				"type": "string",
				"description": "The schema name to list tables from."
			}
		},
		"required": ["target", "schema"]
	}`)
}

// schemaDescribeTable returns the JSON Schema for db_describe_table.
func schemaDescribeTable() json.RawMessage {
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

// schemaListConstraints returns the JSON Schema for db_list_constraints.
func schemaListConstraints() json.RawMessage {
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

// schemaGetTableSize returns the JSON Schema for db_get_table_size.
func schemaGetTableSize() json.RawMessage {
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

// schemaGetColumnStats returns the JSON Schema for db_get_column_stats.
func schemaGetColumnStats() json.RawMessage {
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
// Handlers
// ---------------------------------------------------------------------------

// listTables implements db_list_tables (spec §7.1).
func (s *Service) listTables(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args, true, false)
	if err != nil {
		return s.fail(ctx, toolListTables, aOrEmpty(a), mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	start := time.Now()
	tables, err := s.meta.ListTables(ctx, a.Target, a.Schema)
	duration := time.Since(start)

	if err != nil {
		return s.fail(ctx, toolListTables, a.Target, errCodeForErr(err), err.Error(), duration), nil
	}
	return s.ok(ctx, toolListTables, a.Target, tables, duration, len(tables)), nil
}

// describeTable implements db_describe_table (spec §7.1).
// It combines DescribeTable + ListIndexes + ListConstraints into the
// structured response shape defined in spec §7.1.
func (s *Service) describeTable(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args, true, true)
	if err != nil {
		return s.fail(ctx, toolDescribeTable, aOrEmpty(a), mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	start := time.Now()

	columns, err := s.meta.DescribeTable(ctx, a.Target, a.Schema, a.Table)
	if err != nil {
		duration := time.Since(start)
		return s.fail(ctx, toolDescribeTable, a.Target, errCodeForErr(err), err.Error(), duration), nil
	}

	indexes, err := s.meta.ListIndexes(ctx, a.Target, a.Schema, a.Table)
	if err != nil {
		// Indexes are non-fatal: report columns even if index query fails.
		indexes = []database.IndexInfo{}
	}

	constraints, err := s.meta.ListConstraints(ctx, a.Target, a.Schema, a.Table)
	if err != nil {
		// Constraints are non-fatal: report columns even if constraint query fails.
		constraints = []database.ConstraintInfo{}
	}

	duration := time.Since(start)

	// Assemble the spec §7.1 response shape.
	data := struct {
		Schema      string                    `json:"schema"`
		Table       string                    `json:"table"`
		Columns     []database.ColumnInfo     `json:"columns"`
		Indexes     []database.IndexInfo      `json:"indexes"`
		Constraints []database.ConstraintInfo `json:"constraints"`
	}{
		Schema:      a.Schema,
		Table:       a.Table,
		Columns:     columns,
		Indexes:     indexes,
		Constraints: constraints,
	}

	return s.ok(ctx, toolDescribeTable, a.Target, data, duration, len(columns)), nil
}

// listConstraints implements db_list_constraints (spec §7.1).
func (s *Service) listConstraints(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args, true, true)
	if err != nil {
		return s.fail(ctx, toolListConstraints, aOrEmpty(a), mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	start := time.Now()
	constraints, err := s.meta.ListConstraints(ctx, a.Target, a.Schema, a.Table)
	duration := time.Since(start)

	if err != nil {
		return s.fail(ctx, toolListConstraints, a.Target, errCodeForErr(err), err.Error(), duration), nil
	}
	return s.ok(ctx, toolListConstraints, a.Target, constraints, duration, len(constraints)), nil
}

// getTableSize implements db_get_table_size (spec §7.1).
func (s *Service) getTableSize(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args, true, true)
	if err != nil {
		return s.fail(ctx, toolGetTableSize, aOrEmpty(a), mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	start := time.Now()
	size, err := s.meta.GetTableSize(ctx, a.Target, a.Schema, a.Table)
	duration := time.Since(start)

	if err != nil {
		return s.fail(ctx, toolGetTableSize, a.Target, errCodeForErr(err), err.Error(), duration), nil
	}
	return s.ok(ctx, toolGetTableSize, a.Target, size, duration, 1), nil
}

// getColumnStats implements db_get_column_stats (spec §7.1).
func (s *Service) getColumnStats(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args, true, true)
	if err != nil {
		return s.fail(ctx, toolGetColumnStats, aOrEmpty(a), mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	start := time.Now()
	stats, err := s.meta.GetColumnStats(ctx, a.Target, a.Schema, a.Table)
	duration := time.Since(start)

	if err != nil {
		return s.fail(ctx, toolGetColumnStats, a.Target, errCodeForErr(err), err.Error(), duration), nil
	}
	return s.ok(ctx, toolGetColumnStats, a.Target, stats, duration, len(stats)), nil
}

// aOrEmpty extracts the Target from a possibly-nil baseArgs safely.
func aOrEmpty(a *baseArgs) string {
	if a == nil {
		return ""
	}
	return a.Target
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

func registerListTables(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolListTables,
		Description: listTablesDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaListTables(),
		Handler:     adapt(s.listTables),
	})
}

func registerDescribeTable(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolDescribeTable,
		Description: describeTableDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaDescribeTable(),
		Handler:     adapt(s.describeTable),
	})
}

func registerListConstraints(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolListConstraints,
		Description: listConstraintsDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaListConstraints(),
		Handler:     adapt(s.listConstraints),
	})
}

func registerGetTableSize(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolGetTableSize,
		Description: getTableSizeDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaGetTableSize(),
		Handler:     adapt(s.getTableSize),
	})
}

func registerGetColumnStats(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolGetColumnStats,
		Description: getColumnStatsDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaGetColumnStats(),
		Handler:     adapt(s.getColumnStats),
	})
}
