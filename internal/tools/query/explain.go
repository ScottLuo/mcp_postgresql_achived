package query

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"postgres-dba-mcp/internal/database"
	"postgres-dba-mcp/internal/mcp"
	"postgres-dba-mcp/internal/security"
)

// explainArgs are the arguments shared by db_explain_query and
// db_explain_analyze.
type explainArgs struct {
	Target string `json:"target"`
	SQL    string `json:"sql"`
	Params []any  `json:"params"`
}

// explainSchema is the JSON Schema shared by the EXPLAIN tools.
const explainSchema = `{
  "type": "object",
  "properties": {
    "target": { "type": "string", "description": "Configured target alias. Defaults to the server default target." },
    "sql": { "type": "string", "description": "A single read-only SQL statement (SELECT) to analyze." },
    "params": { "type": "array", "description": "Positional parameter values for the SQL placeholders." }
  },
  "required": ["sql"]
}`

// parseExplainSQL builds the EXPLAIN statement wrapping the caller's SQL.
// formatJSON is "FORMAT JSON" (plan only) or "ANALYZE, FORMAT JSON" (executes).
func parseExplainSQL(formatJSON, sql string) string {
	return fmt.Sprintf("EXPLAIN (%s) %s", formatJSON, sql)
}

// extractPlan pulls the execution plan out of an EXPLAIN result. PostgreSQL
// returns EXPLAIN (FORMAT JSON) as a single row whose first column ("QUERY PLAN")
// is a JSON value (the plan tree). The pgx driver delivers jsonb/json as []byte,
// so we unmarshal it into a generic value for the response. When the value is
// not JSON we fall back to returning it verbatim.
func extractPlan(res *database.ExecResult) any {
	if res == nil || len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return nil
	}
	raw := res.Rows[0][0]
	switch v := raw.(type) {
	case []byte:
		var out any
		if err := json.Unmarshal(v, &out); err == nil {
			return out
		}
		return string(v)
	case string:
		var out any
		if err := json.Unmarshal([]byte(v), &out); err == nil {
			return out
		}
		return v
	default:
		return raw
	}
}

// explainQueryHandler implements db_explain_query (EXPLAIN FORMAT JSON, no
// execution of the underlying query).
type explainQueryHandler struct{ d *Deps }

// NewExplainQueryHandler returns the handler for db_explain_query.
func NewExplainQueryHandler(d *Deps) *explainQueryHandler {
	return &explainQueryHandler{d: d}
}

// Execute generates an EXPLAIN (FORMAT JSON) plan without executing the query.
func (h *explainQueryHandler) Execute(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	const tool = "db_explain_query"
	return h.d.explainCommon(ctx, tool, args, "FORMAT JSON", false)
}

// explainAnalyzeHandler implements db_explain_analyze (EXPLAIN ANALYZE FORMAT
// JSON; executes the query).
type explainAnalyzeHandler struct{ d *Deps }

// NewExplainAnalyzeHandler returns the handler for db_explain_analyze.
func NewExplainAnalyzeHandler(d *Deps) *explainAnalyzeHandler {
	return &explainAnalyzeHandler{d: d}
}

// Execute generates an EXPLAIN (ANALYZE, FORMAT JSON) plan, executing the query
// under controlled limits.
func (h *explainAnalyzeHandler) Execute(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	const tool = "db_explain_analyze"
	return h.d.explainCommon(ctx, tool, args, "ANALYZE, FORMAT JSON", true)
}

// explainCommon is the shared implementation for both EXPLAIN tools. It parses
// and validates the arguments, builds the wrapped EXPLAIN statement, executes it
// through the non-transactional executor, and reports audit/metrics.
//
// executeQuery distinguishes db_explain_analyze (ANALYZE — the query runs) from
// db_explain_query (the query is only planned). It is used for the audit
// operation label and for the "executed" metadata flag.
func (d *Deps) explainCommon(ctx context.Context, tool string, args json.RawMessage, explainFormat string, executes bool) (*mcp.ToolResult, error) {
	start := time.Now()

	var in explainArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return d.errorResult(ctx, tool, "", mcp.ErrCodeSQLInvalid, "invalid arguments: "+security.SanitizeError(err.Error()), start), nil
	}

	if in.SQL == "" {
		return d.errorResult(ctx, tool, d.resolveTarget(in.Target), mcp.ErrCodeSQLInvalid, "parameter \"sql\" is required and must be non-empty", start), nil
	}

	// Enforce the max-SQL-length limit on the caller's SQL (spec §9.3).
	lim := d.queryLimits()
	if len(in.SQL) > lim.MaxSQLLength {
		return d.errorResult(ctx, tool, d.resolveTarget(in.Target), mcp.ErrCodeSQLInvalid,
			fmt.Sprintf("SQL length %d exceeds maximum %d", len(in.SQL), lim.MaxSQLLength), start), nil
	}

	// The analyzed statement must be a single, read-only SELECT. This also
	// rejects multi-statement input, DML/DDL, and data-modifying CTEs — the
	// guard against "expensive or potentially destructive queries" (spec §7.3).
	if err := security.ValidateReadOnly(in.SQL); err != nil {
		return d.errorResult(ctx, tool, d.resolveTarget(in.Target), classifyValidationError(err),
			"SQL not allowed for EXPLAIN: "+security.SanitizeError(err.Error()), start), nil
	}

	target := d.resolveTarget(in.Target)

	// Resolve the target's pool; an unknown target is surfaced as
	// DB_TARGET_NOT_FOUND here.
	pool, poolErr := d.getPoolErr(ctx, tool, target, start)
	if poolErr != nil {
		return poolErr, nil
	}

	// Build the EXPLAIN statement. EXPLAIN (FORMAT JSON) only produces a plan;
	// the underlying query is not run. EXPLAIN (ANALYZE, FORMAT JSON) runs the
	// query to collect actual timing.
	explainSQL := parseExplainSQL(explainFormat, in.SQL)

	exec := d.sqlExec(pool)
	res, err := exec.Execute(ctx, explainSQL, in.Params, 0)
	if err != nil {
		code, _ := classifyExecError(err)
		return d.errorResult(ctx, tool, target, code, security.SanitizeError(err.Error()), start), nil
	}

	plan := extractPlan(res)
	d.recordSuccess(ctx, tool, target, start, res, 0, "")

	data := map[string]any{
		"plan":        plan,
		"executed":    executes,
		"duration_ms": res.Duration.Milliseconds(),
	}
	return mcp.SuccessResult(data, &mcp.Metadata{
		Target:     target,
		DurationMS: res.Duration.Milliseconds(),
	}), nil
}
