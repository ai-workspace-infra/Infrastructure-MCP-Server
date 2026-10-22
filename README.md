# Infrastructure MCP Server

Infrastructure MCP Server is a Go gateway for XWorkmate Agent. It aggregates existing MCP servers and exposes a stable infrastructure-facing tool surface across IaC, CMDB, runtime, security, and the Environment Graph.

```text
XWorkmate Agent
        |
Infrastructure MCP Server (Go)
        |
  +-----+---------+---------+---------+
  |     |         |         |         |
 IaC  CMDB     Runtime   Security  Graph
  |     |         |         |         |
Terraform NetBox Kubernetes Vault  Store
OpenTofu  Inventory Helm              |
Ansible playbook MCP                  |
                                PostgreSQL/pgvector (planned)
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
