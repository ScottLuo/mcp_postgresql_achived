//go:build unit

package config

import (
	"strings"
	"testing"
	"time"
)

// TestMaskDSN verifies that DSN passwords are masked in all forms.
func TestMaskDSN(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want string
	}{
		{
			name: "postgres with password",
			dsn:  "postgres://user:pass@host:5432/db?sslmode=disable",
			want: "postgres://user:****@host:5432/db?sslmode=disable",
		},
		{
			name: "postgresql scheme",
			dsn:  "postgresql://admin:secret@prod:5432/mydb",
			want: "postgresql://admin:****@prod:5432/mydb",
		},
		{
			name: "colon in password",
			dsn:  "postgres://user:pa:ss@word@host:5432/db",
			want: "postgres://user:pa:****@host:5432/db",
		},
		{
			name: "no password",
			dsn:  "postgres://user@host:5432/db",
			want: "postgres://user@host:5432/db",
		},
		{
			name: "non DSN string",
			dsn:  "hello world",
			want: "hello world",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := MaskDSN(tc.dsn)
			if got != tc.want {
				t.Errorf("MaskDSN(%q) = %q, want %q", tc.dsn, got, tc.want)
			}
		})
	}
}

// TestTarget_MaskedDSN verifies Target.MaskedDSN does not leak the password.
func TestTarget_MaskedDSN(t *testing.T) {
	target := Target{
		Name: "development",
		DSN:  "postgres://user:secretpass@localhost:5432/dev",
	}
	masked := target.MaskedDSN()
	if strings.Contains(masked, "secretpass") {
		t.Errorf("MaskedDSN() leaks password: %q", masked)
	}
	if !strings.Contains(masked, "****") {
		t.Errorf("MaskedDSN() should contain mask marker: %q", masked)
	}
}

// TestEnvHelper_RequiredString verifies getRequired returns the value when set.
func TestEnvHelper_RequiredString(t *testing.T) {
	t.Setenv("SOME_TEST_VAR", "hello")
	val, err := getRequired("SOME_TEST_VAR")
	if err != nil {
		t.Fatalf("getRequired returned error: %v", err)
	}
	if val != "hello" {
		t.Errorf("getRequired = %q, want hello", val)
	}
}

// TestEnvHelper_RequiredString_Missing (C-11) verifies getRequired error naming.
func TestEnvHelper_RequiredString_Missing(t *testing.T) {
	t.Setenv("MISSING_TEST_VAR", "")
	_, err := getRequired("MISSING_TEST_VAR")
	if err == nil {
		t.Fatal("getRequired expected error, got nil")
	}
	if !strings.Contains(err.Error(), "MISSING_TEST_VAR") {
		t.Errorf("error should name the variable, got: %v", err)
	}
}

// TestEnvHelper_OptionalString verifies default is returned when unset.
func TestEnvHelper_OptionalString(t *testing.T) {
	t.Setenv("OPT_TEST_VAR", "actual")
	if got := getOptional("OPT_TEST_VAR", "default"); got != "actual" {
		t.Errorf("getOptional = %q, want actual", got)
	}
	if got := getOptional("UNSET_OPT_VAR", "default"); got != "default" {
		t.Errorf("getOptional on unset = %q, want default", got)
	}
}

// TestEnvHelper_OptionalDuration_Default (C-12) verifies default for unset duration.
func TestEnvHelper_OptionalDuration_Default(t *testing.T) {
	d, err := getOptionalDuration("UNSET_DURATION_VAR", 30*time.Second)
	if err != nil {
		t.Fatalf("getOptionalDuration returned error: %v", err)
	}
	if d != 30*time.Second {
		t.Errorf("getOptionalDuration = %v, want 30s", d)
	}
}

// TestEnvHelper_OptionalDuration_Valid verifies parsing a valid duration.
func TestEnvHelper_OptionalDuration_Valid(t *testing.T) {
	t.Setenv("DUR_VAR", "1m30s")
	d, err := getOptionalDuration("DUR_VAR", time.Second)
	if err != nil {
		t.Fatalf("getOptionalDuration returned error: %v", err)
	}
	if d != 90*time.Second {
		t.Errorf("getOptionalDuration = %v, want 90s", d)
	}
}

// TestEnvHelper_OptionalDuration_Invalid verifies parse error naming the var.
func TestEnvHelper_OptionalDuration_Invalid(t *testing.T) {
	t.Setenv("DUR_VAR", "abc")
	_, err := getOptionalDuration("DUR_VAR", time.Second)
	if err == nil {
		t.Fatal("getOptionalDuration expected error, got nil")
	}
	if !strings.Contains(err.Error(), "DUR_VAR") {
		t.Errorf("error should name the variable, got: %v", err)
	}
}

// TestEnvHelper_OptionalInt verifies integer parsing and defaults.
func TestEnvHelper_OptionalInt(t *testing.T) {
	t.Setenv("INT_VAR", "42")
	n, err := getOptionalInt("INT_VAR", 0)
	if err != nil {
		t.Fatalf("getOptionalInt returned error: %v", err)
	}
	if n != 42 {
		t.Errorf("getOptionalInt = %d, want 42", n)
	}

	if n, err := getOptionalInt("UNSET_INT_VAR", 7); err != nil {
		t.Fatalf("getOptionalInt default returned error: %v", err)
	} else if n != 7 {
		t.Errorf("getOptionalInt default = %d, want 7", n)
	}

	t.Setenv("INT_VAR_BAD", "xyz")
	if _, err := getOptionalInt("INT_VAR_BAD", 0); err == nil {
		t.Error("getOptionalInt expected error, got nil")
	}
}

// TestEnvHelper_OptionalBool verifies boolean parsing and defaults.
func TestEnvHelper_OptionalBool(t *testing.T) {
	t.Setenv("BOOL_VAR", "true")
	b, err := getOptionalBool("BOOL_VAR", false)
	if err != nil {
		t.Fatalf("getOptionalBool returned error: %v", err)
	}
	if !b {
		t.Errorf("getOptionalBool = false, want true")
	}

	if b, err := getOptionalBool("UNSET_BOOL_VAR", true); err != nil {
		t.Fatalf("getOptionalBool default returned error: %v", err)
	} else if !b {
		t.Errorf("getOptionalBool default = false, want true")
	}

	t.Setenv("BOOL_VAR_BAD", "nope")
	if _, err := getOptionalBool("BOOL_VAR_BAD", false); err == nil {
		t.Error("getOptionalBool expected error, got nil")
	}
}

// TestValidateTransport verifies transport string validation.
func TestValidateTransport(t *testing.T) {
	if err := validateTransport("stdio"); err != nil {
		t.Errorf("validateTransport(stdio) unexpected error: %v", err)
	}
	if err := validateTransport("http"); err != nil {
		t.Errorf("validateTransport(http) unexpected error: %v", err)
	}
	if err := validateTransport("websocket"); err == nil {
		t.Error("validateTransport(websocket) expected error, got nil")
	}
}

// TestValidateLogLevel verifies log level string validation.
func TestValidateLogLevel(t *testing.T) {
	for _, lvl := range []string{"debug", "info", "warn", "error"} {
		if err := validateLogLevel(lvl); err != nil {
			t.Errorf("validateLogLevel(%q) unexpected error: %v", lvl, err)
		}
	}
	if err := validateLogLevel("verbose"); err == nil {
		t.Error("validateLogLevel(verbose) expected error, got nil")
	}
}
