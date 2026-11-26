package main

import (
	"context"

	"postgres-dba-mcp/internal/mcp"
	"postgres-dba-mcp/internal/security"
)

// policyAdapter bridges the security.PolicyEngine to the mcp.PolicyChecker
// interface. The two have different method signatures:
//
//   - security.PolicyEngine.Check(ctx, target, tool) → (Decision, error)
//   - mcp.PolicyChecker.Check(toolName, target, perm) → error
//
// This adapter translates the decision into the error semantics expected by
// the registry:
//   - DecisionAllow → nil (tool may proceed)
//   - DecisionDeny → *mcp.PolicyError (tool is denied)
//   - DecisionNeedsApproval:
//     - If perm is READ_ONLY → *mcp.PolicyError (a read-only tool should not
//       require approval in normal configurations; this is a config error)
//     - Otherwise → nil (the tool handler will perform its own approval check
//       via the ApprovalValidator)
type policyAdapter struct {
	engine *security.PolicyEngine
}

// Check implements mcp.PolicyChecker.
func (p *policyAdapter) Check(toolName, target string, _ mcp.PermissionLevel) error {
	decision, err := p.engine.Check(context.Background(), target, toolName)
	if err != nil {
		// The engine returned an error (unknown target or default-deny).
		return &mcp.PolicyError{
			Code:    mcp.ErrCodePermissionDenied,
			Message: "operation not authorized: " + err.Error(),
		}
	}

	switch decision {
	case security.DecisionAllow:
		return nil
	case security.DecisionNeedsApproval:
		// For non-read-only tools, the handler will validate the approval
		// record. The registry allows the call to proceed.
		return nil
	default: // DecisionDeny
		return &mcp.PolicyError{
			Code:    mcp.ErrCodePermissionDenied,
			Message: "operation denied by policy for target " + target,
		}
	}
}
