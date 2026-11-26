package security

import "strings"

// SanitizeError removes DSN/credential patterns from an error message so that
// sensitive connection information never leaks to AI Agents or logs.
//
// It masks:
//   - postgres://user:password@host:port/db → postgres://user:****@host:port/db
//   - user:password@ patterns
func SanitizeError(err error) string {
	if err == nil {
		return ""
	}
	return maskDSN(err.Error())
}

// maskDSN masks DSN patterns in the given message.
func maskDSN(msg string) string {
	// Pattern: scheme://user:password@rest
	// Look for "://" followed by credentials before "@".
	protoIdx := strings.Index(msg, "://")
	if protoIdx < 0 {
		return msg
	}
	rest := msg[protoIdx+3:]
	atIdx := strings.Index(rest, "@")
	if atIdx < 0 {
		return msg
	}
	creds := rest[:atIdx]
	colonIdx := strings.Index(creds, ":")
	if colonIdx < 0 {
		// No password in DSN — just mask the username for safety.
		return msg[:protoIdx] + "://" + "***" + rest[atIdx:]
	}
	user := creds[:colonIdx]
	// Preserve the portion after "@" (host:port/db) but mask the password.
	suffix := rest[atIdx:] // includes @host:port/db
	return msg[:protoIdx] + "://" + user + ":****" + suffix
}
