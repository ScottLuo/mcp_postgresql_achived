package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sentinel errors for executor error classification.
var (
	// ErrQueryTimeout indicates the query exceeded the statement timeout.
	ErrQueryTimeout = errors.New("db query timeout")

	// ErrSQLTooLong indicates the SQL string exceeds the maximum length.
	ErrSQLTooLong = errors.New("db sql too long")

	// ErrResultTooLarge indicates the result exceeded the maximum result size.
	ErrResultTooLarge = errors.New("db result too large")
)

// ExecResult holds the structured result of a SQL execution.
type ExecResult struct {
	// Columns is the list of column names returned by the query.
	Columns []string
	// Rows contains the result rows; each inner slice is one row.
	Rows [][]any
	// RowsReturned is the number of rows in the result.
	RowsReturned int
	// Truncated is true if the result was cut off due to row limit or size limit.
	Truncated bool
	// Duration is the total execution time.
	Duration time.Duration
}

// Executor provides a unified SQL execution interface with server-side
// enforcement of limits. Limits are set at construction time and cannot
// be overridden by callers.
type Executor struct {
	pool           *pgxpool.Pool
	timeout        time.Duration
	maxRows        int
	maxResultSize  int
	maxSQLLength   int
}

// NewExecutor creates an Executor with the given limits.
// All limit values must be positive.
func NewExecutor(pool *pgxpool.Pool, timeout time.Duration, maxRows, maxResultSize, maxSQLLength int) *Executor {
	return &Executor{
		pool:          pool,
		timeout:       timeout,
		maxRows:       maxRows,
		maxResultSize: maxResultSize,
		maxSQLLength:  maxSQLLength,
	}
}

// Execute runs a parameterized SQL query with all limits enforced.
// The limit parameter caps the number of rows returned (0 means use maxRows).
// If limit > 0 and limit < maxRows, limit takes precedence.
func (e *Executor) Execute(ctx context.Context, sql string, params []any, limit int) (*ExecResult, error) {
	// Enforce max-SQL-length check first (before any I/O).
	if len(sql) > e.maxSQLLength {
		return nil, fmt.Errorf("%w: length %d exceeds max %d", ErrSQLTooLong, len(sql), e.maxSQLLength)
	}

	// Determine effective row limit.
	effectiveLimit := e.maxRows
	if limit > 0 && limit < e.maxRows {
		effectiveLimit = limit
	}

	// Wrap context with statement timeout.
	// Use min(ctx deadline, timeout) — if ctx already has a shorter deadline, keep it.
	queryCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	start := time.Now()

	// Execute parameterized query.
	rows, err := e.pool.Query(queryCtx, sql, params...)
	if err != nil {
		return nil, e.classifyError(ctx, err)
	}
	defer rows.Close()

	// Extract column names.
	fieldDescriptions := rows.FieldDescriptions()
	columns := make([]string, len(fieldDescriptions))
	for i, fd := range fieldDescriptions {
		columns[i] = string(fd.Name)
	}

	result := &ExecResult{
		Columns: columns,
	}

	// Track serialized result size (approximate: count JSON-encodable size).
	resultBytes := 0

	for rows.Next() {
		if result.RowsReturned >= effectiveLimit {
			result.Truncated = true
			break
		}

		values, err := rows.Values()
		if err != nil {
			return nil, e.classifyError(ctx, err)
		}
		result.Rows = append(result.Rows, values)
		result.RowsReturned++

		// Estimate result size by JSON-encoding each row.
		rowJSON, err := json.Marshal(values)
		if err == nil {
			resultBytes += len(rowJSON)
		}
		if resultBytes > e.maxResultSize {
			result.Truncated = true
			break
		}
	}

	if err := rows.Err(); err != nil {
		return nil, e.classifyError(ctx, err)
	}

	result.Duration = time.Since(start)
	return result, nil
}

// classifyError converts pgx errors into typed sentinel errors.
func (e *Executor) classifyError(ctx context.Context, err error) error {
	// Check if the context was cancelled (not just the queryCtx — the parent ctx).
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s", ErrQueryTimeout, maskError(err))
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s", ErrQueryTimeout, maskError(err))
	}
	// pgconn.PgError for specific PG errors.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return fmt.Errorf("db execution failed: %s (code: %s)", pgErr.Message, pgErr.Code)
	}
	// Mask DSN from generic errors.
	return fmt.Errorf("db execution failed: %s", maskError(err))
}

// maskError sanitizes an error message to remove DSN patterns.
func maskError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	// Mask user:password@host patterns.
	if idx := strings.Index(msg, "@"); idx > 0 {
		// Check if there's a "://" before the @ indicating a DSN.
		if protoIdx := strings.Index(msg, "://"); protoIdx >= 0 && protoIdx < idx {
			rest := msg[protoIdx+3:]
			atRel := idx - (protoIdx + 3)
			creds := rest[:atRel]
			if colonIdx := strings.Index(creds, ":"); colonIdx >= 0 {
				user := creds[:colonIdx]
				return msg[:protoIdx] + "://" + user + ":****" + rest[atRel:]
			}
		}
	}
	return msg
}
