package config

import (
	"os"
	"strconv"
	"time"
)

// envGetString returns the value of the environment variable key,
// or fallback if the variable is unset or empty.
func envGetString(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// envGetBool returns the boolean value of the environment variable key,
// or fallback if the variable is unset or not a valid boolean.
func envGetBool(key string, fallback bool) bool {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

// envGetInt returns the integer value of the environment variable key,
// or fallback if the variable is unset or not a valid integer.
func envGetInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// envGetDuration returns the duration value of the environment variable key,
// or fallback if the variable is unset or not a valid duration.
func envGetDuration(key string, fallback time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
