package security

import (
	"errors"
	"fmt"

	pgq "github.com/pganalyze/pg_query_go/v5"
)

// Sentinel errors for SQL validation failures. Handlers map these to standard
// MCP error codes (ErrInvalidSQL/ErrMultiStatement -> DB_SQL_INVALID;
// ErrNotSelect/ErrDataModifyingCTE -> DB_SQL_NOT_ALLOWED).
var (
	// ErrInvalidSQL is returned when the SQL cannot be parsed.
	ErrInvalidSQL = errors.New("sql: invalid syntax")
	// ErrMultiStatement is returned when the SQL contains more than one statement.
	ErrMultiStatement = errors.New("sql: multiple statements not allowed")
	// ErrNotSelect is returned when the statement is not a read-only SELECT.
	ErrNotSelect = errors.New("sql: statement is not a read-only SELECT")
	// ErrDataModifyingCTE is returned when a CTE performs data modification.
	ErrDataModifyingCTE = errors.New("sql: data-modifying CTE not allowed")
	// ErrForUpdate is returned when the SELECT uses FOR UPDATE/NO KEY UPDATE/
	// FOR SHARE locking clauses.
	ErrForUpdate = errors.New("sql: locking read (FOR UPDATE/SHARE) not allowed")
)

// ValidateReadOnly validates that the given SQL is safe to run in a read-only
// transaction. It enforces (spec §9.2):
//
//  1. Exactly one SQL statement.
//  2. A SELECT statement only.
//  3. Read-only CTEs only (no INSERT/UPDATE/DELETE/MERGE inside a CTE).
//  4. No data modification, DDL, transaction control, COPY, or session changes.
//  5. No locking reads (SELECT ... FOR UPDATE / FOR SHARE).
//
// It does not rely on the first keyword; it inspects the parsed PostgreSQL AST.
func ValidateReadOnly(sql string) error {
	res, err := pgq.Parse(sql)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidSQL, sanitizeParseErr(err))
	}
	// Rule 1: single statement only.
	if len(res.Stmts) != 1 {
		return ErrMultiStatement
	}

	root := res.Stmts[0].GetStmt()
	if root == nil {
		return ErrInvalidSQL
	}

	// Rule 2: must be a SELECT. Any DML/DDL/utility statement is rejected.
	selectStmt := root.GetSelectStmt()
	if selectStmt == nil {
		// Provide a more specific message for common rejected types.
		switch {
		case root.GetInsertStmt() != nil, root.GetUpdateStmt() != nil,
			root.GetDeleteStmt() != nil, root.GetMergeStmt() != nil:
			return fmt.Errorf("%w: data modification statement", ErrNotSelect)
		case root.GetCreateStmt() != nil, root.GetDropStmt() != nil,
			root.GetAlterTableStmt() != nil, root.GetIndexStmt() != nil,
			root.GetVacuumStmt() != nil, root.GetCreateFunctionStmt() != nil:
			return fmt.Errorf("%w: DDL statement", ErrNotSelect)
		case root.GetTransactionStmt() != nil:
			return fmt.Errorf("%w: transaction control statement", ErrNotSelect)
		case root.GetCopyStmt() != nil:
			return fmt.Errorf("%w: COPY statement", ErrNotSelect)
		case root.GetVariableSetStmt() != nil:
			return fmt.Errorf("%w: session configuration change", ErrNotSelect)
		default:
			return ErrNotSelect
		}
	}

	// Rule 5: reject locking reads (FOR UPDATE / FOR SHARE).
	if hasLockingClause(selectStmt) {
		return ErrForUpdate
	}

	// Rule 3: validate CTEs are read-only.
	if wc := selectStmt.GetWithClause(); wc != nil {
		for _, cteNode := range wc.GetCtes() {
			cte := cteNode.GetCommonTableExpr()
			if cte == nil {
				continue
			}
			inner := cte.GetCtequery()
			if inner == nil {
				continue
			}
			if isDataModifying(inner) {
				return fmt.Errorf("%w: CTE %q", ErrDataModifyingCTE, cte.GetCtename())
			}
		}
	}

	return nil
}

// hasLockingClause reports whether the SELECT has any FOR UPDATE / FOR SHARE
// locking clause.
func hasLockingClause(cs *pgq.SelectStmt) bool {
	if cs == nil {
		return false
	}
	return len(cs.GetLockingClause()) > 0
}

// isDataModifying reports whether the given node represents a data-modifying
// statement (INSERT/UPDATE/DELETE/MERGE).
func isDataModifying(node *pgq.Node) bool {
	if node == nil {
		return false
	}
	switch {
	case node.GetInsertStmt() != nil,
		node.GetUpdateStmt() != nil,
		node.GetDeleteStmt() != nil,
		node.GetMergeStmt() != nil:
		return true
	// A sub-SELECT inside a CTE is data-modifying if it has locking or its own
	// data-modifying CTEs.
	case node.GetSelectStmt() != nil:
		cs := node.GetSelectStmt()
		if hasLockingClause(cs) {
			return true
		}
		if wc := cs.GetWithClause(); wc != nil {
			for _, cteNode := range wc.GetCtes() {
				if cte := cteNode.GetCommonTableExpr(); cte != nil {
					if isDataModifying(cte.GetCtequery()) {
						return true
					}
				}
			}
		}
	}
	return false
}

// sanitizeParseErr shortens parser errors to avoid leaking verbose internal
// context into user-facing messages.
func sanitizeParseErr(err error) string {
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200] + "..."
	}
	return msg
}
