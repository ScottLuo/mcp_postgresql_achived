//go:build unit

package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// allMetricFamilies lists the custom metric families that must be registered
// on a fresh registry without collision.
var allMetricFamilies = []string{
	"mcp_tool_calls_total",
	"mcp_tool_errors_total",
	"mcp_tool_duration_seconds",
	"db_pool_connections",
	"db_pool_idle_connections",
	"db_pool_acquired_connections",
	"db_query_duration_seconds",
	"db_query_timeout_total",
	"db_target_health",
	"db_approval_requests_total",
}

// familyNames returns the set of metric family names currently present in the
// registry.
func familyNames(m *Metrics) map[string]bool {
	fams, _ := m.Registry().Gather()
	set := make(map[string]bool, len(fams))
	for _, f := range fams {
		set[f.GetName()] = true
	}
	return set
}

// TestNew_RegistrationNoCollision verifies that New registers all collectors
// on a fresh registry without duplicate-collector errors, and that repeated
// New calls (fresh registries) do not collide with one another.
func TestNew_RegistrationNoCollision(t *testing.T) {
	for i := 0; i < 3; i++ {
		m := New()
		if m.Registry() == nil {
			t.Fatal("New returned nil registry")
		}
		// Emit one sample to each vec collector so the family appears in
		// Gather output.
		m.RecordToolCall("t", "tgt", resultOK)
		m.RecordError("t", "tgt", "E1")
		m.ObserveDuration("t", "tgt", 0.1)
		m.RecordPoolStats(1, 0, 1)
		m.RecordQueryDuration("tgt", 0.1)
		m.RecordQueryTimeout("tgt")
		m.SetTargetHealth("tgt", true)
		m.RecordApprovalRequest("tgt", resultOK)

		names := familyNames(m)
		for _, name := range allMetricFamilies {
			if !names[name] {
				t.Errorf("metric %q not registered", name)
			}
		}
	}
}

// TestRecordToolCall verifies the tool-call counter increments per label set.
func TestRecordToolCall(t *testing.T) {
	m := New()
	m.RecordToolCall("db_get_info", "development", resultOK)
	m.RecordToolCall("db_get_info", "development", resultError)
	m.RecordToolCall("db_vacuum_table", "production", resultOK)

	// Each distinct label combination yields one sample.
	if got := testutil.CollectAndCount(m.toolCalls); got != 3 {
		t.Errorf("mcp_tool_calls_total count = %d, want 3", got)
	}
}

// TestRecordError verifies the error counter increments per error code and
// defaults to "unknown" when the code is empty.
func TestRecordError(t *testing.T) {
	m := New()
	m.RecordError("db_query_readonly", "development", "DB_QUERY_TIMEOUT")
	m.RecordError("db_query_readonly", "development", "DB_SQL_INVALID")
	if got := testutil.CollectAndCount(m.toolErrors); got != 2 {
		t.Errorf("mcp_tool_errors_total count = %d, want 2", got)
	}
	m.RecordError("db_vacuum_table", "production", "")
	if got := testutil.CollectAndCount(m.toolErrors); got != 3 {
		t.Errorf("mcp_tool_errors_total count = %d, want 3", got)
	}
}

// TestObserveDuration verifies the tool-duration histogram records observations.
func TestObserveDuration(t *testing.T) {
	m := New()
	m.ObserveDuration("db_explain_query", "development", 0.125)
	// A histogram observation emits bucket/sum/count samples.
	if got := testutil.CollectAndCount(m.toolDuration); got < 1 {
		t.Errorf("mcp_tool_duration_seconds count = %d, want >= 1", got)
	}
}

// TestRecordPoolStats verifies the pool gauges are set to the reported values.
func TestRecordPoolStats(t *testing.T) {
	m := New()
	m.RecordPoolStats(10, 4, 6)
	if got := testutil.ToFloat64(m.poolConnections); got != 10 {
		t.Errorf("db_pool_connections = %v, want 10", got)
	}
	if got := testutil.ToFloat64(m.poolIdleConnections); got != 4 {
		t.Errorf("db_pool_idle_connections = %v, want 4", got)
	}
	if got := testutil.ToFloat64(m.poolAcquiredConn); got != 6 {
		t.Errorf("db_pool_acquired_connections = %v, want 6", got)
	}
}

// TestRecordQuery verifies query duration histogram and timeout counter.
func TestRecordQuery(t *testing.T) {
	m := New()
	m.RecordQueryDuration("production", 0.01)
	m.RecordQueryTimeout("production")
	if got := testutil.CollectAndCount(m.queryDuration); got < 1 {
		t.Errorf("db_query_duration_seconds count = %d, want >= 1", got)
	}
	if got := testutil.CollectAndCount(m.queryTimeout); got != 1 {
		t.Errorf("db_query_timeout_total count = %d, want 1", got)
	}
}

// TestSetTargetHealthAndHealth verifies SetTargetHealth updates the gauge and
// drives Health() to the correct overall status.
func TestSetTargetHealthAndHealth(t *testing.T) {
	m := New()
	// No health recorded yet.
	if got := m.Health(); got != HealthUnknown {
		t.Errorf("Health() = %q, want %q", got, HealthUnknown)
	}

	m.SetTargetHealth("development", true)
	if got := m.Health(); got != HealthOK {
		t.Errorf("Health() = %q, want %q", got, HealthOK)
	}

	m.SetTargetHealth("production", false)
	if got := m.Health(); got != HealthDegraded {
		t.Errorf("Health() = %q, want %q", got, HealthDegraded)
	}
	if got := testutil.CollectAndCount(m.targetHealth); got != 2 {
		t.Errorf("db_target_health series = %d, want 2", got)
	}
}

// TestRecordApprovalRequest verifies the approval counter.
func TestRecordApprovalRequest(t *testing.T) {
	m := New()
	m.RecordApprovalRequest("production", resultOK)
	m.RecordApprovalRequest("production", "denied")
	if got := testutil.CollectAndCount(m.approvalReqs); got != 2 {
		t.Errorf("db_approval_requests_total count = %d, want 2", got)
	}
}

// TestNoSensitiveLabels verifies that metric label values are low-cardinality
// (no DSN patterns, no "password" substring) by inspecting the exposition text.
func TestNoSensitiveLabels(t *testing.T) {
	m := New()
	m.RecordToolCall("db_get_info", "development", resultOK)

	// Collect the exposition text via the metrics handler.
	h := MetricsHandler(m)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	out := rr.Body.String()

	// No DSN patterns should appear.
	for _, forbidden := range []string{"://", "password", "postgres://"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("exposition text contains forbidden substring %q: %s", forbidden, out)
		}
	}
}

// TestHealthHandler verifies the health HTTP helper returns the correct status
// code and body for each health state.
func TestHealthHandler(t *testing.T) {
	// Unknown: no health recorded -> 503 + "unknown".
	m := New()
	h := HealthHandler(m)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("unknown health: code = %d, want 503", rr.Code)
	}
	if strings.TrimSpace(rr.Body.String()) != HealthUnknown {
		t.Errorf("unknown health: body = %q, want %q", rr.Body.String(), HealthUnknown)
	}

	// Healthy: 200 + "ok".
	m.SetTargetHealth("development", true)
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rr2.Code != http.StatusOK {
		t.Errorf("healthy: code = %d, want 200", rr2.Code)
	}
	if strings.TrimSpace(rr2.Body.String()) != HealthOK {
		t.Errorf("healthy: body = %q, want %q", rr2.Body.String(), HealthOK)
	}

	// Degraded: 503 + "degraded".
	m.SetTargetHealth("production", false)
	rr3 := httptest.NewRecorder()
	h.ServeHTTP(rr3, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rr3.Code != http.StatusServiceUnavailable {
		t.Errorf("degraded: code = %d, want 503", rr3.Code)
	}
	if strings.TrimSpace(rr3.Body.String()) != HealthDegraded {
		t.Errorf("degraded: body = %q, want %q", rr3.Body.String(), HealthDegraded)
	}
}

// TestMetricsHandler verifies the metrics HTTP helper exposes the registry.
func TestMetricsHandler(t *testing.T) {
	m := New()
	m.RecordToolCall("db_get_info", "development", resultOK)
	h := MetricsHandler(m)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("metrics: code = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "mcp_tool_calls_total") {
		t.Errorf("metrics output missing mcp_tool_calls_total: %s", rr.Body.String())
	}
}
