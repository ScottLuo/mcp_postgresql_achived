package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"postgres-dba-mcp/internal/mcp"
)

const toolVacuumTable = "db_vacuum_table"

const vacuumDescription = "Execute VACUUM on a table to reclaim storage and update statistics. Runs outside a transaction block (PostgreSQL requirement). Supports optional FULL (rewrites the table) and ANALYZE (collects statistics) modifiers."

func schemaVacuumTable() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "target": { "type": "string", "description": "Configured database target alias." },
    "schema": { "type": "string", "description": "Schema name of the table." },
    "table": { "type": "string", "description": "Table name to vacuum." },
    "full": { "type": "boolean", "description": "If true, use VACUUM FULL (rewrites the table, takes ACCESS EXCLUSIVE lock)." },
    "analyze": { "type": "boolean", "description": "If true, also collect statistics (equivalent to ANALYZE)." },
    "approval_id": { "type": "string", "description": "Approval ID for targets where the policy engine requires approval." }
  },
  "required": ["target", "schema", "table"]
}`)
}

// vacuumArgs holds the parsed arguments for db_vacuum_table.
type vacuumArgs struct {
	Target     string `json:"target"`
	Schema     string `json:"schema"`
	Table      string `json:"table"`
	Full       bool   `json:"full"`
	Analyze    bool   `json:"analyze"`
	ApprovalID string `json:"approval_id"`
}

// vacuumTable implements the db_vacuum_table handler.
// VACUUM cannot run inside a transaction block, so it uses the
// non-transactional execution path.
func (s *Service) vacuumTable(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	start := time.Now()

	var a vacuumArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return s.errorResult(ctx, toolVacuumTable, "", mcp.ErrCodeSQLInvalid,
				fmt.Sprintf("invalid arguments: %v", err), start, ""), nil
		}
	}
	if a.Target == "" {
		return s.errorResult(ctx, toolVacuumTable, "", mcp.ErrCodeSQLInvalid,
			"missing required argument \"target\"", start, ""), nil
	}
	if a.Schema == "" {
		return s.errorResult(ctx, toolVacuumTable, a.Target, mcp.ErrCodeSQLInvalid,
			"missing required argument \"schema\"", start, a.ApprovalID), nil
	}
	if a.Table == "" {
		return s.errorResult(ctx, toolVacuumTable, a.Target, mcp.ErrCodeSQLInvalid,
			"missing required argument \"table\"", start, a.ApprovalID), nil
	}
	if err := validateIdentifier("schema", a.Schema); err != nil {
		return s.errorResult(ctx, toolVacuumTable, a.Target, mcp.ErrCodeSQLInvalid, err.Error(), start, a.ApprovalID), nil
	}
	if err := validateIdentifier("table", a.Table); err != nil {
		return s.errorResult(ctx, toolVacuumTable, a.Target, mcp.ErrCodeSQLInvalid, err.Error(), start, a.ApprovalID), nil
	}

	// Resolve target to pool.
	pool, err := s.resolvePool(a.Target)
	if err != nil {
		code, msg := s.classifyExecError(err)
		return s.errorResult(ctx, toolVacuumTable, a.Target, code, msg, start, a.ApprovalID), nil
	}

	// Check approval if required.
	approver, approvalErr := s.checkApproval(ctx, toolVacuumTable, a.Target, a.ApprovalID, map[string]any{
		"schema": a.Schema,
		"table":  a.Table,
		"full":   a.Full,
		"analyze": a.Analyze,
	})
	if approvalErr != nil {
		return approvalErr, nil
	}

	// Build SQL: VACUUM [FULL] [ANALYZE] "schema"."table"
	sql := "VACUUM"
	if a.Full {
		sql += " FULL"
	}
	if a.Analyze {
		sql += " ANALYZE"
	}
	sql += " " + qualifiedIdent(a.Schema, a.Table)

	// Execute via non-transactional path (VACUUM cannot run in a transaction block).
	res, err := s.execNonTransactional(ctx, pool, sql, nil)
	if err != nil {
		code, msg := s.classifyExecError(err)
		return s.errorResult(ctx, toolVacuumTable, a.Target, code, msg, start, a.ApprovalID), nil
	}

	data := map[string]any{
		"schema":   a.Schema,
		"table":    a.Table,
		"full":     a.Full,
		"analyze":  a.Analyze,
		"command":  "VACUUM",
	}
	return s.successResult(ctx, toolVacuumTable, a.Target, data, res, start, a.ApprovalID, approver), nil
}

func registerVacuumTable(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolVacuumTable,
		Description: vacuumDescription,
		Permission:  mcp.PermissionControlledMaintenance,
		InputSchema: schemaVacuumTable(),
		Handler:     adapt(s.vacuumTable),
	})
}
