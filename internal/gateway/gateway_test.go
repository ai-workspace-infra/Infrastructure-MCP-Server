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

	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Name] = true
	}

	expected := []string{
		"infra.backends",
		"infra.graph.query",
		"infra.health",
		"infra.observability.status",
		"infra.cmdb.query",
		"infra.cmdb.summary",
		"infra.cmdb.sync",
		"infra.cli.exec",
	}
	for _, exp := range expected {
		if !toolNames[exp] {
			t.Fatalf("missing expected tool: %s", exp)
		}
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

func TestGatewayCMDBAndCLITools(t *testing.T) {
	cfg := config.Default()
	store := graph.NewStore()
	gw := New(cfg, store)
	ctx := context.Background()

	// 1. Test infra.cmdb.summary
	sumRes, err := gw.Call(ctx, "infra.cmdb.summary", nil)
	if err != nil {
		t.Fatalf("failed to call infra.cmdb.summary: %v", err)
	}
	var sumData map[string]any
	if err := json.Unmarshal([]byte(sumRes.Content[0].Text), &sumData); err != nil {
		t.Fatalf("parse summary: %v", err)
	}
	summaries, ok := sumData["summaries"].([]any)
	if !ok || len(summaries) != 3 {
		t.Fatalf("expected 3 summaries (sit, uat, prod), got %#v", sumData)
	}

	// 2. Test infra.cmdb.query with node_id for service/observability-gateway
	nodeRes, err := gw.Call(ctx, "infra.cmdb.query", map[string]any{
		"node_id": "service/observability-gateway",
	})
	if err != nil {
		t.Fatalf("failed to query service/observability-gateway: %v", err)
	}
	var nodeData map[string]any
	if err := json.Unmarshal([]byte(nodeRes.Content[0].Text), &nodeData); err != nil {
		t.Fatalf("parse node query: %v", err)
	}
	if nodeData["found"] != true {
		t.Fatalf("expected node to be found: %#v", nodeData)
	}
	edgeCount, _ := nodeData["edge_count"].(float64)
	if int(edgeCount) != 15 {
		t.Fatalf("expected 15 edges for service/observability-gateway, got %v", edgeCount)
	}

	// 3. Test infra.cmdb.query with environment filter
	uatRes, err := gw.Call(ctx, "infra.cmdb.query", map[string]any{
		"environment": "uat",
	})
	if err != nil {
		t.Fatalf("failed to query UAT resources: %v", err)
	}
	var uatData map[string]any
	if err := json.Unmarshal([]byte(uatRes.Content[0].Text), &uatData); err != nil {
		t.Fatalf("parse uat data: %v", err)
	}
	if uatData["total"].(float64) == 0 {
		t.Fatal("expected uat resources")
	}

	// 4. Test infra.cmdb.sync
	syncRes, err := gw.Call(ctx, "infra.cmdb.sync", nil)
	if err != nil {
		t.Fatalf("failed to call sync: %v", err)
	}
	var syncData map[string]any
	if err := json.Unmarshal([]byte(syncRes.Content[0].Text), &syncData); err != nil {
		t.Fatalf("parse sync: %v", err)
	}
	if syncData["total_synced"].(float64) == 0 {
		t.Fatal("expected total_synced > 0")
	}

	// 5. Test infra.cli.exec safety guardrails
	// Should reject disallowed command
	_, err = gw.Call(ctx, "infra.cli.exec", map[string]any{
		"command": "rm",
		"args":    []string{"-rf", "/"},
	})
	if err == nil {
		t.Fatal("expected error for disallowed command 'rm'")
	}

	// Should reject destructive args
	_, err = gw.Call(ctx, "infra.cli.exec", map[string]any{
		"command": "gh",
		"args":    []string{"repo", "delete", "test"},
	})
	if err == nil {
		t.Fatal("expected safety violation error for 'delete'")
	}

	// Should allow read-only commands
	cliRes, err := gw.Call(ctx, "infra.cli.exec", map[string]any{
		"command": "curl",
		"args":    []string{"--version"},
	})
	if err != nil {
		t.Fatalf("unexpected error for curl --version: %v", err)
	}
	var cliData map[string]any
	if err := json.Unmarshal([]byte(cliRes.Content[0].Text), &cliData); err != nil {
		t.Fatalf("parse cli response: %v", err)
	}
	if cliData["exit_code"].(float64) != 0 {
		t.Fatalf("curl --version exited non-zero: %#v", cliData)
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
