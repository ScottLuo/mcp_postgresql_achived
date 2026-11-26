//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"testing"

	"postgres-dba-mcp/tests/integration/pgtest"
)

// CFG-I01: db_get_config reads a single GUC parameter.
func TestIntegration_GetConfig(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)
	res := pgtest.Exec(t, reg, "db_get_config", "dev", map[string]any{
		"target": "dev",
		"name":   "max_connections",
	})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), "max_connections") {
		t.Errorf("data should include parameter name: %s", string(b))
	}
}

// CFG-I02: db_get_config with an unknown parameter errors without leaking DSN.
func TestIntegration_GetConfig_Unknown(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)
	res := pgtest.Exec(t, reg, "db_get_config", "dev", map[string]any{
		"target": "dev",
		"name":   "no_such_guc_param",
	})
	// An unknown but validly-formatted identifier should error or return empty;
	// either way it must not leak the DSN.
	if !res.Success && res.Error != nil {
		if strings.Contains(res.Error.Message, "postgres://") || strings.Contains(res.Error.Message, "postgresql://") {
			t.Errorf("error leaked DSN: %s", res.Error.Message)
		}
	}
}

// CFG-I03: db_get_config_list returns permitted configuration parameters.
func TestIntegration_GetConfigList(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)
	res := pgtest.Exec(t, reg, "db_get_config_list", "dev", map[string]any{"target": "dev"})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), "max_connections") && !strings.Contains(string(b), "shared_buffers") {
		t.Errorf("config list should include permitted params: %s", string(b))
	}
}

// CFG-I04: db_reload_config reloads the server configuration.
func TestIntegration_ReloadConfig(t *testing.T) {
	pg := pgtest.NewPostgres(t)
	pg.Setup(t)
	reg := pgtest.Registry(t, "dev", pg.DSN)
	res := pgtest.Exec(t, reg, "db_reload_config", "dev", map[string]any{"target": "dev"})
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
}
