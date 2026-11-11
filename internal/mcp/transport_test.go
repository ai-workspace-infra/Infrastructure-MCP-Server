package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/config"
)

func rpcResult(id json.RawMessage, result any) []byte {
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	return data
}

// sessionfulServer mimics the Victoria MCP servers: initialize issues an
// Mcp-Session-Id, every later request must carry it, and unknown sessions
// get HTTP 404.
type sessionfulServer struct {
	mu          sync.Mutex
	sessions    map[string]bool
	initialized map[string]bool
	inits       int32
	sseReplies  bool
	protoSeen   string
}

func (s *sessionfulServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		http.Error(w, "client must accept text/event-stream", http.StatusNotAcceptable)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Method == "initialize" {
		n := atomic.AddInt32(&s.inits, 1)
		sid := fmt.Sprintf("mcp-session-test-%d", n)
		s.sessions[sid] = true
		w.Header().Set("Mcp-Session-Id", sid)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(rpcResult(req.ID, map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]string{"name": "VictoriaMetrics"}}))
		return
	}
	sid := r.Header.Get("Mcp-Session-Id")
	if sid == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}
	if !s.sessions[sid] {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if req.Method == "notifications/initialized" {
		s.initialized[sid] = true
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if !s.initialized[sid] {
		http.Error(w, "not initialized", http.StatusBadRequest)
		return
	}
	s.protoSeen = r.Header.Get("MCP-Protocol-Version")
	var result any
	switch req.Method {
	case "tools/list":
		result = map[string]any{"tools": []Tool{{Name: "query", InputSchema: map[string]any{"type": "object"}}, {Name: "alerts", InputSchema: map[string]any{"type": "object"}}}}
	case "tools/call":
		result = CallToolResult{Content: []ContentBlock{{Type: "text", Text: "up=1"}}}
	case "ping":
		result = map[string]any{}
	default:
		http.Error(w, "unknown", http.StatusBadRequest)
		return
	}
	if s.sseReplies {
		w.Header().Set("Content-Type", "text/event-stream")
		// a server notification first, then the actual response
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", `{"jsonrpc":"2.0","method":"notifications/progress","params":{}}`)
		fmt.Fprintf(w, ": keepalive\n\n")
		fmt.Fprintf(w, "event: message\nid: 1\ndata: %s\n\n", rpcResult(req.ID, result))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(rpcResult(req.ID, result))
}

func newSessionfulServer(sse bool) *sessionfulServer {
	return &sessionfulServer{sessions: map[string]bool{}, initialized: map[string]bool{}, sseReplies: sse}
}

func TestStreamableHTTPSessionful(t *testing.T) {
	mock := newSessionfulServer(false)
	ts := httptest.NewServer(mock)
	defer ts.Close()

	client := NewHTTPClient(config.BackendConfig{Name: "vm", URL: ts.URL, Transport: "streamable-http"})
	ctx := context.Background()
	tools, err := client.Tools(ctx)
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("want 2 tools, got %d", len(tools))
	}
	if client.SessionID() != "mcp-session-test-1" {
		t.Fatalf("session id not stored: %q", client.SessionID())
	}
	res, err := client.Call(ctx, "query", map[string]any{"query": "up"})
	if err != nil || len(res.Content) != 1 || res.Content[0].Text != "up=1" {
		t.Fatalf("call: %v %#v", err, res)
	}
	if mock.protoSeen != "2025-06-18" {
		t.Fatalf("MCP-Protocol-Version header not sent, got %q", mock.protoSeen)
	}
	if atomic.LoadInt32(&mock.inits) != 1 {
		t.Fatalf("expected a single initialize, got %d", mock.inits)
	}
}

func TestStreamableHTTPSessionExpiryReinitializes(t *testing.T) {
	mock := newSessionfulServer(false)
	ts := httptest.NewServer(mock)
	defer ts.Close()

	client := NewHTTPClient(config.BackendConfig{Name: "vl", URL: ts.URL})
	ctx := context.Background()
	if _, err := client.Tools(ctx); err != nil {
		t.Fatalf("tools: %v", err)
	}
	// server restarts and forgets every session
	mock.mu.Lock()
	mock.sessions = map[string]bool{}
	mock.mu.Unlock()

	if _, err := client.Call(ctx, "query", nil); err != nil {
		t.Fatalf("call after expiry should re-initialize: %v", err)
	}
	if got := atomic.LoadInt32(&mock.inits); got != 2 {
		t.Fatalf("expected 2 initialize calls, got %d", got)
	}
	if client.SessionID() != "mcp-session-test-2" {
		t.Fatalf("expected new session, got %q", client.SessionID())
	}
}

func TestStreamableHTTPSSEFramedReply(t *testing.T) {
	mock := newSessionfulServer(true)
	ts := httptest.NewServer(mock)
	defer ts.Close()

	client := NewHTTPClient(config.BackendConfig{Name: "vt", URL: ts.URL})
	ctx := context.Background()
	tools, err := client.Tools(ctx)
	if err != nil || len(tools) != 2 {
		t.Fatalf("tools via SSE-framed reply: %v (%d)", err, len(tools))
	}
	res, err := client.Call(ctx, "query", nil)
	if err != nil || res.Content[0].Text != "up=1" {
		t.Fatalf("call via SSE-framed reply: %v %#v", err, res)
	}
	if err := client.Health(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}
}

// legacySSEServer mimics mcp-grafana behind a path-rewriting proxy: the
// public stream lives at /mcp/v1/grafana/sse but the endpoint event
// announces /v1/grafana/message?sessionid=... (no /mcp prefix).
type legacySSEServer struct {
	mu      sync.Mutex
	streams map[string]chan []byte
	posts   int32
}

func (s *legacySSEServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/mcp/v1/grafana/sse":
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		sid := fmt.Sprintf("S%d", time.Now().UnixNano())
		ch := make(chan []byte, 16)
		s.mu.Lock()
		s.streams[sid] = ch
		s.mu.Unlock()
		fmt.Fprintf(w, "event: endpoint\ndata: /v1/grafana/message?sessionid=%s\n\n", sid)
		flusher.Flush()
		for {
			select {
			case msg := <-ch:
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	case r.Method == http.MethodPost && r.URL.Path == "/mcp/v1/grafana/message":
		atomic.AddInt32(&s.posts, 1)
		s.mu.Lock()
		ch := s.streams[r.URL.Query().Get("sessionid")]
		s.mu.Unlock()
		if ch == nil {
			http.Error(w, "unknown session", http.StatusNotFound)
			return
		}
		var req Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(http.StatusAccepted)
		if len(req.ID) == 0 {
			return
		}
		var result any = map[string]any{}
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05"}
		case "tools/list":
			result = map[string]any{"tools": []Tool{{Name: "search_dashboards", InputSchema: map[string]any{"type": "object"}}}}
		case "tools/call":
			result = CallToolResult{Content: []ContentBlock{{Type: "text", Text: "dashboards: 3"}}}
		}
		ch <- rpcResult(req.ID, result)
	default:
		http.NotFound(w, r)
	}
}

func TestLegacySSETransport(t *testing.T) {
	mock := &legacySSEServer{streams: map[string]chan []byte{}}
	ts := httptest.NewServer(mock)
	defer ts.Close()

	client := NewHTTPClient(config.BackendConfig{Name: "grafana", URL: ts.URL + "/mcp/v1/grafana/sse", Transport: "sse", TimeoutSeconds: 5})
	defer client.Close()
	ctx := context.Background()
	tools, err := client.Tools(ctx)
	if err != nil || len(tools) != 1 || tools[0].Name != "search_dashboards" {
		t.Fatalf("legacy sse tools: %v %#v", err, tools)
	}
	res, err := client.Call(ctx, "search_dashboards", map[string]any{"query": "node"})
	if err != nil || res.Content[0].Text != "dashboards: 3" {
		t.Fatalf("legacy sse call: %v %#v", err, res)
	}
	if client.SessionID() == "" {
		t.Fatalf("expected session id parsed from endpoint")
	}
}

func TestResolveSSEEndpoint(t *testing.T) {
	cases := []struct{ stream, announced, want string }{
		{"https://o.example/mcp/v1/grafana/sse", "/v1/grafana/sse?sessionid=X", "https://o.example/mcp/v1/grafana/sse?sessionid=X"},
		{"https://o.example/mcp/v1/grafana/sse", "/v1/grafana/message?sessionid=X", "https://o.example/mcp/v1/grafana/message?sessionid=X"},
		{"http://svc:8000/v1/grafana/sse", "/v1/grafana/message?sessionid=X", "http://svc:8000/v1/grafana/message?sessionid=X"},
		{"http://svc:8000/sse", "/message?sessionId=1", "http://svc:8000/message?sessionId=1"},
		{"http://svc:8000/sse", "https://other/m?s=1", "https://other/m?s=1"},
	}
	for _, tc := range cases {
		got, err := resolveSSEEndpoint(tc.stream, tc.announced)
		if err != nil || got != tc.want {
			t.Errorf("resolve(%q,%q) = %q, %v; want %q", tc.stream, tc.announced, got, err, tc.want)
		}
	}
}

func TestHTTPServerAuthMiddleware(t *testing.T) {
	httpSrv := NewHTTPServer(NewServer(&dummyProvider{}), "")
	httpSrv.SetAuthToken("s3cret")
	ts := httptest.NewServer(httpSrv.Handler())
	defer ts.Close()

	get := func(path, token string) int {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := get("/healthz", ""); code != http.StatusOK {
		t.Fatalf("/healthz must stay public, got %d", code)
	}
	for _, p := range []string{"/api/v1/tools", "/v1/tools"} {
		if code := get(p, ""); code != http.StatusUnauthorized {
			t.Fatalf("%s without token: want 401, got %d", p, code)
		}
		if code := get(p, "wrong"); code != http.StatusUnauthorized {
			t.Fatalf("%s wrong token: want 401, got %d", p, code)
		}
		if code := get(p, "s3cret"); code != http.StatusOK {
			t.Fatalf("%s with token: want 200, got %d", p, code)
		}
	}
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	resp, err := http.Post(ts.URL+"/mcp", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/mcp without token: want 401, got %d", resp.StatusCode)
	}

	// No token configured: legacy behaviour (open) is preserved.
	open := httptest.NewServer(NewHTTPServer(NewServer(&dummyProvider{}), "").Handler())
	defer open.Close()
	r2, err := http.Get(open.URL + "/api/v1/tools")
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("open server should still answer 200, got %d", r2.StatusCode)
	}
}

func TestProcessClientTimeout(t *testing.T) {
	client := NewProcessClient(config.BackendConfig{Name: "wedged", Command: "sleep", Args: []string{"30"}, TimeoutSeconds: 1})
	defer client.Close()
	start := time.Now()
	_, err := client.Tools(context.Background())
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout not enforced, took %s", elapsed)
	}
	if !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("unexpected error: %v", err)
	}
}
