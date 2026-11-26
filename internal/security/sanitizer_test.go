//go:build unit

package security

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestSanitizeError(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "URL DSN with password",
			input:    "connection failed: postgres://admin:secret123@prod-db:5432/mydb?sslmode=verify-full",
			expected: "connection failed: postgres://admin:****@prod-db:5432/mydb?sslmode=verify-full",
		},
		{
			name:     "postgresql URL scheme",
			input:    "error: postgresql://user:pass123@localhost:5432/db",
			expected: "error: postgresql://user:****@localhost:5432/db",
		},
		{
			name:     "key-value DSN password",
			input:    "connection error: user=admin password=secret host=prod-db port=5432 dbname=mydb",
			expected: "connection error: user=**** password=**** host=prod-db port=5432 dbname=mydb",
		},
		{
			name:     "multiple DSNs in one message",
			input:    "first: postgres://u1:p1@h1:5432/db1, second: postgres://u2:p2@h2:5432/db2",
			expected: "first: postgres://u1:****@h1:5432/db1, second: postgres://u2:****@h2:5432/db2",
		},
		{
			name:     "plain error unchanged",
			input:    `relation "orders" does not exist`,
			expected: `relation "orders" does not exist`,
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "DSN embedded in longer text",
			input:    "error at step 5: failed to connect using postgres://u:pass@h:5432/db, retrying in 3s",
			expected: "error at step 5: failed to connect using postgres://u:****@h:5432/db, retrying in 3s",
		},
		{
			name:     "key-value DSN with multiple fields",
			input:    "user=myuser password=mypass host=prod port=5432 dbname=testdb",
			expected: "user=**** password=**** host=prod port=5432 dbname=testdb",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeError(tc.input)
			if got != tc.expected {
				t.Errorf("SanitizeError() = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestSanitizeError_PasswordNotVisible(t *testing.T) {
	input := "connection failed: postgres://admin:secret123@prod-db:5432/mydb"
	got := SanitizeError(input)
	if strings.Contains(got, "secret123") {
		t.Errorf("password 'secret123' still visible in output: %q", got)
	}
}

func TestSanitizeLog(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		noHost    string
		noPort    string
		noPass    string
	}{
		{
			name:   "key-value DSN all masked",
			input:  "connecting to user=admin password=secret host=prod port=5432 dbname=mydb",
			noHost: "prod",
			noPort: "5432",
			noPass: "secret",
		},
		{
			name:   "URL DSN password masked",
			input:  "error: postgres://user:pass@host:5432/db",
			noPass: "pass",
		},
		{
			name:   "empty string",
			input:  "",
			noHost: "",
			noPort: "",
			noPass: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeLog(tc.input)
			if tc.noHost != "" && strings.Contains(got, "host="+tc.noHost) {
				t.Errorf("host value '%s' still visible: %q", tc.noHost, got)
			}
			if tc.noPort != "" && strings.Contains(got, "port="+tc.noPort) {
				t.Errorf("port value '%s' still visible: %q", tc.noPort, got)
			}
			if tc.noPass != "" && strings.Contains(got, tc.noPass) {
				t.Errorf("password value '%s' still visible: %q", tc.noPass, got)
			}
		})
	}
}

func TestMapPGError(t *testing.T) {
	tests := []struct {
		name     string
		pgErr    *pgconn.PgError
		wantCode string
	}{
		{
			name:     "42601 syntax_error",
			pgErr:    &pgconn.PgError{Code: "42601", Message: "syntax error at or near \"FROM\""},
			wantCode: ErrCodeSQLInvalid,
		},
		{
			name:     "42P01 undefined_table",
			pgErr:    &pgconn.PgError{Code: "42P01", Message: `relation "orders" does not exist`},
			wantCode: ErrCodeExecutionFailed,
		},
		{
			name:     "42703 undefined_column",
			pgErr:    &pgconn.PgError{Code: "42703", Message: `column "x" does not exist`},
			wantCode: ErrCodeExecutionFailed,
		},
		{
			name:     "55006 read_only_sql_transaction",
			pgErr:    &pgconn.PgError{Code: "55006", Message: "cannot execute INSERT in a read-only transaction"},
			wantCode: ErrCodeSQLNotAllowed,
		},
		{
			name:     "55P03 active_sql_transaction",
			pgErr:    &pgconn.PgError{Code: "55P03", Message: "active SQL transaction"},
			wantCode: ErrCodePermissionDenied,
		},
		{
			name:     "57014 query_canceled",
			pgErr:    &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"},
			wantCode: ErrCodeQueryTimeout,
		},
		{
			name:     "53200 out_of_memory",
			pgErr:    &pgconn.PgError{Code: "53200", Message: "out of memory"},
			wantCode: ErrCodeResultTooLarge,
		},
		{
			name:     "42501 insufficient_privilege",
			pgErr:    &pgconn.PgError{Code: "42501", Message: "permission denied for table orders"},
			wantCode: ErrCodePermissionDenied,
		},
		{
			name:     "unknown code 99999",
			pgErr:    &pgconn.PgError{Code: "99999", Message: "some unknown error"},
			wantCode: ErrCodeExecutionFailed,
		},
		{
			name:     "empty code",
			pgErr:    &pgconn.PgError{Code: "", Message: "some error"},
			wantCode: ErrCodeExecutionFailed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := MapPGError(tc.pgErr)
			if code != tc.wantCode {
				t.Errorf("MapPGError() code = %q, want %q", code, tc.wantCode)
			}
		})
	}
}

func TestMapPGError_MessageSanitized(t *testing.T) {
	pgErr := &pgconn.PgError{
		Code:    "08006",
		Message: "connection to server postgres://user:pass@host:5432/db failed",
	}
	_, msg := MapPGError(pgErr)
	if strings.Contains(msg, "pass") {
		t.Errorf("password 'pass' still visible in message: %q", msg)
	}
}

func TestMapPGError_Nil(t *testing.T) {
	code, msg := MapPGError(nil)
	if code != ErrCodeExecutionFailed {
		t.Errorf("MapPGError(nil) code = %q, want %q", code, ErrCodeExecutionFailed)
	}
	if msg != "" {
		t.Errorf("MapPGError(nil) message = %q, want empty", msg)
	}
}

func TestErrCodeConstants_Values(t *testing.T) {
	tests := []struct {
		name     string
		got      string
		expected string
	}{
		{"ErrCodeTargetNotFound", ErrCodeTargetNotFound, "DB_TARGET_NOT_FOUND"},
		{"ErrCodeConnectionFailed", ErrCodeConnectionFailed, "DB_CONNECTION_FAILED"},
		{"ErrCodePermissionDenied", ErrCodePermissionDenied, "DB_PERMISSION_DENIED"},
		{"ErrCodeSQLInvalid", ErrCodeSQLInvalid, "DB_SQL_INVALID"},
		{"ErrCodeSQLNotAllowed", ErrCodeSQLNotAllowed, "DB_SQL_NOT_ALLOWED"},
		{"ErrCodeQueryTimeout", ErrCodeQueryTimeout, "DB_QUERY_TIMEOUT"},
		{"ErrCodeResultTooLarge", ErrCodeResultTooLarge, "DB_RESULT_TOO_LARGE"},
		{"ErrCodeApprovalRequired", ErrCodeApprovalRequired, "DB_APPROVAL_REQUIRED"},
		{"ErrCodeApprovalInvalid", ErrCodeApprovalInvalid, "DB_APPROVAL_INVALID"},
		{"ErrCodeExecutionFailed", ErrCodeExecutionFailed, "DB_EXECUTION_FAILED"},
		{"ErrCodeFeatureUnavailable", ErrCodeFeatureUnavailable, "DB_FEATURE_UNAVAILABLE"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.expected {
				t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.expected)
			}
		})
	}
}

func TestErrCodeConstants_Unique(t *testing.T) {
	codes := []string{
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

	seen := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		if _, exists := seen[c]; exists {
			t.Errorf("duplicate error code: %q", c)
		}
		seen[c] = struct{}{}
	}

	if len(seen) != 11 {
		t.Errorf("expected 11 unique error codes, got %d", len(seen))
	}
}
