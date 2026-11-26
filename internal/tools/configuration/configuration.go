// Package configuration implements the PostgreSQL configuration MCP tools
// (spec §7.5): reading a permitted configuration parameter
// (db_get_config), listing the permitted allow-list of non-secret parameters
// (db_get_config_list), and reloading the server configuration
// (db_reload_config).
//
// Design notes:
//   - db_get_config and db_get_config_list are READ_ONLY and read from
//     pg_settings. Only parameters present in a server-side allow-list of
//     non-secret settings are ever exposed, so sensitive/secret settings
//     (SSL certificate paths, password encryption, etc.) are never returned.
//   - db_reload_config is REQUIRES_APPROVAL and is therefore disabled by
//     default: the registry's policy engine will deny the call unless the
//     target policy lists it in "require_approval" AND a valid approval record
//     is presented. It issues SELECT pg_reload_conf().
//   - Configuration *modification* (SET / ALTER SYSTEM / reload after edit)
//     is intentionally a separate, default-disabled capability and is NOT
//     exposed by this package.
//
// Every handler validates its arguments, resolves the pool via the
// DatabaseManager, executes through the unified database.Executor (which
// enforces timeout/row/result-size limits server-side), emits one audit
// record and records metrics per invocation.
package configuration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"postgres-dba-mcp/internal/audit"
	"postgres-dba-mcp/internal/config"
	"postgres-dba-mcp/internal/database"
	"postgres-dba-mcp/internal/mcp"
	"postgres-dba-mcp/internal/metrics"
)

// Audit operation categories for configuration tools.
const (
	operationRead       = "READ"
	operationMaintenance = "MAINTENANCE"
)

// errInvalidArgs is returned for argument-validation failures so that a bad
// request surfaces as a structured ToolError rather than a raw database error.
var errInvalidArgs = errors.New("invalid tool arguments")

// allowedConfigParams is the server-side allow-list of non-secret PostgreSQL
// GUC parameters that AI agents may read. It is a deliberate subset of
// pg_settings: it covers operational/tuning parameters useful for diagnosis
// and capacity planning while excluding secret or credential-adjacent
// settings (SSL certificate/private-key paths, password_encryption,
// authentication files, etc.). The allow-list is the single source of truth
// for what db_get_config and db_get_config_list may expose.
var allowedConfigParams = map[string]struct{}{
	"max_connections":                     {},
	"shared_buffers":                      {},
	"work_mem":                            {},
	"maintenance_work_mem":                {},
	"effective_cache_size":                {},
	"autovacuum":                          {},
	"autovacuum_max_workers":              {},
	"autovacuum_vacuum_threshold":         {},
	"autovacuum_analyze_threshold":        {},
	"autovacuum_vacuum_cost_delay":        {},
	"autovacuum_analyze_cost_delay":       {},
	"checkpoint_completion_target":        {},
	"checkpoint_timeout":                  {},
	"max_wal_size":                        {},
	"min_wal_size":                        {},
	"max_slot_wal_keep_size":              {},
	"wal_level":                           {},
	"archive_mode":                        {},
	"log_min_duration_statement":          {},
	"statement_timeout":                   {},
	"idle_in_transaction_session_timeout": {},
	"max_worker_processes":                {},
	"max_parallel_workers":                {},
	"max_parallel_maintenance_workers":    {},
	"max_parallel_workers_per_gather":     {},
	"max_replication_slots":               {},
	"max_wal_senders":                     {},
	"fsync":                               {},
	"full_page_writes":                    {},
	"synchronous_commit":                  {},
	"track_activities":                    {},
	"track_counts":                        {},
	"track_io_timing":                     {},
	"track_wal_io_timing":                 {},
	"log_checkpoints":                     {},
	"log_connections":                     {},
	"log_lock_waits":                      {},
	"log_temp_files":                      {},
	"listen_addresses":                    {},
	"port":                                {},
	"timezone":                            {},
	"jit":                                 {},
	"max_pred_locks_per_transaction":      {},
	"max_pred_locks_per_relation":         {},
	"max_pred_locks_per_page":             {},
	"shared_preload_libraries":            {},
	"temp_buffers":                        {},
	"synchronous_standby_names":           {},
}

// isAllowedParam reports whether a GUC parameter name is permitted to be read.
func isAllowedParam(name string) bool {
	_, ok := allowedConfigParams[name]
	return ok
}

// identifierRe enforces a GUC parameter name: starts with a letter or
// underscore, followed by lowercase letters, digits or underscores. This
// matches PostgreSQL GUC naming (dotted hierarchical names are also accepted
// by the pg_settings lookup, but the identifier check here guards the
// unquoted top-level form used for validation).
var identifierRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// maxIdentifierLength mirrors PostgreSQL's identifier length limit.
const maxIdentifierLength = 63

// validateIdentifier checks that value is a legal (unquoted) identifier for
// the named field. It rejects empty values, names over the length limit, and
// names containing characters outside [a-zA-Z_][a-zA-Z0-9_]*.
func validateIdentifier(field, value string) error {
	if value == "" {
		return fmt.Errorf("%w: %s is required", errInvalidArgs, field)
	}
	if len(value) > maxIdentifierLength {
		return fmt.Errorf("%w: %s is too long (max %d)", errInvalidArgs, field, maxIdentifierLength)
	}
	if !identifierRe.MatchString(value) {
		return fmt.Errorf("%w: %s %q is not a valid identifier", errInvalidArgs, field, value)
	}
	return nil
}

// queryRunner is the seam over database.Executor used to run a read-only
// query. *database.Executor satisfies it. It exists so unit tests can stub
// query execution without a live PostgreSQL instance.
type queryRunner interface {
	Execute(ctx context.Context, sql string, params []any, limit int) (*database.ExecResult, error)
}

// Service bundles the dependencies shared by every configuration tool
// handler. It is the value passed to Register and to each handler.
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
}

// NewService constructs a configuration Service.
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
		cfg:     cfg,
		Manager: manager,
		Audit:   auditLogger,
		Metrics: m,
		logger:  slog.Default(),
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

// baseArgs is the argument set for configuration tools. db_get_config uses
// Name; db_get_config_list and db_reload_config use only Target.
type baseArgs struct {
	Target string `json:"target"`
	Name   string `json:"name"`
}

// parseArgs decodes raw tool arguments into baseArgs. requireName reports
// whether the "name" argument is mandatory (db_get_config). It returns a
// wrapped errInvalidArgs on validation failure.
func (s *Service) parseArgs(raw json.RawMessage, requireName bool) (*baseArgs, error) {
	var args baseArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("%w: %v", errInvalidArgs, err)
		}
	}
	if args.Target == "" {
		return nil, fmt.Errorf("%w: missing required argument %q", errInvalidArgs, "target")
	}
	if requireName {
		if args.Name == "" {
			return nil, fmt.Errorf("%w: missing required argument %q", errInvalidArgs, "name")
		}
		if err := validateIdentifier("name", args.Name); err != nil {
			return nil, err
		}
		// Normalize to lowercase: GUC names are case-insensitive and pg_settings
		// stores them in lowercase.
		args.Name = toLower(args.Name)
	}
	return &args, nil
}

// toLower converts a string to lowercase (ASCII).
func toLower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// resolvePool resolves the target alias to a connection pool. The returned
// error is either a wrapped errInvalidArgs (no manager) or a
// database.ErrTargetNotFound for an unknown target.
func (s *Service) resolvePool(target string) (*pgxpool.Pool, error) {
	if s.Manager == nil {
		return nil, fmt.Errorf("%w: no database manager configured", errInvalidArgs)
	}
	pool, err := s.Manager.GetPool(target)
	if err != nil {
		return nil, err
	}
	return pool, nil
}

// runQuery executes a query through the (possibly stubbed) runner, applying
// the configured timeout / row / result-size limits when the real executor is
// used. limit of 0 means "use the configured max rows".
func (s *Service) runQuery(ctx context.Context, pool *pgxpool.Pool, sql string, args []any, limit int) (*database.ExecResult, error) {
	runner := s.runner
	if runner == nil {
		limits := s.cfg.QueryLimits
		runner = database.NewExecutor(pool, limits.Timeout, limits.MaxRows, limits.MaxResultSize, limits.MaxSQLLength)
	}
	return runner.Execute(ctx, sql, args, limit)
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
// structured success result. operation is the audit category ("READ" or
// "MAINTENANCE").
func (s *Service) ok(ctx context.Context, tool, target, operation string, data interface{}, duration time.Duration, truncated bool, rowsReturned int) *mcp.ToolResult {
	s.emitAuditAndMetrics(ctx, tool, target, operation, true, "", duration, rowsReturned)
	return mcp.SuccessResult(data, newMeta(target, duration, truncated, rowsReturned))
}

// fail emits audit + metrics for a failed invocation and returns the
// structured error result.
func (s *Service) fail(ctx context.Context, tool, target, operation, code, msg string, duration time.Duration) *mcp.ToolResult {
	if s.logger != nil {
		s.logger.Debug("configuration tool failed", "tool", tool, "target", target, "error_code", code, "error", msg)
	}
	s.emitAuditAndMetrics(ctx, tool, target, operation, false, code, duration, 0)
	return mcp.ErrorResult(code, msg, newMeta(target, duration, false, 0))
}

// emitAuditAndMetrics writes one audit record and records metrics for a
// finished tool invocation. Both are no-ops when the corresponding
// dependencies are nil, so the helpers are safe to call unconditionally.
func (s *Service) emitAuditAndMetrics(ctx context.Context, tool, target, operation string, success bool, errorCode string, duration time.Duration, rowsReturned int) {
	rec := &audit.AuditRecord{
		Timestamp:    time.Now(),
		Tool:         tool,
		Target:       target,
		Operation:    operation,
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
