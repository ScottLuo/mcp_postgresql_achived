// Package audit provides the structured, credential-safe audit trail for
// every MCP tool invocation, as specified in §14 (Audit Logging) of
// docs/design.md.
//
// Audit output is emitted separately from application/credential logs so that
// database connection credentials (DSNs, passwords) never appear in audit
// records. Sensitive operation parameters (DSN-like values, passwords, raw SQL)
// are masked before emission via the Sanitize method.
package audit

import (
	"strings"
	"time"
)

// AuditRecord describes a single tool invocation.
//
// It intentionally contains no credential/DSN/password fields (spec §14:
// "Never store credentials"). The Params map may hold operation parameters;
// callers must call Sanitize before persisting or logging so that DSN-like,
// password-like, and raw-SQL values are masked.
type AuditRecord struct {
	// Timestamp is the wall-clock time the record was created (RFC 3339 in JSON).
	Timestamp time.Time `json:"timestamp"`
	// RequestID is the MCP request identifier. Low cardinality; safe for audit.
	RequestID string `json:"request_id"`
	// Tool is the MCP tool name (e.g. "db_describe_table").
	Tool string `json:"tool"`
	// Target is the configured target alias (e.g. "production").
	Target string `json:"target"`
	// Operation is the operation category (e.g. "READ", "MAINTENANCE").
	Operation string `json:"operation"`
	// DurationMS is the execution duration in milliseconds.
	DurationMS int64 `json:"duration_ms"`
	// Success reports whether the invocation succeeded.
	Success bool `json:"success"`
	// ErrorCode is the standard error code (spec §10.4) when !Success.
	ErrorCode string `json:"error_code,omitempty"`
	// RowsReturned is the number of rows returned (when applicable).
	RowsReturned int `json:"rows_returned,omitempty"`
	// ApprovalID references a human approval record for maintenance operations.
	ApprovalID string `json:"approval_id,omitempty"`
	// Approver identifies the human who approved the operation.
	Approver string `json:"approver,omitempty"`
	// Params holds non-sensitive operation parameters. Call Sanitize before
	// emission so any DSN/password/raw-SQL values are masked.
	Params map[string]any `json:"params,omitempty"`
}

// sensitiveKeyFragments are substrings that mark a parameter key as sensitive
// when present in its lower-cased form. Values of such parameters are masked.
var sensitiveKeyFragments = []string{
	"dsn",
	"sql",
	"secret",
	"credential",
	"password",
	"pass",
	"token",
}

// isSensitiveKey reports whether a parameter key looks like it carries a
// sensitive value (DSN, SQL text, password, credential, token, ...).
func isSensitiveKey(key string) bool {
	lk := strings.ToLower(key)
	for _, f := range sensitiveKeyFragments {
		if strings.Contains(lk, f) {
			return true
		}
	}
	return false
}

// looksLikeDSN reports whether s resembles a connection string that may embed
// credentials (i.e. it has both a scheme and an "auth@host" segment).
func looksLikeDSN(s string) bool {
	return strings.Contains(s, "://") && strings.Contains(s, "@")
}

// maskDSNValue masks a connection-string value so that no credential, host,
// port or database name is exposed. The URL scheme (e.g. "postgres") is kept
// for readability and everything after "://" is replaced with "****".
//
// This is intentionally more aggressive than config.MaskDSN (which only masks
// the password) so that a DSN-like parameter never leaks host/port/db into
// audit output.
func maskDSNValue(s string) string {
	if i := strings.Index(s, "://"); i >= 0 {
		return s[:i] + "://****"
	}
	return "****"
}

// sanitizeParams returns a copy of in with sensitive values masked:
//   - values under sensitive keys are replaced with "****";
//   - string values that look like a DSN are masked to "<scheme>://****".
//
// A nil/empty input yields a nil result. The original map is not modified.
func sanitizeParams(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if isSensitiveKey(k) {
			out[k] = "****"
			continue
		}
		if s, ok := v.(string); ok && looksLikeDSN(s) {
			out[k] = maskDSNValue(s)
			continue
		}
		out[k] = v
	}
	return out
}

// Sanitize returns a copy of the record with sensitive Params masked and a
// Timestamp set (to now) if unset. The receiver is not modified.
//
// It is safe to call on both values and pointers; the value-receiver form
// guarantees the original record (and its Params map) is left intact.
func (r AuditRecord) Sanitize() AuditRecord {
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now()
	}
	r.Params = sanitizeParams(r.Params)
	return r
}
