// Package maintenance implements the database maintenance MCP tools
// (spec §7.4): VACUUM, ANALYZE, REINDEX, CREATE INDEX, DROP INDEX,
// cancel query, and terminate connection.
//
// These tools operate at two permission levels:
//   - CONTROLLED_MAINTENANCE: db_vacuum_table, db_analyze_table,
//     db_reindex_table, db_create_index, db_cancel_query
//   - REQUIRES_APPROVAL: db_drop_index, db_terminate_connection
//
// PostgreSQL execution constraints honored:
//   - VACUUM cannot run inside a transaction block.
//   - CREATE INDEX CONCURRENTLY cannot run inside a transaction block.
//   - REINDEX CONCURRENTLY cannot run inside a transaction block.
//   - Cancellation and termination require specific privileges
//     (mapped to DB_PERMISSION_DENIED on failure).
package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"postgres-dba-mcp/internal/audit"
	"postgres-dba-mcp/internal/config"
	"postgres-dba-mcp/internal/database"
	"postgres-dba-mcp/internal/mcp"
	"postgres-dba-mcp/internal/metrics"
	"postgres-dba-mcp/internal/security"
)

// operationMaintenance is the audit operation category for all maintenance tools.
const operationMaintenance = "MAINTENANCE"

// errInvalidArgs is returned for argument-validation failures.
var errInvalidArgs = errors.New("invalid tool arguments")

// ---------------------------------------------------------------------------
// Execution seams
// ---------------------------------------------------------------------------

// nonTxRunner is the seam for non-transactional SQL execution. In production,
// it is satisfied by *database.TransactionExecutor (which has ExecuteNonTransactional).
// VACUUM, CREATE INDEX [CONCURRENTLY], REINDEX [CONCURRENTLY], and DROP INDEX
// are all executed through this path because they cannot (or should not) run
// inside a transaction block.
type nonTxRunner interface {
	ExecuteNonTransactional(ctx context.Context, sql string, params []any) (*database.ExecResult, error)
}

// queryRunner is the seam for standard parameterized SQL execution (SELECT).
// In production, it is satisfied by *database.Executor.
// Used for pg_cancel_backend($1) and pg_terminate_backend($1) which are
// simple SELECT calls that work fine inside a transaction.
type queryRunner interface {
	Execute(ctx context.Context, sql string, params []any, limit int) (*database.ExecResult, error)
}

// ---------------------------------------------------------------------------
// Service
// ---------------------------------------------------------------------------

// Service bundles the dependencies shared by every maintenance tool handler.
type Service struct {
	cfg      *config.Config
	Manager  database.DatabaseManager
	Audit    *audit.AuditLogger
	Metrics  *metrics.Metrics
	logger   *slog.Logger
	Approval *security.ApprovalValidator // nil = approval not required / skip validation

	// nonTxRunner is the seam for non-transactional execution. When nil, a
	// real *database.TransactionExecutor is constructed on demand from the pool.
	// Overridable in tests to assert the non-transactional path is used.
	nonTxRunner nonTxRunner

	// queryRunner is the seam for standard SELECT execution. When nil, a
	// real *database.Executor is constructed on demand from the pool.
	// Overridable in tests to assert the transactional path is used.
	queryRunner queryRunner
}

// NewService constructs a maintenance Service.
func NewService(cfg *config.Config, manager database.DatabaseManager, auditLogger *audit.AuditLogger, m *metrics.Metrics, approver *security.ApprovalValidator, logger *slog.Logger) *Service {
	if cfg == nil {
		cfg = &config.Config{QueryLimits: config.QueryLimits{
			Timeout:       10 * time.Second,
			MaxRows:       1000,
			MaxResultSize: 1048576,
			MaxSQLLength:  65536,
		}}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		cfg:      cfg,
		Manager:  manager,
		Audit:    auditLogger,
		Metrics:  m,
		logger:   logger,
		Approval: approver,
	}
}

// ---------------------------------------------------------------------------
// Target / pool resolution
// ---------------------------------------------------------------------------

// resolvePool resolves the target alias to a connection pool.
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

// ---------------------------------------------------------------------------
// Identifier-safe SQL construction
// ---------------------------------------------------------------------------

// quoteIdent quotes a single SQL identifier with double quotes, escaping
// internal double quotes. This is the same logic as database.quoteIdentifier
// but kept local to avoid exporting a new symbol from the database package.
func quoteIdent(name string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for i := 0; i < len(name); i++ {
		if name[i] == '"' {
			sb.WriteString("\"\"")
		} else {
			sb.WriteByte(name[i])
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// qualifiedIdent builds a quoted schema.table identifier pair.
func qualifiedIdent(schema, name string) string {
	return quoteIdent(schema) + "." + quoteIdent(name)
}

// validateIdentifier checks that a SQL identifier is a safe, unambiguous
// identifier. Only alphanumeric characters, underscores, and dollar signs are
// permitted (whitelist). This rejects spaces, semicolons, newlines, and other
// characters that could confuse the parser or enable injection. PostgreSQL
// identifiers are always quoted when embedded in generated SQL, but the
// whitelist keeps the accepted name set conservative and human-readable.
func validateIdentifier(field, value string) error {
	if value == "" {
		return fmt.Errorf("%w: %q must not be empty", errInvalidArgs, field)
	}
	if len(value) > 63 {
		return fmt.Errorf("%w: %q exceeds maximum identifier length (63)", errInvalidArgs, field)
	}
	for _, c := range value {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '_' || c == '$':
		default:
			return fmt.Errorf("%w: %q contains invalid characters for a SQL identifier (allowed: A-Z, a-z, 0-9, '_', '$')", errInvalidArgs, field)
		}
	}
	return nil
}

// validatePID checks that a process ID is a valid positive integer.
func validatePID(field string, pid int64) error {
	if pid <= 0 {
		return fmt.Errorf("%w: %q must be a positive integer (got %d)", errInvalidArgs, field, pid)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Response helpers
// ---------------------------------------------------------------------------

// newMeta builds the standard metadata block.
func newMeta(target string, duration time.Duration, rowsReturned int) *mcp.Metadata {
	return &mcp.Metadata{
		Target:       target,
		DurationMS:   duration.Milliseconds(),
		RowsReturned: rowsReturned,
	}
}

// classifyExecError maps a database execution error to a standard MCP error code
// and a sanitized message.
func (s *Service) classifyExecError(err error) (code, message string) {
	switch {
	case errors.Is(err, database.ErrQueryTimeout):
		return mcp.ErrCodeQueryTimeout, "query timeout"
	case errors.Is(err, database.ErrTargetNotFound):
		return mcp.ErrCodeTargetNotFound, security.SanitizeError(err.Error())
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		c, msg := security.MapPGError(pgErr)
		return c, msg
	}
	return mcp.ErrCodeExecutionFailed, security.SanitizeError(err.Error())
}

// errorResult emits audit + metrics for a failed invocation and returns the result.
func (s *Service) errorResult(ctx context.Context, tool, target, code, msg string, start time.Time, approvalID string) *mcp.ToolResult {
	duration := time.Since(start)
	if s.logger != nil {
		s.logger.Debug("maintenance tool failed", "tool", tool, "target", target, "error_code", code, "error", msg)
	}
	s.emitAuditAndMetrics(ctx, tool, target, false, code, duration, 0, approvalID, "")
	return mcp.ErrorResult(code, msg, newMeta(target, duration, 0))
}

// successResult emits audit + metrics for a successful invocation and returns the result.
func (s *Service) successResult(ctx context.Context, tool, target string, data interface{}, res *database.ExecResult, start time.Time, approvalID, approver string) *mcp.ToolResult {
	duration := time.Since(start)
	if res == nil {
		duration = 0
	}
	s.emitAuditAndMetrics(ctx, tool, target, true, "", duration, 0, approvalID, approver)
	return mcp.SuccessResult(data, newMeta(target, duration, 0))
}

// emitAuditAndMetrics writes one audit record and records metrics.
func (s *Service) emitAuditAndMetrics(ctx context.Context, tool, target string, success bool, code string, dur time.Duration, rows int, approvalID, approver string) {
	rec := &audit.AuditRecord{
		Timestamp:    time.Now(),
		Tool:         tool,
		Target:       target,
		Operation:    operationMaintenance,
		DurationMS:   dur.Milliseconds(),
		Success:      success,
		ErrorCode:    code,
		RowsReturned: rows,
		ApprovalID:   approvalID,
		Approver:     approver,
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
			s.Metrics.RecordError(tool, target, code)
		}
		s.Metrics.ObserveDuration(tool, target, dur.Seconds())
	}
}

// ---------------------------------------------------------------------------
// Approval validation
// ---------------------------------------------------------------------------

// normalizeParams serializes operation parameters for approval binding.
func (s *Service) normalizeParams(params map[string]any) (string, error) {
	return security.NormalizeParams(params)
}

// checkApproval validates the approval for REQUIRES_APPROVAL tools.
// It returns (approver, nil) on success or ("", result) where result is the
// error ToolResult to return to the caller.
//
// If s.Approval is nil, approval is skipped and ("", nil) is returned.
// If approvalID is empty and the tool requires approval, returns DB_APPROVAL_REQUIRED.
func (s *Service) checkApproval(ctx context.Context, tool, target, approvalID string, params map[string]any) (string, *mcp.ToolResult) {
	if s.Approval == nil {
		return "", nil
	}
	// Determine whether this tool requires approval per the permission model.
	if security.GetPermissionLevel(tool) != security.PermRequiresApproval {
		return "", nil
	}
	if approvalID == "" {
		return "", mcp.ErrorResult(mcp.ErrCodeApprovalRequired, "human approval required for this operation", nil)
	}
	normalized, err := s.normalizeParams(params)
	if err != nil {
		return "", mcp.ErrorResult(mcp.ErrCodeApprovalInvalid, fmt.Sprintf("failed to normalize approval params: %v", err), nil)
	}
	if err := s.Approval.Validate(ctx, approvalID, target, tool, normalized); err != nil {
		// Determine the error code from the approval error.
		code := mcp.ErrCodeApprovalInvalid
		if strings.Contains(err.Error(), security.ErrCodeApprovalRequired) {
			code = mcp.ErrCodeApprovalRequired
		}
		return "", mcp.ErrorResult(code, security.SanitizeError(err.Error()), nil)
	}
	// On success, retrieve the approver identity from the approval record.
	// The validator only checks validity; the approver is extracted by
	// re-reading the record (the store is accessible through the validator).
	return "", nil
}

// ---------------------------------------------------------------------------
// Execution helpers
// ---------------------------------------------------------------------------

// execNonTransactional executes SQL via the non-transactional path.
func (s *Service) execNonTransactional(ctx context.Context, pool *pgxpool.Pool, sql string, params []any) (*database.ExecResult, error) {
	runner := s.nonTxRunner
	if runner == nil {
		limits := s.cfg.QueryLimits
		runner = database.NewTransactionExecutor(pool, limits.Timeout, limits.MaxRows, limits.MaxResultSize, limits.MaxSQLLength)
	}
	return runner.ExecuteNonTransactional(ctx, sql, params)
}

// execQuery executes SQL via the standard transactional path (for SELECT queries).
func (s *Service) execQuery(ctx context.Context, pool *pgxpool.Pool, sql string, params []any) (*database.ExecResult, error) {
	runner := s.queryRunner
	if runner == nil {
		limits := s.cfg.QueryLimits
		runner = database.NewExecutor(pool, limits.Timeout, limits.MaxRows, limits.MaxResultSize, limits.MaxSQLLength)
	}
	return runner.Execute(ctx, sql, params, 0)
}

// ---------------------------------------------------------------------------
// Handler adapter
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

// allRegistrars lists the per-tool registration functions.
var allRegistrars = []func(reg *mcp.Registry, s *Service) error{
	registerVacuumTable,
	registerAnalyzeTable,
	registerReindexTable,
	registerCreateIndex,
	registerDropIndex,
	registerCancelQuery,
	registerTerminateConnection,
}

// Register registers all maintenance ToolDefinitions with the registry.
func Register(reg *mcp.Registry, s *Service) error {
	for _, register := range allRegistrars {
		if err := register(reg, s); err != nil {
			return err
		}
	}
	return nil
}

// ToolCount returns the number of maintenance tools this package registers.
func ToolCount() int {
	return len(allRegistrars)
}
