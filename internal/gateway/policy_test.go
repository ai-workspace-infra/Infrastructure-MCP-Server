package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/config"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/graph"
	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/mcp"
)

func sshLikeBackend(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req mcp.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var result any = map[string]any{}
		switch req.Method {
		case "tools/list":
			result = map[string]any{"tools": []mcp.Tool{
				{Name: "execute-command", InputSchema: map[string]any{"type": "object"}},
				{Name: "write-file", InputSchema: map[string]any{"type": "object"}},
				{Name: "upload", InputSchema: map[string]any{"type": "object"}},
				{Name: "list-servers", InputSchema: map[string]any{"type": "object"}},
			}}
		case "tools/call":
			result = mcp.CallToolResult{Content: []mcp.ContentBlock{{Type: "text", Text: "ok"}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
}

func TestGatewayToolPolicyBlocksWriteTools(t *testing.T) {
	ts := sshLikeBackend(t)
	defer ts.Close()
	cfg := config.Default()
	cfg.Backends = []config.BackendConfig{{Name: "ssh", Capability: "runtime", URL: ts.URL, DisabledTools: []string{"write-file", "upload"}}}
	gw := New(cfg, graph.NewStore())
	ctx := context.Background()

	tools, err := gw.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	if !names["ssh.execute-command"] || !names["ssh.list-servers"] {
		t.Fatalf("allowed ssh tools missing: %v", names)
	}
	if names["ssh.write-file"] || names["ssh.upload"] {
		t.Fatalf("disabled tools leaked into tools/list: %v", names)
	}
	if _, err := gw.Call(ctx, "ssh.write-file", map[string]any{"path": "/etc/x"}); err == nil || !strings.Contains(err.Error(), "disabled by gateway policy") {
		t.Fatalf("expected policy error, got %v", err)
	}
	if res, err := gw.Call(ctx, "ssh.execute-command", map[string]any{"cmdString": "uptime"}); err != nil || res.Content[0].Text != "ok" {
		t.Fatalf("allowed call failed: %v", err)
	}
}

func TestGatewaySurfacesBackendToolErrors(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer broken.Close()
	cfg := config.Default()
	cfg.Backends = []config.BackendConfig{{Name: "observability-metrics", Capability: "observability", URL: broken.URL, TimeoutSeconds: 3}}
	gw := New(cfg, graph.NewStore())
	ctx := context.Background()

	res, err := gw.Call(ctx, "infra.backends", nil)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, `"tools_ok":false`) || !strings.Contains(text, "tools_error") || !strings.Contains(text, "HTTP 500") {
		t.Fatalf("infra.backends should report the tools/list failure, got %s", text)
	}
	res, err = gw.Call(ctx, "infra.health", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content[0].Text, `"healthy":false`) {
		t.Fatalf("infra.health should be unhealthy: %s", res.Content[0].Text)
	}
}
