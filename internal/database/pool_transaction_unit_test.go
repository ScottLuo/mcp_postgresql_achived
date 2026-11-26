//go:build unit

package database

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"postgres-dba-mcp/internal/config"
)

func TestNewPool_InvalidDSN(t *testing.T) {
	cfg := &config.Config{
		Pool: config.PoolConfig{
			MinConns: 1,
			MaxConns: 5,
		},
	}
	_, err := newPool(context.Background(), cfg, "invalid-dsn-no-scheme", config.AccessReadOnly)
	if err == nil {
		t.Fatal("expected error for invalid DSN")
	}
	if !containsStr(err.Error(), "pool") {
		t.Errorf("expected 'pool' in error, got: %v", err)
	}
}

func TestNewPool_ValidDSN_ReadWrite(t *testing.T) {
	cfg := &config.Config{
		Pool: config.PoolConfig{
			MinConns: 1,
			MaxConns: 5,
		},
	}
	// Use a valid DSN that won't connect (no real server), but newPool
	// should succeed since pgxpool.NewWithConfig is lazy.
	pool, err := newPool(context.Background(), cfg, "postgres://user:pass@localhost:1/db?sslmode=disable", config.AccessReadWrite)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer pool.Close()
}

func TestNewPool_ValidDSN_ReadOnly(t *testing.T) {
	cfg := &config.Config{
		Pool: config.PoolConfig{
			MinConns: 1,
			MaxConns: 5,
		},
	}
	pool, err := newPool(context.Background(), cfg, "postgres://user:pass@localhost:1/db?sslmode=disable", config.AccessReadOnly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer pool.Close()
}

func TestNewPool_MaxLessThanMin(t *testing.T) {
	cfg := &config.Config{
		Pool: config.PoolConfig{
			MinConns: 5,
			MaxConns: 2,
		},
	}
	_, err := newPool(context.Background(), cfg, "postgres://user:pass@localhost:1/db?sslmode=disable", config.AccessReadWrite)
	if err == nil {
		t.Fatal("expected error when max < min")
	}
	if !containsStr(err.Error(), "max_conns") {
		t.Errorf("expected 'max_conns' in error, got: %v", err)
	}
}

func TestNewPool_NegativeMin(t *testing.T) {
	cfg := &config.Config{
		Pool: config.PoolConfig{
			MinConns: -1,
			MaxConns: 5,
		},
	}
	_, err := newPool(context.Background(), cfg, "postgres://user:pass@localhost:1/db?sslmode=disable", config.AccessReadWrite)
	if err == nil {
		t.Fatal("expected error for negative min_conns")
	}
	if !containsStr(err.Error(), "min_conns") {
		t.Errorf("expected 'min_conns' in error, got: %v", err)
	}
}

func TestSanitizePoolError_WithDSN(t *testing.T) {
	err := errors.New("failed to connect: postgres://admin:secret@prod-db:5432/db")
	sanitized := sanitizePoolError("parse config", err)
	if !containsStr(sanitized.Error(), "pool: parse config failed") {
		t.Errorf("unexpected sanitized error: %v", sanitized)
	}
	if containsStr(sanitized.Error(), "secret") {
		t.Errorf("DSN password should be masked, got: %v", sanitized)
	}
}

func TestSanitizePoolError_WithoutDSN(t *testing.T) {
	err := errors.New("connection refused")
	sanitized := sanitizePoolError("create pool", err)
	if !containsStr(sanitized.Error(), "connection refused") {
		t.Errorf("expected original message preserved, got: %v", sanitized)
	}
}

func TestNewTransactionExecutor(t *testing.T) {
	pool := newUnconnectedPool(t)
	tx := NewTransactionExecutor(pool, 10*time.Second, 100, 1048576, 65536)
	if tx == nil {
		t.Fatal("expected non-nil TransactionExecutor")
	}
	if tx.Executor == nil {
		t.Fatal("expected non-nil inner Executor")
	}
}

func TestExecuteReadOnly_SQLTooLong(t *testing.T) {
	pool := newUnconnectedPool(t)
	tx := NewTransactionExecutor(pool, 10*time.Second, 100, 1048576, 10)

	longSQL := "SELECT " + strings.Repeat("x", 20) // 27 chars > 10
	_, err := tx.ExecuteReadOnly(context.Background(), longSQL, nil, 0)
	if err == nil {
		t.Fatal("expected error for SQL too long")
	}
	if !errors.Is(err, ErrSQLTooLong) {
		t.Errorf("expected ErrSQLTooLong, got: %v", err)
	}
}

func TestExecuteReadOnly_PoolClosed(t *testing.T) {
	pool := newUnconnectedPool(t)
	tx := NewTransactionExecutor(pool, 10*time.Second, 100, 1048576, 65536)
	pool.Close()

	_, err := tx.ExecuteReadOnly(context.Background(), "SELECT 1", nil, 0)
	if err == nil {
		t.Fatal("expected error when pool is closed")
	}
}

func TestExecuteNonTransactional_SQLTooLong(t *testing.T) {
	pool := newUnconnectedPool(t)
	tx := NewTransactionExecutor(pool, 10*time.Second, 100, 1048576, 10)

	longSQL := "VACUUM " + strings.Repeat("x", 20) // 27 chars > 10
	_, err := tx.ExecuteNonTransactional(context.Background(), longSQL, nil)
	if err == nil {
		t.Fatal("expected error for SQL too long")
	}
	if !errors.Is(err, ErrSQLTooLong) {
		t.Errorf("expected ErrSQLTooLong, got: %v", err)
	}
}

func TestExecuteNonTransactional_PoolClosed(t *testing.T) {
	pool := newUnconnectedPool(t)
	tx := NewTransactionExecutor(pool, 10*time.Second, 100, 1048576, 65536)
	pool.Close()

	_, err := tx.ExecuteNonTransactional(context.Background(), "VACUUM", nil)
	if err == nil {
		t.Fatal("expected error when pool is closed")
	}
}

func TestJsonMarshalRow(t *testing.T) {
	data, err := jsonMarshalRow([]any{"hello", 42, true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := `["hello",42,true]`
	if string(data) != expected {
		t.Errorf("expected %s, got %s", expected, string(data))
	}
}

func TestJsonMarshalRow_Empty(t *testing.T) {
	data, err := jsonMarshalRow([]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "[]" {
		t.Errorf("expected '[]', got %s", string(data))
	}
}

func TestJsonMarshalRow_Nil(t *testing.T) {
	data, err := jsonMarshalRow(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "null" {
		t.Errorf("expected 'null', got %s", string(data))
	}
}
