package gateway

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/config"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/graph"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/mcp"
)

type Backend interface {
	Name() string
	Capability() string
	Tools(context.Context) ([]mcp.Tool, error)
	Call(context.Context, string, map[string]any) (mcp.CallToolResult, error)
	Health(context.Context) error
}

type Gateway struct {
	backends []Backend
	graph    *graph.Store
}

func New(cfg config.Config, graphStore *graph.Store) *Gateway {
	backends := make([]Backend, 0, len(cfg.Backends))
	for _, backendCfg := range cfg.Backends {
		if backendCfg.IsEnabled() {
			if backendCfg.URL != "" {
				backends = append(backends, mcp.NewHTTPClient(backendCfg))
			} else {
				backends = append(backends, mcp.NewProcessClient(backendCfg))
			}
		}
	}
	return &Gateway{backends: backends, graph: graphStore}
}

func (g *Gateway) Tools(ctx context.Context) ([]mcp.Tool, error) {
	tools := []mcp.Tool{
		{Name: "infra.backends", Description: "List configured Infrastructure MCP backends and their capabilities.", InputSchema: objectSchema()},
		{Name: "infra.graph.query", Description: "Query the Environment Graph for infrastructure nodes and relationships.", InputSchema: objectSchema(map[string]any{"query": map[string]any{"type": "string", "description": "Optional substring to match node id, kind, or name."}})},
		{Name: "infra.health", Description: "Check connectivity to configured Infrastructure MCP backends.", InputSchema: objectSchema()},
		{Name: "infra.observability.status", Description: "Inspect the live status, data ingestion endpoints, and MCP adapters of the centralized observability stack (observability.svc.plus).", InputSchema: objectSchema()},
	}
	for _, backend := range g.backends {
		backendTools, err := backend.Tools(ctx)
		if err != nil {
			continue
		}
		for _, tool := range backendTools {
			tool.Name = backend.Name() + "." + tool.Name
			tools = append(tools, tool)
		}
	}
	return tools, nil
}

func (g *Gateway) Call(ctx context.Context, name string, arguments map[string]any) (mcp.CallToolResult, error) {
	switch name {
	case "infra.backends":
		backends := make([]map[string]string, 0, len(g.backends))
		for _, backend := range g.backends {
			backends = append(backends, map[string]string{"name": backend.Name(), "capability": backend.Capability()})
		}
		return mcp.JSONResult(map[string]any{"backends": backends})
	case "infra.graph.query":
		query, _ := arguments["query"].(string)
		return mcp.JSONResult(g.graph.Query(query))
	case "infra.health":
		return g.health(ctx)
	case "infra.observability.status":
		return g.observabilityStatus(ctx)
	}

	separator := strings.IndexByte(name, '.')
	if separator <= 0 || separator == len(name)-1 {
		return mcp.CallToolResult{}, fmt.Errorf("unknown tool %q", name)
	}
	backendName, toolName := name[:separator], name[separator+1:]
	for _, backend := range g.backends {
		if backend.Name() == backendName {
			return backend.Call(ctx, toolName, arguments)
		}
	}
	return mcp.CallToolResult{}, fmt.Errorf("unknown backend %q", backendName)
}

func (g *Gateway) observabilityStatus(ctx context.Context) (mcp.CallToolResult, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // don't follow redirects
		},
	}

	targetURL := "https://observability.svc.plus/"
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, targetURL, nil)
	live := false
	statusCode := 0
	latencyMs := int64(0)
	if err == nil {
		start := time.Now()
		resp, reqErr := client.Do(req)
		if reqErr == nil {
			statusCode = resp.StatusCode
			live = resp.StatusCode >= 200 && resp.StatusCode < 500
			latencyMs = time.Since(start).Milliseconds()
			_ = resp.Body.Close()
		}
	}

	data := map[string]any{
		"service": "observability.svc.plus",
		"gateway": map[string]any{
			"url":          targetURL,
			"live":         live,
			"httpStatus":   statusCode,
			"latencyMs":    latencyMs,
			"reverseProxy": "Caddy",
		},
		"datasources": []map[string]any{
			{
				"name":       "VictoriaMetrics (Metrics TSDB)",
				"type":       "metrics",
				"ingest_url": "https://observability.svc.plus/ingest/metrics/api/v1/write",
				"query_url":  "https://observability.svc.plus/vmetrics/",
				"mcp_url":    "https://observability.svc.plus/mcp/v1/metrics/mcp",
				"port":       8428,
				"protocol":   "Prometheus Remote Write / MetricsQL",
			},
			{
				"name":       "VictoriaLogs (High-Cardinality Logs)",
				"type":       "logs",
				"ingest_url": "https://observability.svc.plus/ingest/logs/insert/jsonline",
				"query_url":  "https://observability.svc.plus/vlogs/",
				"mcp_url":    "https://observability.svc.plus/mcp/v1/logs/mcp",
				"port":       9428,
				"protocol":   "LogsQL / JSON Lines / OTLP",
			},
			{
				"name":       "VictoriaTraces (Distributed Traces)",
				"type":       "traces",
				"ingest_url": "https://observability.svc.plus/ingest/otlp/v1/traces",
				"query_url":  "https://observability.svc.plus/vtraces/",
				"mcp_url":    "https://observability.svc.plus/mcp/v1/traces/mcp",
				"port":       10428,
				"protocol":   "OTLP/HTTP / TraceQL",
			},
			{
				"name":     "Grafana (Visual Dashboards & Alerts)",
				"type":     "visualization",
				"ui_url":   "https://observability.svc.plus/grafana/",
				"mcp_url":  "https://observability.svc.plus/mcp/v1/grafana/mcp",
				"port":     3000,
				"protocol": "HTTP / JSON Dashboard Provisioning",
			},
			{
				"name":      "Blackbox Exporter (Synthetic Probes)",
				"type":      "synthetic",
				"probe_url": "https://observability.svc.plus/blackbox/",
				"port":      9115,
				"protocol":  "HTTP/TCP ICMP Probing",
			},
		},
		"repositories": map[string]string{
			"gitops":                "https://github.com/ai-workspace-infra/gitops.git",
			"playbooks":             "https://github.com/ai-workspace-infra/playbooks.git",
			"iac_modules":           "https://github.com/ai-workspace-infra/iac_modules.git",
			"Platform-Orchestrator": "https://github.com/ai-workspace-infra/Platform-Orchestrator.git",
			"knowledge":             "https://github.com/ai-workspace-services/knowledge/tree/main/docs/reference",
		},
		"vault_secret_paths": []string{
			"kv/<env>/platform/observability",
			"kv/data/observability/mcp",
			"kv/data/prod/ulighthost-xconnect/observability.svc.plus",
		},
	}
	return mcp.JSONResult(data)
}

func (g *Gateway) health(ctx context.Context) (mcp.CallToolResult, error) {
	statuses := make([]map[string]any, 0, len(g.backends))
	for _, backend := range g.backends {
		status := map[string]any{"name": backend.Name(), "capability": backend.Capability(), "healthy": true}
		if err := backend.Health(ctx); err != nil {
			status["healthy"] = false
			status["error"] = err.Error()
		}
		statuses = append(statuses, status)
	}
	return mcp.JSONResult(map[string]any{"backends": statuses})
}

func objectSchema(properties ...map[string]any) map[string]any {
	schema := map[string]any{"type": "object"}
	if len(properties) > 0 {
		schema["properties"] = properties[0]
	}
	return schema
}
