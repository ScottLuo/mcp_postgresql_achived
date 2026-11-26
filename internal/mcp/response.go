package mcp

import (
	"encoding/json"
	"fmt"
)

// ToolResult is the standard response shape returned by all tool handlers.
// JSON serialization matches spec §10.4.
type ToolResult struct {
	Success  bool        `json:"success"`
	Data     interface{} `json:"data,omitempty"`
	Error    *ToolError  `json:"error,omitempty"`
	Metadata *Metadata   `json:"metadata,omitempty"`
}

// ToolError describes a structured tool error with a standard error code.
type ToolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Metadata carries auxiliary information about a tool execution.
type Metadata struct {
	Target       string `json:"target,omitempty"`
	DurationMS   int64  `json:"duration_ms"`
	Truncated    bool   `json:"truncated"`
	RowsReturned int    `json:"rows_returned"`
}

// Standard error codes (spec §10.4).
//
// These are the single source of truth. When internal/security defines its
// own error-code constants, this set should be replaced by an import from
// that package to avoid drift.
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

// AllErrorCodes lists every standard error code constant for validation
// and testing purposes.
var AllErrorCodes = []string{
	ErrCodeTargetNotFound,
	ErrCodeConnectionFailed,
	ErrCodePermissionDenied,
	ErrCodeSQLInvalid,
	ErrCodeSQLNotAllowed,
	ErrCodeQueryTimeout,
	ErrCodeResultTooLarge,
	ErrCodeApprovalRequired,
	ErrCodeApprovalInvalid,
	ErrCodeExecutionFailed,
	ErrCodeFeatureUnavailable,
}

// NewToolError creates a ToolError with the given code and message.
func NewToolError(code, message string) *ToolError {
	return &ToolError{Code: code, Message: message}
}

// SuccessResult returns a ToolResult representing a successful execution.
func SuccessResult(data interface{}, meta *Metadata) *ToolResult {
	return &ToolResult{
		Success:  true,
		Data:     data,
		Metadata: meta,
	}
}

// ErrorResult returns a ToolResult representing a failed execution.
// The ToolError is always non-nil.
func ErrorResult(code, message string, meta *Metadata) *ToolResult {
	return &ToolResult{
		Success:  false,
		Error:    NewToolError(code, message),
		Metadata: meta,
	}
}

// Serialize marshals a ToolResult to its JSON wire format.
func Serialize(result *ToolResult) ([]byte, error) {
	if result == nil {
		return nil, fmt.Errorf("response: cannot serialize nil ToolResult")
	}
	return json.Marshal(result)
}
