package monitoring

import (
	"context"
	"encoding/json"
	"fmt"

	"postgres-dba-mcp/internal/mcp"
)

// Tool names for the statistics tools (spec §7.2).
const (
	toolGetSlowQueries    = "db_get_slow_queries"
	toolGetDatabaseStats  = "db_get_database_stats"
	toolGetTableStats     = "db_get_table_stats"
	toolGetIndexStats     = "db_get_index_stats"
	toolGetCacheStats     = "db_get_cache_stats"
	toolGetDeadlocks      = "db_get_deadlocks"
)

// pg_stat_statements extension name.
const extPGStatStatements = "pg_stat_statements"

// ---------------------------------------------------------------------------
// db_get_slow_queries
// ---------------------------------------------------------------------------

// slowQueriesSQL returns the most expensive statements, gated on the
// pg_stat_statements extension.
const slowQueriesSQL = `
SELECT
    COALESCE(round(total_exec_time / NULLIF(calls, 0), 3), 0) AS mean_time_ms,
    round(total_exec_time, 3)                                 AS total_time_ms,
    calls,
    rows,
    left(query, 2000)                                         AS query,
    (SELECT datname FROM pg_database WHERE oid = dbid)        AS database
FROM pg_stat_statements
WHERE query NOT ILIKE '%pg_stat_statements%'
ORDER BY total_time_ms DESC
LIMIT $1`

// getSlowQueries implements db_get_slow_queries (spec §7.2). It is gated on
// the pg_stat_statements extension; when absent it returns DB_FEATURE_UNAVAILABLE
// rather than a raw database error.
func (s *Service) getSlowQueries(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args)
	if err != nil {
		return s.fail(ctx, toolGetSlowQueries, "", mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	pool, err := s.resolvePool(a.Target)
	if err != nil {
		return s.fail(ctx, toolGetSlowQueries, a.Target, errCodeForErr(err), err.Error(), 0), nil
	}

	if !s.featureAvailable(ctx, pool, extPGStatStatements) {
		return s.fail(ctx, toolGetSlowQueries, a.Target, mcp.ErrCodeFeatureUnavailable,
			"pg_stat_statements extension is not installed on the target; slow-query statistics are unavailable", 0), nil
	}

	out, err := s.runQuery(ctx, pool, slowQueriesSQL, []any{a.Limit}, a.Limit)
	if err != nil {
		return s.fail(ctx, toolGetSlowQueries, a.Target, errCodeForErr(err), err.Error(), out.duration), nil
	}

	return s.ok(ctx, toolGetSlowQueries, a.Target, out.rows, out.duration, out.execResult.Truncated, out.execResult.RowsReturned), nil
}

// schemaGetSlowQueries is the JSON Schema for db_get_slow_queries.
func schemaGetSlowQueries() json.RawMessage {
	return json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"target":{"type":"string","description":"Configured database target alias"},"limit":{"type":"integer","minimum":1,"description":"Maximum number of statements to return (optional)"}},"required":["target"],"additionalProperties":false}`)
}

const slowQueriesDescription = "Return the most expensive statements from pg_stat_statements (requires the pg_stat_statements extension). When the extension is not installed the tool reports DB_FEATURE_UNAVAILABLE. READ-ONLY."

func registerSlowQueries(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolGetSlowQueries,
		Description: slowQueriesDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaGetSlowQueries(),
		Handler:     adapt(s.getSlowQueries),
	})
}

// ---------------------------------------------------------------------------
// db_get_database_stats
// ---------------------------------------------------------------------------

// databaseStatsSQL returns per-database statistics from pg_stat_database.
const databaseStatsSQL = `
SELECT
    d.datname,
    s.xact_commit,
    s.xact_rollback,
    s.blks_read,
    s.blks_hit,
    s.tup_returned,
    s.tup_fetched,
    s.tup_inserted,
    s.tup_updated,
    s.tup_deleted,
    s.temp_files,
    s.deadlocks,
    s.numbackends
FROM pg_stat_database s
JOIN pg_database d ON d.oid = s.datid
ORDER BY d.datname`

// getDatabaseStats implements db_get_database_stats (spec §7.2).
func (s *Service) getDatabaseStats(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args)
	if err != nil {
		return s.fail(ctx, toolGetDatabaseStats, "", mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	pool, err := s.resolvePool(a.Target)
	if err != nil {
		return s.fail(ctx, toolGetDatabaseStats, a.Target, errCodeForErr(err), err.Error(), 0), nil
	}

	out, err := s.runQuery(ctx, pool, databaseStatsSQL, nil, a.Limit)
	if err != nil {
		return s.fail(ctx, toolGetDatabaseStats, a.Target, errCodeForErr(err), err.Error(), out.duration), nil
	}

	return s.ok(ctx, toolGetDatabaseStats, a.Target, out.rows, out.duration, out.execResult.Truncated, out.execResult.RowsReturned), nil
}

func schemaGetDatabaseStats() json.RawMessage {
	return json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"target":{"type":"string","description":"Configured database target alias"}},"required":["target"],"additionalProperties":false}`)
}

const databaseStatsDescription = "Return per-database statistics from pg_stat_database: transaction counts, block reads/hits, tuple counts, temp files, deadlocks and current backends. READ-ONLY."

func registerDatabaseStats(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolGetDatabaseStats,
		Description: databaseStatsDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaGetDatabaseStats(),
		Handler:     adapt(s.getDatabaseStats),
	})
}

// ---------------------------------------------------------------------------
// db_get_table_stats
// ---------------------------------------------------------------------------

// tableStatsSQL returns access statistics for a single table. The schema and
// table are identifier-quoted at query construction time.
const tableStatsSQL = `
SELECT
    n.nspname  AS schema,
    c.relname  AS table,
    s.seq_scan,
    s.seq_tup_read,
    s.idx_scan,
    s.n_tup_ins,
    s.n_tup_upd,
    s.n_tup_del,
    s.n_tup_hot_upd,
    s.n_live_tup,
    s.n_dead_tup,
    s.n_mod_since_analyze,
    s.last_vacuum,
    s.last_autovacuum,
    s.last_analyze,
    s.last_autoanalyze
FROM pg_stat_user_tables s
JOIN pg_class c ON c.oid = s.relid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`

// validateSchemaTable ensures schema and table are both provided.
func validateSchemaTable(a *baseArgs) error {
	if a.Schema == "" {
		return fmt.Errorf("%w: missing required argument %q", errInvalidArgs, "schema")
	}
	if a.Table == "" {
		return fmt.Errorf("%w: missing required argument %q", errInvalidArgs, "table")
	}
	return nil
}

// getTableStats implements db_get_table_stats (spec §7.2).
func (s *Service) getTableStats(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args)
	if err != nil {
		return s.fail(ctx, toolGetTableStats, "", mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}
	if err := validateSchemaTable(a); err != nil {
		return s.fail(ctx, toolGetTableStats, a.Target, mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	pool, err := s.resolvePool(a.Target)
	if err != nil {
		return s.fail(ctx, toolGetTableStats, a.Target, errCodeForErr(err), err.Error(), 0), nil
	}

	out, err := s.runQuery(ctx, pool, tableStatsSQL, []any{a.Schema, a.Table}, 1)
	if err != nil {
		return s.fail(ctx, toolGetTableStats, a.Target, errCodeForErr(err), err.Error(), out.duration), nil
	}

	return s.ok(ctx, toolGetTableStats, a.Target, out.rows, out.duration, false, out.execResult.RowsReturned), nil
}

func schemaGetTableStats() json.RawMessage {
	return json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"target":{"type":"string"},"schema":{"type":"string","description":"Schema name"},"table":{"type":"string","description":"Table name"}},"required":["target","schema","table"],"additionalProperties":false}`)
}

const tableStatsDescription = "Return access statistics for a specific table from pg_stat_user_tables: sequential/index scans, tuple inserts/updates/deletes, live/dead tuple counts and last vacuum/analyze times. READ-ONLY."

func registerTableStats(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolGetTableStats,
		Description: tableStatsDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaGetTableStats(),
		Handler:     adapt(s.getTableStats),
	})
}

// ---------------------------------------------------------------------------
// db_get_index_stats
// ---------------------------------------------------------------------------

// indexStatsSQL returns index usage statistics for all indexes on a table.
const indexStatsSQL = `
SELECT
    n.nspname  AS schema,
    c.relname  AS table,
    i.relname  AS index_name,
    s.idx_scan,
    s.idx_tup_read,
    s.idx_tup_fetch,
    pg_relation_size(i.oid)::bigint AS index_size
FROM pg_stat_user_indexes s
JOIN pg_index ix ON ix.indexrelid = s.indexrelid
JOIN pg_class c ON c.oid = ix.indrelid
JOIN pg_class i ON i.oid = s.indexrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`

// getIndexStats implements db_get_index_stats (spec §7.2).
func (s *Service) getIndexStats(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args)
	if err != nil {
		return s.fail(ctx, toolGetIndexStats, "", mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}
	if err := validateSchemaTable(a); err != nil {
		return s.fail(ctx, toolGetIndexStats, a.Target, mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	pool, err := s.resolvePool(a.Target)
	if err != nil {
		return s.fail(ctx, toolGetIndexStats, a.Target, errCodeForErr(err), err.Error(), 0), nil
	}

	out, err := s.runQuery(ctx, pool, indexStatsSQL, []any{a.Schema, a.Table}, a.Limit)
	if err != nil {
		return s.fail(ctx, toolGetIndexStats, a.Target, errCodeForErr(err), err.Error(), out.duration), nil
	}

	return s.ok(ctx, toolGetIndexStats, a.Target, out.rows, out.duration, out.execResult.Truncated, out.execResult.RowsReturned), nil
}

func schemaGetIndexStats() json.RawMessage {
	return json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"target":{"type":"string"},"schema":{"type":"string","description":"Schema name"},"table":{"type":"string","description":"Table name"},"limit":{"type":"integer","minimum":1,"description":"Maximum number of index rows (optional)"}},"required":["target","schema","table"],"additionalProperties":false}`)
}

const indexStatsDescription = "Return index usage statistics for all indexes on a given table from pg_stat_user_indexes: scans, tuples read/fetched and index size. READ-ONLY."

func registerIndexStats(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolGetIndexStats,
		Description: indexStatsDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaGetIndexStats(),
		Handler:     adapt(s.getIndexStats),
	})
}

// ---------------------------------------------------------------------------
// db_get_cache_stats
// ---------------------------------------------------------------------------

// cacheStatsSQL computes the block cache hit ratio for the current database.
const cacheStatsSQL = `
SELECT
    s.datname,
    s.blks_hit,
    s.blks_read,
    CASE
        WHEN (s.blks_hit + s.blks_read) = 0 THEN NULL
        ELSE round((s.blks_hit::float8 / NULLIF(s.blks_hit + s.blks_read, 0))::numeric, 4)
    END AS cache_hit_ratio
FROM pg_stat_database s
JOIN pg_database d ON d.oid = s.datid
WHERE s.datid = d.oid`

// getCacheStats implements db_get_cache_stats (spec §7.2).
func (s *Service) getCacheStats(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args)
	if err != nil {
		return s.fail(ctx, toolGetCacheStats, "", mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	pool, err := s.resolvePool(a.Target)
	if err != nil {
		return s.fail(ctx, toolGetCacheStats, a.Target, errCodeForErr(err), err.Error(), 0), nil
	}

	out, err := s.runQuery(ctx, pool, cacheStatsSQL, nil, 1)
	if err != nil {
		return s.fail(ctx, toolGetCacheStats, a.Target, errCodeForErr(err), err.Error(), out.duration), nil
	}

	return s.ok(ctx, toolGetCacheStats, a.Target, out.rows, out.duration, false, out.execResult.RowsReturned), nil
}

func schemaGetCacheStats() json.RawMessage {
	return json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"target":{"type":"string","description":"Configured database target alias"}},"required":["target"],"additionalProperties":false}`)
}

const cacheStatsDescription = "Return the block cache hit ratio (blks_hit / (blks_hit + blks_read)) for the current database from pg_stat_database. READ-ONLY."

func registerCacheStats(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolGetCacheStats,
		Description: cacheStatsDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaGetCacheStats(),
		Handler:     adapt(s.getCacheStats),
	})
}

// ---------------------------------------------------------------------------
// db_get_deadlocks
// ---------------------------------------------------------------------------

// deadlocksSQL returns the cumulative deadlock count for the current database.
const deadlocksSQL = `
SELECT
    d.datname,
    s.deadlocks,
    s.numbackends
FROM pg_stat_database s
JOIN pg_database d ON d.oid = s.datid
WHERE s.datid = d.oid`

// getDeadlocks implements db_get_deadlocks (spec §7.2).
func (s *Service) getDeadlocks(ctx context.Context, args json.RawMessage) (*mcp.ToolResult, error) {
	a, err := s.parseArgs(args)
	if err != nil {
		return s.fail(ctx, toolGetDeadlocks, "", mcp.ErrCodeSQLInvalid, err.Error(), 0), nil
	}

	pool, err := s.resolvePool(a.Target)
	if err != nil {
		return s.fail(ctx, toolGetDeadlocks, a.Target, errCodeForErr(err), err.Error(), 0), nil
	}

	out, err := s.runQuery(ctx, pool, deadlocksSQL, nil, 1)
	if err != nil {
		return s.fail(ctx, toolGetDeadlocks, a.Target, errCodeForErr(err), err.Error(), out.duration), nil
	}

	return s.ok(ctx, toolGetDeadlocks, a.Target, out.rows, out.duration, false, out.execResult.RowsReturned), nil
}

func schemaGetDeadlocks() json.RawMessage {
	return json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"target":{"type":"string","description":"Configured database target alias"}},"required":["target"],"additionalProperties":false}`)
}

const deadlocksDescription = "Return the cumulative deadlock count for the current database from pg_stat_database.deadlocks. READ-ONLY."

func registerDeadlocks(reg *mcp.Registry, s *Service) error {
	return reg.Register(&mcp.ToolDefinition{
		Name:        toolGetDeadlocks,
		Description: deadlocksDescription,
		Permission:  mcp.PermissionReadOnly,
		Timeout:     s.cfg.QueryLimits.Timeout,
		InputSchema: schemaGetDeadlocks(),
		Handler:     adapt(s.getDeadlocks),
	})
}
