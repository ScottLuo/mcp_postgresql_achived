//go:build unit

package config

import (
	"strings"
	"testing"
)

// TestTargets_ResolveTarget_Valid (C-13) verifies lookup by alias (case-insensitive).
func TestTargets_ResolveTarget_Valid(t *testing.T) {
	targets := []Target{
		{Name: "development", DSN: "postgres://u:p@h:5432/db"},
		{Name: "Staging", DSN: "postgres://u:p@h:5432/db2"},
	}
	target, err := ResolveTarget(targets, "development")
	if err != nil {
		t.Fatalf("ResolveTarget returned error: %v", err)
	}
	if target.Name != "development" {
		t.Errorf("ResolveTarget = %q, want development", target.Name)
	}

	// Case-insensitive lookup.
	target, err = ResolveTarget(targets, "STAGING")
	if err != nil {
		t.Fatalf("ResolveTarget returned error: %v", err)
	}
	if target.Name != "Staging" {
		t.Errorf("ResolveTarget = %q, want Staging", target.Name)
	}
}

// TestTargets_ResolveTarget_Unknown (C-14) verifies lookup of an unknown alias fails.
func TestTargets_ResolveTarget_Unknown(t *testing.T) {
	targets := []Target{{Name: "development"}}
	_, err := ResolveTarget(targets, "production")
	if err == nil {
		t.Fatal("ResolveTarget expected error, got nil")
	}
	if !strings.Contains(err.Error(), "production") {
		t.Errorf("error should name the missing alias, got: %v", err)
	}
}

// TestTargets_MissingDSN (C-15) verifies error naming the missing DSN variable.
func TestTargets_MissingDSN(t *testing.T) {
	envVars := map[string]string{
		"POSTGRES_TARGETS":         "development,production",
		"POSTGRES_DEVELOPMENT_DSN": "postgres://u:p@h:5432/db",
		// POSTGRES_PRODUCTION_DSN missing
	}
	_, err := parseTargets(envVars)
	if err == nil {
		t.Fatal("parseTargets expected error, got nil")
	}
	if !strings.Contains(err.Error(), "POSTGRES_PRODUCTION_DSN") {
		t.Errorf("error should name the missing DSN variable, got: %v", err)
	}
}

// TestTargets_EmptyList verifies empty POSTGRES_TARGETS error.
func TestTargets_EmptyList(t *testing.T) {
	_, err := parseTargets(map[string]string{"POSTGRES_TARGETS": ""})
	if err == nil {
		t.Fatal("parseTargets expected error, got nil")
	}
}

// TestTargets_Undefined verifies error when POSTGRES_TARGETS key is missing.
func TestTargets_Undefined(t *testing.T) {
	_, err := parseTargets(map[string]string{})
	if err == nil {
		t.Fatal("parseTargets expected error, got nil")
	}
	if !strings.Contains(err.Error(), "POSTGRES_TARGETS") {
		t.Errorf("error should name POSTGRES_TARGETS, got: %v", err)
	}
}

// TestParseTargets_Valid verifies successful parsing with correct ConnectionEnv mapping.
func TestParseTargets_Valid(t *testing.T) {
	envVars := map[string]string{
		"POSTGRES_TARGETS":      "dev,prod",
		"POSTGRES_DEV_DSN":      "postgres://u:p@dev:5432/db",
		"POSTGRES_PROD_DSN":     "postgres://u:p@prod:5432/db",
	}
	targets, err := parseTargets(envVars)
	if err != nil {
		t.Fatalf("parseTargets returned error: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("len(targets) = %d, want 2", len(targets))
	}
	if targets[0].Name != "dev" {
		t.Errorf("targets[0].Name = %q, want dev", targets[0].Name)
	}
	if targets[0].ConnectionEnv != "POSTGRES_DEV_DSN" {
		t.Errorf("targets[0].ConnectionEnv = %q, want POSTGRES_DEV_DSN", targets[0].ConnectionEnv)
	}
	if targets[0].DSN != "postgres://u:p@dev:5432/db" {
		t.Errorf("targets[0].DSN mismatch: %q", targets[0].DSN)
	}
	if targets[1].Name != "prod" {
		t.Errorf("targets[1].Name = %q, want prod", targets[1].Name)
	}
	if !targets[1].Enabled {
		t.Errorf("targets[1].Enabled = false, want true")
	}
}

// TestValidateTargetName verifies alias character validation.
func TestValidateTargetName(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"development", false},
		{"Staging_2", false},
		{"A", false},
		{"prod-db", true},
		{"dev space", true},
		{"x.y", true},
		{"", false}, // empty handled by caller
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateTargetName(tc.name)
			if tc.wantErr && err == nil {
				t.Errorf("validateTargetName(%q) expected error, got nil", tc.name)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateTargetName(%q) unexpected error: %v", tc.name, err)
			}
		})
	}
}

// TestAccessModeValidValues verifies all valid access mode strings.
func TestAccessModeValidValues(t *testing.T) {
	cases := []struct {
		input   string
		want    AccessMode
		wantErr bool
	}{
		{"read_only", AccessReadOnly, false},
		{"restricted_write", AccessRestrictedWrite, false},
		{"read_write", AccessReadWrite, false},
		{"bogus", "", true},
		{"", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got, err := validateAccessMode(tc.input)
			if tc.wantErr && err == nil {
				t.Errorf("validateAccessMode(%q) expected error, got nil", tc.input)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateAccessMode(%q) unexpected error: %v", tc.input, err)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("validateAccessMode(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
