package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"postgres-dba-mcp/internal/mcp"
)

const (
	toolReindexTable = "db_reindex_table"
	toolCreateIndex  = "db_create_index"
	toolDropIndex    = "db_drop_index"
)

const reindexDescription = "Rebuild a table index (or all indexes on a table). Supports CONCURRENTLY to avoid blocking reads/writes. Runs outside a transaction block (required for CONCURRENTLY)."

const createIndexDescription = "Create a new index on a table. Supports CONCURRENTLY (non-blocking) mode. Runs outside a transaction block. DDL operations can acquire locks and affect application availability."

const dropIndexDescription = "Drop an existing index. REQUIRES_APPROVAL: explicit human approval is needed before execution. Runs outside a transaction block."

func schemaReindexTable() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "target": { "type": "string", "description": "Configured database target alias." },
    "schema": { "type": "string", "description": "Schema name of the table." },
    "table": { "type": "string", "description": "Table name whose indexes to reindex." },
    "concurrently": { "type": "boolean", "description": "If true, use REINDEX CONCURRENTLY (non-blocking)." },
    "approval_id": { "type": "string", "description": "Approval ID for targets where the policy engine requires approval." }
  },
  "required": ["target", "schema", "table"]
}`)
}

func schemaCreateIndex() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "target": { "type": "string", "description": "Configured database target alias." },
    "schema": { "type": "string", "description": "Schema name of the table." },
    "table": { "type": "string", "description": "Table name on which to create the index." },
    "index_name": { "type": "string", "description": "Name for the new index." },
    "columns": { "type": "array", "items": { "type": "string" }, "description": "Column names to index (in order)." },
    "concurrently": { "type": "boolean", "description": "If true, use CREATE INDEX CONCURRENTLY (non-blocking)." },
    "unique": { "type": "boolean", "description": "If true, create a UNIQUE index." },
    "approval_id": { "type": "string", "description": "Approval ID for targets where the policy engine requires approval." }
  },
  "required": ["target", "schema", "table", "index_name", "columns"]
}`)
}

func schemaDropIndex() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "target": { "type": "string", "description": "Configured database target alias." },
    "schema": { "type": "string", "description": "Schema name of the index." },
    "index": { "type": "string", "description": "Index name to drop." },
    "if_exists": { "type": "boolean", "description": "If true, do not error if the index does not exist." },
    "approval_id": { "type": "string", "description": "Approval ID (required for REQUIRES_APPROVAL targets)." }
  },
  "required": ["target", "schema", "index", "approval_id"]
}`)
}

// reindexArgs holds the parsed arguments for db_reindex_table.
type reindexArgs struct {
	Target       string `json:"target"`
	Schema       string `json:"schema"`
	Table        string `json:"table"`
	Concurrently bool   `json:"concurrently"`
	ApprovalID   string `json:"approval_id"`
}

// createIndexArgs holds the parsed arguments for db_create_index.
type createIndexArgs struct {
	Target       string   `json:"target"`
	Schema       string   `json:"schema"`
	Table        string   `json:"table"`
	IndexName    string   `json:"index_name"`
	Columns      []string `json:"columns"`
	Concurrently bool     `json:"concurrently"`
	Unique       bool     `json:"unique"`
	ApprovalID   string   `json:"approval_id"`
}

// dropIndexArgs holds the parsed arguments for db_drop_index.
type dropIndexArgs struct {
	Target     string `json:"target"`
	Schema     string `json:"schema"`
	Index      string `json:"index"`
	IfExists   bool   `json:"if_exists"`
	ApprovalID string `json:"approval_id"`
}

// reindexTable implements the db_reindex_table handler.
// Uses the non-transactional path for both plain REINDEX and REINDEX CONCURRENTLY.
func (s *Service) reindexTable(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	start := time.Now()

	var a reindexArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return s.errorResult(ctx, toolReindexTable, "", mcp.ErrCodeSQLInvalid,
				fmt.Sprintf("invalid arguments: %v", err), start, ""), nil
		}
	}
	if a.Target == "" {
		return s.errorResult(ctx, toolReindexTable, "", mcp.ErrCodeSQLInvalid,
			"missing required argument \"target\"", start, ""), nil
	}
	if a.Schema == "" {
		return s.errorResult(ctx, toolReindexTable, a.Target, mcp.ErrCodeSQLInvalid,
			"missing required argument \"schema\"", start, a.ApprovalID), nil
	}
	if a.Table == "" {
		return s.errorResult(ctx, toolReindexTable, a.Target, mcp.ErrCodeSQLInvalid,
			"missing required argument \"table\"", start, a.ApprovalID), nil
	}
	if err := validateIdentifier("schema", a.Schema); err != nil {
		return s.errorResult(ctx, toolReindexTable, a.Target, mcp.ErrCodeSQLInvalid, err.Error(), start, a.ApprovalID), nil
	}
	if err := validateIdentifier("table", a.Table); err != nil {
		return s.errorResult(ctx, toolReindexTable, a.Target, mcp.ErrCodeSQLInvalid, err.Error(), start, a.ApprovalID), nil
	}

	pool, err := s.resolvePool(a.Target)
	if err != nil {
		code, msg := s.classifyExecError(err)
		return s.errorResult(ctx, toolReindexTable, a.Target, code, msg, start, a.ApprovalID), nil
	}

	approver, approvalErr := s.checkApproval(ctx, toolReindexTable, a.Target, a.ApprovalID, map[string]any{
		"schema":       a.Schema,
		"table":        a.Table,
		"concurrently": a.Concurrently,
	})
	if approvalErr != nil {
		return approvalErr, nil
	}

	// Build SQL: REINDEX [CONCURRENTLY] TABLE "schema"."table"
	sql := "REINDEX"
	if a.Concurrently {
		sql += " CONCURRENTLY"
	}
	sql += " TABLE " + qualifiedIdent(a.Schema, a.Table)

	// Execute via non-transactional path.
	res, err := s.execNonTransactional(ctx, pool, sql, nil)
	if err != nil {
		code, msg := s.classifyExecError(err)
		return s.errorResult(ctx, toolReindexTable, a.Target, code, msg, start, a.ApprovalID), nil
	}

	data := map[string]any{
		"schema":       a.Schema,
		"table":        a.Table,
		"concurrently": a.Concurrently,
		"command":      "REINDEX",
	}
	return s.successResult(ctx, toolReindexTable, a.Target, data, res, start, a.ApprovalID, approver), nil
}

// createIndex implements the db_create_index handler.
// Uses the non-transactional path (always, since even non-CONCURRENTLY works fine outside a transaction).
func (s *Service) createIndex(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	start := time.Now()

	var a createIndexArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return s.errorResult(ctx, toolCreateIndex, "", mcp.ErrCodeSQLInvalid,
				fmt.Sprintf("invalid arguments: %v", err), start, ""), nil
		}
	}
	if a.Target == "" {
		return s.errorResult(ctx, toolCreateIndex, "", mcp.ErrCodeSQLInvalid,
			"missing required argument \"target\"", start, ""), nil
	}
	if a.Schema == "" {
		return s.errorResult(ctx, toolCreateIndex, a.Target, mcp.ErrCodeSQLInvalid,
			"missing required argument \"schema\"", start, a.ApprovalID), nil
	}
	if a.Table == "" {
		return s.errorResult(ctx, toolCreateIndex, a.Target, mcp.ErrCodeSQLInvalid,
			"missing required argument \"table\"", start, a.ApprovalID), nil
	}
	if a.IndexName == "" {
		return s.errorResult(ctx, toolCreateIndex, a.Target, mcp.ErrCodeSQLInvalid,
			"missing required argument \"index_name\"", start, a.ApprovalID), nil
	}
	if len(a.Columns) == 0 {
		return s.errorResult(ctx, toolCreateIndex, a.Target, mcp.ErrCodeSQLInvalid,
			"missing required argument \"columns\" (must be a non-empty array)", start, a.ApprovalID), nil
	}
	if err := validateIdentifier("schema", a.Schema); err != nil {
		return s.errorResult(ctx, toolCreateIndex, a.Target, mcp.ErrCodeSQLInvalid, err.Error(), start, a.ApprovalID), nil
	}
	if err := validateIdentifier("table", a.Table); err != nil {
		return s.errorResult(ctx, toolCreateIndex, a.Target, mcp.ErrCodeSQLInvalid, err.Error(), start, a.ApprovalID), nil
	}
	if err := validateIdentifier("index_name", a.IndexName); err != nil {
		return s.errorResult(ctx, toolCreateIndex, a.Target, mcp.ErrCodeSQLInvalid, err.Error(), start, a.ApprovalID), nil
	}
	for _, col := range a.Columns {
		if err := validateIdentifier("column", col); err != nil {
			return s.errorResult(ctx, toolCreateIndex, a.Target, mcp.ErrCodeSQLInvalid, err.Error(), start, a.ApprovalID), nil
		}
	}

	pool, err := s.resolvePool(a.Target)
	if err != nil {
		code, msg := s.classifyExecError(err)
		return s.errorResult(ctx, toolCreateIndex, a.Target, code, msg, start, a.ApprovalID), nil
	}

	approver, approvalErr := s.checkApproval(ctx, toolCreateIndex, a.Target, a.ApprovalID, map[string]any{
		"schema":       a.Schema,
		"table":        a.Table,
		"index_name":   a.IndexName,
		"columns":      a.Columns,
		"concurrently": a.Concurrently,
		"unique":       a.Unique,
	})
	if approvalErr != nil {
		return approvalErr, nil
	}

	// Build SQL: CREATE [UNIQUE] INDEX [CONCURRENTLY] "index_name" ON "schema"."table" ("col1", "col2", ...)
	// PostgreSQL grammar: CREATE [ UNIQUE ] INDEX [ CONCURRENTLY ] name ON ...
	// The CONCURRENTLY keyword comes AFTER "INDEX", not before it.
	sql := "CREATE"
	if a.Unique {
		sql += " UNIQUE"
	}
	sql += " INDEX"
	if a.Concurrently {
		sql += " CONCURRENTLY"
	}
	sql += " " + quoteIdent(a.IndexName) + " ON " + qualifiedIdent(a.Schema, a.Table) + " ("
	quotedCols := make([]string, len(a.Columns))
	for i, col := range a.Columns {
		quotedCols[i] = quoteIdent(col)
	}
	sql += strings.Join(quotedCols, ", ") + ")"

	// Execute via non-transactional path.
	res, err := s.execNonTransactional(ctx, pool, sql, nil)
	if err != nil {
		code, msg := s.classifyExecError(err)
		return s.errorResult(ctx, toolCreateIndex, a.Target, code, msg, start, a.ApprovalID), nil
	}

	data := map[string]any{
		"schema":       a.Schema,
		"table":        a.Table,
		"index_name":   a.IndexName,
		"columns":      a.Columns,
		"concurrently": a.Concurrently,
		"unique":       a.Unique,
		"command":      "CREATE INDEX",
	}
	return s.successResult(ctx, toolCreateIndex, a.Target, data, res, start, a.ApprovalID, approver), nil
}

// dropIndex implements the db_drop_index handler.
// This is a REQUIRES_APPROVAL tool.
func (s *Service) dropIndex(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	start := time.Now()

	var a dropIndexArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return s.errorResult(ctx, toolDropIndex, "", mcp.ErrCodeSQLInvalid,
				fmt.Sprintf("invalid arguments: %v", err), start, ""), nil
		}
	}
	if a.Target == "" {
		return s.errorResult(ctx, toolDropIndex, "", mcp.ErrCodeSQLInvalid,
			"missing required argument \"target\"", start, ""), nil
	}
	if a.Schema == "" {
		return s.errorResult(ctx, toolDropIndex, a.Target, mcp.ErrCodeSQLInvalid,
			"missing required argument \"schema\"", start, a.ApprovalID), nil
	}
	if a.Index == "" {
		return s.errorResult(ctx, toolDropIndex, a.Target, mcp.ErrCodeSQLInvalid,
			"missing required argument \"index\"", start, a.ApprovalID), nil
	}
	if err := validateIdentifier("schema", a.Schema); err != nil {
		return s.errorResult(ctx, toolDropIndex, a.Target, mcp.ErrCodeSQLInvalid, err.Error(), start, a.ApprovalID), nil
	}
	if err := validateIdentifier("index", a.Index); err != nil {
		return s.errorResult(ctx, toolDropIndex, a.Target, mcp.ErrCodeSQLInvalid, err.Error(), start, a.ApprovalID), nil
	}

	pool, err := s.resolvePool(a.Target)
	if err != nil {
		code, msg := s.classifyExecError(err)
		return s.errorResult(ctx, toolDropIndex, a.Target, code, msg, start, a.ApprovalID), nil
	}

	approver, approvalErr := s.checkApproval(ctx, toolDropIndex, a.Target, a.ApprovalID, map[string]any{
		"schema":   a.Schema,
		"index":    a.Index,
		"if_exists": a.IfExists,
	})
	if approvalErr != nil {
		return approvalErr, nil
	}

	// Build SQL: DROP INDEX [IF EXISTS] "schema"."index"
	sql := "DROP INDEX"
	if a.IfExists {
		sql += " IF EXISTS"
	}
	sql += " " + qualifiedIdent(a.Schema, a.Index)

	// Execute via non-transactional path.
	res, err := s.execNonTransactional(ctx, pool, sql, nil)
	if err != nil {
		code, msg := s.classifyExecError(err)
		return s.errorResult(ctx, toolDropIndex, a.Target, code, msg, start, a.ApprovalID), nil
	}

	data := map[string]any{
		"schema":    a.Schema,
		"index":     a.Index,
		"if_exists": a.IfExists,
		"command":   "DROP INDEX",
	}
	return s.successResult(ctx, toolDropIndex, a.Target, data, res, start, a.ApprovalID, approver), nil
}

func registerReindexTable(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolReindexTable,
		Description: reindexDescription,
		Permission:  mcp.PermissionControlledMaintenance,
		InputSchema: schemaReindexTable(),
		Handler:     adapt(s.reindexTable),
	})
}

func registerCreateIndex(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolCreateIndex,
		Description: createIndexDescription,
		Permission:  mcp.PermissionControlledMaintenance,
		InputSchema: schemaCreateIndex(),
		Handler:     adapt(s.createIndex),
	})
}

func registerDropIndex(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolDropIndex,
		Description: dropIndexDescription,
		Permission:  mcp.PermissionRequiresApproval,
		InputSchema: schemaDropIndex(),
		Handler:     adapt(s.dropIndex),
	})
}
