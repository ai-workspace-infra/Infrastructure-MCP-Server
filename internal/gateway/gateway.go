package gateway

import (
	"context"
	"fmt"
	"strings"

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
			backends = append(backends, mcp.NewProcessClient(backendCfg))
		}
	}
	return &Gateway{backends: backends, graph: graphStore}
}

func (g *Gateway) Tools(ctx context.Context) ([]mcp.Tool, error) {
	tools := []mcp.Tool{
		{Name: "infra.backends", Description: "List configured Infrastructure MCP backends and their capabilities.", InputSchema: objectSchema()},
		{Name: "infra.graph.query", Description: "Query the Environment Graph for infrastructure nodes and relationships.", InputSchema: objectSchema(map[string]any{"query": map[string]any{"type": "string", "description": "Optional substring to match node id, kind, or name."}})},
		{Name: "infra.health", Description: "Check connectivity to configured Infrastructure MCP backends.", InputSchema: objectSchema()},
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
