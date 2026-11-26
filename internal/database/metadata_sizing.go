package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// GetTableSize
// ---------------------------------------------------------------------------

// TableSizeInfo represents the disk usage of a table and its indexes.
type TableSizeInfo struct {
	Schema    string `json:"schema"`
	Table     string `json:"table"`
	TotalSize string `json:"total_size"`
	TableSize string `json:"table_size"`
	IndexSize string `json:"index_size"`
	ToastSize string `json:"toast_size"`
}

// GetTableSize returns the disk size breakdown for a table.
func GetTableSize(ctx context.Context, pool *pgxpool.Pool, schema, table string) (*TableSizeInfo, error) {
	var result TableSizeInfo
	result.Schema = schema
	result.Table = table

	// Resolve the table to a regclass once. format('%I.%I', $1, $2) produces
	// a properly-quoted, context-appropriate name as a *text* value which is
	// then cast to regclass. The explicit ::text cast on the parameters is
	// required because format's arguments are undetermined-type otherwise,
	// and casting a text value is unambiguous (unlike casting a raw
	// "schema"."table" identifier expression, where "::" binds tighter than ".").
	query := `
		SELECT
			pg_size_pretty(pg_total_relation_size(rc)),
			pg_size_pretty(pg_relation_size(rc)),
			pg_size_pretty(pg_indexes_size(rc)),
			pg_size_pretty(
				pg_total_relation_size(rc)
				- pg_relation_size(rc)
				- pg_indexes_size(rc)
			)
		FROM (SELECT format('%I.%I', $1::text, $2::text)::regclass AS rc) AS t
	`
	err := pool.QueryRow(ctx, query, schema, table).Scan(
		&result.TotalSize, &result.TableSize, &result.IndexSize, &result.ToastSize,
	)
	if err != nil {
		return nil, fmt.Errorf("metadata: get table size for %s.%s: %w", schema, table, err)
	}
	return &result, nil
}

// ---------------------------------------------------------------------------
// GetColumnStats
// ---------------------------------------------------------------------------

// ColumnStats represents basic statistics for a column.
type ColumnStats struct {
	Column         string  `json:"column"`
	NumNull        int64   `json:"num_null"`
	NumDistinct    int64   `json:"num_distinct"`
	MostCommonVal  string  `json:"most_common_value,omitempty"`
	MostCommonFreq float64 `json:"most_common_freq,omitempty"`
}

// GetColumnStats returns column statistics from pg_statistic.
// Returns ErrFeatureUnavailable if the pg_statistic view has no rows
// (e.g., ANALYZE has never been run on the table).
func GetColumnStats(ctx context.Context, pool *pgxpool.Pool, schema, table string) ([]ColumnStats, error) {
	// Check if statistics exist for this table.
	var count int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM pg_statistic s
		JOIN pg_class c ON c.oid = s.starelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2
	`, schema, table).Scan(&count)
	if err != nil {
		return nil, fmt.Errorf("metadata: check stats for %s.%s: %w", schema, table, err)
	}
	if count == 0 {
		return nil, fmt.Errorf("%w: no column statistics available for %s.%s (run ANALYZE)",
			ErrFeatureUnavailable, schema, table)
	}

	simpleQuery := `
		SELECT
			a.attname,
			0::bigint AS num_null,
			COALESCE(s.stadistinct, 0)::bigint AS num_distinct,
			CASE
				WHEN s.stavalues1 IS NOT NULL
				THEN split_part(array_to_string(s.stavalues1, ','), ',', 1)
				ELSE ''
			END AS mcv,
			CASE
				WHEN s.stanumbers1 IS NOT NULL THEN s.stanumbers1[1]::float8
				ELSE 0
			END AS mcf
		FROM pg_statistic s
		JOIN pg_class c ON c.oid = s.starelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = s.starelid AND a.attnum = s.staattnum
		WHERE n.nspname = $1 AND c.relname = $2
		ORDER BY a.attname
	`
	rows, err := pool.Query(ctx, simpleQuery, schema, table)
	if err != nil {
		return nil, fmt.Errorf("metadata: get column stats for %s.%s: %w", schema, table, err)
	}
	defer rows.Close()

	var result []ColumnStats
	for rows.Next() {
		var st ColumnStats
		if err := rows.Scan(&st.Column, &st.NumNull, &st.NumDistinct, &st.MostCommonVal, &st.MostCommonFreq); err != nil {
			return nil, fmt.Errorf("metadata: scan column stats for %s.%s: %w", schema, table, err)
		}
		result = append(result, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("metadata: iterate column stats: %w", err)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// CheckFeatureAvailable
// ---------------------------------------------------------------------------

// CheckFeatureAvailable checks whether a specific PostgreSQL extension
// (e.g., pg_stat_statements) is installed.
// Returns true if the extension is available, false otherwise.
func CheckFeatureAvailable(ctx context.Context, pool *pgxpool.Pool, extensionName string) bool {
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = $1)`,
		extensionName,
	).Scan(&exists)
	return err == nil && exists
}
