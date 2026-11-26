// Package query implements the Query Analysis MCP tools (spec §7.3):
//
//   - db_query_readonly      — run a read-only, parameterized, AST-validated SQL
//     inside a read-only transaction with enforced limits.
//   - db_explain_query       — generate an EXPLAIN (FORMAT JSON) plan without
//     executing the query.
//   - db_explain_analyze     — run EXPLAIN (ANALYZE, FORMAT JSON); separately
//     authorized (CONTROLLED_MAINTENANCE) and executes the query.
//   - db_estimate_query_cost — estimate plan cost without execution.
//
// The handlers never inspect permissions themselves: the MCP registry enforces
// the permission/approval policy before invoking a handler (spec §8, task #15).
package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"postgres-dba-mcp/internal/audit"
	"postgres-dba-mcp/internal/config"
	"postgres-dba-mcp/internal/database"
	"postgres-dba-mcp/internal/mcp"
	"postgres-dba-mcp/internal/metrics"
	"postgres-dba-mcp/internal/security"
)

// readonlyExecutor is the minimal read-only execution surface used by
// db_query_readonly. It is satisfied by *database.TransactionExecutor and by
// the unit-test fakes. Keeping it as an unexported interface keeps the package
// public surface free of test-only abstractions.
type readonlyExecutor interface {
	ExecuteReadOnly(ctx context.Context, sql string, params []any, limit int) (*database.ExecResult, error)
}

// sqlExecutor is the minimal non-transactional execution surface used by the
// EXPLAIN and cost tools. It is satisfied by *database.Executor and fakes.
type sqlExecutor interface {
	Execute(ctx context.Context, sql string, params []any, limit int) (*database.ExecResult, error)
}

// Deps bundles the shared dependencies required by every query tool handler.
// A single Deps instance is shared by all four handlers so that they observe
// the same configuration, audit logger and metrics registry (data-same-source).
type Deps struct {
	DBManager database.DatabaseManager
	Cfg       *config.Config
	Audit     *audit.AuditLogger
	Metrics   *metrics.Metrics
	Logger    *slog.Logger

	// ReadonlyExec and Exec are optional injection points used by unit tests to
	// avoid a real PostgreSQL connection. When non-nil the corresponding
	// executor construction is bypassed.
	ReadonlyExec readonlyExecutor
	Exec         sqlExecutor

	// poolProvider resolves the *pgxpool.Pool for a target. It defaults to
	// DBManager.GetPool; tests may override it. When the corresponding
	// ReadonlyExec/Exec field is set, poolProvider is not called for that path.
	poolProvider func(target string) (*pgxpool.Pool, error)
}

// NewDeps builds a Deps with poolProvider wired to DBManager.GetPool.
func NewDeps(dbm database.DatabaseManager, cfg *config.Config, aud *audit.AuditLogger, met *metrics.Metrics, logger *slog.Logger) *Deps {
	if logger == nil {
		logger = slog.Default()
	}
	d := &Deps{
		DBManager: dbm,
		Cfg:       cfg,
		Audit:     aud,
		Metrics:   met,
		Logger:    logger,
	}
	d.poolProvider = func(target string) (*pgxpool.Pool, error) {
		if dbm == nil {
			return nil, database.NewTargetNotFound(target)
		}
		return dbm.GetPool(target)
	}
	return d
}

// Register registers the four query-analysis tools with the MCP registry.
func Register(registry *mcp.Registry, deps *Deps) error {
	if registry == nil {
		return errors.New("query: registry is nil")
	}
	if deps == nil {
		return errors.New("query: deps is nil")
	}

	timeout := deps.Cfg.QueryLimits.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	tools := []*mcp.ToolDefinition{
		{
			Name:        "db_query_readonly",
			Description: "Execute a read-only, parameterized SQL statement inside a read-only transaction. The SQL must be a single SELECT (read-only CTEs allowed). Enforced limits: statement timeout, max rows, result size, and max SQL length.",
			Permission:  mcp.PermissionReadOnly,
			Timeout:     timeout,
			InputSchema: json.RawMessage(queryReadonlySchema),
			Handler:     NewQueryReadonlyHandler(deps),
		},
		{
			Name:        "db_explain_query",
			Description: "Generate an EXPLAIN (FORMAT JSON) execution plan for a single read-only SELECT without executing the query.",
			Permission:  mcp.PermissionReadOnly,
			Timeout:     timeout,
			InputSchema: json.RawMessage(explainSchema),
			Handler:     NewExplainQueryHandler(deps),
		},
		{
			Name:        "db_explain_analyze",
			Description: "Generate an EXPLAIN (ANALYZE, FORMAT JSON) plan. This EXECUTES the query under controlled execution limits. Restricted to a single read-only SELECT. Separately authorized by policy.",
			Permission:  mcp.PermissionControlledMaintenance,
			Timeout:     timeout,
			InputSchema: json.RawMessage(explainSchema),
			Handler:     NewExplainAnalyzeHandler(deps),
		},
		{
			Name:        "db_estimate_query_cost",
			Description: "Estimate the planner cost (startup_cost and total_cost) for a single read-only SELECT without executing the query.",
			Permission:  mcp.PermissionReadOnly,
			Timeout:     timeout,
			InputSchema: json.RawMessage(explainSchema),
			Handler:     NewCostEstimateHandler(deps),
		},
	}

	for _, def := range tools {
		if err := registry.Register(def); err != nil {
			return fmt.Errorf("query: register %q: %w", def.Name, err)
		}
	}
	return nil
}

// resolveTarget returns the target alias, defaulting to the configured default
// target when the caller did not supply one.
func (d *Deps) resolveTarget(target string) string {
	if target != "" {
		return target
	}
	if d.Cfg != nil {
		return d.Cfg.DefaultTarget
	}
	return ""
}

// getPoolErr resolves the target's pool and reports the outcome. It returns the
// pool (non-nil on success) or, on failure, a DB_TARGET_NOT_FOUND /
// DB_CONNECTION_FAILED ToolResult ready to be returned to the caller.
func (d *Deps) getPoolErr(ctx context.Context, tool, target string, start time.Time) (*pgxpool.Pool, *mcp.ToolResult) {
	pool, err := d.poolProvider(target)
	if err != nil {
		code := mcp.ErrCodeConnectionFailed
		var tnfe *database.TargetNotFoundError
		if errors.As(err, &tnfe) {
			code = mcp.ErrCodeTargetNotFound
		}
		return nil, d.errorResult(ctx, tool, target, code, security.SanitizeError(err.Error()), start)
	}
	return pool, nil
}

// readonlyExec returns the read-only executor to use. The pool has already been
// resolved (and target-not-found surfaced) by the caller via getPoolErr. When
// the test fake is set it is returned instead, so pool may be nil on that path.
func (d *Deps) readonlyExec(pool *pgxpool.Pool) readonlyExecutor {
	if d.ReadonlyExec != nil {
		return d.ReadonlyExec
	}
	if pool == nil {
		pool = &pgxpool.Pool{}
	}
	lim := d.queryLimits()
	return database.NewTransactionExecutor(pool, lim.Timeout, lim.MaxRows, lim.MaxResultSize, lim.MaxSQLLength)
}

// sqlExec returns the non-transactional executor to use. The pool has already
// been resolved by the caller via getPoolErr.
func (d *Deps) sqlExec(pool *pgxpool.Pool) sqlExecutor {
	if d.Exec != nil {
		return d.Exec
	}
	if pool == nil {
		pool = &pgxpool.Pool{}
	}
	lim := d.queryLimits()
	return database.NewExecutor(pool, lim.Timeout, lim.MaxRows, lim.MaxResultSize, lim.MaxSQLLength)
}

func (d *Deps) queryLimits() config.QueryLimits {
	if d.Cfg == nil {
		return config.QueryLimits{Timeout: 10 * time.Second, MaxRows: 1000, MaxResultSize: 1048576, MaxSQLLength: 65536}
	}
	return d.Cfg.QueryLimits
}

// errorResult builds a failed ToolResult and emits the corresponding audit
// record and error metric. It is the single funnel through which every query
// handler reports failures, guaranteeing consistent observability.
func (d *Deps) errorResult(ctx context.Context, tool, target, code, message string, start time.Time) *mcp.ToolResult {
	dur := time.Since(start)
	d.auditLog(ctx, tool, target, dur, false, code, 0, "")
	d.recordError(tool, target, code, start, dur)
	return mcp.ErrorResult(code, message, &mcp.Metadata{
		Target:     target,
		DurationMS: dur.Milliseconds(),
	})
}

// auditLog writes an audit record for a query tool invocation.
func (d *Deps) auditLog(ctx context.Context, tool, target string, dur time.Duration, success bool, code string, rows int, approvalID string) {
	if d.Audit == nil {
		return
	}
	rec := &audit.AuditRecord{
		Tool:         tool,
		Target:       target,
		Operation:    operationForTool(tool),
		DurationMS:   dur.Milliseconds(),
		Success:      success,
		ErrorCode:    code,
		RowsReturned: rows,
		ApprovalID:   approvalID,
	}
	d.Audit.Log(ctx, rec)
}

// recordError emits the error counter and duration histogram for a failed call.
func (d *Deps) recordError(tool, target, code string, start time.Time, dur time.Duration) {
	if d.Metrics == nil {
		return
	}
	d.Metrics.RecordToolCall(tool, target, "error")
	d.Metrics.RecordError(tool, target, code)
	d.Metrics.ObserveDuration(tool, target, dur.Seconds())
	if code == mcp.ErrCodeQueryTimeout {
		d.Metrics.RecordQueryTimeout(target)
	}
}

// recordSuccess emits the success counter and duration/query metrics for a
// successful call.
func (d *Deps) recordSuccess(ctx context.Context, tool, target string, start time.Time, res *database.ExecResult, rows int, approvalID string) {
	dur := time.Since(start)
	if res != nil {
		dur = res.Duration
		if dur <= 0 {
			dur = time.Since(start)
		}
	}
	if d.Metrics != nil {
		d.Metrics.RecordToolCall(tool, target, "ok")
		d.Metrics.ObserveDuration(tool, target, dur.Seconds())
		d.Metrics.RecordQueryDuration(target, dur.Seconds())
	}
	d.auditLog(ctx, tool, target, dur, true, "", rows, approvalID)
}

// classifyValidationError maps a security.ValidateReadOnly error to a standard
// error code. Parse/multi-statement failures are "invalid SQL"; everything
// else (non-SELECT, data-modifying CTE, locking read) is "not allowed".
func classifyValidationError(err error) string {
	if errors.Is(err, security.ErrInvalidSQL) || errors.Is(err, security.ErrMultiStatement) {
		return mcp.ErrCodeSQLInvalid
	}
	return mcp.ErrCodeSQLNotAllowed
}

// classifyExecError maps a database execution error to a standard error code
// and a sanitized message.
func classifyExecError(err error) (code, message string) {
	switch {
	case errors.Is(err, database.ErrQueryTimeout):
		return mcp.ErrCodeQueryTimeout, "query timed out"
	case errors.Is(err, database.ErrSQLTooLong):
		return mcp.ErrCodeSQLInvalid, "SQL exceeds maximum length"
	case errors.Is(err, database.ErrResultTooLarge):
		return mcp.ErrCodeResultTooLarge, "query result exceeds size limit"
	default:
		return mcp.ErrCodeExecutionFailed, "database execution failed"
	}
}

// operationForTool returns the audit operation category for a query tool.
func operationForTool(tool string) string {
	switch tool {
	case "db_explain_analyze":
		return "EXPLAIN_ANALYZE"
	case "db_explain_query":
		return "EXPLAIN"
	case "db_estimate_query_cost":
		return "COST_ESTIMATE"
	default:
		return "READ"
	}
}

// queryReadonlyArgs are the arguments for db_query_readonly.
type queryReadonlyArgs struct {
	Target string `json:"target"`
	SQL    string `json:"sql"`
	Params []any  `json:"params"`
	Limit  int    `json:"limit"`
}

// queryReadonlySchema is the JSON Schema for db_query_readonly.
const queryReadonlySchema = `{
  "type": "object",
  "properties": {
    "target": { "type": "string", "description": "Configured target alias. Defaults to the server default target." },
    "sql": { "type": "string", "description": "A single read-only SQL statement (SELECT). Use $1, $2, ... for parameters." },
    "params": { "type": "array", "description": "Positional parameter values for the SQL placeholders." },
    "limit": { "type": "integer", "description": "Optional cap on returned rows. May not exceed the configured maximum." }
  },
  "required": ["sql"]
}`

// queryReadonlyHandler implements db_query_readonly.
type queryReadonlyHandler struct{ d *Deps }

// NewQueryReadonlyHandler returns the handler for db_query_readonly.
func NewQueryReadonlyHandler(d *Deps) *queryReadonlyHandler {
	return &queryReadonlyHandler{d: d}
}

// Execute runs the read-only query (spec §7.3 db_query_readonly).
func (h *queryReadonlyHandler) Execute(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	const tool = "db_query_readonly"
	start := time.Now()
	d := h.d

	var in queryReadonlyArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return d.errorResult(ctx, tool, "", mcp.ErrCodeSQLInvalid, "invalid arguments: "+security.SanitizeError(err.Error()), start), nil
	}

	if in.SQL == "" {
		return d.errorResult(ctx, tool, d.resolveTarget(in.Target), mcp.ErrCodeSQLInvalid, "parameter \"sql\" is required and must be non-empty", start), nil
	}

	// Enforce the max-SQL-length limit before any database I/O. The limit is
	// applied server-side and cannot be bypassed by the caller (spec §9.3).
	lim := d.queryLimits()
	if len(in.SQL) > lim.MaxSQLLength {
		return d.errorResult(ctx, tool, d.resolveTarget(in.Target), mcp.ErrCodeSQLInvalid,
			fmt.Sprintf("SQL length %d exceeds maximum %d", len(in.SQL), lim.MaxSQLLength), start), nil
	}

	// AST validation: single, read-only, SELECT-only statement (spec §9.2).
	if err := security.ValidateReadOnly(in.SQL); err != nil {
		return d.errorResult(ctx, tool, d.resolveTarget(in.Target), classifyValidationError(err),
			"SQL not allowed for read-only execution: "+security.SanitizeError(err.Error()), start), nil
	}

	target := d.resolveTarget(in.Target)

	// Resolve the target's pool; an unknown target is surfaced as
	// DB_TARGET_NOT_FOUND here.
	pool, poolErr := d.getPoolErr(ctx, tool, target, start)
	if poolErr != nil {
		return poolErr, nil
	}

	// The executor re-checks SQL length internally; we validated it above so the
	// two are consistent (data-same-source).
	exec := d.readonlyExec(pool)
	res, err := exec.ExecuteReadOnly(ctx, in.SQL, in.Params, in.Limit)
	if err != nil {
		code, _ := classifyExecError(err)
		return d.errorResult(ctx, tool, target, code, security.SanitizeError(err.Error()), start), nil
	}

	d.recordSuccess(ctx, tool, target, start, res, res.RowsReturned, "")

	data := map[string]any{
		"columns":       res.Columns,
		"rows":          res.Rows,
		"rows_returned": res.RowsReturned,
		"truncated":     res.Truncated,
		"duration_ms":   res.Duration.Milliseconds(),
	}
	return mcp.SuccessResult(data, &mcp.Metadata{
		Target:       target,
		DurationMS:   res.Duration.Milliseconds(),
		Truncated:    res.Truncated,
		RowsReturned: res.RowsReturned,
	}), nil
}
