package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotInTransaction is returned when a non-transactional operation is attempted
// inside a transaction block, or vice versa.
var ErrNotInTransaction = errors.New("db: operation must not run inside a transaction block")

// TransactionExecutor provides transaction-aware SQL execution.
type TransactionExecutor struct {
	*Executor
}

// NewTransactionExecutor wraps an Executor with transaction support.
func NewTransactionExecutor(pool *pgxpool.Pool, timeout time.Duration, maxRows, maxResultSize, maxSQLLength int) *TransactionExecutor {
	return &TransactionExecutor{
		Executor: NewExecutor(pool, timeout, maxRows, maxResultSize, maxSQLLength),
	}
}

// ExecuteReadOnly runs a parameterized query inside a READ ONLY transaction.
// This provides double enforcement: PostgreSQL will reject any write statements
// that slip through SQL validation.
//
// The transaction is always rolled back on error and committed on success
// (commit of a read-only transaction is a no-op for state).
func (tx *TransactionExecutor) ExecuteReadOnly(ctx context.Context, sql string, params []any, limit int) (*ExecResult, error) {
	if len(sql) > tx.maxSQLLength {
		return nil, fmt.Errorf("%w: length %d exceeds max %d", ErrSQLTooLong, len(sql), tx.maxSQLLength)
	}

	// Determine effective row limit.
	effectiveLimit := tx.maxRows
	if limit > 0 && limit < tx.maxRows {
		effectiveLimit = limit
	}

	// Apply statement timeout via context.
	queryCtx, cancel := context.WithTimeout(ctx, tx.timeout)
	defer cancel()

	start := time.Now()

	// Begin a read-only transaction.
	txOpts := pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	}

	conn, err := tx.pool.Acquire(queryCtx)
	if err != nil {
		return nil, tx.classifyError(ctx, err)
	}
	defer conn.Release()

	txn, err := conn.BeginTx(queryCtx, txOpts)
	if err != nil {
		return nil, tx.classifyError(ctx, err)
	}

	// Always defer: commit on success, rollback on error.
	defer func() {
		if err != nil {
			_ = txn.Rollback(queryCtx)
			return
		}
		commitErr := txn.Commit(queryCtx)
		if commitErr != nil && commitErr != pgx.ErrTxClosed {
			err = commitErr
		}
	}()

	// Execute the query within the transaction.
	rows, err := txn.Query(queryCtx, sql, params...)
	if err != nil {
		return nil, tx.classifyError(ctx, err)
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

	resultBytes := 0
	for rows.Next() {
		if result.RowsReturned >= effectiveLimit {
			result.Truncated = true
			break
		}
		values, err := rows.Values()
		if err != nil {
			return nil, tx.classifyError(ctx, err)
		}
		result.Rows = append(result.Rows, values)
		result.RowsReturned++

		// Track result size.
		if b, marshalErr := jsonMarshalRow(values); marshalErr == nil {
			resultBytes += len(b)
		}
		if resultBytes > tx.maxResultSize {
			result.Truncated = true
			break
		}
	}

	if err := rows.Err(); err != nil {
		return nil, tx.classifyError(ctx, err)
	}

	result.Duration = time.Since(start)
	return result, nil
}

// ExecuteNonTransactional executes a SQL statement on a single pool connection
// WITHOUT a transaction block. This is required for operations that cannot
// run inside a transaction:
//   - VACUUM
//   - CREATE INDEX ... CONCURRENTLY
//   - REINDEX ... CONCURRENTLY
//
// The statement is executed via pgx Exec, and the result (if any rows are
// returned by the command) is captured.
func (tx *TransactionExecutor) ExecuteNonTransactional(ctx context.Context, sql string, params []any) (*ExecResult, error) {
	if len(sql) > tx.maxSQLLength {
		return nil, fmt.Errorf("%w: length %d exceeds max %d", ErrSQLTooLong, len(sql), tx.maxSQLLength)
	}

	queryCtx, cancel := context.WithTimeout(ctx, tx.timeout)
	defer cancel()

	start := time.Now()

	// Acquire a single connection (not inside a transaction).
	conn, err := tx.pool.Acquire(queryCtx)
	if err != nil {
		return nil, tx.classifyError(ctx, err)
	}
	defer conn.Release()

	// Use Exec for commands that return rows (e.g., VACUUM returns no rows,
	// but some DDL statements return command tags).
	cmdTag, err := conn.Exec(queryCtx, sql, params...)
	if err != nil {
		return nil, tx.classifyError(ctx, err)
	}

	result := &ExecResult{
		Columns:      []string{"command"},
		Rows:         [][]any{{cmdTag.String()}},
		RowsReturned: 1,
		Duration:     time.Since(start),
	}
	return result, nil
}

// jsonMarshalRow serializes a row for result-size tracking.
func jsonMarshalRow(values []any) ([]byte, error) {
	return json.Marshal(values)
}
