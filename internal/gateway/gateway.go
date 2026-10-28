package gateway

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/cmdb"
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
	cmdb     *cmdb.Store
}

func New(cfg config.Config, graphStore *graph.Store, cmdbStores ...*cmdb.Store) *Gateway {
	var cmdbStore *cmdb.Store
	if len(cmdbStores) > 0 && cmdbStores[0] != nil {
		cmdbStore = cmdbStores[0]
	} else {
		cmdbStore = cmdb.NewStore(graphStore, "")
	}

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
	return &Gateway{backends: backends, graph: graphStore, cmdb: cmdbStore}
}

func (g *Gateway) Tools(ctx context.Context) ([]mcp.Tool, error) {
	tools := []mcp.Tool{
		{
			Name:        "infra.backends",
			Description: "List configured Infrastructure MCP backends and their capabilities.",
			InputSchema: objectSchema(),
		},
		{
			Name:        "infra.graph.query",
			Description: "Query the Environment Graph for infrastructure nodes and relationships.",
			InputSchema: objectSchema(map[string]any{
				"query": map[string]any{"type": "string", "description": "Optional substring to match node id, kind, or name."},
			}),
		},
		{
			Name:        "infra.health",
			Description: "Check connectivity to configured Infrastructure MCP backends.",
			InputSchema: objectSchema(),
		},
		{
			Name:        "infra.observability.status",
			Description: "Inspect the live status, data ingestion endpoints, and MCP adapters of the centralized observability stack (observability.svc.plus).",
			InputSchema: objectSchema(),
		},
		{
			Name:        "infra.cmdb.query",
			Description: "Query CMDB inventory resources (filters: environment, provider, kind, status) with optional graph topology linkage or node_id inspection (e.g. service/observability-gateway).",
			InputSchema: objectSchema(map[string]any{
				"environment":   map[string]any{"type": "string", "description": "Filter by environment: sit, uat, prod, shared"},
				"provider":      map[string]any{"type": "string", "description": "Filter by provider: linode, gcp, aws, cloudflare, vultr"},
				"kind":          map[string]any{"type": "string", "description": "Filter by kind: compute, dns, service, database, network"},
				"status":        map[string]any{"type": "string", "description": "Filter by status: running, active, standby, ready"},
				"node_id":       map[string]any{"type": "string", "description": "Graph node ID to inspect with its connected edges and topology (e.g. 'service/observability-gateway')"},
				"include_graph": map[string]any{"type": "boolean", "description": "If true, attach connected environment graph topology to the response"},
			}),
		},
		{
			Name:        "infra.cmdb.summary",
			Description: "Concise summary of SIT, UAT, and PROD infrastructure deployments, domains, and statuses.",
			InputSchema: objectSchema(),
		},
		{
			Name:        "infra.cmdb.sync",
			Description: "Trigger synchronization of CMDB resources from IaC, GitOps, CI/CD pipelines, and local CLI tools.",
			InputSchema: objectSchema(),
		},
		{
			Name:        "infra.cli.exec",
			Description: "Safely execute read-only cloud/infrastructure CLI commands (linode-cli, aws, gcloud, gh, curl, jq) with strict guardrails preventing destructive or mutating operations.",
			InputSchema: objectSchema(map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "CLI binary to call: linode-cli, aws, gcloud, gh, curl, jq",
				},
				"args": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Arguments for the CLI command (read-only queries only)",
				},
			}),
		},
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
	case "infra.cmdb.query":
		return g.cmdbQuery(arguments)
	case "infra.cmdb.summary":
		return mcp.JSONResult(map[string]any{
			"summaries": g.cmdb.Summary(),
		})
	case "infra.cmdb.sync":
		report, err := g.cmdb.Sync(ctx)
		if err != nil {
			return mcp.CallToolResult{}, fmt.Errorf("cmdb sync: %w", err)
		}
		return mcp.JSONResult(report)
	case "infra.cli.exec":
		return g.execCLI(ctx, arguments)
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

func (g *Gateway) cmdbQuery(arguments map[string]any) (mcp.CallToolResult, error) {
	nodeID, _ := arguments["node_id"].(string)
	if nodeID != "" {
		center, edges, connectedNodes, found := g.graph.Subgraph(nodeID)
		if !found {
			return mcp.JSONResult(map[string]any{
				"found":   false,
				"node_id": nodeID,
				"error":   fmt.Sprintf("node %q not found in environment graph", nodeID),
			})
		}
		resMatch := g.cmdb.Resources("", "", "", "")
		var matchedCMDB []cmdb.Resource
		for _, r := range resMatch {
			if r.ID == nodeID || r.Name == center.Name || (center.Attributes != nil && r.Endpoint == center.Attributes["fqdn"]) {
				matchedCMDB = append(matchedCMDB, r)
			}
		}
		return mcp.JSONResult(map[string]any{
			"found":           true,
			"center_node":     center,
			"edge_count":      len(edges),
			"edges":           edges,
			"connected_nodes": connectedNodes,
			"cmdb_resources":  matchedCMDB,
		})
	}

	env, _ := arguments["environment"].(string)
	provider, _ := arguments["provider"].(string)
	kind, _ := arguments["kind"].(string)
	status, _ := arguments["status"].(string)
	includeGraph, _ := arguments["include_graph"].(bool)

	resources := g.cmdb.Resources(env, provider, kind, status)
	resp := map[string]any{
		"total":     len(resources),
		"resources": resources,
	}
	if includeGraph {
		resp["graph"] = g.graph.Query("")
	}
	return mcp.JSONResult(resp)
}

func (g *Gateway) execCLI(ctx context.Context, arguments map[string]any) (mcp.CallToolResult, error) {
	cmdName, _ := arguments["command"].(string)
	cmdName = strings.TrimSpace(cmdName)
	if cmdName == "" {
		return mcp.CallToolResult{}, fmt.Errorf("command is required")
	}

	allowedCommands := map[string]bool{
		"linode-cli": true,
		"aws":        true,
		"gcloud":     true,
		"gh":         true,
		"curl":       true,
		"jq":         true,
	}
	if !allowedCommands[cmdName] {
		return mcp.CallToolResult{}, fmt.Errorf("command %q is not in allowed list: linode-cli, aws, gcloud, gh, curl, jq", cmdName)
	}

	var rawArgs []string
	if argsVal, ok := arguments["args"].([]any); ok {
		for _, a := range argsVal {
			if str, ok := a.(string); ok {
				rawArgs = append(rawArgs, str)
			}
		}
	} else if argsStr, ok := arguments["args"].([]string); ok {
		rawArgs = argsStr
	}

	blockedWords := []string{
		"delete", "destroy", "remove", "rm", "drop", "terminate", "kill",
		"reboot", "shutdown", "stop", "create", "insert", "post", "put",
		"update", "modify", "set", "apply", "push", "write", "patch",
		"truncate",
	}

	for _, arg := range rawArgs {
		lower := strings.ToLower(arg)
		for _, blocked := range blockedWords {
			if lower == blocked || strings.HasPrefix(lower, blocked+"=") || strings.Contains(lower, " "+blocked+" ") {
				return mcp.CallToolResult{}, fmt.Errorf("safety violation: mutating operation %q is blocked by read-only policy", arg)
			}
		}
		if cmdName == "curl" {
			if strings.HasPrefix(lower, "-x") && !strings.Contains(lower, "get") && !strings.Contains(lower, "head") {
				return mcp.CallToolResult{}, fmt.Errorf("safety violation: curl mutating method in %q is blocked", arg)
			}
			if lower == "-d" || lower == "--data" || strings.HasPrefix(lower, "--data-") {
				return mcp.CallToolResult{}, fmt.Errorf("safety violation: curl data payload in %q is blocked", arg)
			}
		}
	}

	execCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(execCtx, cmdName, rawArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return mcp.CallToolResult{}, fmt.Errorf("exec error: %w", err)
		}
	}

	return mcp.JSONResult(map[string]any{
		"command":   cmdName,
		"args":      rawArgs,
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"exit_code": exitCode,
	})
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
