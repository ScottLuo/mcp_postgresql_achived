// Package security provides foundational security primitives for the MCP PostgreSQL gateway.
// It defines standard error-code constants, DSN sanitization functions, and permission-level mapping.
package security

import (
	"regexp"

	"github.com/jackc/pgx/v5/pgconn"
)

// Standard error-code constants (single source of truth).
// These are referenced by the MCP layer (internal/mcp/response.go) and all security components.
const (
	ErrCodeTargetNotFound     = "DB_TARGET_NOT_FOUND"
	ErrCodeConnectionFailed   = "DB_CONNECTION_FAILED"
	ErrCodePermissionDenied   = "DB_PERMISSION_DENIED"
	ErrCodeSQLInvalid         = "DB_SQL_INVALID"
	ErrCodeSQLNotAllowed      = "DB_SQL_NOT_ALLOWED"
	ErrCodeQueryTimeout       = "DB_QUERY_TIMEOUT"
	ErrCodeResultTooLarge     = "DB_RESULT_TOO_LARGE"
	ErrCodeApprovalRequired   = "DB_APPROVAL_REQUIRED"
	ErrCodeApprovalInvalid    = "DB_APPROVAL_INVALID"
	ErrCodeExecutionFailed    = "DB_EXECUTION_FAILED"
	ErrCodeFeatureUnavailable = "DB_FEATURE_UNAVAILABLE"
)

// urlDSNPattern matches URL-form DSNs: postgres://user:password@host:port/db...
// Group 1: scheme (postgres or postgresql)
// Group 2: username
// Group 3: password
// Group 4: host:port/db... (the rest after @)
var urlDSNPattern = regexp.MustCompile(`(postgres(?:ql)?)://(\w+):([^@/]+)@(\S+)`)

// keyValPasswordPattern matches key-value DSN password field: password=secret
var keyValPasswordPattern = regexp.MustCompile(`password=(\S+)`)

// keyValUserPattern matches key-value DSN user field: user=admin
var keyValUserPattern = regexp.MustCompile(`user=(\S+)`)

// keyValHostPattern matches key-value DSN host field: host=prod-db
var keyValHostPattern = regexp.MustCompile(`host=(\S+)`)

// keyValPortPattern matches key-value DSN port field: port=5432
var keyValPortPattern = regexp.MustCompile(`port=(\S+)`)

// SanitizeError masks credentials in error messages before they are returned to the caller.
// It handles both URL-form DSNs (postgres://user:password@host:port/db)
// and key-value DSNs (password=secret, user=admin).
// Host/port/db are preserved for debuggability; only the password is masked.
func SanitizeError(msg string) string {
	if msg == "" {
		return msg
	}
	// Mask password in URL-form DSNs: postgres://user:pass@host:port/db → postgres://user:****@host:port/db
	msg = urlDSNPattern.ReplaceAllString(msg, "${1}://${2}:****@${4}")
	// Mask password in key-value DSN format
	msg = keyValPasswordPattern.ReplaceAllString(msg, "password=****")
	// Mask user in key-value DSN format
	msg = keyValUserPattern.ReplaceAllString(msg, "user=****")
	return msg
}

// SanitizeLog is a more aggressive variant for log output.
// In addition to what SanitizeError does, it also masks host= and port= values
// in key-value DSNs to reduce information leakage in logs.
func SanitizeLog(msg string) string {
	msg = SanitizeError(msg)
	if msg == "" {
		return msg
	}
	// Mask host in key-value DSN format
	msg = keyValHostPattern.ReplaceAllString(msg, "host=****")
	// Mask port in key-value DSN format
	msg = keyValPortPattern.ReplaceAllString(msg, "port=****")
	return msg
}

// MapPGError maps a raw pgconn.PgError to a standard error code and sanitized message.
// The returned code is one of the ErrCode* constants defined in this package.
// The returned message has DSN credentials stripped via SanitizeError.
func MapPGError(pgErr *pgconn.PgError) (code, message string) {
	if pgErr == nil {
		return ErrCodeExecutionFailed, ""
	}

	switch pgErr.Code {
	case "42601": // syntax_error
		code = ErrCodeSQLInvalid
	case "42P01": // undefined_table
		code = ErrCodeExecutionFailed
	case "42703": // undefined_column
		code = ErrCodeExecutionFailed
	case "55006": // read_only_sql_transaction
		code = ErrCodeSQLNotAllowed
	case "55P03": // active_sql_transaction
		code = ErrCodePermissionDenied
	case "57014": // query_canceled / statement_timeout
		code = ErrCodeQueryTimeout
	case "53200": // out_of_memory
		code = ErrCodeResultTooLarge
	case "42501": // insufficient_privilege
		code = ErrCodePermissionDenied
	default:
		code = ErrCodeExecutionFailed
	}

	message = SanitizeError(pgErr.Message)
	return code, message
}
