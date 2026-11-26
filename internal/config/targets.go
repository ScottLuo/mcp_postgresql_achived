package config

import (
	"fmt"
	"os"
	"strings"
)

// Target represents a configured PostgreSQL connection target.
type Target struct {
	Name          string     `json:"name"`
	Enabled       bool       `json:"enabled"`
	AccessMode    AccessMode `json:"access_mode"`
	ConnectionEnv string     `json:"connection_env"`
}

// DSN returns the DSN resolved from the environment variable referenced by
// ConnectionEnv. Returns an error if the variable is unset or empty.
func (t *Target) DSN() (string, error) {
	if t.ConnectionEnv == "" {
		return "", fmt.Errorf("config: target %q has no connection_env set", t.Name)
	}
	dsn, ok := os.LookupEnv(t.ConnectionEnv)
	if !ok || dsn == "" {
		return "", fmt.Errorf("config: environment variable %q for target %q is not set", t.ConnectionEnv, t.Name)
	}
	return dsn, nil
}

// LoadTargets reads POSTGRES_TARGETS (comma-separated alias list) and
// POSTGRES_<NAME>_DSN environment variables.
// Returns a slice of Target definitions with validated DSN availability.
func LoadTargets() ([]Target, error) {
	targetsVar := os.Getenv("POSTGRES_TARGETS")
	if targetsVar == "" {
		return nil, fmt.Errorf("config: POSTGRES_TARGETS environment variable is required")
	}

	names := strings.Split(targetsVar, ",")
	targets := make([]Target, 0, len(names))

	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		envVar := "POSTGRES_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_DSN"
		t := Target{
			Name:          name,
			Enabled:       true,
			AccessMode:    AccessReadOnly,
			ConnectionEnv: envVar,
		}

		// Verify DSN is available
		if _, err := t.DSN(); err != nil {
			return nil, fmt.Errorf("config: target %q: %w", name, err)
		}

		// Allow per-target access mode override via env: POSTGRES_<NAME>_ACCESS
		accessVar := "POSTGRES_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_ACCESS"
		if am := os.Getenv(accessVar); am != "" {
			switch AccessMode(am) {
			case AccessReadOnly, AccessRestrictedWrite, AccessReadWrite:
				t.AccessMode = AccessMode(am)
			default:
				return nil, fmt.Errorf("config: target %q: invalid access mode %q (must be read_only, restricted_write, or read_write)", name, am)
			}
		}

		targets = append(targets, t)
	}

	if len(targets) == 0 {
		return nil, fmt.Errorf("config: POSTGRES_TARGETS contains no valid target names")
	}
	return targets, nil
}
