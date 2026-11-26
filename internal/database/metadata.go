package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrFeatureUnavailable indicates a PostgreSQL feature (extension, view, etc.)
// is not available on the target server.
var ErrFeatureUnavailable = errors.New("db feature unavailable")

// quoteIdentifier returns a safely quoted SQL identifier.
// Internal double-quotes are escaped by doubling them.
// This implements PostgreSQL identifier quoting (spec §9.4).
func quoteIdentifier(name string) string {
	if name == "" {
		return `""`
	}
	// Double any internal quotes and wrap in double quotes.
	escaped := ""
	for _, c := range name {
		if c == '"' {
			escaped += `""`
		} else {
			escaped += string(c)
		}
	}
	return `"` + escaped + `"`
}

// qualifiedIdentifier builds a schema-qualified, identifier-safe name.
func qualifiedIdentifier(schema, table string) string {
	return quoteIdentifier(schema) + "." + quoteIdentifier(table)
}

// ---------------------------------------------------------------------------
// GetInfo
// ---------------------------------------------------------------------------

// ServerInfo holds basic server information.
type ServerInfo struct {
	Version   string `json:"version"`
	Name      string `json:"name"`
	StartTime string `json:"start_time"`
	Database  string `json:"database"`
}

// GetInfo returns basic server information: version, server name, and start time.
func GetInfo(ctx context.Context, pool *pgxpool.Pool) (*ServerInfo, error) {
	var info ServerInfo

	// Get server version.
	err := pool.QueryRow(ctx, "SELECT version()").Scan(&info.Version)
	if err != nil {
		return nil, fmt.Errorf("metadata: get version: %w", err)
	}

	// Get current database name.
	err = pool.QueryRow(ctx, "SELECT current_database()").Scan(&info.Database)
	if err != nil {
		return nil, fmt.Errorf("metadata: get database name: %w", err)
	}

	// Get server start time (requires read access to pg_stat_activity).
	err = pool.QueryRow(ctx, `
		SELECT MIN(backend_start) FROM pg_stat_activity
	`).Scan(&info.StartTime)
	if err != nil {
		// Not fatal; start time is best-effort.
		info.StartTime = "unknown"
	}

	// Get server name from pg_settings.
	err = pool.QueryRow(ctx, `
		SELECT setting FROM pg_settings WHERE name = 'server_version'
	`).Scan(&info.Name)
	if err != nil {
		info.Name = "unknown"
	}

	return &info, nil
}

// ---------------------------------------------------------------------------
// ListDatabases
// ---------------------------------------------------------------------------

// DatabaseInfo represents a database visible to the configured role.
type DatabaseInfo struct {
	Name      string `json:"name"`
	Encoding  string `json:"encoding"`
	Owner     string `json:"owner"`
	Size      string `json:"size"`
	IsCurrent bool   `json:"is_current"`
}

// ListDatabases returns all databases visible to the current role.
func ListDatabases(ctx context.Context, pool *pgxpool.Pool) ([]DatabaseInfo, error) {
	rows, err := pool.Query(ctx, `
		SELECT
			d.datname,
			pg_encoding_to_char(d.encoding),
			r.rolname AS owner,
			pg_size_pretty(pg_database_size(d.datname)) AS size,
			d.datname = current_database() AS is_current
		FROM pg_database d
		JOIN pg_roles r ON r.oid = d.datdba
		ORDER BY d.datname
	`)
	if err != nil {
		return nil, fmt.Errorf("metadata: list databases: %w", err)
	}
	defer rows.Close()

	var result []DatabaseInfo
	for rows.Next() {
		var d DatabaseInfo
		if err := rows.Scan(&d.Name, &d.Encoding, &d.Owner, &d.Size, &d.IsCurrent); err != nil {
			return nil, fmt.Errorf("metadata: scan database row: %w", err)
		}
		result = append(result, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("metadata: iterate databases: %w", err)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// ListSchemas
// ---------------------------------------------------------------------------

// SchemaInfo represents a schema in the current database.
type SchemaInfo struct {
	Name      string `json:"name"`
	Owner     string `json:"owner"`
	Namespace bool   `json:"is_system"`
}

// ListSchemas returns all schemas in the current database.
func ListSchemas(ctx context.Context, pool *pgxpool.Pool) ([]SchemaInfo, error) {
	rows, err := pool.Query(ctx, `
		SELECT
			n.nspname,
			COALESCE(r.rolname, '(unknown)') AS owner,
			n.nspname LIKE 'pg_%' AND n.nspname NOT LIKE 'pg_toast%' AS is_system
		FROM pg_namespace n
		LEFT JOIN pg_roles r ON r.oid = n.nspowner
		ORDER BY n.nspname
	`)
	if err != nil {
		return nil, fmt.Errorf("metadata: list schemas: %w", err)
	}
	defer rows.Close()

	var result []SchemaInfo
	for rows.Next() {
		var s SchemaInfo
		if err := rows.Scan(&s.Name, &s.Owner, &s.Namespace); err != nil {
			return nil, fmt.Errorf("metadata: scan schema row: %w", err)
		}
		result = append(result, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("metadata: iterate schemas: %w", err)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// ListTables
// ---------------------------------------------------------------------------

// TableInfo represents a table in a schema.
type TableInfo struct {
	Schema      string `json:"schema"`
	Name        string `json:"name"`
	Owner       string `json:"owner"`
	RowCount    int64  `json:"row_count_estimate"`
	HasIndexes  bool   `json:"has_indexes"`
	HasFKs      bool   `json:"has_foreign_keys"`
}

// ListTables returns all tables and views in the specified schema.
func ListTables(ctx context.Context, pool *pgxpool.Pool, schema string) ([]TableInfo, error) {
	// Use parameterized query for schema name via $1.
	// pg_get_userbyid requires an oid; we join pg_roles directly.
	query := `
		SELECT
			n.nspname,
			c.relname,
			COALESCE(r.rolname, '(unknown)') AS owner,
			COALESCE(c.reltuples::bigint, 0) AS row_estimate,
			EXISTS (SELECT 1 FROM pg_index WHERE pg_index.indrelid = c.oid) AS has_indexes,
			EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE pg_constraint.conrelid = c.oid
				  AND pg_constraint.contype = 'f'
			) AS has_fks
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_roles r ON r.oid = c.relowner
		WHERE n.nspname = $1
		  AND c.relkind IN ('r', 'v', 'm')  -- tables, views, materialized views
		ORDER BY c.relname
	`
	rows, err := pool.Query(ctx, query, schema)
	if err != nil {
		return nil, fmt.Errorf("metadata: list tables in %q: %w", schema, err)
	}
	defer rows.Close()

	var result []TableInfo
	for rows.Next() {
		var t TableInfo
		if err := rows.Scan(&t.Schema, &t.Name, &t.Owner, &t.RowCount, &t.HasIndexes, &t.HasFKs); err != nil {
			return nil, fmt.Errorf("metadata: scan table row: %w", err)
		}
		result = append(result, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("metadata: iterate tables: %w", err)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// DescribeTable
// ---------------------------------------------------------------------------

// ColumnInfo represents a column in a table.
type ColumnInfo struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Nullable     bool   `json:"nullable"`
	DefaultValue *string `json:"default_value,omitempty"`
	IsPrimaryKey bool   `json:"primary_key"`
}

// tableExists reports whether a user-defined table or view exists in the
// given schema. Used to surface a clear "table not found" error instead of
// returning an empty column list for a nonexistent table (DIS-I11).
func tableExists(ctx context.Context, pool *pgxpool.Pool, schema, table string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1
			  AND c.relname = $2
			  AND c.relkind IN ('r', 'v', 'm', 'p')
		)
	`, schema, table).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("metadata: check table existence for %s.%s: %w", schema, table, err)
	}
	return exists, nil
}

// DescribeTable returns column definitions and constraints for the specified table.
func DescribeTable(ctx context.Context, pool *pgxpool.Pool, schema, table string) ([]ColumnInfo, error) {
	// Verify the table exists so a missing table surfaces as an error rather
	// than an empty result set.
	exists, err := tableExists(ctx, pool, schema, table)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("metadata: table %s.%s not found", schema, table)
	}

	// Query column info using pg_catalog with identifier-safe names.
	// We use $1/$2 parameterization for the table name via a subquery approach,
	// but since pg_class.relname is a text column, we can parameterize it.
	query := `
		SELECT
			a.attname,
			format_type(a.atttypid, a.atttypmod),
			a.attnotnull = FALSE AS nullable,
			d.description AS default_value
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_description d
			ON d.objoid = a.attrelid
			AND d.objsubid = a.attnum
			AND d.classoid = 'pg_class'::regclass
		WHERE n.nspname = $1
		  AND c.relname = $2
		  AND a.attnum > 0
		  AND a.attisdropped = FALSE
		ORDER BY a.attnum
	`
	rows, err := pool.Query(ctx, query, schema, table)
	if err != nil {
		return nil, fmt.Errorf("metadata: describe table %s.%s: %w", schema, table, err)
	}
	defer rows.Close()

	var columns []ColumnInfo
	for rows.Next() {
		var col ColumnInfo
		var nullDefault *string
		if err := rows.Scan(&col.Name, &col.Type, &col.Nullable, &nullDefault); err != nil {
			return nil, fmt.Errorf("metadata: scan column: %w", err)
		}
		col.DefaultValue = nullDefault
		columns = append(columns, col)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("metadata: iterate columns: %w", err)
	}

	// Mark primary key columns.
	pkQuery := `
		SELECT a.attname
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE i.indisprimary
		  AND n.nspname = $1
		  AND c.relname = $2
	`
	pkRows, err := pool.Query(ctx, pkQuery, schema, table)
	if err != nil {
		// PK query failed but columns are already returned; log but don't fail.
		return columns, nil
	}
	defer pkRows.Close()

	pkSet := make(map[string]bool)
	for pkRows.Next() {
		var pkCol string
		if err := pkRows.Scan(&pkCol); err != nil {
			break
		}
		pkSet[pkCol] = true
	}

	for i := range columns {
		if pkSet[columns[i].Name] {
			columns[i].IsPrimaryKey = true
		}
	}

	return columns, nil
}

// ---------------------------------------------------------------------------
// ListIndexes
// ---------------------------------------------------------------------------

// IndexInfo represents an index on a table.
type IndexInfo struct {
	Name        string `json:"name"`
	Def         string `json:"definition"`
	IsUnique    bool   `json:"is_unique"`
	IsPrimary   bool   `json:"is_primary"`
	IsValid     bool   `json:"is_valid"`
	ScanCount   int64  `json:"scan_count"`
	IdxTupRead  int64  `json:"idx_tup_read"`
	IdxTupFetch int64  `json:"idx_tup_fetch"`
	Size        string `json:"size"`
}

// ListIndexes returns all indexes for the specified table.
func ListIndexes(ctx context.Context, pool *pgxpool.Pool, schema, table string) ([]IndexInfo, error) {
	query := `
		SELECT
			idx.relname,
			pg_get_indexdef(i.indexrelid),
			i.indisunique,
			i.indisprimary,
			i.indisvalid,
			COALESCE(s.idx_scan, 0) AS scan_count,
			COALESCE(s.idx_tup_read, 0) AS idx_tup_read,
			COALESCE(s.idx_tup_fetch, 0) AS idx_tup_fetch,
			pg_size_pretty(pg_relation_size(idx.oid)) AS size
		FROM pg_index i
		JOIN pg_class idx ON idx.oid = i.indexrelid
		JOIN pg_class tbl ON tbl.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = tbl.relnamespace
		LEFT JOIN pg_stat_user_indexes s
			ON s.indexrelid = idx.oid
			AND s.schemaname = n.nspname
		WHERE n.nspname = $1
		  AND tbl.relname = $2
		ORDER BY idx.relname
	`
	rows, err := pool.Query(ctx, query, schema, table)
	if err != nil {
		return nil, fmt.Errorf("metadata: list indexes for %s.%s: %w", schema, table, err)
	}
	defer rows.Close()

	var result []IndexInfo
	for rows.Next() {
		var idx IndexInfo
		if err := rows.Scan(&idx.Name, &idx.Def, &idx.IsUnique, &idx.IsPrimary, &idx.IsValid,
			&idx.ScanCount, &idx.IdxTupRead, &idx.IdxTupFetch, &idx.Size); err != nil {
			return nil, fmt.Errorf("metadata: scan index row: %w", err)
		}
		result = append(result, idx)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("metadata: iterate indexes: %w", err)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// ListConstraints
// ---------------------------------------------------------------------------

// ConstraintInfo represents a constraint on a table.
type ConstraintInfo struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Definition string `json:"definition"`
}

// ListConstraints returns all constraints (PK, FK, unique, check) for the specified table.
func ListConstraints(ctx context.Context, pool *pgxpool.Pool, schema, table string) ([]ConstraintInfo, error) {
	query := `
		SELECT
			con.conname,
			CASE con.contype
				WHEN 'p' THEN 'PRIMARY KEY'
				WHEN 'f' THEN 'FOREIGN KEY'
				WHEN 'u' THEN 'UNIQUE'
				WHEN 'c' THEN 'CHECK'
				ELSE con.contype
			END AS type,
			pg_get_constraintdef(con.oid) AS definition
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1
		  AND c.relname = $2
		ORDER BY con.conname
	`
	rows, err := pool.Query(ctx, query, schema, table)
	if err != nil {
		return nil, fmt.Errorf("metadata: list constraints for %s.%s: %w", schema, table, err)
	}
	defer rows.Close()

	var result []ConstraintInfo
	for rows.Next() {
		var c ConstraintInfo
		if err := rows.Scan(&c.Name, &c.Type, &c.Definition); err != nil {
			return nil, fmt.Errorf("metadata: scan constraint row: %w", err)
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("metadata: iterate constraints: %w", err)
	}
	return result, nil
}

// Note: GetTableSize, GetColumnStats, and CheckFeatureAvailable have been
// moved to metadata_sizing.go to keep this file under 600 lines.
