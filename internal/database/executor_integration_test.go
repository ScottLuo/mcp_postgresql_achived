//go:build integration

package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// newPoolFromDSN creates a pgxpool with minimal settings and waits for it to be ready.
func newPoolFromDSN(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()

	ctx := context.Background()
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("failed to parse DSN: %v", err)
	}
	poolCfg.MaxConns = 5
	poolCfg.MinConns = 1

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}

	// Retry ping up to 30 times with 500ms intervals (up to ~15s total).
	// This handles the case where the container port is open but PostgreSQL
	// is still initializing internally.
	var lastErr error
	for i := 0; i < 30; i++ {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		lastErr = pool.Ping(pingCtx)
		cancel()
		if lastErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if lastErr != nil {
		pool.Close()
		t.Fatalf("pool ping failed after retries: %v", lastErr)
	}

	return pool
}

// startPostgres starts a PostgreSQL container via testcontainers.
func startPostgres(t *testing.T) (dsn string, cleanup func()) {
	t.Helper()

	ctx := context.Background()

	container, err := postgres.RunContainer(ctx,
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		postgres.WithDatabase("testdb"),
	)
	if err != nil {
		t.Fatalf("failed to start postgres container: %v", err)
	}

	connStr, err := container.ConnectionString(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("failed to get connection string: %v", err)
	}

	return connStr, func() {
		if err := container.Terminate(ctx); err != nil {
			t.Logf("warning: failed to terminate container: %v", err)
		}
	}
}

// TestExecutorPoolInit verifies that pool initialization works against a real DB.
func TestExecutorPoolInit(t *testing.T) {
	dsn, cleanup := startPostgres(t)
	defer cleanup()

	pool := newPoolFromDSN(t, dsn)
	defer pool.Close()

	// Verify we can query the server version.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var version string
	if err := pool.QueryRow(ctx, "SELECT version()").Scan(&version); err != nil {
		t.Fatalf("failed to query version: %v", err)
	}
	if !strings.Contains(version, "PostgreSQL") {
		t.Errorf("version = %q, expected to contain 'PostgreSQL'", version)
	}
}

// TestExecutorRowLimit verifies that row-limit enforcement works against a real DB.
func TestExecutorRowLimit(t *testing.T) {
	dsn, cleanup := startPostgres(t)
	defer cleanup()

	pool := newPoolFromDSN(t, dsn)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Setup test data.
	if _, err := pool.Exec(ctx, `
		DROP TABLE IF EXISTS test_rows;
		CREATE TABLE test_rows (id INT PRIMARY KEY, val TEXT);
		INSERT INTO test_rows
		SELECT i, 'val_' || i FROM generate_series(1, 50) AS i;
	`); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	// Create executor with maxRows=10.
	exec := NewExecutor(pool, 10*time.Second, 10, 1024, 65536)

	// Execute with limit=5.
	result, err := exec.Execute(ctx,
		"SELECT id, val FROM test_rows ORDER BY id", nil, 5)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.RowsReturned != 5 {
		t.Errorf("RowsReturned = %d, want 5", result.RowsReturned)
	}
	if !result.Truncated {
		t.Error("Truncated = false, want true (only 5 of 50 rows returned)")
	}

	// Execute with limit=20 (> maxRows=10, should cap at 10).
	result2, err := exec.Execute(ctx,
		"SELECT id, val FROM test_rows ORDER BY id", nil, 20)
	if err != nil {
		t.Fatalf("Execute2 failed: %v", err)
	}
	if result2.RowsReturned != 10 {
		t.Errorf("RowsReturned = %d, want 10 (capped at maxRows)", result2.RowsReturned)
	}
	if !result2.Truncated {
		t.Error("Truncated = false, want true (capped at maxRows=10 of 50)")
	}
}

// TestExecutorStatementTimeout verifies that statement timeout enforcement works.
func TestExecutorStatementTimeout(t *testing.T) {
	dsn, cleanup := startPostgres(t)
	defer cleanup()

	pool := newPoolFromDSN(t, dsn)
	defer pool.Close()

	// Create executor with very short timeout.
	exec := NewExecutor(pool, 500*time.Millisecond, 100, 1024, 65536)

	// A query that sleeps longer than the timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := exec.Execute(ctx, `SELECT pg_sleep(2)`, nil, 0)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, ErrQueryTimeout) {
		t.Errorf("expected ErrQueryTimeout, got: %v", err)
	}
}

// TestReadOnlyTransactionRejectsWrites verifies read-only transaction behavior.
func TestReadOnlyTransactionRejectsWrites(t *testing.T) {
	dsn, cleanup := startPostgres(t)
	defer cleanup()

	pool := newPoolFromDSN(t, dsn)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Setup table.
	if _, err := pool.Exec(ctx, `
		DROP TABLE IF EXISTS test_readonly;
		CREATE TABLE test_readonly (id INT PRIMARY KEY, val TEXT);
		INSERT INTO test_readonly VALUES (1, 'a');
	`); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	// Create a read-only transaction executor.
	txExec := NewTransactionExecutor(pool, 10*time.Second, 100, 1024, 65536)

	// A SELECT should work.
	result, err := txExec.ExecuteReadOnly(ctx,
		"SELECT id, val FROM test_readonly", nil, 0)
	if err != nil {
		t.Fatalf("ExecuteReadOnly SELECT failed: %v", err)
	}
	if result.RowsReturned != 1 {
		t.Errorf("RowsReturned = %d, want 1", result.RowsReturned)
	}

	// An UPDATE should fail (read-only transaction rejects writes).
	_, err = txExec.ExecuteReadOnly(ctx,
		"UPDATE test_readonly SET val = 'b' WHERE id = 1", nil, 0)
	if err == nil {
		t.Fatal("expected read-only transaction to reject UPDATE, got nil error")
	}
	// The error should mention "read-only" or contain the PG error code.
	errStr := strings.ToLower(err.Error())
	if !strings.Contains(errStr, "read-only") &&
		!strings.Contains(errStr, "readonly") &&
		!strings.Contains(errStr, "40006") {
		t.Logf("Note: error message was: %v", err)
	}
}

// TestExecuteNonTransactional verifies that non-transactional VACUUM works.
func TestExecuteNonTransactional(t *testing.T) {
	dsn, cleanup := startPostgres(t)
	defer cleanup()

	pool := newPoolFromDSN(t, dsn)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Setup table.
	if _, err := pool.Exec(ctx, `
		DROP TABLE IF EXISTS test_maint;
		CREATE TABLE test_maint (id INT PRIMARY KEY, val TEXT);
		INSERT INTO test_maint VALUES (1, 'a');
	`); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	txExec := NewTransactionExecutor(pool, 10*time.Second, 100, 1024, 65536)

	// VACUUM cannot run inside a transaction block — verify it works here.
	result, err := txExec.ExecuteNonTransactional(ctx, "VACUUM test_maint", nil)
	if err != nil {
		t.Fatalf("ExecuteNonTransactional VACUUM failed: %v", err)
	}
	if result.RowsReturned != 1 {
		t.Errorf("VACUUM RowsReturned = %d, want 1", result.RowsReturned)
	}
	if len(result.Rows) > 0 && len(result.Rows[0]) > 0 {
		cmdTag, ok := result.Rows[0][0].(string)
		if !ok {
			t.Errorf("command tag is not string: %T", result.Rows[0][0])
		} else if !strings.HasPrefix(cmdTag, "VACUUM") {
			t.Errorf("command tag = %q, want prefix VACUUM", cmdTag)
		}
	}
}

// TestGetInfoIntegration verifies GetInfo works against a real DB.
func TestGetInfoIntegration(t *testing.T) {
	dsn, cleanup := startPostgres(t)
	defer cleanup()

	pool := newPoolFromDSN(t, dsn)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info, err := GetInfo(ctx, pool)
	if err != nil {
		t.Fatalf("GetInfo failed: %v", err)
	}
	if !strings.Contains(info.Version, "PostgreSQL") {
		t.Errorf("Version = %q, expected to contain 'PostgreSQL'", info.Version)
	}
	if info.Database == "" {
		t.Error("Database is empty")
	}
	fmt.Println("INFO:", info.Version, info.Database)
}
