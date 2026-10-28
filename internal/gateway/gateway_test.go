package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/config"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/graph"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/mcp"
)

func TestGatewayBuiltinTools(t *testing.T) {
	cfg := config.Default()
	store := graph.NewStore()
	gw := New(cfg, store)
	ctx := context.Background()

	tools, err := gw.Tools(ctx)
	if err != nil {
		t.Fatalf("unexpected tools error: %v", err)
	}

	foundStatus := false
	foundBackends := false
	foundGraph := false
	for _, tool := range tools {
		if tool.Name == "infra.observability.status" {
			foundStatus = true
		}
		if tool.Name == "infra.backends" {
			foundBackends = true
		}
		if tool.Name == "infra.graph.query" {
			foundGraph = true
		}
	}
	if !foundStatus || !foundBackends || !foundGraph {
		t.Fatalf("expected all builtin tools, got: %#v", tools)
	}

	// Test infra.backends call
	res, err := gw.Call(ctx, "infra.backends", nil)
	if err != nil {
		t.Fatalf("failed to call infra.backends: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatal("expected content in infra.backends result")
	}

	// Test infra.observability.status call
	obsRes, err := gw.Call(ctx, "infra.observability.status", nil)
	if err != nil {
		t.Fatalf("failed to call infra.observability.status: %v", err)
	}
	var obsData map[string]any
	if err := json.Unmarshal([]byte(obsRes.Content[0].Text), &obsData); err != nil {
		t.Fatalf("failed to parse observability status JSON: %v", err)
	}
	if obsData["service"] != "observability.svc.plus" {
		t.Fatalf("unexpected service: %v", obsData["service"])
	}

	// Test infra.graph.query call
	graphRes, err := gw.Call(ctx, "infra.graph.query", map[string]any{"query": "observability"})
	if err != nil {
		t.Fatalf("failed to call infra.graph.query: %v", err)
	}
	if len(graphRes.Content) == 0 {
		t.Fatal("expected content in infra.graph.query result")
	}
}

func TestGatewayWithHTTPBackend(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req mcp.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(mcp.Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]any{
					"protocolVersion": "2025-06-18",
					"serverInfo":      map[string]string{"name": "remote-mock", "version": "1.0.0"},
				},
			})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(mcp.Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]any{
					"tools": []mcp.Tool{
						{Name: "search_dashboards", Description: "Search Grafana dashboards", InputSchema: map[string]any{"type": "object"}},
					},
				},
			})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(mcp.Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: mcp.CallToolResult{
					Content: []mcp.ContentBlock{{Type: "text", Text: `[{"uid":"main","title":"Main Overview"}]`}},
				},
			})
		case "ping":
			_ = json.NewEncoder(w).Encode(mcp.Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
		}
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Backends = []config.BackendConfig{
		{
			Name:       "grafana",
			Capability: "observability",
			URL:        server.URL,
		},
	}
	store := graph.NewStore()
	gw := New(cfg, store)
	ctx := context.Background()

	tools, err := gw.Tools(ctx)
	if err != nil {
		t.Fatalf("unexpected tools error: %v", err)
	}
	foundGrafanaTool := false
	for _, tool := range tools {
		if tool.Name == "grafana.search_dashboards" {
			foundGrafanaTool = true
			break
		}
	}
	if !foundGrafanaTool {
		t.Fatalf("expected grafana.search_dashboards tool, got: %#v", tools)
	}

	callRes, err := gw.Call(ctx, "grafana.search_dashboards", map[string]any{"query": "Main"})
	if err != nil {
		t.Fatalf("failed to call grafana.search_dashboards: %v", err)
	}
	if len(callRes.Content) == 0 || callRes.Content[0].Text != `[{"uid":"main","title":"Main Overview"}]` {
		t.Fatalf("unexpected call result: %#v", callRes)
	}
}
