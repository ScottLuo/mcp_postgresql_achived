//go:build unit

package security

import (
	"strings"
	"testing"
)

// TestValidateReadOnly exercises the read-only SQL validator with a table of
// representative statements, verifying both the accept/reject decision and the
// error code carried by a rejection.
func TestValidateReadOnly(t *testing.T) {
	v := NewSQLValidator(65536)

	// Generate a SQL string longer than the 65536 limit for the max-length case.
	longSQL := "SELECT 1" + strings.Repeat(" ", 70*1024)

	tests := []struct {
		name        string
		sql         string
		wantValid   bool
		wantErrCode string // expected error code when wantValid is false ("" when valid)
	}{
		// Valid read-only SELECTs.
		{name: "simple select", sql: "SELECT 1", wantValid: true},
		{name: "select with param", sql: "SELECT id FROM orders WHERE id = $1", wantValid: true},
		{name: "read-only cte", sql: "WITH active AS (SELECT id FROM orders) SELECT * FROM active", wantValid: true},
		{name: "multiple read-only ctes", sql: "WITH a AS (SELECT 1), b AS (SELECT 2) SELECT * FROM a JOIN b ON a.a = b.b", wantValid: true},
		{name: "subquery in from", sql: "SELECT * FROM (SELECT id FROM orders WHERE status='active') sub", wantValid: true},
		{name: "union", sql: "SELECT 1 UNION SELECT 2", wantValid: true},
		{name: "harmless function", sql: "SELECT count(*) FROM orders", wantValid: true},
		{name: "join select", sql: "SELECT o.id FROM orders o JOIN t ON o.id = t.id", wantValid: true},
		{name: "where with op", sql: "SELECT * FROM orders WHERE id > 0 AND status = 'active'", wantValid: true},
		{name: "case expr", sql: "SELECT CASE WHEN status='active' THEN 1 ELSE 0 END FROM orders", wantValid: true},
		{name: "aggregate with subquery arg", sql: "SELECT count(id) FROM (SELECT id FROM orders) sub", wantValid: true},

		// Rejected: wrong statement type.
		{name: "update", sql: "UPDATE orders SET status='x' WHERE id=1", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "delete", sql: "DELETE FROM orders WHERE id=1", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "insert", sql: "INSERT INTO orders VALUES (1)", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "drop table", sql: "DROP TABLE orders", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "create table", sql: "CREATE TABLE test (id INT)", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "alter table", sql: "ALTER TABLE orders ADD COLUMN x INT", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "truncate", sql: "TRUNCATE orders", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "begin", sql: "BEGIN", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "commit", sql: "COMMIT", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "copy", sql: "COPY orders FROM STDIN", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "set", sql: "SET work_mem = '64MB'", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "vacuum", sql: "VACUUM orders", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "explain", sql: "EXPLAIN SELECT 1", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},

		// Rejected: multiple statements.
		{name: "multi statement", sql: "SELECT 1; SELECT 2", wantValid: false, wantErrCode: ErrCodeSQLInvalid},

		// Rejected: data-modifying CTE.
		{name: "cte delete", sql: "WITH deleted AS (DELETE FROM orders WHERE id=1 RETURNING *) SELECT * FROM deleted", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "cte update", sql: "WITH updated AS (UPDATE orders SET status='x' WHERE id=1 RETURNING *) SELECT * FROM updated", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "cte insert", sql: "WITH ins AS (INSERT INTO orders VALUES (1) RETURNING *) SELECT * FROM ins", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},

		// Rejected: dangerous functions.
		{name: "pg_terminate_backend", sql: "SELECT pg_terminate_backend(123)", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "pg_cancel_backend", sql: "SELECT pg_cancel_backend(456)", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "pg_reload_conf", sql: "SELECT pg_reload_conf()", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "pg_switch_wal", sql: "SELECT pg_switch_wal()", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "qualified dangerous func", sql: "SELECT public.pg_cancel_backend(1)", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "dangerous func in where", sql: "SELECT * FROM orders WHERE id > 0 AND pg_cancel_backend(1)", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "dangerous func in subquery", sql: "SELECT * FROM (SELECT pg_terminate_backend(1) FROM orders) sub", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "dangerous func in case", sql: "SELECT CASE WHEN true THEN pg_cancel_backend(1) ELSE pg_cancel_backend(2) END", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "dangerous func in aggregate", sql: "SELECT array_agg(pg_cancel_backend(1)) FROM t", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},
		{name: "dangerous func in join qual", sql: "SELECT * FROM orders o JOIN t ON o.id = pg_reload_conf()", wantValid: false, wantErrCode: ErrCodeSQLNotAllowed},

		// Rejected: parse error / empty.
		{name: "parse error", sql: "SELECT FROM WHERE", wantValid: false, wantErrCode: ErrCodeSQLInvalid},
		{name: "empty string", sql: "", wantValid: false, wantErrCode: ErrCodeSQLInvalid},
		{name: "whitespace only", sql: "   \n  ", wantValid: false, wantErrCode: ErrCodeSQLInvalid},

		// Rejected: exceeds max length.
		{name: "exceeds max length", sql: longSQL, wantValid: false, wantErrCode: ErrCodeSQLInvalid},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotValid, err := v.ValidateReadOnly(tc.sql)
			if gotValid != tc.wantValid {
				t.Fatalf("ValidateReadOnly(%q) valid = %v, want %v (err=%v)", tc.sql, gotValid, tc.wantValid, err)
			}
			if tc.wantValid {
				if err != nil {
					t.Fatalf("ValidateReadOnly(%q) unexpected error: %v", tc.sql, err)
				}
				return
			}
			// Rejection: error must be non-nil and carry the expected code.
			if err == nil {
				t.Fatalf("ValidateReadOnly(%q) = valid, want error with code %s", tc.sql, tc.wantErrCode)
			}
			if tc.wantErrCode != "" && !strings.Contains(err.Error(), tc.wantErrCode) {
				t.Fatalf("ValidateReadOnly(%q) error = %q, want code %s", tc.sql, err.Error(), tc.wantErrCode)
			}
		})
	}
}

// TestValidateReadOnlyMaxLengthBoundary checks the exact boundary of the length
// limit: a SQL of exactly maxSQLLength bytes is accepted, one byte more is not.
func TestValidateReadOnlyMaxLengthBoundary(t *testing.T) {
	const maxLen = 65536
	v := NewSQLValidator(maxLen)

	// Build a SELECT that is exactly maxLen bytes: "SELECT 1" (8 bytes) plus padding.
	base := "SELECT 1"
	pad := maxLen - len(base)
	atLimit := base + strings.Repeat(" ", pad)
	if len(atLimit) != maxLen {
		t.Fatalf("atLimit length = %d, want %d", len(atLimit), maxLen)
	}
	if ok, err := v.ValidateReadOnly(atLimit); !ok {
		t.Fatalf("at-limit SQL rejected: %v", err)
	}

	over := base + strings.Repeat(" ", pad+1)
	if ok, err := v.ValidateReadOnly(over); ok {
		t.Fatalf("over-limit SQL accepted, want reject (err=%v)", err)
	}
}

// TestClassifyStatement verifies top-level statement classification.
func TestClassifyStatement(t *testing.T) {
	v := NewSQLValidator(65536)

	tests := []struct {
		name      string
		sql       string
		wantType  StatementType
		wantError bool
	}{
		{name: "select", sql: "SELECT 1", wantType: StmtSelect},
		{name: "insert", sql: "INSERT INTO t VALUES (1)", wantType: StmtInsert},
		{name: "update", sql: "UPDATE t SET x=1", wantType: StmtUpdate},
		{name: "delete", sql: "DELETE FROM t WHERE id=1", wantType: StmtDelete},
		{name: "create table", sql: "CREATE TABLE t (id INT)", wantType: StmtDDL},
		{name: "drop table", sql: "DROP TABLE t", wantType: StmtDDL},
		{name: "alter table", sql: "ALTER TABLE t ADD COLUMN x INT", wantType: StmtDDL},
		{name: "truncate", sql: "TRUNCATE t", wantType: StmtDDL},
		{name: "create index", sql: "CREATE INDEX idx ON t(id)", wantType: StmtDDL},
		{name: "create schema", sql: "CREATE SCHEMA s", wantType: StmtDDL},
		{name: "begin", sql: "BEGIN", wantType: StmtTransaction},
		{name: "commit", sql: "COMMIT", wantType: StmtTransaction},
		{name: "copy", sql: "COPY t FROM STDIN", wantType: StmtCopy},
		{name: "set", sql: "SET work_mem='64MB'", wantType: StmtSet},
		{name: "show", sql: "SHOW search_path", wantType: StmtSet},
		{name: "vacuum", sql: "VACUUM t", wantType: StmtVacuum},
		{name: "explain", sql: "EXPLAIN SELECT 1", wantType: StmtExplain},

		// Errors.
		{name: "invalid sql", sql: "NOT A VALID SQL STATEMENT !!!", wantType: StmtUnknown, wantError: true},
		{name: "multi statement", sql: "SELECT 1; SELECT 2", wantType: StmtUnknown, wantError: true},
		{name: "empty", sql: "", wantType: StmtUnknown, wantError: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotType, err := v.ClassifyStatement(tc.sql)
			if tc.wantError {
				if err == nil {
					t.Fatalf("ClassifyStatement(%q) = %s, want error", tc.sql, gotType)
				}
				if gotType != StmtUnknown {
					t.Fatalf("ClassifyStatement(%q) type = %s, want %s", tc.sql, gotType, StmtUnknown)
				}
				return
			}
			if err != nil {
				t.Fatalf("ClassifyStatement(%q) unexpected error: %v", tc.sql, err)
			}
			if gotType != tc.wantType {
				t.Fatalf("ClassifyStatement(%q) = %s, want %s", tc.sql, gotType, tc.wantType)
			}
		})
	}
}
