//go:build tools

// Package tools pins project dependencies in go.mod so that later
// implementation tasks can build against stable, pre-resolved versions.
package tools

import (
	_ "github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/knadh/koanf/v2"
	_ "github.com/pganalyze/pg_query_go/v5"
	_ "github.com/prometheus/client_golang/prometheus"
)
