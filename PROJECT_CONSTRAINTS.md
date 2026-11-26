# Technical Constraints & Specifications

## 1. Language & Architecture
- **Protocol**: MCP over stdio (Standard Input/Standard Output)
- **Architecture**: 
  - 主程序入口为 `cmd/server/main.go`。
  - 使用成熟的 MCP Go 库（`github.com/modelcontextprotocol/go-sdk`）处理 stdio 通信和 JSON-RPC 协议。
  - 确保程序在接收到退出信号（SIGTERM/SIGINT）或标准输入关闭（EOF）时优雅退出。
  - 日志输出到 stderr，MCP 协议消息使用 stdout。

## 2. Configuration Management (Critical)
- **所有** 运行时所需的配置参数（如 API Keys, Endpoints, Mode, etc.）必须从 **Environment Variables (ENV)** 中读取。
- 严禁硬编码配置。
- 在程序启动时验证必需的环境变量是否存在，如果缺失，应打印明确的错误信息到 stderr 并退出。
- 对于敏感信息（Secrets），在 `tool.yaml` 中声明为空字符串 `""`，并在代码中直接从 ENV 读取（运行时解析）。

## 3. Tool Definition Logic
- 实现 PostgreSQL DBA 工具集（30+ DBA tools across 5 categories: Discovery, Monitoring, Query Analysis, Maintenance, Configuration）。
- 每个 Tool 应接收用户参数，从 ENV 获取必要的后端配置（数据库连接参数等），执行逻辑，并返回结构化结果。
- 工具通过 `internal/tools/` 包组织，按功能分组注册到 MCP Server。
- 错误处理：捕获所有 panic 和 error，将其格式化为 MCP 标准错误响应（`ToolError` with standard error codes）。

## 4. File Structure
你必须生成以下文件：
1. `go.mod`: 包含必要的依赖（`github.com/modelcontextprotocol/go-sdk`, `github.com/jackc/pgx/v5`, `github.com/pganalyze/pg_query_go/v5`, `github.com/knadh/koanf/v2`, `github.com/prometheus/client_golang`）。
2. `cmd/server/main.go`: 入口文件，初始化 MCP Server，注册 Tools/Resources/Prompts，启动 stdio loop。
3. `tool.yaml`: 符合指定 Schema 的运行时契约文件。
4. `docs/design.md`: 完整技术设计文档（design of record）。

## 5. `tool.yaml` Schema Compliance
必须创建 `tool.yaml`，严格遵循以下 Schema。注意 `env` 部分的处理逻辑：

```yaml
language: go
kind: compiled
entrypoint: ./postgres-dba-mcp  # 相对路径，指向编译后的二进制文件
args: []
env:
  # 非敏感配置
  MCP_TRANSPORT: "stdio"
  MCP_LOG_LEVEL: "info"
  MCP_AUDIT_ENABLED: "true"
  # 敏感配置（空字符串表示运行时从 ENV 解析）
  POSTGRES_TARGETS: ""
  POSTGRES_DEVELOPMENT_DSN: ""
  POSTGRES_STAGING_DSN: ""
  POSTGRES_PRODUCTION_DSN: ""
  DB_POOL_MIN_CONNS: "2"
  DB_POOL_MAX_CONNS: "10"
  DB_QUERY_TIMEOUT: "10s"
  DB_MAX_ROWS: "1000"
  DB_MAX_RESULT_SIZE: "1048576"
  DB_DEFAULT_TARGET: "development"
  DB_DEFAULT_ACCESS: "read_only"
timeout_seconds: 30
```

*注意：`entrypoint` 指向的必须是编译好的二进制文件路径。*

## 6. CI/CD Packaging Logic
构建命令必须针对 `linux/amd64`：
```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o build/postgres-dba-mcp ./cmd/server/
```
生成的发布包（tar.gz）必须包含：
1. 编译好的二进制文件 (`postgres-dba-mcp`)
2. `tool.yaml` 文件
- 确保二进制文件具有执行权限 (`chmod +x`)。

# Execution Steps
1. 初始化 Go 模块（`go mod init postgres-dba-mcp`）。
2. 编写 `cmd/server/main.go` 实现 MCP stdio 协议交互。
3. 实现 PostgreSQL DBA Tool 逻辑（数据库连接、SQL 执行、元数据发现、监控、维护等）。
4. 生成 `tool.yaml` 文件。
5. 提供构建指令，确保生成 Linux amd64 二进制文件。
