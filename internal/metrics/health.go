package metrics

import (
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// HealthHandler returns an http.Handler that reports the overall health status
// of the MCP server's database targets. It is intended for HTTP deployments
// (spec §15.2 / §16 "Provide a health endpoint for HTTP deployments").
//
// The response body is a single human-readable status string
// (HealthOK / HealthDegraded / HealthUnknown). The HTTP status code reflects
// health: 200 when healthy, 503 otherwise.
//
// The handler reads only the in-memory health state of m (no collector data),
// so it is safe and cheap to call concurrently.
func HealthHandler(m *Metrics) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		status := m.Health()

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if status == HealthOK {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_, _ = fmt.Fprintln(w, status)
	})
}

// MetricsHandler returns an http.Handler that exposes the Prometheus operational
// metrics registered on m (see New). It is the /metrics endpoint for HTTP
// deployments; the entrypoint is free to mount it on any route.
func MetricsHandler(m *Metrics) http.Handler {
	return promhttp.HandlerFor(m.Registry(), promhttp.HandlerOpts{})
}
