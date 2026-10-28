# Infrastructure MCP Server

Infrastructure MCP Server is a Go gateway for XWorkmate Agent. It aggregates existing MCP servers and exposes a stable infrastructure-facing tool surface across IaC, CMDB, runtime, security, and the Environment Graph.

```text
XWorkmate Agent
        |
Infrastructure MCP Server (Go)
        |
  +-----+---------+---------+---------+---------------+
  |     |         |         |         |               |
 IaC  CMDB     Runtime   Security  Observability    Graph
  |     |         |         |         |             Store
Terraform NetBox Kubernetes Vault  Grafana / VM       |
OpenTofu  Inventory Helm           Logs / Traces      |
Ansible playbook MCP               (observability.    |
                                    svc.plus)   PostgreSQL/pgvector / QMD
```

## Status

This repository contains the initial implementation scaffold:

- MCP stdio server implementing `initialize`, `ping`, `tools/list`, and `tools/call`.
- Process-based MCP client for reusing downstream servers without reimplementing their domain tools.
- Namespaced aggregation (`kubernetes.<tool>`, `netbox.<tool>`, etc.).
- Built-in `infra.backends`, `infra.health`, and `infra.graph.query` tools.
- Thread-safe in-memory Environment Graph store, ready to be replaced or backed by PostgreSQL/pgvector.
- JSON configuration for downstream MCP processes.

Downstream adapters are intentionally configured rather than hard-coded. This lets the gateway reuse the chosen open-source Kubernetes, Helm, NetBox, Terraform, Vault, and Ansible Playbook MCP implementations while keeping the gateway independent of package-specific APIs.

## 💡 Code Agent / IDE 客户端 MCP 配置速查 (TL;DR)

针对 **Trae**、**Cursor**、**Cherry Studio**、**Claude Desktop** 及 **观测云 Obsy Agent Teams** 等智能体，请查阅完整配置速查文档：[TLDR.md](TLDR.md)。

> [!IMPORTANT]
> **客户端实测验证结论**：
> * ✅ **Infrastructure**：握手通过，稳定提供 8 个 CMDB / 拓扑查询工具。
> * ✅ **QMD**：握手通过，提供 23 个工具，当前本地 PG 15432 已完整索引 193 篇白皮书并生效 **BM25 词法全文检索**（外部向量 API Key 注入后可激活向量计算）。
> * ❌ **可观测性 4-MCP**：未通过协议验证。请求返回 HTTP 302 重定向至 Grafana 网页（`text/html`），服务端 Caddy 尚未开放 MCP 代理。**客户端请暂缓挂载这 4 个远程地址**。
>
> **避坑提示**：在各类 IDE 客户端的单服务配置中，底层均强制要求保留最外层的 `"mcpServers"` 结构，否则报错提示“未解析到 MCP 服务项”。

### 1. Infrastructure-MCP-Server（多云 CMDB 与 15 边拓扑，实测通过）

**标准原生配置（推荐）：**
```json
{
  "mcpServers": {
    "infrastructure": {
      "args": [
        "-config",
        "/Users/shenlan/.config/infrastructure-mcp-server/config.json"
      ],
      "command": "/Users/shenlan/.local/bin/infrastructure-mcp-server"
    }
  }
}
```

*表单模式填写*：
* **类型 (Type)**：`STDIO`
* **命令 (Command)**：`/Users/shenlan/.local/bin/infrastructure-mcp-server`
* **参数 (Args)**：
  * `-config`
  * `/Users/shenlan/.config/infrastructure-mcp-server/config.json`

> 💡 **备选 0 参数 Wrapper 模式**（防客户端丢参数）：
> ```json
> {
>   "mcpServers": {
>     "infrastructure": {
>       "command": "/Users/shenlan/.local/bin/run-infrastructure-mcp.sh"
>     }
>   }
> }
> ```

### 2. QMD MCP Server（193 篇白皮书 & SOP 记忆，实测通过）

```json
{
  "mcpServers": {
    "qmd": {
      "command": "/Users/shenlan/.local/bin/run-qmd-mcp.sh"
    }
  }
}
```

### 3. 可观测性 4-MCP（待服务端网关上线，暂缓挂载）
* **Grafana**: `{ "mcpServers": { "observability-grafana": { "url": "https://observability.svc.plus/mcp/v1/grafana/mcp" } } }`
* **VictoriaMetrics**: `{ "mcpServers": { "observability-metrics": { "url": "https://observability.svc.plus/mcp/v1/metrics/mcp" } } }`
* **VictoriaLogs**: `{ "mcpServers": { "observability-logs": { "url": "https://observability.svc.plus/mcp/v1/logs/mcp" } } }`
* **VictoriaTraces**: `{ "mcpServers": { "observability-traces": { "url": "https://observability.svc.plus/mcp/v1/traces/mcp" } } }`

## 🚀 快速开始

在 Debian 12+ 或 Ubuntu 22.04+ 服务器上，可使用一键安装脚本部署 Infrastructure-MCP-Server 及其 Caddy HTTPS 反向代理。

### 独立模式 (Standalone)

本地单机部署，自动生成初始服务凭证（仅在安装结束时展示一次，建议写入 Vault）：

```bash
curl -fsSL https://raw.githubusercontent.com/ai-workspace-infra/Infrastructure-MCP-Server/main/scripts/setup-mcp-server.sh | \
  bash -s -- --domain mcp.example.com --standalone
```

### 团队模式 (Team / Vault 动态注入)

结合团队凭据管理，在执行安装前由 Vault 或环境变量注入运行时凭证（执行后立即清理临时变量）：

```bash
export MCP_DOMAIN="mcp.example.com"
export MCP_SERVICE_TOKEN="<runtime-secret>"
curl -fsSL https://raw.githubusercontent.com/ai-workspace-infra/Infrastructure-MCP-Server/main/scripts/setup-mcp-server.sh | \
  bash -s -- --domain "$MCP_DOMAIN" --with-backends
unset MCP_SERVICE_TOKEN
```

### 卸载 (Uninstall)

停止并禁用 systemd 服务与 Caddy 代理容器，清理二进制文件，并将当前配置和数据自动备份至 `/var/backups/`：

```bash
curl -fsSL https://raw.githubusercontent.com/ai-workspace-infra/Infrastructure-MCP-Server/main/scripts/setup-mcp-server.sh | \
  bash -s -- --uninstall
```

## Run

With no configuration, the server starts with only the built-in tools:

```bash
go run ./cmd/infrastructure-mcp-server
```

Copy `config.example.json` to `config.json`, replace the placeholder commands with the MCP servers used in your environment, enable the desired backends, and run:

```bash
go run ./cmd/infrastructure-mcp-server -config config.json
```

The server speaks newline-delimited JSON-RPC over stdin/stdout. Downstream process stderr is kept on stderr so it cannot corrupt the MCP stream.

## Design boundaries for the next iteration

1. Add HTTP/SSE or streamable HTTP downstream transport where a source is not a local process.
2. Add OpenTofu and Ansible Playbook domain normalization where their existing MCP tools differ from Terraform conventions.
3. Add Environment Graph ingestion from backend discovery results.
4. Add PostgreSQL/pgvector persistence and graph-aware search.
5. Add authorization, audit events, timeouts, and allow-listed mutating operations before production use.

## Verify

```bash
gofmt -w cmd internal
go test ./...
go vet ./...
```
