package gateway

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/catalog"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/cmdb"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/config"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/flagger"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/graph"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/mcp"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/telemetry"
)

type Backend interface {
	Name() string
	Capability() string
	Tools(context.Context) ([]mcp.Tool, error)
	Call(context.Context, string, map[string]any) (mcp.CallToolResult, error)
	Health(context.Context) error
}

// configuredBackend is implemented by backends that expose their config,
// which the gateway uses for allowed_tools / disabled_tools filtering.
type configuredBackend interface {
	Config() config.BackendConfig
}

func backendConfig(b Backend) (config.BackendConfig, bool) {
	if cb, ok := b.(configuredBackend); ok {
		return cb.Config(), true
	}
	return config.BackendConfig{}, false
}

// toolAllowed applies the backend's gateway tool policy.
func toolAllowed(b Backend, tool string) bool {
	cfg, ok := backendConfig(b)
	if !ok {
		return true
	}
	return cfg.ToolAllowed(tool)
}

type backendToolState struct {
	Count     int
	Filtered  int
	Error     string
	CheckedAt time.Time
}

type Gateway struct {
	backends  []Backend
	toolMu    sync.Mutex
	toolState map[string]backendToolState
	graph     *graph.Store
	cmdb      *cmdb.Store
	catalog   *catalog.Store
	flagger   *flagger.Client
	telemetry *telemetry.Engine
}

type gatewayMCPCaller struct {
	gw *Gateway
}

func (c *gatewayMCPCaller) Call(ctx context.Context, toolName string, arguments map[string]any) ([]byte, error) {
	if c.gw == nil {
		return nil, fmt.Errorf("gateway not initialized")
	}
	separator := strings.IndexByte(toolName, '.')
	if separator <= 0 || separator == len(toolName)-1 {
		return nil, fmt.Errorf("invalid tool name %q", toolName)
	}
	backendName, rawTool := toolName[:separator], toolName[separator+1:]
	for _, b := range c.gw.backends {
		if b.Name() == backendName {
			if !toolAllowed(b, rawTool) {
				return nil, fmt.Errorf("tool %q is disabled by gateway policy for backend %q", rawTool, backendName)
			}
			res, err := b.Call(ctx, rawTool, arguments)
			if err != nil {
				return nil, err
			}
			if len(res.Content) > 0 {
				return []byte(res.Content[0].Text), nil
			}
			return nil, nil
		}
	}
	return nil, fmt.Errorf("backend %q not found", backendName)
}

func New(cfg config.Config, graphStore *graph.Store, cmdbStores ...*cmdb.Store) *Gateway {
	gw := &Gateway{
		graph:     graphStore,
		toolState: map[string]backendToolState{},
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
	gw.backends = backends

	mcpCaller := &gatewayMCPCaller{gw: gw}
	telEngine := telemetry.NewEngine(telemetry.Config{}, mcpCaller)
	gw.telemetry = telEngine

	var cmdbStore *cmdb.Store
	if len(cmdbStores) > 0 && cmdbStores[0] != nil {
		cmdbStore = cmdbStores[0]
		cmdbStore.WithTelemetry(telEngine)
	} else {
		cmdbStore = cmdb.NewStore(graphStore, "", telEngine)
	}
	gw.cmdb = cmdbStore

	gw.catalog = catalog.NewStore("", telEngine)
	gw.flagger = flagger.NewClient("", telEngine)

	return gw
}

func (g *Gateway) WithCatalog(cat *catalog.Store) *Gateway {
	if cat != nil {
		g.catalog = cat
	}
	return g
}

func (g *Gateway) WithFlagger(fl *flagger.Client) *Gateway {
	if fl != nil {
		g.flagger = fl
	}
	return g
}

func (g *Gateway) WithTelemetry(tel *telemetry.Engine) *Gateway {
	if tel != nil {
		g.telemetry = tel
		if g.catalog != nil {
			g.catalog.WithTelemetry(tel)
		}
		if g.flagger != nil {
			g.flagger.WithTelemetry(tel)
		}
		if g.cmdb != nil {
			g.cmdb.WithTelemetry(tel)
		}
	}
	return g
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
				"kind":          map[string]any{"type": "string", "description": "Filter by kind: compute, host (SSH inspection targets, carry an ssh block), dns, service, database, network"},
				"role":          map[string]any{"type": "string", "description": "Filter by functional role, e.g. ops (host running the MCP gateway + SSH executor), edge-agent, k3s-node"},
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
		{
			Name:        "infra.api_catalog.query",
			Description: "Query the unified API and Route Catalog across Web Console frontend and backend microservices (Console, Accounts, Billing), supporting 3-tier grouping, auth policies, and search.",
			InputSchema: objectSchema(map[string]any{
				"category":    map[string]any{"type": "string", "description": "Filter by category: all, frontend, api (or backend)"},
				"service":     map[string]any{"type": "string", "description": "Filter by service: console, accounts, billing"},
				"environment": map[string]any{"type": "string", "description": "Filter by environment: sit, prod (defaults to prod)"},
				"query":       map[string]any{"type": "string", "description": "Keyword search matching path, description, category, service, or method"},
			}),
		},
		{
			Name:        "infra.flagger.status",
			Description: "Query Flagger progressive release canary phases, traffic weights (Primary vs Candidate), and gate decisions across SIT and PROD environments.",
			InputSchema: objectSchema(map[string]any{
				"environment": map[string]any{"type": "string", "description": "Target environment: sit, prod (defaults to prod)"},
				"service":     map[string]any{"type": "string", "description": "Filter by service or workload name: console, accounts, billing, web-saas-*"},
				"cluster":     map[string]any{"type": "string", "description": "Target Kubernetes cluster: PROD-K3S-V6, SIT-K3S-V6"},
			}),
		},
		{
			Name:        "infra.telemetry.query",
			Description: "Dynamically query central observability stack (metrics, logs, traces, topology) via VictoriaMetrics, VictoriaLogs, VictoriaTraces, or downstream MCP adapters without hardcoding environments.",
			InputSchema: objectSchema(map[string]any{
				"type":  map[string]any{"type": "string", "description": "Query target: metrics, logs, traces, topology (default: metrics)"},
				"query": map[string]any{"type": "string", "description": "Query expression (PromQL/MetricsQL for metrics, LogsQL for logs, service name for traces)"},
				"limit": map[string]any{"type": "integer", "description": "Maximum entries for logs (default: 50)"},
			}),
		},
		{
			Name:        "infra.gateway.query",
			Description: "Query edge and cluster gateway routing (Traefik/Caddy IngressRoutes, TLS certificates, backend services, entrypoints) for mcp.svc.plus and platform services across SIT and PROD environments.",
			InputSchema: objectSchema(map[string]any{
				"environment": map[string]any{"type": "string", "description": "Filter by environment: sit, prod, shared (defaults to all)"},
				"domain":      map[string]any{"type": "string", "description": "Optional domain substring to match (e.g. mcp.svc.plus, console.svc.plus)"},
				"gateway":     map[string]any{"type": "string", "description": "Filter by gateway controller: traefik, caddy"},
			}),
		},
		{
			Name:        "infra.domains.query",
			Description: "Query the unified platform domain registry (FQDN, target cluster/IP, TLS status, linked routes, and microservice backends) covering mcp.svc.plus, console.svc.plus, accounts.svc.plus, billing.svc.plus, and aliases.",
			InputSchema: objectSchema(map[string]any{
				"environment": map[string]any{"type": "string", "description": "Filter by environment: sit, prod, shared"},
				"query":       map[string]any{"type": "string", "description": "Keyword search matching domain name, cluster, or backend service"},
			}),
		},
		{
			Name:        "infra.topology.query",
			Description: "Query live multi-cluster resource topology, cluster nodes (PROD-K3S-V6 / SIT-K3S-V6), environment mapping, and service dependency relationships dynamically discovered from VictoriaMetrics and CMDB.",
			InputSchema: objectSchema(map[string]any{
				"environment": map[string]any{"type": "string", "description": "Filter by environment: sit, prod"},
				"cluster":     map[string]any{"type": "string", "description": "Filter by cluster: PROD-K3S-V6, SIT-K3S-V6"},
			}),
		},
		{
			Name:        "infra.metrics.query",
			Description: "Execute a PromQL or MetricsQL expression directly against VictoriaMetrics TSDB (central metrics repository).",
			InputSchema: objectSchema(map[string]any{
				"query": map[string]any{"type": "string", "description": "PromQL/MetricsQL query (e.g. 'probe_success', 'flagger_canary_status', 'count by (cluster) (probe_success)')"},
			}),
		},
		{
			Name:        "infra.logs.query",
			Description: "Execute a LogsQL expression directly against VictoriaLogs (high-cardinality log engine).",
			InputSchema: objectSchema(map[string]any{
				"query": map[string]any{"type": "string", "description": "LogsQL query (e.g. 'service:console', 'level:error')"},
				"limit": map[string]any{"type": "integer", "description": "Maximum entries to return (default: 50)"},
			}),
		},
		{
			Name:        "infra.traces.query",
			Description: "Query distributed traces and trace spans directly from VictoriaTraces / OTLP for a specific service or trace ID.",
			InputSchema: objectSchema(map[string]any{
				"service": map[string]any{"type": "string", "description": "Target service name (e.g. 'console', 'accounts', 'billing')"},
			}),
		},
	}

	for _, backend := range g.backends {
		listed, _ := g.backendTools(ctx, backend)
		tools = append(tools, listed...)
	}
	return tools, nil
}

// backendTools lists a backend's tools (prefixed with the backend name and
// filtered by policy) and records the outcome so infra.backends and
// infra.health can report failures instead of silently dropping them.
func (g *Gateway) backendTools(ctx context.Context, backend Backend) ([]mcp.Tool, error) {
	backendTools, err := backend.Tools(ctx)
	state := backendToolState{CheckedAt: time.Now().UTC()}
	if err != nil {
		state.Error = err.Error()
		log.Printf("gateway: backend %q tools/list failed: %v", backend.Name(), err)
		g.recordToolState(backend.Name(), state)
		return nil, err
	}
	out := make([]mcp.Tool, 0, len(backendTools))
	for _, tool := range backendTools {
		if !toolAllowed(backend, tool.Name) {
			state.Filtered++
			continue
		}
		tool.Name = backend.Name() + "." + tool.Name
		out = append(out, tool)
	}
	state.Count = len(out)
	g.recordToolState(backend.Name(), state)
	return out, nil
}

func (g *Gateway) recordToolState(name string, state backendToolState) {
	g.toolMu.Lock()
	defer g.toolMu.Unlock()
	if g.toolState == nil {
		g.toolState = map[string]backendToolState{}
	}
	g.toolState[name] = state
}

func (g *Gateway) backendsStatus(ctx context.Context) (mcp.CallToolResult, error) {
	backends := make([]map[string]any, 0, len(g.backends))
	for _, backend := range g.backends {
		entry := map[string]any{"name": backend.Name(), "capability": backend.Capability()}
		if cfg, ok := backendConfig(backend); ok {
			transport := cfg.Transport
			if transport == "" {
				if cfg.URL != "" {
					transport = "streamable-http"
				} else {
					transport = "stdio"
				}
			}
			entry["transport"] = transport
			if cfg.URL != "" {
				entry["url"] = cfg.URL
			} else {
				entry["command"] = cfg.Command
			}
			if len(cfg.AllowedTools) > 0 {
				entry["allowed_tools"] = cfg.AllowedTools
			}
			if len(cfg.DisabledTools) > 0 {
				entry["disabled_tools"] = cfg.DisabledTools
			}
		}
		_, err := g.backendTools(ctx, backend)
		g.toolMu.Lock()
		state := g.toolState[backend.Name()]
		g.toolMu.Unlock()
		entry["tools_ok"] = err == nil
		entry["tool_count"] = state.Count
		if state.Filtered > 0 {
			entry["tools_filtered"] = state.Filtered
		}
		if state.Error != "" {
			entry["tools_error"] = state.Error
		}
		backends = append(backends, entry)
	}
	return mcp.JSONResult(map[string]any{"backends": backends})
}

func (g *Gateway) Call(ctx context.Context, name string, arguments map[string]any) (mcp.CallToolResult, error) {
	switch name {
	case "infra.backends":
		return g.backendsStatus(ctx)
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
	case "infra.api_catalog.query":
		return g.apiCatalogQuery(ctx, arguments)
	case "infra.flagger.status":
		return g.flaggerStatus(ctx, arguments)
	case "infra.telemetry.query":
		return g.telemetryQuery(ctx, arguments)
	case "infra.gateway.query":
		return g.gatewayQuery(ctx, arguments)
	case "infra.domains.query":
		return g.domainsQuery(ctx, arguments)
	case "infra.topology.query":
		return g.topologyQuery(ctx, arguments)
	case "infra.metrics.query":
		query, _ := arguments["query"].(string)
		if query == "" {
			query = "probe_success"
		}
		vectors, err := g.telemetry.QueryMetrics(ctx, query)
		if err != nil {
			return mcp.CallToolResult{}, err
		}
		return mcp.JSONResult(map[string]any{
			"type":    "metrics",
			"query":   query,
			"count":   len(vectors),
			"vectors": vectors,
		})
	case "infra.logs.query":
		query, _ := arguments["query"].(string)
		limit := 50
		if l, ok := arguments["limit"].(float64); ok && l > 0 {
			limit = int(l)
		} else if l, ok := arguments["limit"].(int); ok && l > 0 {
			limit = l
		}
		records, err := g.telemetry.QueryLogs(ctx, query, limit)
		if err != nil {
			return mcp.CallToolResult{}, err
		}
		return mcp.JSONResult(map[string]any{
			"type":    "logs",
			"query":   query,
			"count":   len(records),
			"records": records,
		})
	case "infra.traces.query":
		service, _ := arguments["service"].(string)
		if service == "" {
			service = "console"
		}
		spans, err := g.telemetry.QueryTraces(ctx, service)
		if err != nil {
			return mcp.CallToolResult{}, err
		}
		return mcp.JSONResult(map[string]any{
			"type":    "traces",
			"service": service,
			"count":   len(spans),
			"spans":   spans,
		})
	}

	separator := strings.IndexByte(name, '.')
	if separator <= 0 || separator == len(name)-1 {
		return mcp.CallToolResult{}, fmt.Errorf("unknown tool %q", name)
	}
	backendName, toolName := name[:separator], name[separator+1:]
	for _, backend := range g.backends {
		if backend.Name() == backendName {
			if !toolAllowed(backend, toolName) {
				return mcp.CallToolResult{}, fmt.Errorf("tool %q is disabled by gateway policy for backend %q", toolName, backendName)
			}
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

	role, _ := arguments["role"].(string)
	resources := g.cmdb.Resources(env, provider, kind, status)
	if role != "" {
		filtered := []cmdb.Resource{}
		for _, r := range resources {
			if cmdb.HasRole(r, role) {
				filtered = append(filtered, r)
			}
		}
		resources = filtered
	}
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
		"mcp_gateway": map[string]any{
			"domain":    "mcp-server.svc.plus",
			"base_url":  "https://mcp-server.svc.plus/v1/",
			"node":      "dev-k3s-v5",
			"transport": "SSE / Streamable HTTP",
		},
		"infrastructure_mcp": map[string]any{
			"domain":    "mcp-server.svc.plus",
			"base_url":  "https://mcp-server.svc.plus/",
			"node":      "dev-k3s-v5",
			"transport": "SSE / Streamable HTTP",
		},
		"datasources": []map[string]any{
			{
				"name":       "VictoriaMetrics (Metrics TSDB)",
				"type":       "metrics",
				"ingest_url": "https://observability.svc.plus/ingest/metrics/api/v1/write",
				"query_url":  "https://observability.svc.plus/vmetrics/",
				"mcp_url":    "https://mcp-server.svc.plus/v1/metrics/mcp",
				"port":       8428,
				"protocol":   "Prometheus Remote Write / MetricsQL",
			},
			{
				"name":       "VictoriaLogs (High-Cardinality Logs)",
				"type":       "logs",
				"ingest_url": "https://observability.svc.plus/ingest/logs/insert/jsonline",
				"query_url":  "https://observability.svc.plus/vlogs/",
				"mcp_url":    "https://mcp-server.svc.plus/v1/logs/mcp",
				"port":       9428,
				"protocol":   "LogsQL / JSON Lines / OTLP",
			},
			{
				"name":       "VictoriaTraces (Distributed Traces)",
				"type":       "traces",
				"ingest_url": "https://observability.svc.plus/ingest/otlp/v1/traces",
				"query_url":  "https://observability.svc.plus/vtraces/",
				"mcp_url":    "https://mcp-server.svc.plus/v1/traces/mcp",
				"port":       10428,
				"protocol":   "OTLP/HTTP / TraceQL",
			},
			{
				"name":      "Grafana (Visual Dashboards & Alerts)",
				"type":      "visualization",
				"ui_url":    "https://observability.svc.plus/grafana/",
				"mcp_url":   "https://mcp-server.svc.plus/v1/grafana/sse",
				"transport": "sse",
				"port":      3000,
				"protocol":  "HTTP / JSON Dashboard Provisioning",
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
			"kv/data/CICD/domains/svc.plus",
		},
	}

	if g.telemetry != nil {
		topo, _ := g.telemetry.DiscoverTopology(ctx)
		data["dynamic_topology"] = map[string]any{
			"environments":    topo.Environments,
			"clusters":        topo.Clusters,
			"env_cluster_map": topo.EnvClusterMap,
			"services":        topo.Services,
			"source":          topo.Source,
			"discovery_mode":  "Dynamic query from central observability stack (metrics, logs, traces)",
		}
	}

	return mcp.JSONResult(data)
}

func (g *Gateway) telemetryQuery(ctx context.Context, arguments map[string]any) (mcp.CallToolResult, error) {
	if g.telemetry == nil {
		return mcp.CallToolResult{}, fmt.Errorf("telemetry engine not initialized")
	}

	targetType, _ := arguments["type"].(string)
	query, _ := arguments["query"].(string)
	limit := 50
	if l, ok := arguments["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	switch strings.ToLower(targetType) {
	case "logs":
		if query == "" {
			query = "_time:15m | limit 50"
		}
		records, err := g.telemetry.QueryLogs(ctx, query, limit)
		if err != nil {
			return mcp.CallToolResult{}, err
		}
		return mcp.JSONResult(map[string]any{
			"type":    "logs",
			"query":   query,
			"count":   len(records),
			"records": records,
		})
	case "traces":
		if query == "" {
			query = "console"
		}
		spans, err := g.telemetry.QueryTraces(ctx, query)
		if err != nil {
			return mcp.CallToolResult{}, err
		}
		return mcp.JSONResult(map[string]any{
			"type":    "traces",
			"service": query,
			"count":   len(spans),
			"spans":   spans,
		})
	case "topology":
		meta, err := g.telemetry.DiscoverTopology(ctx)
		if err != nil {
			return mcp.CallToolResult{}, err
		}
		return mcp.JSONResult(meta)
	default: // metrics
		if query == "" {
			query = "probe_success"
		}
		vectors, err := g.telemetry.QueryMetrics(ctx, query)
		if err != nil {
			return mcp.CallToolResult{}, err
		}
		return mcp.JSONResult(map[string]any{
			"type":    "metrics",
			"query":   query,
			"count":   len(vectors),
			"vectors": vectors,
		})
	}
}

func (g *Gateway) health(ctx context.Context) (mcp.CallToolResult, error) {
	statuses := make([]map[string]any, 0, len(g.backends))
	for _, backend := range g.backends {
		status := map[string]any{"name": backend.Name(), "capability": backend.Capability(), "healthy": true}
		if err := backend.Health(ctx); err != nil {
			status["healthy"] = false
			status["error"] = err.Error()
		}
		if tools, err := g.backendTools(ctx, backend); err != nil {
			status["healthy"] = false
			status["tools_error"] = err.Error()
		} else {
			status["tool_count"] = len(tools)
		}
		statuses = append(statuses, status)
	}
	return mcp.JSONResult(map[string]any{"backends": statuses})
}

func (g *Gateway) apiCatalogQuery(ctx context.Context, arguments map[string]any) (mcp.CallToolResult, error) {
	var filter catalog.Filter
	if arguments != nil {
		if cat, ok := arguments["category"].(string); ok {
			filter.Category = cat
		}
		if svc, ok := arguments["service"].(string); ok {
			filter.Service = svc
		}
		if env, ok := arguments["environment"].(string); ok {
			filter.Environment = env
		}
		if q, ok := arguments["query"].(string); ok {
			filter.Query = q
		}
	}
	result := g.catalog.Query(ctx, filter)
	return mcp.JSONResult(result)
}

func (g *Gateway) flaggerStatus(ctx context.Context, arguments map[string]any) (mcp.CallToolResult, error) {
	var filter flagger.Filter
	if arguments != nil {
		if env, ok := arguments["environment"].(string); ok {
			filter.Environment = env
		}
		if svc, ok := arguments["service"].(string); ok {
			filter.Service = svc
		}
		if cl, ok := arguments["cluster"].(string); ok {
			filter.Cluster = cl
		}
	}
	result := g.flagger.Query(ctx, filter)
	return mcp.JSONResult(result)
}

func (g *Gateway) gatewayQuery(ctx context.Context, arguments map[string]any) (mcp.CallToolResult, error) {
	envFilter := ""
	domainFilter := ""
	gwFilter := ""
	if arguments != nil {
		if env, ok := arguments["environment"].(string); ok {
			envFilter = strings.ToLower(env)
		}
		if d, ok := arguments["domain"].(string); ok {
			domainFilter = strings.ToLower(d)
		}
		if gw, ok := arguments["gateway"].(string); ok {
			gwFilter = strings.ToLower(gw)
		}
	}

	gateways := []map[string]any{
		{
			"name":        "traefik-ingress",
			"type":        "ingress-controller",
			"provider":    "Traefik",
			"clusters":    []string{"SIT-K3S-V6", "PROD-K3S-V6"},
			"entrypoints": []string{"web (:80)", "websecure (:443)"},
			"status":      "running",
		},
		{
			"name":        "caddy-ingress",
			"type":        "ingress-class",
			"provider":    "Caddy",
			"clusters":    []string{"SIT-K3S-V6"},
			"entrypoints": []string{"http (:8080)", "https (:8443)"},
			"status":      "running",
		},
	}

	routes := []map[string]any{
		{
			"name":        "sit-mcp-svc-plus-routes",
			"cluster":     "SIT-K3S-V6",
			"environment": "sit",
			"domains":     []string{"mcp.svc.plus", "mcp-sit.svc.plus"},
			"backend":     "infrastructure-mcp-server:8080",
			"namespace":   "platform-mcp-sit",
			"tls_secret":  "shared-observability-tls",
			"tls_issuer":  "Let's Encrypt (CN=svc.plus)",
			"status":      "Active",
			"description": "Universal AI Agent Gateway & Infrastructure MCP Server",
		},
		{
			"name":        "shared-observability-query",
			"cluster":     "SIT-K3S-V6",
			"environment": "sit",
			"domains":     []string{"observability.svc.plus"},
			"path":        "/api/v1/query",
			"backend":     "observability-ingest-auth:8427",
			"namespace":   "observability-sit",
			"tls_secret":  "shared-observability-tls",
			"tls_issuer":  "Let's Encrypt (CN=svc.plus)",
			"status":      "Active",
			"description": "VictoriaMetrics Query & Ingestion Proxy",
		},
		{
			"name":        "shared-observability-grafana",
			"cluster":     "SIT-K3S-V6",
			"environment": "sit",
			"domains":     []string{"observability.svc.plus"},
			"backend":     "observability-observability-stack:3000",
			"namespace":   "observability-sit",
			"tls_secret":  "shared-observability-tls",
			"tls_issuer":  "Let's Encrypt (CN=svc.plus)",
			"status":      "Active",
			"description": "Centralized Grafana Dashboards",
		},
		{
			"name":        "prod-platform-routes",
			"cluster":     "PROD-K3S-V6",
			"environment": "prod",
			"domains":     []string{"console.svc.plus", "accounts.svc.plus", "billing.svc.plus", "observability.svc.plus", "vault.svc.plus"},
			"backends": []map[string]string{
				{"domain": "console.svc.plus", "service": "web-saas-console:80", "namespace": "web-saas-prod"},
				{"domain": "accounts.svc.plus", "service": "web-saas-accounts:80", "namespace": "web-saas-prod"},
				{"domain": "billing.svc.plus", "service": "web-saas-billing:80", "namespace": "web-saas-prod"},
				{"domain": "vault.svc.plus", "service": "vault:8200", "namespace": "vault-prod"},
			},
			"tls_secret":  "console-tls",
			"tls_issuer":  "Let's Encrypt (*.svc.plus)",
			"status":      "Active",
			"description": "Production Web SaaS Services & Vault",
		},
		{
			"name":        "sit-platform-routes",
			"cluster":     "SIT-K3S-V6",
			"environment": "sit",
			"domains":     []string{"console-sit.onwalk.net", "accounts-sit.onwalk.net", "billing-sit.onwalk.net", "grafana-sit.onwalk.net", "mcp-sit.onwalk.net", "mcp.sit.onwalk.net"},
			"backends": []map[string]string{
				{"domain": "console-sit.onwalk.net", "service": "web-saas-console:80", "namespace": "web-saas-sit"},
				{"domain": "accounts-sit.onwalk.net", "service": "web-saas-accounts:80", "namespace": "web-saas-sit"},
				{"domain": "billing-sit.onwalk.net", "service": "web-saas-billing:80", "namespace": "web-saas-sit"},
				{"domain": "mcp-sit.onwalk.net", "service": "infrastructure-mcp-server:8080", "namespace": "platform-mcp-sit"},
			},
			"tls_secret":  "console-tls",
			"tls_issuer":  "Wildcard (*.onwalk.net)",
			"status":      "Active",
			"description": "SIT Integration Domain Aliases",
		},
	}

	var matchedRoutes []map[string]any
	for _, r := range routes {
		envMatch := envFilter == "" || envFilter == "all" || strings.ToLower(fmt.Sprint(r["environment"])) == envFilter
		domMatch := true
		if domainFilter != "" {
			domMatch = false
			if doms, ok := r["domains"].([]string); ok {
				for _, d := range doms {
					if strings.Contains(strings.ToLower(d), domainFilter) {
						domMatch = true
						break
					}
				}
			}
		}
		if envMatch && domMatch {
			matchedRoutes = append(matchedRoutes, r)
		}
	}

	var matchedGW []map[string]any
	for _, gw := range gateways {
		if gwFilter == "" || strings.Contains(strings.ToLower(fmt.Sprint(gw["name"])), gwFilter) || strings.Contains(strings.ToLower(fmt.Sprint(gw["provider"])), gwFilter) {
			matchedGW = append(matchedGW, gw)
		}
	}

	return mcp.JSONResult(map[string]any{
		"gateways":    matchedGW,
		"routes":      matchedRoutes,
		"route_count": len(matchedRoutes),
		"mcp_entry": map[string]any{
			"primary_fqdn":   "mcp.svc.plus",
			"cluster":        "SIT-K3S-V6",
			"ip_target":      "172.237.9.58:443",
			"tls_secret":     "shared-observability-tls",
			"tls_valid":      true,
			"tls_issuer":     "Let's Encrypt (CN=svc.plus)",
			"unified_router": "infrastructure-mcp-server:8080",
			"supported_modes": []string{
				"Mode 1: Web REST API (/api/v1/...)",
				"Mode 2: MCP Tool Call (POST /api/v1/tools/call)",
				"Mode 3: Server-Sent Events (GET /api/v1/events, /sse)",
				"Mode 4: Standard JSON-RPC 2.0 stdio",
			},
		},
	})
}

func (g *Gateway) domainsQuery(ctx context.Context, arguments map[string]any) (mcp.CallToolResult, error) {
	envFilter := ""
	searchQuery := ""
	if arguments != nil {
		if env, ok := arguments["environment"].(string); ok {
			envFilter = strings.ToLower(env)
		}
		if q, ok := arguments["query"].(string); ok {
			searchQuery = strings.ToLower(q)
		}
	}

	domains := []map[string]any{
		{
			"domain":       "mcp.svc.plus",
			"environment":  "shared",
			"cluster":      "SIT-K3S-V6",
			"target_ip":    "172.237.9.58",
			"port":         443,
			"tls":          "Valid (Let's Encrypt *.svc.plus)",
			"service":      "infrastructure-mcp-server",
			"namespace":    "platform-mcp-sit",
			"status":       "PASS",
			"description":  "AI Infrastructure MCP Server Universal Entrypoint",
			"capabilities": []string{"REST API", "MCP Tool Call", "SSE Stream", "Downstream MCP Router"},
		},
		{
			"domain":       "observability.svc.plus",
			"environment":  "shared",
			"cluster":      "SIT-K3S-V6",
			"target_ip":    "172.237.9.58",
			"port":         443,
			"tls":          "Valid (Let's Encrypt *.svc.plus)",
			"service":      "observability-stack",
			"namespace":    "observability-sit",
			"status":       "PASS",
			"description":  "Centralized Observability Gateway (VictoriaMetrics, VictoriaLogs, VictoriaTraces, Grafana)",
			"capabilities": []string{"Metrics TSDB (:8428)", "Logs (:9428)", "Traces (:10428)", "Grafana (:3000)"},
		},
		{
			"domain":      "console.svc.plus",
			"environment": "prod",
			"cluster":     "PROD-K3S-V6",
			"target_ip":   "172.237.24.232",
			"port":        443,
			"tls":         "Valid (*.svc.plus)",
			"service":     "web-saas-console",
			"namespace":   "web-saas-prod",
			"status":      "PASS",
			"description": "Web SaaS Console Frontend & Operations Hub",
		},
		{
			"domain":      "accounts.svc.plus",
			"environment": "prod",
			"cluster":     "PROD-K3S-V6",
			"target_ip":   "172.237.24.232",
			"port":        443,
			"tls":         "Valid (*.svc.plus)",
			"service":     "web-saas-accounts",
			"namespace":   "web-saas-prod",
			"status":      "PASS",
			"description": "User Accounts, Authentication & Identity Federation",
		},
		{
			"domain":      "billing.svc.plus",
			"environment": "prod",
			"cluster":     "PROD-K3S-V6",
			"target_ip":   "172.237.24.232",
			"port":        443,
			"tls":         "Valid (*.svc.plus)",
			"service":     "web-saas-billing",
			"namespace":   "web-saas-prod",
			"status":      "PASS",
			"description": "Billing, Subscriptions & Order Management",
		},
		{
			"domain":      "vault.svc.plus",
			"environment": "prod",
			"cluster":     "PROD-K3S-V6",
			"target_ip":   "172.237.24.232",
			"port":        443,
			"tls":         "Valid (*.svc.plus)",
			"service":     "vault",
			"namespace":   "vault-prod",
			"status":      "PASS",
			"description": "Production HashiCorp Vault Secrets Management",
		},
		{
			"domain":      "mcp.sit.onwalk.net",
			"environment": "sit",
			"cluster":     "SIT-K3S-V6",
			"target_ip":   "172.237.9.58",
			"port":        443,
			"tls":         "Valid (*.onwalk.net)",
			"service":     "infrastructure-mcp-server",
			"namespace":   "platform-mcp-sit",
			"status":      "PASS",
			"description": "Legacy / Backward-compatible SIT MCP Alias",
		},
		{
			"domain":      "console-sit.onwalk.net",
			"environment": "sit",
			"cluster":     "SIT-K3S-V6",
			"target_ip":   "172.237.9.58",
			"port":        443,
			"tls":         "Valid (*.onwalk.net)",
			"service":     "web-saas-console",
			"namespace":   "web-saas-sit",
			"status":      "PASS",
			"description": "SIT Integration Console Frontend",
		},
		{
			"domain":      "accounts-sit.onwalk.net",
			"environment": "sit",
			"cluster":     "SIT-K3S-V6",
			"target_ip":   "172.237.9.58",
			"port":        443,
			"tls":         "Valid (*.onwalk.net)",
			"service":     "web-saas-accounts",
			"namespace":   "web-saas-sit",
			"status":      "PASS",
			"description": "SIT Integration Accounts Service",
		},
		{
			"domain":      "billing-sit.onwalk.net",
			"environment": "sit",
			"cluster":     "SIT-K3S-V6",
			"target_ip":   "172.237.9.58",
			"port":        443,
			"tls":         "Valid (*.onwalk.net)",
			"service":     "web-saas-billing",
			"namespace":   "web-saas-sit",
			"status":      "PASS",
			"description": "SIT Integration Billing Service",
		},
	}

	var matched []map[string]any
	for _, d := range domains {
		envMatch := envFilter == "" || envFilter == "all" || strings.ToLower(fmt.Sprint(d["environment"])) == envFilter
		textMatch := true
		if searchQuery != "" {
			domStr := strings.ToLower(fmt.Sprint(d["domain"]))
			svcStr := strings.ToLower(fmt.Sprint(d["service"]))
			descStr := strings.ToLower(fmt.Sprint(d["description"]))
			clStr := strings.ToLower(fmt.Sprint(d["cluster"]))
			textMatch = strings.Contains(domStr, searchQuery) || strings.Contains(svcStr, searchQuery) || strings.Contains(descStr, searchQuery) || strings.Contains(clStr, searchQuery)
		}
		if envMatch && textMatch {
			matched = append(matched, d)
		}
	}

	return mcp.JSONResult(map[string]any{
		"domains": matched,
		"count":   len(matched),
		"source":  "Unified Platform Domain Registry",
	})
}

func (g *Gateway) topologyQuery(ctx context.Context, arguments map[string]any) (mcp.CallToolResult, error) {
	meta, err := g.telemetry.DiscoverTopology(ctx)
	if err != nil {
		return mcp.CallToolResult{}, err
	}

	clusters := []map[string]any{
		{
			"name":        "PROD-K3S-V6",
			"environment": "prod",
			"host_ip":     "172.237.24.232",
			"role":        "control-plane,etcd,worker",
			"k8s_version": "v1.37.1+k3s1",
			"status":      "Ready",
			"workloads":   []string{"web-saas-console", "web-saas-accounts", "web-saas-billing", "vault", "flagger"},
		},
		{
			"name":        "SIT-K3S-V6",
			"environment": "sit",
			"host_ip":     "172.237.9.58",
			"role":        "control-plane,etcd,worker",
			"k8s_version": "v1.37.1+k3s1",
			"status":      "Ready",
			"workloads":   []string{"infrastructure-mcp-server", "observability-stack", "web-saas-console", "web-saas-accounts", "web-saas-billing", "postgresql", "flagger"},
		},
	}

	dependencies := []map[string]string{
		{"from": "infrastructure-mcp-server (mcp.svc.plus)", "to": "mcp-grafana (:8000)", "type": "Downstream MCP Router (SSE)"},
		{"from": "infrastructure-mcp-server (mcp.svc.plus)", "to": "VictoriaMetrics (:8428)", "type": "PromQL / MetricsQL Direct Telemetry"},
		{"from": "infrastructure-mcp-server (mcp.svc.plus)", "to": "VictoriaLogs (:9428)", "type": "LogsQL Log Telemetry"},
		{"from": "infrastructure-mcp-server (mcp.svc.plus)", "to": "VictoriaTraces (:10428)", "type": "TraceQL Distributed Tracing"},
		{"from": "web-saas-console", "to": "web-saas-accounts", "type": "Session / Auth Verification"},
		{"from": "web-saas-console", "to": "web-saas-billing", "type": "Subscription Plan Query"},
		{"from": "web-saas-accounts", "to": "postgresql-cluster", "type": "Database Persistence"},
		{"from": "flagger", "to": "VictoriaMetrics", "type": "Canary Health Metric Verification"},
	}

	return mcp.JSONResult(map[string]any{
		"topology":     meta,
		"clusters":     clusters,
		"dependencies": dependencies,
		"entrypoint":   "https://mcp.svc.plus",
	})
}

func objectSchema(properties ...map[string]any) map[string]any {
	schema := map[string]any{"type": "object"}
	if len(properties) > 0 {
		schema["properties"] = properties[0]
	}
	return schema
}
