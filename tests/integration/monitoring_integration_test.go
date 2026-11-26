//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"testing"

	"postgres-dba-mcp/internal/mcp"
	"postgres-dba-mcp/tests/integration/pgtest"
)

func newMonEnv(t *testing.T) *mcp.Registry {
	t.Helper()
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	pg.EnableStatStatements()
	return pgtest.Registry(t, "dev", pg.DSN)
}

// MON-I01: db_get_activity returns active sessions.
func TestIntegration_GetActivity(t *testing.T) {
	reg := newMonEnv(t)
	res := pgtest.Exec(t, reg, "db_get_activity", "dev", map[string]any{
		"target": "dev",
		"limit":  50,
	})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	t.Logf("activity data: %s", string(b))
}

// MON-I03: db_get_connections returns connection statistics.
func TestIntegration_GetConnections(t *testing.T) {
	reg := newMonEnv(t)
	res := pgtest.Exec(t, reg, "db_get_connections", "dev", map[string]any{"target": "dev"})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), "max_connections") && !strings.Contains(string(b), "count") {
		t.Errorf("connection stats should include connection info: %s", string(b))
	}
}

// MON-I04: db_get_locks returns lock information.
func TestIntegration_GetLocks(t *testing.T) {
	reg := newMonEnv(t)
	res := pgtest.Exec(t, reg, "db_get_locks", "dev", map[string]any{"target": "dev"})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
}

// MON-I05: db_get_slow_queries returns slow statements when pg_stat_statements is available.
func TestIntegration_GetSlowQueries(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	pg.EnableStatStatements()
	reg := pgtest.Registry(t, "dev", pg.DSN)
	// Generate a slow query to register.
	pool := pg.Pool(t, pg.DSN)
	_, _ = pool.Exec(t.Context(), "SELECT pg_sleep(0.1)")
	pool.Close()

	res := pgtest.Exec(t, reg, "db_get_slow_queries", "dev", map[string]any{
		"target": "dev",
		"limit":  10,
	})
	if !res.Success {
		// If the extension is somehow unavailable, that is acceptable but logged.
		if res.Error != nil && res.Error.Code == mcp.ErrCodeFeatureUnavailable {
			t.Skipf("pg_stat_statements unavailable: %s", res.Error.Message)
		}
		t.Fatalf("expected success, got error: %v", res.Error)
	}
}

// MON-I06: db_get_slow_queries reports DB_FEATURE_UNAVAILABLE when extension is absent.
func TestIntegration_GetSlowQueries_NoExtension(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	// Do NOT enable pg_stat_statements.
	reg := pgtest.Registry(t, "dev", pg.DSN)
	res := pgtest.Exec(t, reg, "db_get_slow_queries", "dev", map[string]any{
		"target": "dev",
		"limit":  10,
	})
	if !res.Success && res.Error != nil && res.Error.Code != mcp.ErrCodeFeatureUnavailable {
		// Some setups may have the extension pre-installed; in that case success is fine.
		t.Errorf("expected DB_FEATURE_UNAVAILABLE or success, got error code %s: %s", res.Error.Code, res.Error.Message)
	}
}

// MON-I07: db_get_database_stats returns pg_stat_database metrics.
func TestIntegration_GetDatabaseStats(t *testing.T) {
	reg := newMonEnv(t)
	res := pgtest.Exec(t, reg, "db_get_database_stats", "dev", map[string]any{"target": "dev"})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), "xact_commit") && !strings.Contains(string(b), "xact") {
		t.Errorf("database stats should include transaction counters: %s", string(b))
	}
}

// MON-I08: db_get_table_stats returns table access statistics.
func TestIntegration_GetTableStats(t *testing.T) {
	reg := newMonEnv(t)
	res := pgtest.Exec(t, reg, "db_get_table_stats", "dev",
		map[string]any{"target": "dev", "schema": "public", "table": "orders"})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), "seq_scan") && !strings.Contains(string(b), "idx_scan") {
		t.Errorf("table stats should include scan counters: %s", string(b))
	}
}

// MON-I09: db_get_index_stats returns index usage statistics.
func TestIntegration_GetIndexStats(t *testing.T) {
	reg := newMonEnv(t)
	res := pgtest.Exec(t, reg, "db_get_index_stats", "dev",
		map[string]any{"target": "dev", "schema": "public", "table": "orders"})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
}

// MON-I10: db_get_cache_stats returns cache hit statistics.
func TestIntegration_GetCacheStats(t *testing.T) {
	reg := newMonEnv(t)
	res := pgtest.Exec(t, reg, "db_get_cache_stats", "dev", map[string]any{"target": "dev"})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), "blcache") && !strings.Contains(string(b), "hit") {
		t.Errorf("cache stats should include hit ratio data: %s", string(b))
	}
}

// MON-I11: db_get_replication_status works on a primary (not a standby).
func TestIntegration_GetReplicationStatus(t *testing.T) {
	reg := newMonEnv(t)
	res := pgtest.Exec(t, reg, "db_get_replication_status", "dev", map[string]any{"target": "dev"})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
}

// MON-I12: db_get_replication_lag works on a single-node primary.
func TestIntegration_GetReplicationLag(t *testing.T) {
	reg := newMonEnv(t)
	res := pgtest.Exec(t, reg, "db_get_replication_lag", "dev", map[string]any{"target": "dev"})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
}

// MON-I13: db_get_deadlocks returns deadlock statistics.
func TestIntegration_GetDeadlocks(t *testing.T) {
	reg := newMonEnv(t)
	res := pgtest.Exec(t, reg, "db_get_deadlocks", "dev", map[string]any{"target": "dev"})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
}
