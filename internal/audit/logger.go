package audit

import (
	"context"
	"log/slog"
)

// defaultLogger is used when a nil logger is supplied to New.
// It writes to stderr at info level, keeping audit output separate from
// application logs by default.
func defaultLogger() *slog.Logger {
	return slog.Default()
}

// AuditLogger writes structured audit records to a dedicated slog.Logger.
//
// It is gated by an enabled flag sourced from config.Config.AuditEnabled (the
// MCP_AUDIT_ENABLED environment variable). When disabled, Log is a no-op so
// that no audit output is produced at all.
//
// AuditLogger is safe for concurrent use.
type AuditLogger struct {
	enabled bool
	logger  *slog.Logger
}

// New creates an AuditLogger. When enabled is false the logger emits nothing.
// If logger is nil, a default stderr logger is used.
func New(enabled bool, logger *slog.Logger) *AuditLogger {
	if logger == nil {
		logger = defaultLogger()
	}
	return &AuditLogger{
		enabled: enabled,
		logger:  logger,
	}
}

// Enabled reports whether the logger is currently writing records.
func (l *AuditLogger) Enabled() bool {
	return l.enabled
}

// Log writes rec as a single structured JSON record. Before emission the record
// is sanitized so that sensitive operation parameters (DSN-like values,
// passwords, raw SQL) are masked and never appear in the output.
//
// A nil record is ignored. When the logger is disabled, Log performs no
// allocation-dependent work and writes nothing.
func (l *AuditLogger) Log(ctx context.Context, rec *AuditRecord) {
	if l == nil || !l.enabled || rec == nil {
		return
	}

	sanitized := rec.Sanitize()

	// Build a slice of attributes; params are merged into the top-level record
	// so the emitted JSON mirrors the spec §14 example shape.
	attrs := make([]slog.Attr, 0, 12)
	attrs = append(attrs,
		slog.String("timestamp", sanitized.Timestamp.UTC().Format("2006-01-02T15:04:05Z07:00")),
		slog.String("request_id", sanitized.RequestID),
		slog.String("tool", sanitized.Tool),
		slog.String("target", sanitized.Target),
		slog.String("operation", sanitized.Operation),
		slog.Int64("duration_ms", sanitized.DurationMS),
		slog.Bool("success", sanitized.Success),
	)
	if sanitized.ErrorCode != "" {
		attrs = append(attrs, slog.String("error_code", sanitized.ErrorCode))
	}
	if sanitized.RowsReturned > 0 {
		attrs = append(attrs, slog.Int("rows_returned", sanitized.RowsReturned))
	}
	if sanitized.ApprovalID != "" {
		attrs = append(attrs, slog.String("approval_id", sanitized.ApprovalID))
	}
	if sanitized.Approver != "" {
		attrs = append(attrs, slog.String("approver", sanitized.Approver))
	}
	if len(sanitized.Params) > 0 {
		attrs = append(attrs, slog.Any("params", sanitized.Params))
	}

	l.logger.LogAttrs(ctx, slog.LevelInfo, "mcp_tool_audit", attrs...)
}
