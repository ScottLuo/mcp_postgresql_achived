package config

import (
	"fmt"
	"strings"
)

// Target represents a preconfigured PostgreSQL connection target.
type Target struct {
	Name          string     // Alias (e.g., "development")
	Enabled       bool       // Whether the target is enabled
	AccessMode    AccessMode // Per-target access mode
	ConnectionEnv string     // Name of the ENV var holding the DSN
	DSN           string     // Resolved DSN value (sensitive; use MaskDSN in logs)
}

// MaskedDSN returns a masked version of the DSN safe for logging.
func (t Target) MaskedDSN() string {
	return MaskDSN(t.DSN)
}

// parseTargets reads POSTGRES_TARGETS (comma-separated alias list) and resolves
// per-target POSTGRES_<NAME>_DSN environment variables.
// Every listed target must have a corresponding non-empty DSN variable.
func parseTargets(envVars map[string]string) ([]Target, error) {
	raw, ok := envVars["POSTGRES_TARGETS"]
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("required environment variable POSTGRES_TARGETS is unset or empty")
	}

	aliases := strings.Split(raw, ",")
	if len(aliases) == 0 {
		return nil, fmt.Errorf("POSTGRES_TARGETS is empty")
	}

	targets := make([]Target, 0, len(aliases))
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			return nil, fmt.Errorf("POSTGRES_TARGETS contains an empty alias (check for trailing/leading commas)")
		}

		// Validate alias name: alphanumeric + underscore only
		if err := validateTargetName(alias); err != nil {
			return nil, fmt.Errorf("POSTGRES_TARGETS: %w", err)
		}

		// Build the DSN env var name: POSTGRES_<UPPER_ALIAS>_DSN
		envName := "POSTGRES_" + strings.ToUpper(alias) + "_DSN"
		dsn, ok := envVars[envName]
		if !ok || strings.TrimSpace(dsn) == "" {
			return nil, fmt.Errorf("environment variable %q is unset or empty (required for target %q)", envName, alias)
		}

		targets = append(targets, Target{
			Name:          alias,
			Enabled:       true,
			AccessMode:    AccessReadOnly, // default; can be overridden by Load
			ConnectionEnv: envName,
			DSN:           dsn,
		})
	}
	return targets, nil
}

// validateTargetName ensures the alias contains only ASCII alphanumeric and underscore characters.
func validateTargetName(name string) error {
	for _, ch := range name {
		isLower := ch >= 'a' && ch <= 'z'
		isUpper := ch >= 'A' && ch <= 'Z'
		isDigit := ch >= '0' && ch <= '9'
		if !isLower && !isUpper && !isDigit && ch != '_' {
			return fmt.Errorf("invalid target alias %q: must contain only alphanumeric characters and underscores", name)
		}
	}
	return nil
}

// ResolveTarget looks up a target by alias (case-insensitive).
// Returns a *Target and error if not found.
func ResolveTarget(targets []Target, name string) (*Target, error) {
	nameLower := strings.ToLower(name)
	for i := range targets {
		if strings.ToLower(targets[i].Name) == nameLower {
			return &targets[i], nil
		}
	}
	return nil, fmt.Errorf("target %q not found: valid targets are [%s]", name, joinTargetNames(targets))
}

// joinTargetNames returns a comma-separated list of target names for error messages.
func joinTargetNames(targets []Target) string {
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.Name
	}
	return strings.Join(names, ", ")
}

// MaskDSN masks the password portion of a PostgreSQL DSN.
// It handles both postgres:// and postgresql:// URL schemes.
func MaskDSN(dsn string) string {
	return maskDSN(dsn)
}
