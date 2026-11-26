package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"postgres-dba-mcp/internal/mcp"
)

const toolAnalyzeTable = "db_analyze_table"

const analyzeDescription = "Execute ANALYZE on a table to collect statistics for the query planner. Runs outside a transaction block for consistency with other maintenance operations."

func schemaAnalyzeTable() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "target": { "type": "string", "description": "Configured database target alias." },
    "schema": { "type": "string", "description": "Schema name of the table." },
    "table": { "type": "string", "description": "Table name to analyze." },
    "approval_id": { "type": "string", "description": "Approval ID for targets where the policy engine requires approval." }
  },
  "required": ["target", "schema", "table"]
}`)
}

// analyzeArgs holds the parsed arguments for db_analyze_table.
type analyzeArgs struct {
	Target     string `json:"target"`
	Schema     string `json:"schema"`
	Table      string `json:"table"`
	ApprovalID string `json:"approval_id"`
}

// analyzeTable implements the db_analyze_table handler.
// Uses the non-transactional execution path for consistency.
func (s *Service) analyzeTable(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	start := time.Now()

	var a analyzeArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return s.errorResult(ctx, toolAnalyzeTable, "", mcp.ErrCodeSQLInvalid,
				fmt.Sprintf("invalid arguments: %v", err), start, ""), nil
		}
	}
	if a.Target == "" {
		return s.errorResult(ctx, toolAnalyzeTable, "", mcp.ErrCodeSQLInvalid,
			"missing required argument \"target\"", start, ""), nil
	}
	if a.Schema == "" {
		return s.errorResult(ctx, toolAnalyzeTable, a.Target, mcp.ErrCodeSQLInvalid,
			"missing required argument \"schema\"", start, a.ApprovalID), nil
	}
	if a.Table == "" {
		return s.errorResult(ctx, toolAnalyzeTable, a.Target, mcp.ErrCodeSQLInvalid,
			"missing required argument \"table\"", start, a.ApprovalID), nil
	}
	if err := validateIdentifier("schema", a.Schema); err != nil {
		return s.errorResult(ctx, toolAnalyzeTable, a.Target, mcp.ErrCodeSQLInvalid, err.Error(), start, a.ApprovalID), nil
	}
	if err := validateIdentifier("table", a.Table); err != nil {
		return s.errorResult(ctx, toolAnalyzeTable, a.Target, mcp.ErrCodeSQLInvalid, err.Error(), start, a.ApprovalID), nil
	}

	pool, err := s.resolvePool(a.Target)
	if err != nil {
		code, msg := s.classifyExecError(err)
		return s.errorResult(ctx, toolAnalyzeTable, a.Target, code, msg, start, a.ApprovalID), nil
	}

	approver, approvalErr := s.checkApproval(ctx, toolAnalyzeTable, a.Target, a.ApprovalID, map[string]any{
		"schema": a.Schema,
		"table":  a.Table,
	})
	if approvalErr != nil {
		return approvalErr, nil
	}

	sql := "ANALYZE " + qualifiedIdent(a.Schema, a.Table)

	// Execute via non-transactional path.
	res, err := s.execNonTransactional(ctx, pool, sql, nil)
	if err != nil {
		code, msg := s.classifyExecError(err)
		return s.errorResult(ctx, toolAnalyzeTable, a.Target, code, msg, start, a.ApprovalID), nil
	}

	data := map[string]any{
		"schema":  a.Schema,
		"table":   a.Table,
		"command": "ANALYZE",
	}
	return s.successResult(ctx, toolAnalyzeTable, a.Target, data, res, start, a.ApprovalID, approver), nil
}

func registerAnalyzeTable(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolAnalyzeTable,
		Description: analyzeDescription,
		Permission:  mcp.PermissionControlledMaintenance,
		InputSchema: schemaAnalyzeTable(),
		Handler:     adapt(s.analyzeTable),
	})
}
