package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// getRequired reads a required environment variable.
// Returns an error naming the variable if unset or empty.
func getRequired(key string) (string, error) {
	val, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(val) == "" {
		return "", fmt.Errorf("required environment variable %q is unset or empty", key)
	}
	return val, nil
}

// getOptional reads an optional environment variable; returns defaultValue if unset or empty.
func getOptional(key, defaultValue string) string {
	val, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(val) == "" {
		return defaultValue
	}
	return val
}

// getOptionalDuration parses an optional duration environment variable.
// Returns the default if unset or empty.
func getOptionalDuration(key string, defaultValue time.Duration) (time.Duration, error) {
	val, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(val) == "" {
		return defaultValue, nil
	}
	d, err := time.ParseDuration(val)
	if err != nil {
		return 0, fmt.Errorf("environment variable %q has invalid duration %q: %w", key, val, err)
	}
	return d, nil
}

// getOptionalInt parses an optional integer environment variable.
// Returns the default if unset or empty.
func getOptionalInt(key string, defaultValue int) (int, error) {
	val, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(val) == "" {
		return defaultValue, nil
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return 0, fmt.Errorf("environment variable %q has invalid integer value %q: %w", key, val, err)
	}
	return n, nil
}

// getOptionalBool parses an optional boolean environment variable.
// Returns the default if unset or empty.
func getOptionalBool(key string, defaultValue bool) (bool, error) {
	val, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(val) == "" {
		return defaultValue, nil
	}
	b, err := strconv.ParseBool(val)
	if err != nil {
		return false, fmt.Errorf("environment variable %q has invalid boolean value %q: %w", key, val, err)
	}
	return b, nil
}

// maskDSN masks the password portion of a PostgreSQL DSN.
// Handles both postgres:// and postgresql:// schemes.
// The password is replaced with "****".
func maskDSN(dsn string) string {
	// Try both scheme prefixes
	for _, prefix := range []string{"postgres://", "postgresql://"} {
		if strings.HasPrefix(dsn, prefix) {
			rest := dsn[len(prefix):]
			atIdx := strings.LastIndex(rest, "@")
			if atIdx <= 0 {
				return dsn
			}
			// rest[:atIdx] is "user:pass" or "user"
			userPart := rest[:atIdx]
			colonIdx := strings.LastIndex(userPart, ":")
			if colonIdx < 0 {
				// No password in DSN
				return dsn
			}
			user := userPart[:colonIdx]
			// Mask everything after colon (the password)
			return prefix + user + ":****" + rest[atIdx:]
		}
	}
	return dsn
}

// validateAccessMode validates an access mode string against known values.
func validateAccessMode(s string) (AccessMode, error) {
	switch AccessMode(s) {
	case AccessReadOnly:
		return AccessReadOnly, nil
	case AccessRestrictedWrite:
		return AccessRestrictedWrite, nil
	case AccessReadWrite:
		return AccessReadWrite, nil
	default:
		return "", fmt.Errorf("invalid access mode %q: must be one of %q, %q, %q", s, AccessReadOnly, AccessRestrictedWrite, AccessReadWrite)
	}
}

// validateTransport validates the MCP transport value.
func validateTransport(s string) error {
	switch s {
	case "stdio", "http":
		return nil
	default:
		return fmt.Errorf("invalid MCP_TRANSPORT value %q: must be \"stdio\" or \"http\"", s)
	}
}

// validateLogLevel validates the log level value.
func validateLogLevel(s string) error {
	switch s {
	case "debug", "info", "warn", "error":
		return nil
	default:
		return fmt.Errorf("invalid MCP_LOG_LEVEL value %q: must be one of \"debug\", \"info\", \"warn\", \"error\"", s)
	}
}
