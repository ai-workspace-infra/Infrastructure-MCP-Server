package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/config"
)

func TestHTTPClientToolsAndCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]any{
					"protocolVersion": "2025-06-18",
					"serverInfo":      map[string]string{"name": "mock-mcp", "version": "1.0.0"},
				},
			})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]any{
					"tools": []Tool{
						{Name: "query_metric", Description: "Query a Prometheus metric", InputSchema: map[string]any{"type": "object"}},
					},
				},
			})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: CallToolResult{
					Content: []ContentBlock{{Type: "text", Text: "metric_result: 42"}},
				},
			})
		case "ping":
			_ = json.NewEncoder(w).Encode(Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  map[string]any{},
			})
		default:
			http.Error(w, "unknown method", http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := config.BackendConfig{
		Name:       "test-metrics",
		Capability: "observability",
		URL:        server.URL,
	}
	client := NewHTTPClient(cfg)
	ctx := context.Background()

	tools, err := client.Tools(ctx)
	if err != nil {
		t.Fatalf("unexpected tools error: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "query_metric" {
		t.Fatalf("unexpected tools returned: %#v", tools)
	}

	result, err := client.Call(ctx, "query_metric", map[string]any{"query": "up"})
	if err != nil {
		t.Fatalf("unexpected call error: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "metric_result: 42" {
		t.Fatalf("unexpected call result: %#v", result)
	}

	if err := client.Health(ctx); err != nil {
		t.Fatalf("unexpected health error: %v", err)
	}
}
