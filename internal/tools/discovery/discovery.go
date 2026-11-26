// Package discovery implements the database discovery MCP tools (spec §7.1):
// server info, database listing, schema listing, table listing, table
// description (columns + constraints), index listing, constraint listing,
// table size, and column statistics.
//
// Every tool is READ_ONLY. All handlers:
//   - validate their arguments (target is required; schema/table where
//     applicable; identifiers are checked for invalid characters),
//   - resolve the connection pool via the metaExecutor,
//   - call the database metadata functions (which execute parameterized or
//     identifier-quoted catalog queries),
//   - emit one audit record and record metrics per invocation,
//   - report "feature unavailable" (DB_FEATURE_UNAVAILABLE) when statistics
//     have never been collected.
package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"postgres-dba-mcp/internal/audit"
	"postgres-dba-mcp/internal/config"
	"postgres-dba-mcp/internal/database"
	"postgres-dba-mcp/internal/mcp"
	"postgres-dba-mcp/internal/metrics"
)

// operationRead is the audit operation category for all discovery tools.
const operationRead = "READ"

// errInvalidArgs is returned for argument-validation failures.
var errInvalidArgs = errors.New("invalid tool arguments")

// ---------------------------------------------------------------------------
// metaExecutor abstracts pool resolution + metadata queries so that unit
// tests can stub catalog access without a live PostgreSQL.
// ---------------------------------------------------------------------------

// metaExecutor is the seam over the database metadata layer. Each method
// resolves the target to a pool internally and delegates to the corresponding
// database.* function.
type metaExecutor interface {
	GetInfo(ctx context.Context, target string) (*database.ServerInfo, error)
	ListDatabases(ctx context.Context, target string) ([]database.DatabaseInfo, error)
	ListSchemas(ctx context.Context, target string) ([]database.SchemaInfo, error)
	ListTables(ctx context.Context, target, schema string) ([]database.TableInfo, error)
	DescribeTable(ctx context.Context, target, schema, table string) ([]database.ColumnInfo, error)
	ListIndexes(ctx context.Context, target, schema, table string) ([]database.IndexInfo, error)
	ListConstraints(ctx context.Context, target, schema, table string) ([]database.ConstraintInfo, error)
	GetTableSize(ctx context.Context, target, schema, table string) (*database.TableSizeInfo, error)
	GetColumnStats(ctx context.Context, target, schema, table string) ([]database.ColumnStats, error)
}

// realMetaExecutor is the production metaExecutor. It resolves the pool via
// the DatabaseManager and delegates to the database package metadata functions.
type realMetaExecutor struct {
	manager database.DatabaseManager
}

func (r *realMetaExecutor) GetInfo(ctx context.Context, target string) (*database.ServerInfo, error) {
	pool, err := r.manager.GetPool(target)
	if err != nil {
		return nil, err
	}
	return database.GetInfo(ctx, pool)
}

func (r *realMetaExecutor) ListDatabases(ctx context.Context, target string) ([]database.DatabaseInfo, error) {
	pool, err := r.manager.GetPool(target)
	if err != nil {
		return nil, err
	}
	return database.ListDatabases(ctx, pool)
}

func (r *realMetaExecutor) ListSchemas(ctx context.Context, target string) ([]database.SchemaInfo, error) {
	pool, err := r.manager.GetPool(target)
	if err != nil {
		return nil, err
	}
	return database.ListSchemas(ctx, pool)
}

func (r *realMetaExecutor) ListTables(ctx context.Context, target, schema string) ([]database.TableInfo, error) {
	pool, err := r.manager.GetPool(target)
	if err != nil {
		return nil, err
	}
	return database.ListTables(ctx, pool, schema)
}

func (r *realMetaExecutor) DescribeTable(ctx context.Context, target, schema, table string) ([]database.ColumnInfo, error) {
	pool, err := r.manager.GetPool(target)
	if err != nil {
		return nil, err
	}
	return database.DescribeTable(ctx, pool, schema, table)
}

func (r *realMetaExecutor) ListIndexes(ctx context.Context, target, schema, table string) ([]database.IndexInfo, error) {
	pool, err := r.manager.GetPool(target)
	if err != nil {
		return nil, err
	}
	return database.ListIndexes(ctx, pool, schema, table)
}

func (r *realMetaExecutor) ListConstraints(ctx context.Context, target, schema, table string) ([]database.ConstraintInfo, error) {
	pool, err := r.manager.GetPool(target)
	if err != nil {
		return nil, err
	}
	return database.ListConstraints(ctx, pool, schema, table)
}

func (r *realMetaExecutor) GetTableSize(ctx context.Context, target, schema, table string) (*database.TableSizeInfo, error) {
	pool, err := r.manager.GetPool(target)
	if err != nil {
		return nil, err
	}
	return database.GetTableSize(ctx, pool, schema, table)
}

func (r *realMetaExecutor) GetColumnStats(ctx context.Context, target, schema, table string) ([]database.ColumnStats, error) {
	pool, err := r.manager.GetPool(target)
	if err != nil {
		return nil, err
	}
	return database.GetColumnStats(ctx, pool, schema, table)
}

// ---------------------------------------------------------------------------
// Service
// ---------------------------------------------------------------------------

// Service bundles the dependencies shared by every discovery tool handler.
type Service struct {
	cfg     *config.Config
	Manager database.DatabaseManager
	Audit   *audit.AuditLogger
	Metrics *metrics.Metrics
	logger  *slog.Logger

	// meta is the seam over database metadata. Defaults to a
	// *realMetaExecutor wrapping the Manager. Overridable in tests.
	meta metaExecutor
}

// NewService constructs a discovery Service.
func NewService(cfg *config.Config, manager database.DatabaseManager, auditLogger *audit.AuditLogger, m *metrics.Metrics) *Service {
	if cfg == nil {
		cfg = &config.Config{QueryLimits: config.QueryLimits{
			Timeout:       10 * time.Second,
			MaxRows:       1000,
			MaxResultSize: 1048576,
			MaxSQLLength:  65536,
		}}
	}
	svc := &Service{
		cfg:     cfg,
		Manager: manager,
		Audit:   auditLogger,
		Metrics: m,
		logger:  slog.Default(),
	}
	if manager != nil {
		svc.meta = &realMetaExecutor{manager: manager}
	}
	return svc
}

// ---------------------------------------------------------------------------
// Argument parsing and validation
// ---------------------------------------------------------------------------

// baseArgs is the argument set shared by every discovery tool.
type baseArgs struct {
	Target string `json:"target"`
	Schema string `json:"schema"`
	Table  string `json:"table"`
}

// parseArgs decodes raw tool arguments into baseArgs and validates the
// required fields.
//   - target is always required.
//   - schema is required when requireSchema is true.
//   - table is required when requireTable is true.
//
// Identifier fields (schema, table) are validated to reject characters that
// could interfere with SQL quoting (semicolons, newlines, carriage returns).
func (s *Service) parseArgs(raw json.RawMessage, requireSchema, requireTable bool) (*baseArgs, error) {
	var args baseArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("%w: %v", errInvalidArgs, err)
		}
	}
	if args.Target == "" {
		return nil, fmt.Errorf("%w: missing required argument %q", errInvalidArgs, "target")
	}
	if requireSchema {
		if args.Schema == "" {
			return nil, fmt.Errorf("%w: missing required argument %q", errInvalidArgs, "schema")
		}
		if err := validateIdentifier("schema", args.Schema); err != nil {
			return nil, err
		}
	}
	if requireTable {
		if args.Table == "" {
			return nil, fmt.Errorf("%w: missing required argument %q", errInvalidArgs, "table")
		}
		if err := validateIdentifier("table", args.Table); err != nil {
			return nil, err
		}
	}
	return &args, nil
}

// validateIdentifier checks that a SQL identifier does not contain characters
// that would break quoted-identifier syntax or enable injection.
// PostgreSQL identifiers must not contain: NUL, semicolons, newlines.
// The identifier is always wrapped in double quotes by the database layer.
func validateIdentifier(field, value string) error {
	if strings.ContainsAny(value, ";\n\r\x00") {
		return fmt.Errorf("%w: %q contains invalid characters for a SQL identifier", errInvalidArgs, field)
	}
	if len(value) > 63 {
		return fmt.Errorf("%w: %q exceeds maximum identifier length (63)", errInvalidArgs, field)
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

// errCodeForErr maps a database error to a standard MCP error code.
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

// ok emits audit + metrics for a successful invocation and returns the result.
func (s *Service) ok(ctx context.Context, tool, target string, data interface{}, duration time.Duration, rowsReturned int) *mcp.ToolResult {
	s.emitAuditAndMetrics(ctx, tool, target, true, "", duration, rowsReturned)
	return mcp.SuccessResult(data, newMeta(target, duration, rowsReturned))
}

// fail emits audit + metrics for a failed invocation and returns the result.
func (s *Service) fail(ctx context.Context, tool, target, code, msg string, duration time.Duration) *mcp.ToolResult {
	if s.logger != nil {
		s.logger.Debug("discovery tool failed", "tool", tool, "target", target, "error_code", code, "error", msg)
	}
	s.emitAuditAndMetrics(ctx, tool, target, false, code, duration, 0)
	return mcp.ErrorResult(code, msg, newMeta(target, duration, 0))
}

// emitAuditAndMetrics writes one audit record and records metrics.
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
	registerGetInfo,
	registerListDatabases,
	registerListSchemas,
	registerListTables,
	registerDescribeTable,
	registerListIndexes,
	registerListConstraints,
	registerGetTableSize,
	registerGetColumnStats,
}

// Register registers all discovery ToolDefinitions with the registry.
// Every tool is READ_ONLY.
func Register(reg *mcp.Registry, s *Service) error {
	for _, register := range allRegistrars {
		if err := register(reg, s); err != nil {
			return err
		}
	}
	return nil
}

// ToolCount returns the number of discovery tools this package registers.
func ToolCount() int {
	return len(allRegistrars)
}
