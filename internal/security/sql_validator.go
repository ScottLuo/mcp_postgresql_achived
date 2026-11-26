// Package security: sql_validator.go provides server-side SQL AST validation.
//
// The gateway enforces read-only SQL on the server (never trusting an AI's
// claim) by parsing the raw statement with pg_query_go and inspecting the parse
// tree. Dangerous-function detection uses two layers: a recursive AST walk that
// matches *FuncCall* names, plus a deparse-based name scan as a robust fallback
// that covers expression contexts the walk does not traverse explicitly.
package security

import (
	"errors"
	"fmt"
	"strings"

	pgq "github.com/pganalyze/pg_query_go/v5"
)

// StatementType identifies the kind of a top-level SQL statement.
type StatementType string

// Statement-type classification constants.
const (
	StmtSelect      StatementType = "SELECT"
	StmtInsert      StatementType = "INSERT"
	StmtUpdate      StatementType = "UPDATE"
	StmtDelete      StatementType = "DELETE"
	StmtDDL         StatementType = "DDL"
	StmtTransaction StatementType = "TRANSACTION"
	StmtCopy        StatementType = "COPY"
	StmtSet         StatementType = "SET"
	StmtVacuum      StatementType = "VACUUM"
	StmtExplain     StatementType = "EXPLAIN"
	StmtUnknown     StatementType = "UNKNOWN"
)

// dangerousFuncNames are built-in functions that, when called from a read-only
// query, can have side effects beyond a pure read. A read-only query must not
// invoke any of these. The set is intentionally small and conservative.
var dangerousFuncNames = map[string]struct{}{
	"pg_terminate_backend": {},
	"pg_cancel_backend":    {},
	"pg_reload_conf":       {},
	"pg_switch_wal":        {},
}

// SQLValidator enforces SQL safety rules for a configured maximum statement length.
type SQLValidator struct {
	maxSQLLength int
}

// NewSQLValidator returns a validator that rejects any SQL longer than maxSQLLength.
func NewSQLValidator(maxSQLLength int) *SQLValidator {
	return &SQLValidator{maxSQLLength: maxSQLLength}
}

// ValidateReadOnly reports whether sql is a single, read-only SELECT statement.
// It enforces: at most one statement, top-level SELECT, read-only CTEs (no
// data-modifying WITH ... RETURNING), and no dangerous functions.
func (v *SQLValidator) ValidateReadOnly(sql string) (bool, error) {
	if len(sql) > v.maxSQLLength {
		return false, fmt.Errorf("%s: SQL length %d exceeds max %d", ErrCodeSQLInvalid, len(sql), v.maxSQLLength)
	}

	tree, err := pgq.Parse(sql)
	if err != nil {
		return false, fmt.Errorf("%s: %v", ErrCodeSQLInvalid, err)
	}

	if len(tree.Stmts) == 0 {
		return false, fmt.Errorf("%s: empty SQL statement", ErrCodeSQLInvalid)
	}
	if len(tree.Stmts) > 1 {
		return false, fmt.Errorf("%s: multiple statements not allowed (got %d)", ErrCodeSQLInvalid, len(tree.Stmts))
	}

	stmtNode := tree.Stmts[0].Stmt // *pg_query.Node
	topNode := stmtNode.GetNode()
	selectStmt, ok := topNode.(*pgq.Node_SelectStmt)
	if !ok {
		return false, fmt.Errorf("%s: read-only query must be a SELECT statement, got %s", ErrCodeSQLNotAllowed, nodeTypeName(topNode))
	}

	// Read-only CTEs: each WITH entry must wrap a SELECT (no data-modifying CTEs).
	if err := checkReadOnlyCTEs(selectStmt.SelectStmt); err != nil {
		return false, err
	}

	// No dangerous functions anywhere in the tree. The AST walk is the primary
	// check; a deparse-based name scan is a robust fallback that also covers
	// contexts the walk does not explicitly traverse (e.g. JOIN quals, A_Expr).
	if findDangerousFuncCall(stmtNode) || deparseContainsDangerousFunc(tree) {
		return false, fmt.Errorf("%s: dangerous function not allowed in read-only query", ErrCodeSQLNotAllowed)
	}

	return true, nil
}

// deparseContainsDangerousFunc reports whether the deparsed SQL text contains a
// call to a dangerous function. This is a name-based fallback that complements
// the OID/name-based AST walk.
func deparseContainsDangerousFunc(tree *pgq.ParseResult) bool {
	deparse, err := pgq.Deparse(tree)
	if err != nil {
		return false
	}
	text := string(deparse)
	for name := range dangerousFuncNames {
		if strings.Contains(text, name) {
			return true
		}
	}
	return false
}

// checkReadOnlyCTEs ensures every CTE in the WITH clause is a read-only SELECT.
func checkReadOnlyCTEs(st *pgq.SelectStmt) error {
	if st.WithClause == nil {
		return nil
	}
	for _, cteNode := range st.WithClause.Ctes {
		cte := cteNode.GetCommonTableExpr()
		if cte == nil || cte.Ctequery == nil {
			continue
		}
		inner := cte.Ctequery.GetNode()
		if _, isSelect := inner.(*pgq.Node_SelectStmt); !isSelect {
			return fmt.Errorf("%s: data-modifying CTE (%s) not allowed in read-only query", ErrCodeSQLNotAllowed, nodeTypeName(inner))
		}
	}
	return nil
}

// findDangerousFuncCall walks a raw parse-tree node reporting whether it (or any
// descendant) contains a call to a dangerous function.
func findDangerousFuncCall(node *pgq.Node) bool {
	if node == nil {
		return false
	}
	switch n := node.GetNode().(type) {
	case *pgq.Node_FuncCall:
		if funcNameIsDangerous(n.FuncCall) {
			return true
		}
		for _, arg := range n.FuncCall.Args {
			if findDangerousFuncCall(arg) {
				return true
			}
		}
	case *pgq.Node_ResTarget:
		return findDangerousFuncCall(n.ResTarget.Val)
	case *pgq.Node_SelectStmt:
		return selectHasDangerousFunc(n.SelectStmt)
	case *pgq.Node_CommonTableExpr:
		return findDangerousFuncCall(n.CommonTableExpr.Ctequery)
	case *pgq.Node_WithClause:
		for _, cte := range n.WithClause.Ctes {
			if findDangerousFuncCall(cte) {
				return true
			}
		}
	case *pgq.Node_BoolExpr:
		for _, arg := range n.BoolExpr.Args {
			if findDangerousFuncCall(arg) {
				return true
			}
		}
	case *pgq.Node_SubLink:
		if findDangerousFuncCall(n.SubLink.Subselect) || findDangerousFuncCall(n.SubLink.Testexpr) {
			return true
		}
	case *pgq.Node_CaseExpr:
		if findDangerousFuncCall(n.CaseExpr.Arg) || findDangerousFuncCall(n.CaseExpr.Defresult) {
			return true
		}
		for _, w := range n.CaseExpr.Args {
			if findDangerousFuncCall(w) {
				return true
			}
		}
	case *pgq.Node_CaseWhen:
		if findDangerousFuncCall(n.CaseWhen.Expr) || findDangerousFuncCall(n.CaseWhen.Result) {
			return true
		}
	case *pgq.Node_ArrayExpr:
		for _, el := range n.ArrayExpr.Elements {
			if findDangerousFuncCall(el) {
				return true
			}
		}
	case *pgq.Node_Aggref:
		for _, a := range n.Aggref.Args {
			if findDangerousFuncCall(a) {
				return true
			}
		}
	case *pgq.Node_OpExpr:
		for _, a := range n.OpExpr.Args {
			if findDangerousFuncCall(a) {
				return true
			}
		}
	case *pgq.Node_JoinExpr:
		if findDangerousFuncCall(n.JoinExpr.Larg) || findDangerousFuncCall(n.JoinExpr.Rarg) || findDangerousFuncCall(n.JoinExpr.Quals) {
			return true
		}
	case *pgq.Node_FromExpr:
		for _, f := range n.FromExpr.Fromlist {
			if findDangerousFuncCall(f) {
				return true
			}
		}
		return findDangerousFuncCall(n.FromExpr.Quals)
	case *pgq.Node_RangeSubselect:
		return findDangerousFuncCall(n.RangeSubselect.Subquery)
	}
	return false
}

// selectHasDangerousFunc reports whether a SelectStmt (including its set-op
// operands and CTEs) contains a call to a dangerous function.
func selectHasDangerousFunc(sel *pgq.SelectStmt) bool {
	if sel == nil {
		return false
	}
	// Single expression fields.
	if findDangerousFuncCall(sel.WhereClause) || findDangerousFuncCall(sel.HavingClause) ||
		findDangerousFuncCall(sel.LimitOffset) || findDangerousFuncCall(sel.LimitCount) {
		return true
	}
	// Slice fields that may embed expressions.
	for _, list := range [][]*pgq.Node{
		sel.TargetList, sel.FromClause, sel.GroupClause,
		sel.SortClause, sel.ValuesLists, sel.LockingClause,
	} {
		for _, expr := range list {
			if findDangerousFuncCall(expr) {
				return true
			}
		}
	}
	// CTEs defined alongside this SELECT.
	if sel.WithClause != nil {
		for _, cte := range sel.WithClause.Ctes {
			if findDangerousFuncCall(cte) {
				return true
			}
		}
	}
	// Set operations (UNION / INTERSECT / EXCEPT) recurse into the operands.
	if sel.Larg != nil && selectHasDangerousFunc(sel.Larg) {
		return true
	}
	if sel.Rarg != nil && selectHasDangerousFunc(sel.Rarg) {
		return true
	}
	return false
}

// funcNameIsDangerous checks whether a FuncCall's unqualified name is in the
// dangerous set. Qualified names (e.g. "public.pg_cancel_backend") are handled
// by using the final component of the name.
func funcNameIsDangerous(fc *pgq.FuncCall) bool {
	if fc == nil || len(fc.Funcname) == 0 {
		return false
	}
	last := fc.Funcname[len(fc.Funcname)-1].GetString_()
	if last == nil {
		return false
	}
	_, ok := dangerousFuncNames[last.Sval]
	return ok
}

// ClassifyStatement maps a single SQL statement to its StatementType.
// It returns StmtUnknown (with an error) if the SQL cannot be parsed, exceeds
// the length limit, or contains multiple statements.
func (v *SQLValidator) ClassifyStatement(sql string) (StatementType, error) {
	if len(sql) > v.maxSQLLength {
		return StmtUnknown, fmt.Errorf("%s: SQL length %d exceeds max %d", ErrCodeSQLInvalid, len(sql), v.maxSQLLength)
	}

	tree, err := pgq.Parse(sql)
	if err != nil {
		return StmtUnknown, fmt.Errorf("%s: %v", ErrCodeSQLInvalid, err)
	}

	if len(tree.Stmts) != 1 {
		return StmtUnknown, fmt.Errorf("%s: expected exactly one statement, got %d", ErrCodeSQLInvalid, len(tree.Stmts))
	}

	node := tree.Stmts[0].Stmt.GetNode()
	if node == nil {
		return StmtUnknown, fmt.Errorf("%s: empty statement", ErrCodeSQLInvalid)
	}

	switch node.(type) {
	case *pgq.Node_SelectStmt:
		return StmtSelect, nil
	case *pgq.Node_InsertStmt:
		return StmtInsert, nil
	case *pgq.Node_UpdateStmt:
		return StmtUpdate, nil
	case *pgq.Node_DeleteStmt:
		return StmtDelete, nil
	case *pgq.Node_TruncateStmt,
		*pgq.Node_CreateStmt,
		*pgq.Node_IndexStmt,
		*pgq.Node_AlterTableStmt,
		*pgq.Node_DropStmt,
		*pgq.Node_ViewStmt,
		*pgq.Node_CreateSchemaStmt,
		*pgq.Node_RenameStmt,
		*pgq.Node_CommentStmt,
		*pgq.Node_AlterDatabaseStmt,
		*pgq.Node_AlterRoleStmt:
		return StmtDDL, nil
	case *pgq.Node_TransactionStmt:
		return StmtTransaction, nil
	case *pgq.Node_CopyStmt:
		return StmtCopy, nil
	case *pgq.Node_VariableSetStmt, *pgq.Node_VariableShowStmt:
		return StmtSet, nil
	case *pgq.Node_VacuumStmt:
		return StmtVacuum, nil
	case *pgq.Node_ExplainStmt:
		return StmtExplain, nil
	default:
		return StmtUnknown, fmt.Errorf("%s: unrecognized statement type %s", ErrCodeSQLInvalid, nodeTypeName(node))
	}
}

// nodeTypeName returns a human-readable type name for a raw parse-tree node.
func nodeTypeName(node interface{}) string {
	if node == nil {
		return "nil"
	}
	return fmt.Sprintf("%T", node)
}

// Package-level validation entry points.
//
// The query-analysis tool handlers (internal/tools/query) call ValidateReadOnly
// and classify the returned error with the sentinel errors below. The length
// limit used here mirrors the configuration default (MaxSQLLength) so the two
// enforcement points stay consistent (data-same-source).
const defaultMaxSQLLength = 65536

// Sentinel errors returned by ValidateReadOnly. Tool handlers map these to the
// standard error-code set via errors.Is: parse / multi-statement failures are
// "invalid SQL"; everything else (non-SELECT, data-modifying CTE, ...) is
// "not allowed".
var (
	// ErrInvalidSQL indicates the SQL could not be parsed or is structurally
	// invalid (e.g. multiple statements, empty input, over the length limit).
	ErrInvalidSQL = errors.New("SQL is invalid")
	// ErrMultiStatement indicates the input contained more than one statement.
	ErrMultiStatement = errors.New("multiple statements not allowed")
)

// ValidateReadOnly reports whether sql is a single, read-only SELECT statement,
// returning a sentinel-wrapped error otherwise. It enforces: at most one
// statement, top-level SELECT, read-only CTEs, and no dangerous functions.
//
// Structural/parse failures are wrapped with ErrInvalidSQL (multi-statement
// input with ErrMultiStatement); semantic rejections (non-SELECT, data-modifying
// CTE, dangerous function) are returned as-is so callers map them to the
// "not allowed" standard code.
func ValidateReadOnly(sql string) error {
	_, err := NewSQLValidator(defaultMaxSQLLength).ValidateReadOnly(sql)
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), ErrCodeSQLInvalid) {
		if strings.Contains(err.Error(), "multiple statements") {
			return fmt.Errorf("%w: %s", ErrMultiStatement, err.Error())
		}
		return fmt.Errorf("%w: %s", ErrInvalidSQL, err.Error())
	}
	return err
}
