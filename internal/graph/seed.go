package graph

// SeedDefaults populates the environment graph store with topology, data sources,
// and configuration metadata from the connected infrastructure repositories:
// gitops, playbooks, iac_modules, Platform-Orchestrator, and knowledge.
func (s *Store) SeedDefaults() {
	// 1. Central Observability Gateway & Data Sources
	s.UpsertNode(Node{
		ID:   "service/observability-gateway",
		Kind: "service",
		Name: "observability.svc.plus",
		Attributes: map[string]any{
			"fqdn":        "observability.svc.plus",
			"description": "Central reverse proxy gateway and TLS termination for all observability systems",
			"proxy":       "Caddy",
			"auth_mode":   "Basic Auth / Forward Auth (Zitadel JWT)",
		},
	})
	s.UpsertNode(Node{
		ID:   "datasource/victoriametrics",
		Kind: "datasource",
		Name: "VictoriaMetrics TSDB",
		Attributes: map[string]any{
			"type":        "metrics",
			"port":        8428,
			"ingest_path": "/ingest/metrics/api/v1/write",
			"query_path":  "/vmetrics/",
			"protocol":    "Prometheus Remote Write / MetricsQL",
		},
	})
	s.UpsertNode(Node{
		ID:   "datasource/victorialogs",
		Kind: "datasource",
		Name: "VictoriaLogs Log Engine",
		Attributes: map[string]any{
			"type":        "logs",
			"port":        9428,
			"ingest_path": "/ingest/logs/insert/jsonline",
			"query_path":  "/vlogs/",
			"protocol":    "LogsQL / JSON Lines / OTLP",
		},
	})
	s.UpsertNode(Node{
		ID:   "datasource/victoriatraces",
		Kind: "datasource",
		Name: "VictoriaTraces Trace Engine",
		Attributes: map[string]any{
			"type":        "traces",
			"port":        10428,
			"ingest_path": "/ingest/otlp/v1/traces",
			"query_path":  "/vtraces/",
			"protocol":    "OTLP/HTTP / TraceQL",
		},
	})
	s.UpsertNode(Node{
		ID:   "datasource/grafana",
		Kind: "datasource",
		Name: "Grafana Visualization",
		Attributes: map[string]any{
			"type":     "visualization",
			"port":     3000,
			"ui_url":   "https://observability.svc.plus/grafana/",
			"protocol": "HTTP / Dashboard Provisioning",
		},
	})
	s.UpsertNode(Node{
		ID:   "datasource/blackbox",
		Kind: "datasource",
		Name: "Blackbox Exporter",
		Attributes: map[string]any{
			"type":       "synthetic_monitoring",
			"port":       9115,
			"probe_path": "/blackbox/",
			"protocol":   "HTTP/TCP/DNS ICMP Prober",
		},
	})

	// 2. Observability MCP Adapters (Decoupled on mcp.svc.plus, deployed on dev-k3s-v5)
	s.UpsertNode(Node{
		ID:   "mcp/grafana",
		Kind: "mcp_server",
		Name: "Grafana MCP Adapter",
		Attributes: map[string]any{
			"url":         "https://mcp.svc.plus/v1/grafana/sse",
			"capability":  "observability",
			"transport":   "sse",
			"role":        "Dashboards, datasources, and alert status extraction",
			"access_mode": "read-only (--disable-write --disable-admin)",
			"node":        "dev-k3s-v5",
		},
	})
	s.UpsertNode(Node{
		ID:   "mcp/victoriametrics",
		Kind: "mcp_server",
		Name: "VictoriaMetrics MCP Adapter",
		Attributes: map[string]any{
			"url":         "https://mcp.svc.plus/v1/metrics/mcp",
			"capability":  "observability",
			"transport":   "streamable-http",
			"role":        "MetricsQL time series query and metric discovery",
			"access_mode": "read-only",
			"node":        "dev-k3s-v5",
		},
	})
	s.UpsertNode(Node{
		ID:   "mcp/victorialogs",
		Kind: "mcp_server",
		Name: "VictoriaLogs MCP Adapter",
		Attributes: map[string]any{
			"url":         "https://mcp.svc.plus/v1/logs/mcp",
			"capability":  "observability",
			"transport":   "streamable-http",
			"role":        "LogsQL high-cardinality log search and stream inspection",
			"access_mode": "read-only",
			"node":        "dev-k3s-v5",
		},
	})
	s.UpsertNode(Node{
		ID:   "mcp/victoriatraces",
		Kind: "mcp_server",
		Name: "VictoriaTraces MCP Adapter",
		Attributes: map[string]any{
			"url":         "https://mcp.svc.plus/v1/traces/mcp",
			"capability":  "observability",
			"transport":   "streamable-http",
			"role":        "Distributed trace retrieval and span bottleneck analysis",
			"access_mode": "read-only",
			"node":        "dev-k3s-v5",
		},
	})

	// 2.1 Nodes for dev-k3s-v5 and Dedicated MCP Gateways
	s.UpsertNode(Node{
		ID:   "host/dev-k3s-v5",
		Kind: "host",
		Name: "dev-k3s-v5.svc.plus",
		Attributes: map[string]any{
			"provider": "akamai-cloud",
			"region":   "jp-tyo-3",
			"type":     "g6-standard-2 (2C4G)",
			"role":     "Dev K3s cluster node hosting temporary SIT lanes and MCP gateways",
		},
	})
	s.UpsertNode(Node{
		ID:   "service/mcp-server",
		Kind: "service",
		Name: "mcp-server.svc.plus",
		Attributes: map[string]any{
			"fqdn":        "mcp-server.svc.plus",
			"description": "Central Infrastructure & CMDB MCP Aggregation Gateway",
			"node":        "dev-k3s-v5",
			"transport":   "SSE / Streamable HTTP (:8080)",
		},
	})
	s.UpsertNode(Node{
		ID:   "service/observability-mcp",
		Kind: "service",
		Name: "mcp.svc.plus",
		Attributes: map[string]any{
			"fqdn":        "mcp.svc.plus",
			"description": "Dedicated Observability 4-MCP Gateway proxying to observability.svc.plus",
			"node":        "dev-k3s-v5",
			"transport":   "SSE / Streamable HTTP (:8000-:8003)",
		},
	})

	// 3. Infrastructure & Connected Repositories
	s.UpsertNode(Node{
		ID:   "repo/gitops",
		Kind: "repository",
		Name: "gitops",
		Attributes: map[string]any{
			"url":        "https://github.com/ai-workspace-infra/gitops.git",
			"role":       "Declarative Kubernetes and Doco-CD continuous delivery",
			"vault_path": "kv/<env>/platform/observability",
			"namespaces": []string{"observability", "web-saas", "agent-context"},
		},
	})
	s.UpsertNode(Node{
		ID:   "repo/playbooks",
		Kind: "repository",
		Name: "playbooks",
		Attributes: map[string]any{
			"url":           "https://github.com/ai-workspace-infra/playbooks.git",
			"role":          "Ansible automation for host configuration and Docker Compose stacks",
			"key_playbooks": []string{"deploy_observability.yml", "deploy_observability_agent.yml"},
			"server_role":   "roles/docker/observability-server",
			"agent_role":    "roles/vhosts/vector-agent",
		},
	})
	s.UpsertNode(Node{
		ID:   "repo/iac-modules",
		Kind: "repository",
		Name: "iac_modules",
		Attributes: map[string]any{
			"url":         "https://github.com/ai-workspace-infra/iac_modules.git",
			"role":        "Terraform & OpenTofu multi-cloud platform infrastructure modules",
			"dns_record":  "observability.svc.plus A record via Cloudflare module",
			"shared_node": "open-platform (Akamai / GCP shared node)",
		},
	})
	s.UpsertNode(Node{
		ID:   "repo/platform-orchestrator",
		Kind: "repository",
		Name: "Platform-Orchestrator",
		Attributes: map[string]any{
			"url":       "https://github.com/ai-workspace-infra/Platform-Orchestrator.git",
			"role":      "Unified CI/CD orchestrator workflows and multi-environment validation gates",
			"workflows": []string{"observability-server.yml", "selfhost-orchestrator.yml", "daily-main-snapshot.yaml"},
		},
	})
	s.UpsertNode(Node{
		ID:   "repo/knowledge",
		Kind: "repository",
		Name: "knowledge",
		Attributes: map[string]any{
			"url":       "https://github.com/ai-workspace-services/knowledge/tree/main/docs/reference",
			"role":      "Engineering standards, architecture blueprints, 4-MCP AIOps runbooks",
			"reference": "docs/reference/platform-ops-toolkit/02-delivery-and-orchestration/observability-mcp-tldr.md",
		},
	})

	// 4. Core Environments & Service Peers
	s.UpsertNode(Node{ID: "env/prod", Kind: "environment", Name: "Production Environment", Attributes: map[string]any{"tier": "prod"}})
	s.UpsertNode(Node{ID: "env/uat", Kind: "environment", Name: "UAT Environment", Attributes: map[string]any{"tier": "uat"}})
	s.UpsertNode(Node{ID: "service/vault", Kind: "service", Name: "vault.svc.plus", Attributes: map[string]any{"domain": "vault.svc.plus", "role": "Central secret storage and PKI"}})
	s.UpsertNode(Node{ID: "service/iam", Kind: "service", Name: "iam.svc.plus", Attributes: map[string]any{"domain": "iam.svc.plus", "role": "Zitadel IAM SSO and OIDC authority"}})
	s.UpsertNode(Node{ID: "service/accounts", Kind: "service", Name: "accounts.svc.plus", Attributes: map[string]any{"domain": "accounts.svc.plus", "role": "Web SaaS accounts service"}})
	s.UpsertNode(Node{ID: "service/console", Kind: "service", Name: "console.svc.plus", Attributes: map[string]any{"domain": "console.svc.plus", "role": "AI Workspace Web Console UI"}})

	// 5. Topology Edges
	s.UpsertEdge(Edge{From: "repo/playbooks", To: "service/observability-gateway", Type: "deploys"})
	s.UpsertEdge(Edge{From: "repo/iac-modules", To: "service/observability-gateway", Type: "provisions_dns"})
	s.UpsertEdge(Edge{From: "repo/gitops", To: "service/observability-gateway", Type: "declares_topology"})
	s.UpsertEdge(Edge{From: "repo/platform-orchestrator", To: "service/observability-gateway", Type: "orchestrates"})

	s.UpsertEdge(Edge{From: "service/observability-gateway", To: "datasource/victoriametrics", Type: "routes_to"})
	s.UpsertEdge(Edge{From: "service/observability-gateway", To: "datasource/victorialogs", Type: "routes_to"})
	s.UpsertEdge(Edge{From: "service/observability-gateway", To: "datasource/victoriatraces", Type: "routes_to"})
	s.UpsertEdge(Edge{From: "service/observability-gateway", To: "datasource/grafana", Type: "routes_to"})
	s.UpsertEdge(Edge{From: "service/observability-gateway", To: "datasource/blackbox", Type: "routes_to"})

	s.UpsertEdge(Edge{From: "service/observability-gateway", To: "mcp/grafana", Type: "exposes_mcp"})
	s.UpsertEdge(Edge{From: "service/observability-gateway", To: "mcp/victoriametrics", Type: "exposes_mcp"})
	s.UpsertEdge(Edge{From: "service/observability-gateway", To: "mcp/victorialogs", Type: "exposes_mcp"})
	s.UpsertEdge(Edge{From: "service/observability-gateway", To: "mcp/victoriatraces", Type: "exposes_mcp"})

	s.UpsertEdge(Edge{From: "datasource/grafana", To: "datasource/victoriametrics", Type: "queries"})
	s.UpsertEdge(Edge{From: "datasource/grafana", To: "datasource/victorialogs", Type: "queries"})
	s.UpsertEdge(Edge{From: "datasource/grafana", To: "datasource/victoriatraces", Type: "queries"})

	s.UpsertEdge(Edge{From: "service/vault", To: "service/observability-gateway", Type: "provides_credentials"})
	s.UpsertEdge(Edge{From: "service/iam", To: "service/observability-gateway", Type: "provides_auth"})

	// 6. dev-k3s-v5 Edge Deployments & Aggregation
	s.UpsertEdge(Edge{From: "service/mcp-server", To: "host/dev-k3s-v5", Type: "deployed_on"})
	s.UpsertEdge(Edge{From: "service/observability-mcp", To: "host/dev-k3s-v5", Type: "deployed_on"})
	s.UpsertEdge(Edge{From: "service/mcp-server", To: "service/observability-mcp", Type: "aggregates"})
	s.UpsertEdge(Edge{From: "repo/gitops", To: "service/mcp-server", Type: "manages_via_flux"})
	s.UpsertEdge(Edge{From: "repo/gitops", To: "service/observability-mcp", Type: "manages_via_flux"})
}
