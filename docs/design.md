# PostgreSQL DBA MCP Gateway — Technical Design & Development Specification

**Version:** 1.0  
**Status:** Design of Record  
**Language:** Go  
**Protocol:** Model Context Protocol (MCP)  
**Database:** PostgreSQL

---

## §1 Project Overview

### 1.1 Purpose

PostgreSQL DBA MCP Gateway is a Go-based MCP Server that exposes PostgreSQL database
administration capabilities to AI Agents through standardized MCP Tools.

The system provides a controlled interface between AI Agents and PostgreSQL databases.
The MCP Server is responsible for:

- PostgreSQL connection management
- Database authentication
- Database authorization
- SQL execution
- Database metadata discovery
- Performance monitoring
- Query analysis
- Database maintenance
- Security policy enforcement
- Operation auditing

AI Agents interact exclusively with the MCP interface.
AI Agents must never receive database connection strings, database passwords, or other
database authentication credentials.

### 1.2 Design Principles

**Principle 1: Credential Isolation**  
Database connection credentials are obtained from environment variables or an external
secrets provider. Credentials remain inside the MCP Server process. They must never appear
in MCP Tool parameters, responses, Resources, AI prompts, application logs, or error messages.

**Principle 2: Pre-established Database Connections**  
The MCP Server establishes PostgreSQL connection pools during startup. Database connection
creation and authentication are handled internally. AI Agents only invoke database
operations through registered MCP Tools.

**Principle 3: Capability-based Tool Design**  
Expose database operations through specific Tools instead of exposing unrestricted database
access (e.g., `db_describe_table`, `db_get_activity`, `db_explain_query`, `db_vacuum_table`).
Avoid exposing a universal unrestricted SQL execution interface.

**Principle 4: Least Privilege**  
Database access must follow the principle of least privilege. Different database targets
and operation categories should have independent permission policies.

**Principle 5: Separation of Diagnosis and Execution**  
AI Agents may automatically investigate database conditions and generate recommendations.
Operations that modify database structures, data, or configuration must follow explicit
authorization policies.

**Principle 6: Extensibility**  
The architecture should allow future support for multiple PostgreSQL instances, multiple
database versions, additional database engines, custom DBA Tools, external approval
systems, Prometheus monitoring, and scheduled DBA tasks.

---

## §2 System Architecture

### 2.1 High-level Architecture

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

### 2.2 Logical Components

**MCP Server** — Implements MCP protocol, registers Tools/Resources/Prompts, handles
MCP requests, validates Tool parameters, returns structured results.

**Tool Registry** — Maintains all available DBA Tools. Each Tool defines: name,
description, input schema, required permission, execution handler, timeout policy,
output schema.

**Database Manager** — Loads database configurations, establishes connection pools,
maintains database connections, performs health checking, manages connection lifecycle,
supports multiple database targets.

**Query Executor** — Provides a unified database execution interface: executes
parameterized SQL, applies statement timeouts, enforces row limits, handles transactions
(including read-only), sanitizes errors, measures execution time.

**Policy Engine** — Controls which operations can be executed: tool permission checking,
target-level access control, SQL operation validation, destructive operation protection,
approval verification, resource consumption limits.

**Audit Logger** — Records all Tool executions with: timestamp, tool name, target
database, operation category, execution duration, success/failure, error code, and
approval reference (when applicable). Never logs database passwords or sensitive
connection information.

---

## §3 Technology Stack

| Component | Technology |
|---|---|
| Programming Language | Go 1.20+ |
| MCP SDK | `github.com/modelcontextprotocol/go-sdk` v1.8.0 |
| PostgreSQL Driver | `github.com/jackc/pgx/v5` v5.13.0 |
| Connection Pool | `pgxpool` (included in pgx/v5) |
| SQL Parser | `github.com/pganalyze/pg_query_go/v5` v5.1.0 |
| Configuration | `github.com/knadh/koanf/v2` v2.4.1 |
| Logging | `log/slog` (standard library) |
| Metrics | `github.com/prometheus/client_golang` v1.25.0 |
| Testing | `testing` + `testcontainers-go` |
| Container | Docker |

All dependencies use stable released versions and are pinned in `go.mod`.

---

## §4 Project Structure

```
postgres-dba-mcp/
├── cmd/
│   └── server/
│       └── main.go              # Entrypoint
├── internal/
│   ├── config/
│   │   ├── config.go            # Config struct + Load()
│   │   ├── env.go               # ENV helpers, validation
│   │   └── targets.go           # Target parsing, DSN resolution
│   ├── database/
│   │   ├── manager.go           # DatabaseManager interface + impl
│   │   ├── pool.go              # pgxpool construction
│   │   ├── executor.go          # Unified SQL executor
│   │   ├── transaction.go       # Read-only txn helper
│   │   ├── metadata.go          # Catalog queries
│   │   └── health.go            # Health check
│   ├── mcp/
│   │   ├── server.go            # MCP SDK wiring
│   │   ├── registry.go          # Tool registry
│   │   ├── response.go          # ToolResult, error codes
│   │   ├── resources.go         # MCP resources
│   │   └── prompts.go           # MCP prompts
│   ├── tools/
│   │   ├── discovery/           # Database discovery tools
│   │   ├── monitoring/          # Monitoring tools
│   │   ├── query/               # Query analysis tools
│   │   ├── maintenance/         # Maintenance tools
│   │   └── configuration/       # Configuration tools
│   ├── security/
│   │   ├── policy.go            # Policy engine
│   │   ├── permission.go        # Permission levels
│   │   ├── sql_validator.go     # AST validation
│   │   ├── approval.go          # Approval binding
│   │   └── sanitizer.go         # Error sanitization
│   ├── audit/
│   │   ├── logger.go            # Audit logger
│   │   └── models.go            # Audit record types
│   └── metrics/
│       └── metrics.go           # Prometheus collectors
├── configs/
│   ├── tools.yaml               # Tool metadata
│   └── policy.yaml              # Permission policies
├── deployments/
│   └── docker-compose.yaml
├── docs/
│   └── design.md                # This file
├── scripts/
├── tests/
│   ├── integration/
│   └── security/
├── tools.go                     # Dependency pinning
├── Dockerfile
├── Makefile
├── go.mod
├── go.sum
└── README.md
```

---

## §5 Configuration Management

### 5.1 Environment Variables

All configuration is loaded from environment variables. No hardcoded configuration.

**Transport:**

| Variable | Description | Default | Required |
|---|---|---|---|
| `MCP_TRANSPORT` | Transport mode: `stdio` or `http` | `stdio` | Yes |

**Database Targets:**

| Variable | Description | Required |
|---|---|---|
| `POSTGRES_TARGETS` | Comma-separated list of target names | Yes |
| `POSTGRES_<TARGET>_DSN` | DSN for each target (e.g., `POSTGRES_DEVELOPMENT_DSN`) | Yes (per target) |

**Connection Pool:**

| Variable | Description | Default |
|---|---|---|
| `DB_POOL_MIN_CONNS` | Minimum pool connections | `2` |
| `DB_POOL_MAX_CONNS` | Maximum pool connections | `10` |

**Query Limits:**

| Variable | Description | Default |
|---|---|---|
| `DB_QUERY_TIMEOUT` | Statement timeout duration | `10s` |
| `DB_MAX_ROWS` | Maximum returned rows | `1000` |
| `DB_MAX_RESULT_SIZE` | Maximum result size in bytes | `1048576` (1 MB) |

**Logging & Audit:**

| Variable | Description | Default |
|---|---|---|
| `MCP_LOG_LEVEL` | Log level: debug, info, warn, error | `info` |
| `MCP_AUDIT_ENABLED` | Enable audit logging | `true` |

**Defaults:**

| Variable | Description | Default |
|---|---|---|
| `DB_DEFAULT_TARGET` | Default target when not specified | — |
| `DB_DEFAULT_ACCESS` | Default access mode: `read_only`, `read_write` | `read_only` |

Production deployments should use a secret manager or container secrets rather than
plaintext environment files.

### 5.2 Target Configuration

Each target represents a preconfigured PostgreSQL connection. The AI Agent selects a
configured target by its alias. It cannot supply hostname, port, username, password, DSN,
or arbitrary connection parameters. The server resolves target aliases internally.

### 5.3 Startup Validation

During startup:
1. Load environment variables
2. Validate required configuration
3. Validate target definitions
4. Initialize connection pools
5. Ping PostgreSQL
6. Verify database permissions
7. Register MCP Tools
8. Start MCP Server

If a required target fails initialization, startup behavior follows an explicit
configuration policy. Production can require all targets healthy; development can allow
optional targets to remain unavailable.

---

## §6 Database Connection Management

### 6.1 Connection Pool

Use `pgxpool`. Each target has an independent connection pool.

```go
type DatabaseTarget struct {
    Name string
    Pool *pgxpool.Pool
}

type DatabaseManager interface {
    GetPool(target string) (*pgxpool.Pool, error)
    ListTargets() []string
    HealthCheck(ctx context.Context) error
    Close()
}
```

### 6.2 Connection Pool Configuration

| Setting | Default |
|---|---:|
| Min Connections | 2 |
| Max Connections | 10 |
| Max Connection Lifetime | 1 hour |
| Max Idle Time | 15 minutes |
| Query Timeout | 10 seconds |

All values must be configurable via environment variables.

### 6.3 Database Roles

| Role | Permissions |
|---|---|
| `dba_reader` | Read-only access |
| `dba_monitor` | Monitoring and permitted statistics |
| `dba_maintenance` | Approved maintenance operations |
| `dba_admin` | Restricted administrative operations |

Do not use a PostgreSQL superuser as the default MCP connection identity. Prefer separate
connection pools for different privilege levels. The default pool should be read-only.

---

## §7 MCP Tool Design

Tools are divided into five functional groups.

### 7.1 Database Discovery Tools

Purpose: Allow AI Agents to understand database structure.

| Tool | Parameters | Description |
|---|---|---|
| `db_get_info` | target | Database version, uptime, server information |
| `db_list_databases` | target | List databases visible to the configured role |
| `db_list_schemas` | target | List schemas |
| `db_list_tables` | target, schema | List tables |
| `db_describe_table` | target, schema, table | Table columns and constraints |
| `db_list_indexes` | target, schema, table | Index information |
| `db_list_constraints` | target, schema, table | PK, FK, unique, check constraints |
| `db_get_table_size` | target, schema, table | Table and index size |
| `db_get_column_stats` | target, schema, table | Available column statistics |

### 7.2 Database Monitoring Tools

Purpose: Provide operational information for database diagnosis.

| Tool | Parameters | Description |
|---|---|---|
| `db_get_activity` | target, limit | Current database activity |
| `db_get_connections` | target | Connection statistics |
| `db_get_locks` | target | Lock information |
| `db_get_slow_queries` | target, limit | Slow query statistics |
| `db_get_database_stats` | target | Database statistics |
| `db_get_table_stats` | target, schema, table | Table access statistics |
| `db_get_index_stats` | target, schema, table | Index usage statistics |
| `db_get_cache_stats` | target | Cache hit statistics |
| `db_get_replication_status` | target | Replication information |
| `db_get_replication_lag` | target | Replication lag |
| `db_get_deadlocks` | target | Deadlock statistics |

Note: Some monitoring capabilities depend on PostgreSQL extensions, server version,
configuration, and role privileges. The Tool must clearly indicate when a statistic is
unavailable.

### 7.3 Query Analysis Tools

Purpose: Allow AI Agents to investigate and analyze SQL performance.

| Tool | Parameters | Description |
|---|---|---|
| `db_query_readonly` | target, sql, params, limit | Execute read-only SQL |
| `db_explain_query` | target, sql, params | Generate EXPLAIN plan |
| `db_explain_analyze` | target, sql, params | Execute EXPLAIN ANALYZE under strict controls |
| `db_estimate_query_cost` | target, sql | Estimate query plan cost |

**db_query_readonly requirements:**
- Read-only database role, read-only transaction
- Statement timeout, row limit, result size limit
- Parameterized SQL, SQL AST validation
- No multiple SQL statements
- The server must not allow modifications through this Tool

**db_explain_query:** Default behavior is `EXPLAIN (FORMAT JSON) SELECT ...`. Must not
execute the actual query.

**db_explain_analyze:** Executes the statement. Must be separately authorized with
execution timeouts, resource controls, and query-type validation.

### 7.4 Database Maintenance Tools

Purpose: Provide controlled maintenance operations.

| Tool | Parameters | Description |
|---|---|---|
| `db_vacuum_table` | target, schema, table | VACUUM |
| `db_analyze_table` | target, schema, table | ANALYZE |
| `db_reindex_table` | target, schema, table | REINDEX |
| `db_create_index` | target, schema, table, index_definition | Create index |
| `db_drop_index` | target, schema, index | Drop index |
| `db_cancel_query` | target, pid | Cancel query |
| `db_terminate_connection` | target, pid | Terminate backend |

Important PostgreSQL considerations:
- VACUUM cannot run inside a transaction block
- CREATE INDEX CONCURRENTLY cannot run inside a transaction block
- REINDEX CONCURRENTLY has different execution constraints
- Cancellation and termination require specific privileges
- DDL operations can acquire locks and affect application availability

### 7.5 Configuration Tools

| Tool | Parameters | Description |
|---|---|---|
| `db_get_config` | target, name | Read configuration |
| `db_get_config_list` | target | Read permitted configuration parameters |
| `db_reload_config` | target | Reload configuration |

Configuration modification is a separate capability and disabled by default.

---

## §8 Tool Permission Model

### 8.1 Permission Levels

**Level 1: READ_ONLY** — Automatically executable within configured limits.
Examples: database discovery, schema inspection, table metadata, monitoring, read-only SQL, EXPLAIN.

**Level 2: CONTROLLED_MAINTENANCE** — Allowed only when the target policy and
operation-specific authorization permit it.
Examples: ANALYZE, VACUUM, approved index creation, approved index maintenance, query cancellation.

**Level 3: REQUIRES_APPROVAL** — Requires explicit human approval.
Examples: DROP INDEX, DROP TABLE, ALTER TABLE, DELETE, UPDATE, TRUNCATE, database
termination operations, critical configuration changes.

### 8.2 Policy Configuration

Policy is defined per target with `default_access`, `allowed` (no approval needed), and
`require_approval` lists. An approval policy must be enforced by the server, not by an
AI-generated claim. An approval record binds: target, tool, exact operation parameters or
normalized SQL, expiration time, approver identity, and approval ID. Changing operation
parameters invalidates the approval.

Example (`configs/policy.yaml`):

```yaml
policies:
  development:
    default_access: read_only
    allowed:
      - db_query_readonly
      - db_explain_query
      - db_analyze_table
      - db_vacuum_table
      - db_create_index
    require_approval:
      - db_drop_index
      - db_reindex_table

  staging:
    default_access: read_only
    allowed:
      - db_query_readonly
      - db_explain_query
      - db_analyze_table
    require_approval:
      - db_create_index
      - db_drop_index
      - db_vacuum_table

  production:
    default_access: read_only
    allowed:
      - db_query_readonly
      - db_explain_query
      - db_get_activity
      - db_get_locks
    require_approval:
      - db_analyze_table
      - db_vacuum_table
      - db_create_index
      - db_drop_index
```

---

## §9 SQL Security and Validation

### 9.1 SQL Parser

Use `pg_query_go` to parse SQL into PostgreSQL AST. Do not rely on checking the first SQL
keyword. Validation must consider: statement type, number of statements, nested
statements, CTE operations, data modification statements, system functions, potentially
dangerous functions, and access to unauthorized schemas.

### 9.2 Read-only SQL Rules

For `db_query_readonly`:
1. Only one SQL statement
2. SELECT statements only
3. Read-only CTEs only
4. No data modification
5. No DDL
6. No transaction control
7. No COPY
8. No session configuration changes
9. No unauthorized system functions
10. No access outside the permitted database scope

Use PostgreSQL read-only transactions and restricted database roles as additional enforcement.

### 9.3 Execution Limits

| Limit | Value |
|---|---:|
| Query timeout | 10 seconds |
| Maximum returned rows | 1000 |
| Maximum result size | 1 MB |
| Maximum SQL length | 64 KB |
| Maximum concurrent queries per target | Configurable |
| Default statement timeout | 10 seconds |

Limits must be enforced by the server. The AI Agent must not be able to disable them by
passing additional SQL parameters.

### 9.4 SQL Identifier Validation

Schema names, table names, and index names must be treated as identifiers, not SQL
fragments. Use proper identifier quoting or PostgreSQL catalog lookups. Do not concatenate
arbitrary identifiers into SQL statements. SQL values must use parameterized queries.

---

## §10 Go Internal Interfaces

### 10.1 Tool Execution Context

```go
type ToolContext struct {
    Context context.Context
    Target  string
    RequestID   string
    OperationID string
}
```

Target aliases are resolved internally. The ToolContext must never contain database
passwords or DSNs.

### 10.2 Database Manager Interface

```go
type DatabaseManager interface {
    GetPool(target string) (*pgxpool.Pool, error)
    ListTargets() []string
    HealthCheck(ctx context.Context) error
    Close()
}
```

### 10.3 Tool Definition

```go
type ToolDefinition struct {
    Name        string
    Description string
    Permission  PermissionLevel
    Timeout     time.Duration
    Handler     ToolHandler
}

type ToolHandler interface {
    Execute(ctx context.Context, args json.RawMessage) (*ToolResult, error)
}
```

The MCP adapter translates these definitions into the official Go SDK Tool registration format.

### 10.4 Standard Response

```go
type ToolResult struct {
    Success  bool        `json:"success"`
    Data     interface{} `json:"data,omitempty"`
    Error    *ToolError  `json:"error,omitempty"`
    Metadata *Metadata   `json:"metadata,omitempty"`
}

type ToolError struct {
    Code    string `json:"code"`
    Message string `json:"message"`
}

type Metadata struct {
    Target       string `json:"target,omitempty"`
    DurationMS   int64  `json:"duration_ms"`
    Truncated    bool   `json:"truncated"`
    RowsReturned int    `json:"rows_returned"`
}
```

**Standard error codes:**

| Code | Meaning |
|---|---|
| `DB_TARGET_NOT_FOUND` | Unknown target |
| `DB_CONNECTION_FAILED` | Connection unavailable |
| `DB_PERMISSION_DENIED` | Operation not authorized |
| `DB_SQL_INVALID` | Invalid SQL |
| `DB_SQL_NOT_ALLOWED` | SQL operation prohibited |
| `DB_QUERY_TIMEOUT` | Query timeout |
| `DB_RESULT_TOO_LARGE` | Result exceeds limits |
| `DB_APPROVAL_REQUIRED` | Human approval required |
| `DB_APPROVAL_INVALID` | Invalid or expired approval |
| `DB_EXECUTION_FAILED` | Database execution failed |
| `DB_FEATURE_UNAVAILABLE` | Feature not supported |

Never expose raw database connection errors containing sensitive connection information.

---

## §11 MCP Resources

Resources provide reusable database context. Recommended Resources:

| URI | Description |
|---|---|
| `postgres://targets` | Configured target aliases and permitted metadata |
| `postgres://database/{target}/info` | Database information |
| `postgres://database/{target}/schemas` | Schema list |
| `postgres://database/{target}/schema/{schema}` | Schema metadata |
| `postgres://database/{target}/monitoring` | Monitoring summary |
| `postgres://database/{target}/health` | Health status |

Resources must obey the same permission policies as Tools. Do not expose DSNs, usernames,
passwords, or secret configuration values through Resources.

---

## §12 MCP Prompts

Provide reusable DBA workflows. Recommended Prompts:

| Prompt | Purpose |
|---|---|
| `dba_health_check` | General database health check |
| `dba_performance_diagnosis` | Diagnose database performance |
| `dba_slow_query_analysis` | Investigate slow SQL |
| `dba_index_recommendation` | Analyze index opportunities |
| `dba_lock_analysis` | Investigate lock contention |
| `dba_connection_analysis` | Analyze connection usage |
| `dba_capacity_planning` | Analyze database capacity |
| `dba_replication_analysis` | Investigate replication status |

Prompts should guide the Agent through tool usage. They must not grant additional permissions.

---

## §13 DBA Diagnosis Workflow

The MCP Server provides atomic Tools. The AI Agent combines these Tools into higher-level
DBA workflows.

Example: Database performance diagnosis:

```
User: Investigate why the database is slow.

Agent:
  1. Call db_get_info.
  2. Call db_get_database_stats.
  3. Call db_get_connections.
  4. Call db_get_activity.
  5. Call db_get_locks.
  6. Call db_get_slow_queries.
  7. Identify suspicious SQL.
  8. Call db_explain_query.
  9. Inspect index statistics.
  10. Generate diagnosis.
  11. Recommend corrective actions.
  12. Request approval if changes are needed.
  13. Execute approved maintenance.
  14. Verify results.
```

The Agent should produce a structured report:

```
Database Performance Report

Target: production
Problem: Slow query execution.
Findings:
  - High sequential scan count.
  - Possible missing index.
  - Several long-running queries.
Evidence:
  - Query execution plan.
  - Table statistics.
  - Index statistics.
Recommendations:
  1. Review the customer_id access pattern.
  2. Consider a suitable index.
  3. Compare execution plans before and after.
Risk: Index creation can consume disk space and affect database performance.
Execution: Not executed.
Status: Awaiting approval.
```

Diagnosis and recommendations should distinguish observed facts from AI-generated hypotheses.

---

## §14 Audit Logging

Every Tool invocation must be audited. Example:

```json
{
  "timestamp": "2026-11-25T20:00:00Z",
  "request_id": "req-123",
  "tool": "db_describe_table",
  "target": "production",
  "operation": "READ",
  "duration_ms": 25,
  "success": true,
  "rows_returned": 12
}
```

For maintenance operations, also record: approval ID, approver, operation parameters,
execution status, verification status.

Avoid logging sensitive query parameters and returned business data. Audit logs should be
stored separately from the database connection credentials.

---

## §15 MCP Transport and Authentication

### 15.1 STDIO Transport

Recommended for local AI Agent integration. The MCP Server runs as a child process. The
Agent communicates using standard input/output.

Requirements:
- Do not print application logs to stdout
- MCP protocol messages must use stdout
- Logs must use stderr
- Environment variables are inherited by the server process
- Database credentials remain server-side

### 15.2 HTTP Transport

For remote or multi-Agent deployment, support Streamable HTTP. The HTTP deployment must
implement independent MCP client authentication. Recommended controls: TLS, OAuth 2.0 or
appropriate authentication, client identity verification, per-client Tool authorization,
request rate limiting, network access restrictions.

MCP authentication and PostgreSQL authentication are separate security layers. An Agent
authenticated to the MCP Server does not automatically receive administrative database
privileges.

---

## §16 Operational Monitoring

The MCP Server exposes operational Prometheus metrics:

```
mcp_tool_calls_total
mcp_tool_errors_total
mcp_tool_duration_seconds
db_pool_connections
db_pool_idle_connections
db_pool_acquired_connections
db_query_duration_seconds
db_query_timeout_total
db_target_health
db_approval_requests_total
```

Do not expose database credentials or sensitive query contents in metrics labels. Provide
a health endpoint for HTTP deployments.

---

## §17 Testing Strategy

### 17.1 Unit Tests

Test: configuration loading, target resolution, permission evaluation, SQL AST validation,
tool argument validation, response serialization, error sanitization, approval validation.

### 17.2 Integration Tests

Use PostgreSQL containers (testcontainers-go). Test: connection pool initialization,
database metadata queries, monitoring queries, read-only SQL execution, query timeout,
row limits, transaction behavior, maintenance operations, multiple database targets.

### 17.3 Security Tests

Verify: passwords never in Tool schemas/responses, read-only tools reject DML/DDL,
multiple statements rejected, unauthorized targets rejected, invalid/expired approvals
rejected, SQL errors do not reveal DSNs, query limits cannot be bypassed, restricted
PostgreSQL roles cannot exceed privileges.

---

## §18 Development Roadmap

### Phase 1: Core Infrastructure
Go project initialization, ENV configuration, Database Manager, pgxpool, MCP Server,
Tool Registry, standard response format, logging, basic policy engine.

**Acceptance:** Server starts, pools initialize, MCP client can list Tools and invoke a
test Tool, no credentials exposed.

### Phase 2: Read-only DBA Tools
`db_get_info`, `db_list_schemas`, `db_list_tables`, `db_describe_table`,
`db_list_indexes`, `db_get_activity`, `db_get_connections`, `db_get_locks`,
`db_get_database_stats`, `db_query_readonly`, `db_explain_query`.

**Acceptance:** All Tools return structured results, SQL validation works, read-only
enforcement works, query limits work, audit logging works.

### Phase 3: Performance Analysis
`db_get_slow_queries`, `db_get_table_stats`, `db_get_index_stats`, `db_get_cache_stats`,
`db_get_replication_status`, `db_explain_analyze`, additional performance metrics.

**Acceptance:** Agent can investigate performance, collect evidence, produce structured diagnosis.

### Phase 4: Maintenance Operations
`db_vacuum_table`, `db_analyze_table`, `db_reindex_table`, `db_create_index`,
`db_drop_index`, `db_cancel_query`, approval workflow.

**Acceptance:** Maintenance permissions enforced, approval records validated, execution
audited, operations verified after execution.

### Phase 5: Production Readiness
Multi-target deployment, HTTP transport, MCP client authentication, Prometheus metrics,
Docker deployment, graceful shutdown, integration and security testing, operational docs.

---

## §19 Non-functional Requirements

| Requirement | Target |
|---|---|
| Language | Go |
| Protocol | MCP |
| Database | PostgreSQL |
| Connection | Pre-established pools |
| Credential isolation | Mandatory |
| Multi-target | Supported |
| Default permission | Read-only |
| SQL validation | Mandatory |
| Query timeout | Configurable |
| Result limits | Configurable |
| Audit logging | Mandatory |
| Graceful shutdown | Mandatory |
| Unit tests | Required |
| Integration tests | Required |
| Docker support | Required |

Performance targets should be validated under representative database workloads rather
than treated as fixed guarantees.

---

## §20 Future Extensions

The architecture should allow the following extensions without changing the core MCP interface:

**Additional Database Engines** — Introduce a common database abstraction where practical.
Avoid forcing database-specific functionality into an overly generic interface.

**DBA Knowledge Base** — Integrate documentation and operational knowledge for PostgreSQL
internals, query planner behavior, index design, vacuum/autovacuum, replication,
connection management, and performance tuning.

**Automated DBA Agent** — Build a separate Agent layer that uses MCP Tools to perform
scheduled health checks, performance diagnosis, slow query investigation, index
recommendation, capacity analysis, and maintenance planning. The Agent should not be
responsible for enforcing database security.

---

## §21 Final Architecture Decision

The project is implemented as a standalone PostgreSQL DBA MCP Gateway.

| Component | Responsibility |
|---|---|
| AI Agent | Understand DBA requests and plan operations |
| MCP Server | Expose standardized capabilities |
| Tool Layer | Implement individual DBA operations |
| Policy Engine | Authorize and validate operations |
| Database Manager | Manage database connections |
| Query Executor | Execute SQL safely |
| PostgreSQL | Enforce actual database permissions |
| Audit Logger | Record operations |

**The most important architectural decision:** The AI Agent never manages database
connections. It only operates through explicitly registered and authorized MCP
capabilities. This makes the system reusable by different AI Agents, independently
deployable, and suitable for managing development, staging, and production PostgreSQL
environments.

---

## §22 CI/CD & Release Process

### Trigger
The release pipeline (`.github/workflows/release.yml`) is triggered on **push to `main`**.

### Versioning
- Scheme: semantic versioning (`MAJOR.MINOR.PATCH`).
- Starting version: `v0.1.0`.
- Auto-increment: the pipeline reads the latest git tag matching `v*`, extracts the
  patch component, increments it by 1, and uses the result as the new release version.
  If no tag exists, it defaults to `v0.1.0`.

### Build
- Target: `linux/amd64` with `CGO_ENABLED=1` (required by `pg_query_go`).
- Version injection: `-ldflags "-X main.appVersion=${VERSION}"` sets the binary's
  version string at compile time.

### Packaging
- The release artifact is a `tar.gz` containing:
  1. `postgres-dba-mcp` — the compiled binary (executable).
  2. `tool.yaml` — the runtime contract file.

### Publishing
- A git tag `v${VERSION}` is created and pushed to the remote.
- A GitHub Release is created with the tag, a release name, description, and the
  tar.gz as a downloadable asset.
- Required GitHub permission: `contents: write`.
