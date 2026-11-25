# Technical Constraints & Specifications

## 1. Language & Architecture
- **Protocol**: MCP over stdio (Standard Input/Standard Output)
- **Architecture**: 
  - 主程序入口为 `main.go`。
  - 使用成熟的 MCP Go 库（如 `github.com/modelcontextprotocol/go-sdk` 或类似标准库）处理 stdio 通信和 JSON-RPC 协议。
  - 确保程序在接收到退出信号或标准输入关闭时优雅退出。

## 2. Configuration Management (Critical)
- **所有** 运行时所需的配置参数（如 API Keys, Endpoints, Mode, etc.）必须从 **Environment Variables (ENV)** 中读取。
- 严禁硬编码配置。
- 在程序启动时验证必需的环境变量是否存在，如果缺失，应打印明确的错误信息到 stderr 并退出。
- 对于敏感信息（Secrets），在 `tool.yaml` 中声明为空字符串 `""`，并在代码中直接从 ENV 读取（运行时解析）。

## 3. Tool Definition Logic
- 请实现一个通用的示例 Tool（例如：`get_weather` 或 `query_database`，或者根据我后续提供的需求动态生成）。
- 该 Tool 应接收用户参数，从 ENV 获取必要的后端配置，执行逻辑，并返回结果。
- 错误处理：捕获所有 panic 和 error，将其格式化为 MCP 标准错误响应。

## 4. File Structure
你必须生成以下文件：
1. `go.mod`: 包含必要的依赖。
2. `main.go`: 入口文件，初始化 MCP Server，注册 Tool，启动 stdio loop。
3. `tool.yaml`: 符合指定 Schema 的运行时契约文件。

## 5. `tool.yaml` Schema Compliance
必须创建 `tool.yaml`，严格遵循以下 Schema。注意 `env` 部分的处理逻辑：

```yaml
language: go
kind: compiled
entrypoint: ./bin/mcp-tool-binary  # 相对路径，指向编译后的二进制文件
# install: "" # 对于 compiled 类型通常不需要 install 脚本，但如果需要预构建可保留为空或注释
# interpreter: none # Go 是编译型语言
args: [] # 可选，如果有默认参数
env:
  # 示例：非敏感配置
  LOG_LEVEL: "INFO"
  # 示例：敏感配置（空字符串表示运行时从 ENV 解析）
  API_KEY: ""
  DATABASE_URL: ""
timeout_seconds: 30
```

*注意：`entrypoint` 指向的必须是编译好的二进制文件路径。*

## 6. CI/CD Packaging Logic (Simulated in Code/Docs)
虽然你主要生成代码，但请在代码或 README 中明确说明如何满足以下 CI/CD 需求（Push to main -> Release -> Linux amd64 Package）：
- 构建命令必须针对 `linux/amd64`。
- 生成的发布包（tar.gz 或 zip）必须包含：
  1. 编译好的二进制文件 (`mcp-tool`)
  2. `tool.yaml` 文件
- 确保二进制文件具有执行权限。

# Execution Steps
1. 初始化 Go 模块。
2. 编写 `main.go` 实现 MCP stdio 协议交互。
3. 实现具体的 Tool 逻辑（假设一个场景：调用一个假设的 HTTP API 获取数据，URL 和 Key 从 ENV 获取）。
4. 生成 `tool.yaml` 文件。
5. 提供构建指令，确保生成 Linux amd64 二进制文件。
