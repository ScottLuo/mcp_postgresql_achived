// Package monitoring implements the database monitoring MCP tools
// (spec §7.2): current activity, connections, locks, statistics, slow
// queries, cache statistics, deadlocks and replication status/lag.
//
// Every tool is READ_ONLY. All handlers:
//   - validate their arguments (target is required; limit/schema/table where
//     applicable),
//   - resolve the connection pool via the DatabaseManager,
//   - execute a read-only catalog / pg_stat_* query through the unified
//     database.Executor (which enforces timeout, row and result-size limits
//     server-side),
//   - emit one audit record and record metrics per invocation,
//   - report "feature unavailable" (DB_FEATURE_UNAVAILABLE) instead of a raw
//     database error when a statistic depends on an extension or on a
//     replication topology that is not present.
package monitoring

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
)

// operationRead is the audit operation category for all monitoring tools.
// Monitoring tools only read, so a single constant is the source of truth.
const operationRead = "READ"

// errInvalidArgs is returned for argument-validation failures so that a bad
// request surfaces as a structured ToolError rather than a raw database error.
var errInvalidArgs = errors.New("invalid tool arguments")

// queryRunner is the seam over database.Executor used to run a read-only
// query. *database.Executor satisfies it. It exists so unit tests can stub
// query execution without a live PostgreSQL instance.
type queryRunner interface {
	Execute(ctx context.Context, sql string, params []any, limit int) (*database.ExecResult, error)
}

// featureCheckFn reports whether a PostgreSQL extension is installed.
type featureCheckFn func(ctx context.Context, pool *pgxpool.Pool, extension string) bool

// Service bundles the dependencies shared by every monitoring tool handler.
// It is the value passed to Register and to each handler.
type Service struct {
	cfg     *config.Config
	Manager database.DatabaseManager
	Audit   *audit.AuditLogger
	Metrics *metrics.Metrics
	logger  *slog.Logger

	// runner is a seam over the database.Executor so unit tests can stub
	// query execution without a real PostgreSQL. When nil, a real
	// *database.Executor is constructed on demand from the pool.
	runner queryRunner

	// featureCheck reports whether an extension is installed. Defaults to
	// database.CheckFeatureAvailable. Overridable in tests.
	featureCheck featureCheckFn
}

// NewService constructs a monitoring Service.
//
//   - cfg supplies the query limits (timeout / max rows / max size); nil uses
//     the documented defaults.
//   - manager resolves target aliases to connection pools.
//   - auditLogger records one record per tool invocation (nil is a no-op).
//   - m records call/error/duration (nil is a no-op).
func NewService(cfg *config.Config, manager database.DatabaseManager, auditLogger *audit.AuditLogger, m *metrics.Metrics) *Service {
	if cfg == nil {
		cfg = &config.Config{QueryLimits: defaultQueryLimits()}
	}
	return &Service{
		cfg:          cfg,
		Manager:      manager,
		Audit:        auditLogger,
		Metrics:      m,
		logger:       slog.Default(),
		featureCheck: database.CheckFeatureAvailable,
	}
}

// defaultQueryLimits returns the same defaults as the config layer, so that a
// Service built without a config still enforces the documented limits.
func defaultQueryLimits() config.QueryLimits {
	return config.QueryLimits{
		Timeout:       10 * time.Second,
		MaxRows:       1000,
		MaxResultSize: 1048576,
		MaxSQLLength:  65536,
	}
}

// baseArgs is the argument set shared by every monitoring tool.
type baseArgs struct {
	Target string `json:"target"`
	Limit  int    `json:"limit"`
	Schema string `json:"schema"`
	Table  string `json:"table"`
}

// runOutput bundles the result of a single read-only query execution.
type runOutput struct {
	execResult *database.ExecResult
	rows       []map[string]any
	duration   time.Duration
}

// parseArgs decodes raw tool arguments into baseArgs and validates the shared
// fields:
//   - target is required and must be non-empty,
//   - limit is optional; a non-positive value falls back to the configured
//     default row cap (cfg.QueryLimits.MaxRows), and a value above the cap is
//     clamped to the cap.
//
// It returns errInvalidArgs (wrapped) on validation failure.
func (s *Service) parseArgs(raw json.RawMessage) (*baseArgs, error) {
	var args baseArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("%w: %v", errInvalidArgs, err)
		}
	}
	if args.Target == "" {
		return nil, fmt.Errorf("%w: missing required argument %q", errInvalidArgs, "target")
	}
	args.Limit = s.normalizeLimit(args.Limit)
	return &args, nil
}

// normalizeLimit enforces the documented limit bounds:
//   - limit <= 0 -> the configured default row cap (a default limit is always
//     enforced),
//   - limit > 0 but > cap -> the configured row cap.
func (s *Service) normalizeLimit(limit int) int {
	cap := s.cfg.QueryLimits.MaxRows
	if cap <= 0 {
		cap = 1000
	}
	if limit <= 0 {
		return cap
	}
	if limit > cap {
		return cap
	}
	return limit
}

// resolvePool resolves the target alias to a connection pool. The returned
// error is either a wrapped errInvalidArgs (missing target / no manager) or a
// database.ErrTargetNotFound for an unknown target.
func (s *Service) resolvePool(target string) (*pgxpool.Pool, error) {
	if target == "" {
		return nil, fmt.Errorf("%w: missing target", errInvalidArgs)
	}
	if s.Manager == nil {
		return nil, fmt.Errorf("%w: no database manager configured", errInvalidArgs)
	}
	pool, err := s.Manager.GetPool(target)
	if err != nil {
		return nil, err
	}
	return pool, nil
}

// runQuery executes a read-only query, applying the configured timeout / row /
// result-size limits, and converts the result to column-name-keyed maps.
func (s *Service) runQuery(ctx context.Context, pool *pgxpool.Pool, sql string, args []any, limit int) (*runOutput, error) {
	runner := s.runner
	if runner == nil {
		limits := s.cfg.QueryLimits
		runner = database.NewExecutor(pool, limits.Timeout, limits.MaxRows, limits.MaxResultSize, limits.MaxSQLLength)
	}
	execResult, err := runner.Execute(ctx, sql, args, limit)
	if err != nil {
		// Return a non-nil runOutput so callers can safely read out.duration
		// even on failure (out.execResult is nil).
		return &runOutput{rows: []map[string]any{}}, err
	}
	rows, err := toRowMaps(execResult)
	if err != nil {
		return &runOutput{rows: []map[string]any{}}, err
	}
	return &runOutput{execResult: execResult, rows: rows, duration: execResult.Duration}, nil
}

// featureAvailable reports whether the given extension is installed, using the
// (possibly injected) feature check.
func (s *Service) featureAvailable(ctx context.Context, pool *pgxpool.Pool, extension string) bool {
	return s.featureCheck(ctx, pool, extension)
}

// toRowMaps converts an ExecResult into []map[string]any keyed by column name.
func toRowMaps(execResult *database.ExecResult) ([]map[string]any, error) {
	if execResult == nil {
		return []map[string]any{}, nil
	}
	rows := make([]map[string]any, 0, len(execResult.Rows))
	for _, row := range execResult.Rows {
		m := make(map[string]any, len(execResult.Columns))
		for i, col := range execResult.Columns {
			if i < len(row) {
				m[col] = row[i]
			}
		}
		rows = append(rows, m)
	}
	return rows, nil
}

// newMeta builds the standard metadata block for a handler response.
func newMeta(target string, duration time.Duration, truncated bool, rowsReturned int) *mcp.Metadata {
	return &mcp.Metadata{
		Target:       target,
		DurationMS:   duration.Milliseconds(),
		Truncated:    truncated,
		RowsReturned: rowsReturned,
	}
}

// errCodeForErr maps a database execution error to a standard MCP error code.
// Unknown errors map to DB_EXECUTION_FAILED.
func errCodeForErr(err error) string {
	switch {
	case errors.Is(err, database.ErrQueryTimeout):
		return mcp.ErrCodeQueryTimeout
	case errors.Is(err, database.ErrResultTooLarge):
		return mcp.ErrCodeResultTooLarge
	case errors.Is(err, database.ErrSQLTooLong):
		return mcp.ErrCodeSQLInvalid
	case errors.Is(err, database.ErrTargetNotFound):
		return mcp.ErrCodeTargetNotFound
	case errors.Is(err, database.ErrFeatureUnavailable):
		return mcp.ErrCodeFeatureUnavailable
	default:
		return mcp.ErrCodeExecutionFailed
	}
}

// ok emits audit + metrics for a successful invocation and returns the
// structured success result.
func (s *Service) ok(ctx context.Context, tool, target string, data interface{}, duration time.Duration, truncated bool, rowsReturned int) *mcp.ToolResult {
	s.emitAuditAndMetrics(ctx, tool, target, true, "", duration, rowsReturned)
	return mcp.SuccessResult(data, newMeta(target, duration, truncated, rowsReturned))
}

// fail emits audit + metrics for a failed invocation and returns the
// structured error result.
func (s *Service) fail(ctx context.Context, tool, target, code, msg string, duration time.Duration) *mcp.ToolResult {
	if s.logger != nil {
		s.logger.Debug("monitoring tool failed", "tool", tool, "target", target, "error_code", code, "error", msg)
	}
	s.emitAuditAndMetrics(ctx, tool, target, false, code, duration, 0)
	return mcp.ErrorResult(code, msg, newMeta(target, duration, false, 0))
}

// emitAuditAndMetrics writes one audit record and records metrics for a
// finished tool invocation. Both are no-ops when the corresponding
// dependencies are nil, so the helpers are safe to call unconditionally.
func (s *Service) emitAuditAndMetrics(ctx context.Context, tool, target string, success bool, errorCode string, duration time.Duration, rowsReturned int) {
	rec := &audit.AuditRecord{
		Timestamp:    time.Now(),
		Tool:         tool,
		Target:       target,
		Operation:    operationRead,
		DurationMS:   duration.Milliseconds(),
		Success:      success,
		ErrorCode:    errorCode,
		RowsReturned: rowsReturned,
	}
	if s.Audit != nil {
		s.Audit.Log(ctx, rec)
	}
	if s.Metrics != nil {
		result := "ok"
		if !success {
			result = "error"
		}
		s.Metrics.RecordToolCall(tool, target, result)
		if !success {
			s.Metrics.RecordError(tool, target, errorCode)
		}
		s.Metrics.ObserveDuration(tool, target, duration.Seconds())
	}
}

// toolFunc adapts a plain handler function to the mcp.ToolHandler interface.
// Each monitoring tool is implemented as a named method on *Service; wrapping
// it with toolFunc gives it the Execute(ctx, args) method the registry expects.
type toolFunc struct {
	fn func(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error)
}

// Execute implements mcp.ToolHandler.
func (t toolFunc) Execute(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	return t.fn(ctx, args)
}

// adapt is a convenience constructor for toolFunc.
func adapt(fn func(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error)) mcp.ToolHandler {
	return toolFunc{fn: fn}
}

// allRegistrars lists the per-tool registration functions. Keeping them in a
// slice (rather than inline calls) gives a single place to enumerate the full
// set of monitoring tools and makes the count trivially testable.
var allRegistrars = []func(reg *mcp.Registry, s *Service) error{
	registerActivity,
	registerConnections,
	registerLocks,
	registerSlowQueries,
	registerDatabaseStats,
	registerTableStats,
	registerIndexStats,
	registerCacheStats,
	registerDeadlocks,
	registerReplicationStatus,
	registerReplicationLag,
}

// Register registers all monitoring ToolDefinitions with the registry. Every
// tool is READ_ONLY. It returns the first error encountered so that the caller
// can stop registration cleanly.
func Register(reg *mcp.Registry, s *Service) error {
	for _, register := range allRegistrars {
		if err := register(reg, s); err != nil {
			return err
		}
	}
	return nil
}

// ToolCount returns the number of monitoring tools this package registers.
// It is useful for tests and startup validation.
func ToolCount() int {
	return len(allRegistrars)
}
