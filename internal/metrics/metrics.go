// Package metrics provides the operational Prometheus collectors and a
// health-endpoint helper for the MCP server, as specified in §16 (Operational
// Monitoring) of docs/design.md.
//
// All custom collectors are registered on a fresh, non-default
// prometheus.Registry so they never collide with the process-wide default
// registry (or with other components registering the same metric names).
//
// Labels are kept minimal and low-cardinality (tool, target, op, result).
// High-cardinality values such as request_id, raw SQL text, or DSN/credentials
// are never used as label values.
package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// resultOK/resultError are the low-cardinality result label values used for
// tool calls and approval requests.
const (
	resultOK    = "ok"
	resultError = "error"
)

// toolDurationBuckets are the histogram buckets for tool / query durations in
// seconds, tuned for sub-second to ~30s tool invocations.
var toolDurationBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30,
}

// Health status strings returned by Health and the health HTTP handler.
const (
	HealthOK       = "ok"
	HealthDegraded = "degraded"
	HealthUnknown  = "unknown"
)

// Metrics holds the Prometheus collectors plus an isolated registry that all
// of them are registered against. The registry is fresh (non-default), so
// registering these collectors does not conflict with the process default
// registry.
//
// Metrics is safe for concurrent use; the underlying collectors are.
type Metrics struct {
	registry *prometheus.Registry

	toolCalls    *prometheus.CounterVec
	toolErrors   *prometheus.CounterVec
	toolDuration *prometheus.HistogramVec

	poolConnections     prometheus.Gauge
	poolIdleConnections prometheus.Gauge
	poolAcquiredConn    prometheus.Gauge

	queryDuration *prometheus.HistogramVec
	queryTimeout  *prometheus.CounterVec
	targetHealth  *prometheus.GaugeVec
	approvalReqs  *prometheus.CounterVec

	// healthState mirrors the last recorded health of each target so that
	// Health can compute an overall status. Protected by mu.
	mu          sync.RWMutex
	healthState map[string]bool
}

// New registers all operational collectors on a fresh prometheus.Registry and
// returns the ready-to-use Metrics. The returned value should be used for all
// metric emission; call Registry to obtain the prometheus.Collector set for
// HTTP exposition.
func New() *Metrics {
	reg := prometheus.NewRegistry()

	// Register the standard Go runtime collector alongside our metrics so a
	// /metrics endpoint has full context. On a fresh registry this cannot
	// collide with our custom collectors.
	_ = reg.Register(collectors.NewGoCollector())

	m := &Metrics{
		registry:    reg,
		healthState: make(map[string]bool),
		toolCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mcp_tool_calls_total",
			Help: "Total number of MCP tool invocations, partitioned by tool, target and result.",
		}, []string{"tool", "target", "result"}),
		toolErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mcp_tool_errors_total",
			Help: "Total number of failed MCP tool invocations, partitioned by tool, target and error code.",
		}, []string{"tool", "target", "error_code"}),
		toolDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "mcp_tool_duration_seconds",
			Help:    "Latency distribution of MCP tool invocations in seconds, by tool and target.",
			Buckets: toolDurationBuckets,
		}, []string{"tool", "target"}),
		poolConnections: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "db_pool_connections",
			Help: "Total number of database connections allocated per target.",
		}),
		poolIdleConnections: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "db_pool_idle_connections",
			Help: "Number of idle database connections per target.",
		}),
		poolAcquiredConn: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "db_pool_acquired_connections",
			Help: "Number of acquired (in-use) database connections per target.",
		}),
		queryDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "db_query_duration_seconds",
			Help:    "Latency distribution of executed database queries in seconds, by target.",
			Buckets: toolDurationBuckets,
		}, []string{"target"}),
		queryTimeout: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "db_query_timeout_total",
			Help: "Total number of database query timeouts, by target.",
		}, []string{"target"}),
		targetHealth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "db_target_health",
			Help: "Health status of a database target: 1 = healthy, 0 = down.",
		}, []string{"target"}),
		approvalReqs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "db_approval_requests_total",
			Help: "Total approval requests, partitioned by target and result.",
		}, []string{"target", "result"}),
	}

	// Register everything in a single batch so any duplicate registration is
	// reported together; on a fresh registry this cannot collide.
	reg.MustRegister(
		m.toolCalls,
		m.toolErrors,
		m.toolDuration,
		m.poolConnections,
		m.poolIdleConnections,
		m.poolAcquiredConn,
		m.queryDuration,
		m.queryTimeout,
		m.targetHealth,
		m.approvalReqs,
	)

	return m
}

// Registry returns the prometheus.Registry holding all collectors. Use it with
// promhttp (see MetricsHandler) to expose a /metrics endpoint.
func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}

// RecordToolCall increments the tool-call counter with the given result
// ("ok" or "error").
func (m *Metrics) RecordToolCall(tool, target, result string) {
	if result == "" {
		result = resultOK
	}
	m.toolCalls.WithLabelValues(tool, target, result).Inc()
}

// RecordError increments the tool-error counter for the given error code.
func (m *Metrics) RecordError(tool, target, errorCode string) {
	if errorCode == "" {
		errorCode = "unknown"
	}
	m.toolErrors.WithLabelValues(tool, target, errorCode).Inc()
}

// ObserveDuration records a tool invocation duration (in seconds).
func (m *Metrics) ObserveDuration(tool, target string, seconds float64) {
	m.toolDuration.WithLabelValues(tool, target).Observe(seconds)
}

// RecordPoolStats updates the three pool gauges.
//
// db_pool_* metrics carry no target label because the spec lists them as a
// single series; callers managing multiple targets should track the
// last-observed value (or keep one Metrics instance per target).
func (m *Metrics) RecordPoolStats(connections, idle, acquired float64) {
	m.poolConnections.Set(connections)
	m.poolIdleConnections.Set(idle)
	m.poolAcquiredConn.Set(acquired)
}

// RecordQueryDuration records a database query duration (in seconds).
func (m *Metrics) RecordQueryDuration(target string, seconds float64) {
	m.queryDuration.WithLabelValues(target).Observe(seconds)
}

// RecordQueryTimeout increments the query-timeout counter for a target.
func (m *Metrics) RecordQueryTimeout(target string) {
	m.queryTimeout.WithLabelValues(target).Inc()
}

// SetTargetHealth records the health status of a target: 1 when up, 0 when
// down. It updates both the db_target_health gauge and the in-memory state used
// by Health.
func (m *Metrics) SetTargetHealth(target string, up bool) {
	v := float64(0)
	if up {
		v = 1
	}
	m.targetHealth.WithLabelValues(target).Set(v)

	m.mu.Lock()
	m.healthState[target] = up
	m.mu.Unlock()
}

// RecordApprovalRequest increments the approval-request counter with the given
// result ("ok", "error", "denied", ...).
func (m *Metrics) RecordApprovalRequest(target, result string) {
	if result == "" {
		result = resultOK
	}
	m.approvalReqs.WithLabelValues(target, result).Inc()
}

// Health returns an overall health status string derived from the last
// recorded health of every target:
//   - HealthUnknown  when no target health has been recorded yet;
//   - HealthOK       when every recorded target is up;
//   - HealthDegraded when at least one recorded target is down.
func (m *Metrics) Health() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.healthState) == 0 {
		return HealthUnknown
	}
	for _, up := range m.healthState {
		if !up {
			return HealthDegraded
		}
	}
	return HealthOK
}
