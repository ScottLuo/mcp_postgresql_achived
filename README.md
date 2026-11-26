# PostgreSQL DBA MCP Gateway

A Go-based MCP (Model Context Protocol) Server that exposes PostgreSQL database
administration capabilities to AI Agents through standardized, permission-gated Tools.

## Project Overview

```
AI Agent  ⇄  MCP Protocol  ⇄  PostgreSQL DBA MCP Server  ⇄  PostgreSQL
```

**Design Principles:**

- **Credential Isolation** — Database credentials are read from environment variables
  and never exposed to the AI Agent through tool parameters, responses, or logs.
- **Capability-based Tools** — Each database operation is a specific MCP Tool
  (e.g., `db_describe_table`, `db_get_activity`), not an unrestricted SQL interface.
- **Least Privilege** — Multi-target support with independent permission policies
  per target. Default access is read-only.
- **Separation of Diagnosis and Execution** — AI Agents diagnose freely;
  modifications require explicit authorization.

## Architecture

```
                   AI Agent
                      |
                      |
                 MCP Protocol
                      |
                      v
          +------------------------+
          |   PostgreSQL DBA MCP   |
          |         Server         |
          +------------------------+
                      |
          +-----------+------------+
          |           |            |
          v           v            v
      Tool Layer  Policy Engine  Audit Logger
          |           |            |
          +-----------+------------+
                      |
                      v
             Database Manager
                      |
          +-----------+------------+
          |                        |
          v                        v
     Connection Pool          Query Executor
          |                        |
          +-----------+------------+
                      |
                      v
                 PostgreSQL
```

## Configuration

All configuration is loaded from environment variables. No hardcoded values.

### Environment Variables

| Variable | Description | Default | Required |
|---|---|---|---|
| `MCP_TRANSPORT` | Transport mode: `stdio` or `http` | `stdio` | Yes |
| `POSTGRES_TARGETS` | Comma-separated target names | — | Yes |
| `POSTGRES_<TARGET>_DSN` | DSN for each target | — | Yes (per target) |
| `DB_POOL_MIN_CONNS` | Minimum pool connections | `2` | No |
| `DB_POOL_MAX_CONNS` | Maximum pool connections | `10` | No |
| `DB_QUERY_TIMEOUT` | Statement timeout | `10s` | No |
| `DB_MAX_ROWS` | Maximum returned rows | `1000` | No |
| `DB_MAX_RESULT_SIZE` | Maximum result size (bytes) | `1048576` | No |
| `MCP_LOG_LEVEL` | Log level: debug, info, warn, error | `info` | No |
| `MCP_AUDIT_ENABLED` | Enable audit logging | `true` | No |
| `DB_DEFAULT_TARGET` | Default target | — | No |
| `DB_DEFAULT_ACCESS` | Default access mode | `read_only` | No |

### Example Configuration

```bash
export MCP_TRANSPORT=stdio
export POSTGRES_TARGETS=development,production

export POSTGRES_DEVELOPMENT_DSN="postgres://dba_reader:password@localhost:5432/devdb?sslmode=disable"
export POSTGRES_PRODUCTION_DSN="postgres://dba_reader:password@prod-db:5432/prod?sslmode=verify-full"

export DB_POOL_MIN_CONNS=2
export DB_POOL_MAX_CONNS=10
export DB_QUERY_TIMEOUT=10s
export DB_MAX_ROWS=1000
export MCP_LOG_LEVEL=info
export MCP_AUDIT_ENABLED=true
export DB_DEFAULT_TARGET=development
export DB_DEFAULT_ACCESS=read_only
```

## Target & Policy Configuration

Targets are defined via environment variables. Each target has an independent
connection pool and permission policy.

The 3-level permission model:

| Level | Name | Behavior |
|---|---|---|
| 1 | `READ_ONLY` | Automatically executable within limits |
| 2 | `CONTROLLED_MAINTENANCE` | Allowed when policy + operation authorization permit |
| 3 | `REQUIRES_APPROVAL` | Requires explicit human approval (server-validated) |

Policy is configured in `configs/policy.yaml` (see [design doc §8.2](docs/design.md)).

## DBA Tools

### Discovery (9 tools)
| Tool | Description |
|---|---|
| `db_get_info` | Database version, uptime, server info |
| `db_list_databases` | List visible databases |
| `db_list_schemas` | List schemas |
| `db_list_tables` | List tables in a schema |
| `db_describe_table` | Table columns and constraints |
| `db_list_indexes` | Index information |
| `db_list_constraints` | PK, FK, unique, check constraints |
| `db_get_table_size` | Table and index size |
| `db_get_column_stats` | Column statistics |

### Monitoring (11 tools)
| Tool | Description |
|---|---|
| `db_get_activity` | Current database activity |
| `db_get_connections` | Connection statistics |
| `db_get_locks` | Lock information |
| `db_get_slow_queries` | Slow query statistics |
| `db_get_database_stats` | Database statistics |
| `db_get_table_stats` | Table access statistics |
| `db_get_index_stats` | Index usage statistics |
| `db_get_cache_stats` | Cache hit statistics |
| `db_get_replication_status` | Replication information |
| `db_get_replication_lag` | Replication lag |
| `db_get_deadlocks` | Deadlock statistics |

### Query Analysis (4 tools)
| Tool | Description |
|---|---|
| `db_query_readonly` | Execute read-only SQL |
| `db_explain_query` | Generate EXPLAIN plan (no execution) |
| `db_explain_analyze` | Execute EXPLAIN ANALYZE |
| `db_estimate_query_cost` | Estimate query plan cost |

### Maintenance (7 tools)
| Tool | Description |
|---|---|
| `db_vacuum_table` | VACUUM a table |
| `db_analyze_table` | ANALYZE a table |
| `db_reindex_table` | REINDEX a table |
| `db_create_index` | Create an index |
| `db_drop_index` | Drop an index |
| `db_cancel_query` | Cancel a running query |
| `db_terminate_connection` | Terminate a backend connection |

### Configuration (3 tools)
| Tool | Description |
|---|---|
| `db_get_config` | Read a configuration parameter |
| `db_get_config_list` | List permitted configuration parameters |
| `db_reload_config` | Reload configuration |

## Permission Model & Approval Workflow

1. **READ_ONLY** tools execute automatically within configured limits.
2. **CONTROLLED_MAINTENANCE** tools require the target policy to allow them.
3. **REQUIRES_APPROVAL** tools require a server-validated approval record that binds:
   - Target, Tool, exact operation parameters or normalized SQL
   - Expiration time, approver identity, approval ID

Changing operation parameters after approval invalidates the approval.

## Audit & Metrics

- **Audit** — Every tool invocation is logged with timestamp, request ID, tool name,
  target, operation category, duration, success/failure, error code, and approval
  reference. Sensitive parameters and DSNs are never logged.
- **Metrics** — Prometheus metrics exposed for operational monitoring:
  `mcp_tool_calls_total`, `mcp_tool_errors_total`, `mcp_tool_duration_seconds`,
  `db_pool_connections`, `db_query_duration_seconds`, `db_query_timeout_total`,
  `db_target_health`, `db_approval_requests_total`.

## Transport

- **stdio** (default) — Local AI Agent integration. The server runs as a child process.
  Protocol messages on stdout, logs on stderr.
- **http** (Streamable HTTP) — Remote or multi-Agent deployment. Requires independent
  MCP client authentication (TLS, OAuth 2.0, rate limiting).

## Build & Run

### Prerequisites
- Go 1.20+
- PostgreSQL 14+ (for integration)

### Build

```bash
make build          # Build for linux/amd64 (CGO_ENABLED=1, required by pg_query_go)
# Or manually:
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -o build/postgres-dba-mcp ./cmd/server/
```

### Run

```bash
# Set required environment variables
export POSTGRES_TARGETS=development
export POSTGRES_DEVELOPMENT_DSN="postgres://dba_reader:password@localhost:5432/devdb?sslmode=disable"

# Run the server (stdio transport)
./build/postgres-dba-mcp
```

### Makefile Targets

| Target | Description |
|---|---|
| `make build` | Cross-compile for linux/amd64 |
| `make test` | Run unit tests (`go test -tags=unit ./...`) |
| `make test-integration` | Run integration tests (testcontainers) |
| `make lint` | Run golangci-lint |
| `make package` | Create tar.gz release (binary + tool.yaml) |
| `make docker` | Build Docker image |

### Docker

```bash
docker build -t postgres-dba-mcp .
docker run -i --env-file .env postgres-dba-mcp
```

## CI/CD Packaging

On push to `main`, the CI pipeline:

1. Cross-compiles for `linux/amd64`:
   ```bash
   CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -o build/postgres-dba-mcp ./cmd/server/
   ```
2. Creates a `tar.gz` containing:
   - `postgres-dba-mcp` (executable binary, `chmod +x`)
   - `tool.yaml` (runtime contract)
3. Publishes as a GitHub Release artifact.

## DBA Diagnosis Workflow Example

The AI Agent combines atomic tools into higher-level workflows:

```
User: "Investigate why the database is slow."

Agent workflow:
  1. db_get_info            → Server version, uptime
  2. db_get_database_stats  → Commit/rollback counts, I/O
  3. db_get_connections     → Connection count vs max
  4. db_get_activity        → Currently running queries
  5. db_get_locks           → Lock contention
  6. db_get_slow_queries    → Top slow queries
  7. Identify suspicious SQL
  8. db_explain_query       → Execution plan analysis
  9. db_get_index_stats     → Index usage
 10. Generate structured diagnosis report
 11. Recommend corrective actions
 12. Request approval for maintenance
 13. Execute approved operations
 14. Verify results
```

The Agent produces a structured report distinguishing observed facts from
AI-generated hypotheses, with risk assessment and execution status.

## References

- [Technical Design & Development Specification](docs/design.md) — the complete
  design of record (spec §1–§21)
- [PROJECT_CONSTRAINTS.md](PROJECT_CONSTRAINTS.md) — project constraints and
  packaging requirements
