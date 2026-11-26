package query

import (
	"context"
	"encoding/json"
	"time"

	"postgres-dba-mcp/internal/mcp"
	"postgres-dba-mcp/internal/security"
)

// costEstimateHandler implements db_estimate_query_cost: it produces a planner
// cost estimate for a single read-only SELECT without executing the query.
//
// EXPLAIN (FORMAT JSON) yields a cost estimate (startup_cost / total_cost) as
// part of the plan without running the query. This is distinct from
// db_explain_analyze (which runs the query) and db_explain_query (which returns
// the raw plan). Here we additionally surface the extracted cost figures.
type costEstimateHandler struct{ d *Deps }

// NewCostEstimateHandler returns the handler for db_estimate_query_cost.
func NewCostEstimateHandler(d *Deps) *costEstimateHandler {
	return &costEstimateHandler{d: d}
}

// Execute estimates the query plan cost (spec §7.3 db_estimate_query_cost).
func (h *costEstimateHandler) Execute(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	const tool = "db_estimate_query_cost"
	start := time.Now()
	d := h.d

	var in explainArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return d.errorResult(ctx, tool, "", mcp.ErrCodeSQLInvalid, "invalid arguments: "+security.SanitizeError(err), start), nil
	}

	if in.SQL == "" {
		return d.errorResult(ctx, tool, d.resolveTarget(in.Target), mcp.ErrCodeSQLInvalid, "parameter \"sql\" is required and must be non-empty", start), nil
	}

	// Enforce the max-SQL-length limit on the caller's SQL (spec §9.3).
	lim := d.queryLimits()
	if len(in.SQL) > lim.MaxSQLLength {
		return d.errorResult(ctx, tool, d.resolveTarget(in.Target), mcp.ErrCodeSQLInvalid,
			"SQL length exceeds maximum", start), nil
	}

	// The cost is estimated for a single, read-only SELECT only.
	if err := security.ValidateReadOnly(in.SQL); err != nil {
		return d.errorResult(ctx, tool, d.resolveTarget(in.Target), classifyValidationError(err),
			"SQL not allowed for cost estimation: "+security.SanitizeError(err), start), nil
	}

	target := d.resolveTarget(in.Target)

	// Resolve the target's pool; an unknown target is surfaced as
	// DB_TARGET_NOT_FOUND here.
	pool, poolErr := d.getPoolErr(ctx, tool, target, start)
	if poolErr != nil {
		return poolErr, nil
	}

	// EXPLAIN (FORMAT JSON) estimates cost without executing the query.
	exec := d.sqlExec(pool)
	res, err := exec.Execute(ctx, parseExplainSQL("FORMAT JSON", in.SQL), in.Params, 0)
	if err != nil {
		code, _ := classifyExecError(err)
		return d.errorResult(ctx, tool, target, code, security.SanitizeError(err), start), nil
	}

	plan := extractPlan(res)
	costs := extractCost(plan)
	d.recordSuccess(ctx, tool, target, start, res, 0, "")

	data := map[string]any{
		"startup_cost": costs.Startup,
		"total_cost":   costs.Total,
		"plan":         plan,
		"duration_ms":  res.Duration.Milliseconds(),
	}
	return mcp.SuccessResult(data, &mcp.Metadata{
		Target:     target,
		DurationMS: res.Duration.Milliseconds(),
	}), nil
}

// planCosts holds the top-level planner cost figures.
type planCosts struct {
	Startup float64
	Total   float64
}

// extractCost pulls startup_cost and total_cost from the top-level of an
// EXPLAIN (FORMAT JSON) plan. The plan is the JSON value returned by PostgreSQL
// (a slice whose first element is an object with "Plan"). The cost fields are
// located at that object under "Plan"->"Startup Cost"/"Total Cost".
func extractCost(plan any) planCosts {
	out := planCosts{}
	if plan == nil {
		return out
	}

	// EXPLAIN (FORMAT JSON) returns: [ { "Plan": { "Startup Cost": .., "Total Cost": .., ... } } ]
	var root = plan
	if m, ok := plan.(map[string]any); ok {
		// Sometimes the value is already the object rather than a slice.
		root = m
	}

	var planObj map[string]any
	switch v := root.(type) {
	case []any:
		if len(v) == 0 {
			return out
		}
		if m, ok := v[0].(map[string]any); ok {
			if p, ok := m["Plan"].(map[string]any); ok {
				planObj = p
			}
		}
	case map[string]any:
		if p, ok := v["Plan"].(map[string]any); ok {
			planObj = p
		}
	}

	if planObj != nil {
		if f, ok := planObj["Startup Cost"].(float64); ok {
			out.Startup = f
		}
		if f, ok := planObj["Total Cost"].(float64); ok {
			out.Total = f
		}
	}
	return out
}
