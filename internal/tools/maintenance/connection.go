package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"postgres-dba-mcp/internal/mcp"
)

const (
	toolCancelQuery          = "db_cancel_query"
	toolTerminateConnection  = "db_terminate_connection"
)

const cancelQueryDescription = "Request cancellation of a running query by its process ID (PID). The target backend will receive a cancel signal; it may take a moment to take effect. Requires appropriate PostgreSQL privileges (mapped to DB_PERMISSION_DENIED on insufficient privilege)."

const terminateConnectionDescription = "Terminate a database connection by its process ID (PID). The backend is immediately disconnected. REQUIRES_APPROVAL: explicit human approval is needed. Requires appropriate PostgreSQL privileges (mapped to DB_PERMISSION_DENIED on insufficient privilege)."

func schemaCancelQuery() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "target": { "type": "string", "description": "Configured database target alias." },
    "pid": { "type": "integer", "description": "Process ID of the backend to cancel." },
    "approval_id": { "type": "string", "description": "Approval ID for targets where the policy engine requires approval." }
  },
  "required": ["target", "pid"]
}`)
}

func schemaTerminateConnection() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "target": { "type": "string", "description": "Configured database target alias." },
    "pid": { "type": "integer", "description": "Process ID of the backend to terminate." },
    "approval_id": { "type": "string", "description": "Approval ID (required for REQUIRES_APPROVAL targets)." }
  },
  "required": ["target", "pid", "approval_id"]
}`)
}

// cancelQueryArgs holds the parsed arguments for db_cancel_query.
type cancelQueryArgs struct {
	Target     string `json:"target"`
	PID        int64  `json:"pid"`
	ApprovalID string `json:"approval_id"`
}

// terminateConnectionArgs holds the parsed arguments for db_terminate_connection.
type terminateConnectionArgs struct {
	Target     string `json:"target"`
	PID        int64  `json:"pid"`
	ApprovalID string `json:"approval_id"`
}

// cancelQuery implements the db_cancel_query handler.
// Uses the standard query path (pg_cancel_backend is a SELECT call).
// Maps privilege failure (pg error 42501) to DB_PERMISSION_DENIED.
func (s *Service) cancelQuery(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	start := time.Now()

	var a cancelQueryArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return s.errorResult(ctx, toolCancelQuery, "", mcp.ErrCodeSQLInvalid,
				fmt.Sprintf("invalid arguments: %v", err), start, ""), nil
		}
	}
	if a.Target == "" {
		return s.errorResult(ctx, toolCancelQuery, "", mcp.ErrCodeSQLInvalid,
			"missing required argument \"target\"", start, ""), nil
	}
	if err := validatePID("pid", a.PID); err != nil {
		return s.errorResult(ctx, toolCancelQuery, a.Target, mcp.ErrCodeSQLInvalid, err.Error(), start, a.ApprovalID), nil
	}

	pool, err := s.resolvePool(a.Target)
	if err != nil {
		code, msg := s.classifyExecError(err)
		return s.errorResult(ctx, toolCancelQuery, a.Target, code, msg, start, a.ApprovalID), nil
	}

	approver, approvalErr := s.checkApproval(ctx, toolCancelQuery, a.Target, a.ApprovalID, map[string]any{
		"pid": a.PID,
	})
	if approvalErr != nil {
		return approvalErr, nil
	}

	// Use parameterized query: SELECT pg_cancel_backend($1)
	sql := "SELECT pg_cancel_backend($1)"
	res, err := s.execQuery(ctx, pool, sql, []any{a.PID})
	if err != nil {
		code, msg := s.classifyExecError(err)
		return s.errorResult(ctx, toolCancelQuery, a.Target, code, msg, start, a.ApprovalID), nil
	}

	// The result is a single row with a boolean column.
	data := map[string]any{
		"pid":    a.PID,
		"result": "cancellation requested",
	}
	if res != nil && len(res.Rows) > 0 && len(res.Rows[0]) > 0 {
		data["cancelled"] = res.Rows[0][0]
	}

	return s.successResult(ctx, toolCancelQuery, a.Target, data, res, start, a.ApprovalID, approver), nil
}

// terminateConnection implements the db_terminate_connection handler.
// This is a REQUIRES_APPROVAL tool.
// Uses the standard query path (pg_terminate_backend is a SELECT call).
// Maps privilege failure (pg error 42501) to DB_PERMISSION_DENIED.
func (s *Service) terminateConnection(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	start := time.Now()

	var a terminateConnectionArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return s.errorResult(ctx, toolTerminateConnection, "", mcp.ErrCodeSQLInvalid,
				fmt.Sprintf("invalid arguments: %v", err), start, ""), nil
		}
	}
	if a.Target == "" {
		return s.errorResult(ctx, toolTerminateConnection, "", mcp.ErrCodeSQLInvalid,
			"missing required argument \"target\"", start, ""), nil
	}
	if err := validatePID("pid", a.PID); err != nil {
		return s.errorResult(ctx, toolTerminateConnection, a.Target, mcp.ErrCodeSQLInvalid, err.Error(), start, a.ApprovalID), nil
	}

	pool, err := s.resolvePool(a.Target)
	if err != nil {
		code, msg := s.classifyExecError(err)
		return s.errorResult(ctx, toolTerminateConnection, a.Target, code, msg, start, a.ApprovalID), nil
	}

	approver, approvalErr := s.checkApproval(ctx, toolTerminateConnection, a.Target, a.ApprovalID, map[string]any{
		"pid": a.PID,
	})
	if approvalErr != nil {
		return approvalErr, nil
	}

	// Use parameterized query: SELECT pg_terminate_backend($1)
	sql := "SELECT pg_terminate_backend($1)"
	res, err := s.execQuery(ctx, pool, sql, []any{a.PID})
	if err != nil {
		code, msg := s.classifyExecError(err)
		return s.errorResult(ctx, toolTerminateConnection, a.Target, code, msg, start, a.ApprovalID), nil
	}

	data := map[string]any{
		"pid":    a.PID,
		"result": "termination requested",
	}
	if res != nil && len(res.Rows) > 0 && len(res.Rows[0]) > 0 {
		data["terminated"] = res.Rows[0][0]
	}

	return s.successResult(ctx, toolTerminateConnection, a.Target, data, res, start, a.ApprovalID, approver), nil
}

func registerCancelQuery(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolCancelQuery,
		Description: cancelQueryDescription,
		Permission:  mcp.PermissionControlledMaintenance,
		InputSchema: schemaCancelQuery(),
		Handler:     adapt(s.cancelQuery),
	})
}

func registerTerminateConnection(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolTerminateConnection,
		Description: terminateConnectionDescription,
		Permission:  mcp.PermissionRequiresApproval,
		InputSchema: schemaTerminateConnection(),
		Handler:     adapt(s.terminateConnection),
	})
}
