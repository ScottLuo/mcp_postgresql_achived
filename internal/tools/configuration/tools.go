package configuration

import (
	"context"
	"encoding/json"
	"time"

	"postgres-dba-mcp/internal/mcp"
)

// Tool name constants.
const (
	toolGetConfig      = "db_get_config"
	toolGetConfigList  = "db_get_config_list"
	toolReloadConfig   = "db_reload_config"
)

// Tool descriptions.
const (
	getConfigDescription = "Read a permitted PostgreSQL configuration parameter from pg_settings. Returns the current value, unit, category, and short description. Only allow-listed, non-secret parameters are accessible. READ-ONLY."
	getConfigListDescription = "List all permitted PostgreSQL configuration parameters from pg_settings (the allow-list of non-secret settings). Returns name, value, unit, category, and short description for each. READ-ONLY."
	reloadConfigDescription = "Reload the PostgreSQL server configuration (equivalent to SELECT pg_reload_conf()), applying changes to parameters that support runtime reload. Requires approval on most targets. REQUIRES_APPROVAL."
)

// getConfigSQL reads a single pg_settings row by name.
const getConfigSQL = `
SELECT name, setting, unit, category, short_desc
FROM pg_settings
WHERE name = $1`

// getConfigListSQL reads all pg_settings rows so the allow-list filter can be
// applied server-side over the result.
const getConfigListSQL = `
SELECT name, setting, unit, category, short_desc
FROM pg_settings
ORDER BY name`

// reloadConfigSQL issues the server configuration reload.
const reloadConfigSQL = `SELECT pg_reload_conf() AS reloaded`

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// getDBConfig implements db_get_config (spec §7.5).
//
// It reads a single, allow-listed, non-secret GUC parameter from
// pg_settings. The parameter name is validated as an identifier, must be in
// the allow-list, and is compared against pg_settings with a parameterized
// query. Unknown or secret parameters are rejected with
// DB_PERMISSION_DENIED (allow-list) so that no secret/sensitive setting or
// DSN ever leaks.
func (s *Service) getDBConfig(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args, true)
	if err != nil {
		return s.fail(ctx, toolGetConfig, "", operationRead, mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	// Allow-list enforcement: a GUC not in allowedConfigParams is treated as
	// a permission denial so secret/sensitive settings are never exposed.
	if !isAllowedParam(a.Name) {
		return s.fail(ctx, toolGetConfig, a.Target, operationRead,
			mcp.ErrCodePermissionDenied,
			"configuration parameter "+q(a.Name)+" is not in the allowed list", 0), nil
	}

	pool, err := s.resolvePool(a.Target)
	if err != nil {
		return s.fail(ctx, toolGetConfig, a.Target, operationRead, errCodeForErr(err), err.Error(), 0), nil
	}

	start := time.Now()
	execResult, err := s.runQuery(ctx, pool, getConfigSQL, []any{a.Name}, 1)
	duration := time.Since(start)
	if err != nil {
		return s.fail(ctx, toolGetConfig, a.Target, operationRead, errCodeForErr(err), err.Error(), duration), nil
	}

	// Zero rows: the parameter exists in the catalog but not on this server
	// (version/feature dependent). Surface a clean execution error.
	if execResult.RowsReturned == 0 {
		return s.fail(ctx, toolGetConfig, a.Target, operationRead,
			mcp.ErrCodeExecutionFailed,
			"configuration parameter "+q(a.Name)+" not found", duration), nil
	}

	rows, err := toRowMaps(execResult)
	if err != nil {
		return s.fail(ctx, toolGetConfig, a.Target, operationRead, mcp.ErrCodeExecutionFailed, err.Error(), duration), nil
	}
	return s.ok(ctx, toolGetConfig, a.Target, operationRead, rows[0], duration, false, 1), nil
}

// getDBConfigList implements db_get_config_list (spec §7.5).
//
// It reads pg_settings and returns only the rows whose name is in the
// allow-list, so the response contains exclusively permitted, non-secret
// settings. The row cap from the configured limits is enforced by the
// executor for the underlying query.
func (s *Service) getDBConfigList(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args, false)
	if err != nil {
		return s.fail(ctx, toolGetConfigList, "", operationRead, mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	pool, err := s.resolvePool(a.Target)
	if err != nil {
		return s.fail(ctx, toolGetConfigList, a.Target, operationRead, errCodeForErr(err), err.Error(), 0), nil
	}

	start := time.Now()
	execResult, err := s.runQuery(ctx, pool, getConfigListSQL, nil, 0)
	duration := time.Since(start)
	if err != nil {
		return s.fail(ctx, toolGetConfigList, a.Target, operationRead, errCodeForErr(err), err.Error(), duration), nil
	}

	rows, err := toRowMaps(execResult)
	if err != nil {
		return s.fail(ctx, toolGetConfigList, a.Target, operationRead, mcp.ErrCodeExecutionFailed, err.Error(), duration), nil
	}

	// Filter to the allow-list. This guarantees only permitted, non-secret
	// settings are exposed even if the server adds new parameters.
	allowed := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		name, _ := row["name"].(string)
		if name != "" && isAllowedParam(name) {
			allowed = append(allowed, row)
		}
	}

	truncated := execResult.Truncated && len(rows) != len(allowed)
	return s.ok(ctx, toolGetConfigList, a.Target, operationRead, allowed, duration, truncated, len(allowed)), nil
}

// reloadDBConfig implements db_reload_config (spec §7.5).
//
// It issues SELECT pg_reload_conf(). The tool is registered as
// REQUIRES_APPROVAL, so the registry's policy engine denies it by default
// unless the target policy lists it in "require_approval" and a valid
// approval record is presented — i.e. it is "disabled by default".
func (s *Service) reloadDBConfig(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args, false)
	if err != nil {
		return s.fail(ctx, toolReloadConfig, "", operationMaintenance, mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	pool, err := s.resolvePool(a.Target)
	if err != nil {
		return s.fail(ctx, toolReloadConfig, a.Target, operationMaintenance, errCodeForErr(err), err.Error(), 0), nil
	}

	start := time.Now()
	execResult, err := s.runQuery(ctx, pool, reloadConfigSQL, nil, 1)
	duration := time.Since(start)
	if err != nil {
		return s.fail(ctx, toolReloadConfig, a.Target, operationMaintenance, errCodeForErr(err), err.Error(), duration), nil
	}

	// pg_reload_conf() returns a single boolean (true when the reload signal
	// was accepted). Normalize the response so callers get a stable shape.
	data := map[string]any{"reloaded": true}
	if execResult.RowsReturned > 0 && len(execResult.Rows[0]) > 0 {
		if b, ok := execResult.Rows[0][0].(bool); ok {
			data["reloaded"] = b
		}
	}
	return s.ok(ctx, toolReloadConfig, a.Target, operationMaintenance, data, duration, false, 1), nil
}

// q quotes a short string for safe inclusion in an error message.
func q(s string) string { return `"` + s + `"` }

// ---------------------------------------------------------------------------
// JSON Schemas
// ---------------------------------------------------------------------------

// schemaGetConfig is the JSON Schema for db_get_config.
func schemaGetConfig() json.RawMessage {
	return json.RawMessage(`{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"target": { "type": "string", "description": "Configured database target alias." },
			"name": { "type": "string", "description": "GUC parameter name to read (must be in the allow-list of non-secret settings)." }
		},
		"required": ["target", "name"],
		"additionalProperties": false
	}`)
}

// schemaGetConfigList is the JSON Schema for db_get_config_list.
func schemaGetConfigList() json.RawMessage {
	return json.RawMessage(`{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"target": { "type": "string", "description": "Configured database target alias." }
		},
		"required": ["target"],
		"additionalProperties": false
	}`)
}

// schemaReloadConfig is the JSON Schema for db_reload_config.
func schemaReloadConfig() json.RawMessage {
	return json.RawMessage(`{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"target": { "type": "string", "description": "Configured database target alias." }
		},
		"required": ["target"],
		"additionalProperties": false
	}`)
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

func registerGetConfig(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolGetConfig,
		Description: getConfigDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaGetConfig(),
		Handler:     adapt(s.getDBConfig),
	})
}

func registerGetConfigList(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolGetConfigList,
		Description: getConfigListDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaGetConfigList(),
		Handler:     adapt(s.getDBConfigList),
	})
}

func registerReloadConfig(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolReloadConfig,
		Description: reloadConfigDescription,
		Permission:  mcp.PermissionRequiresApproval,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaReloadConfig(),
		Handler:     adapt(s.reloadDBConfig),
	})
}

// allRegistrars lists the per-tool registration functions. Keeping them in a
// slice (rather than inline calls) gives a single place to enumerate the full
// set of configuration tools and makes the count trivially testable.
var allRegistrars = []func(reg *mcp.Registry, s *Service) error{
	registerGetConfig,
	registerGetConfigList,
	registerReloadConfig,
}

// Register registers all configuration ToolDefinitions with the registry.
// It returns the first error encountered so that the caller can stop
// registration cleanly.
func Register(reg *mcp.Registry, s *Service) error {
	for _, register := range allRegistrars {
		if err := register(reg, s); err != nil {
			return err
		}
	}
	return nil
}

// ToolCount returns the number of configuration tools this package registers.
// It is useful for tests and startup validation.
func ToolCount() int {
	return len(allRegistrars)
}
