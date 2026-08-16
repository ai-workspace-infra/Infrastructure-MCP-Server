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
