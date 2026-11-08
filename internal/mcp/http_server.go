package mcp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Session struct {
	ID        string
	Messages  chan []byte
	CreatedAt time.Time
}

type HTTPServer struct {
	server   *Server
	sessions map[string]*Session
	mu       sync.RWMutex
	basePath string
}

func NewHTTPServer(server *Server, basePath string) *HTTPServer {
	return &HTTPServer{
		server:   server,
		sessions: make(map[string]*Session),
		basePath: strings.TrimRight(basePath, "/"),
	}
}

func (h *HTTPServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.handleHealth)
	mux.HandleFunc("/health", h.handleHealth)
	mux.HandleFunc("/sse", h.handleSSE)
	mux.HandleFunc("/v1/sse", h.handleSSE)
	mux.HandleFunc("/message", h.handleMessage)
	mux.HandleFunc("/v1/message", h.handleMessage)
	mux.HandleFunc("/mcp", h.handleStreamable)
	mux.HandleFunc("/v1/mcp", h.handleStreamable)

	// Web universal HTTP REST APIs for Third-Party AI Agent Teams
	mux.HandleFunc("/api/v1/tools", h.handleWebToolsList)
	mux.HandleFunc("/v1/tools", h.handleWebToolsList)
	mux.HandleFunc("/api/v1/tools/call", h.handleWebToolCall)
	mux.HandleFunc("/v1/tools/call", h.handleWebToolCall)

	// Direct convenience REST endpoints for catalog, flagger, cmdb, telemetry
	mux.HandleFunc("/api/v1/catalog", h.handleWebCatalog)
	mux.HandleFunc("/v1/catalog", h.handleWebCatalog)
	mux.HandleFunc("/api/v1/flagger", h.handleWebFlagger)
	mux.HandleFunc("/v1/flagger", h.handleWebFlagger)
	mux.HandleFunc("/api/v1/cmdb", h.handleWebCMDB)
	mux.HandleFunc("/v1/cmdb", h.handleWebCMDB)
	mux.HandleFunc("/api/v1/telemetry/topology", h.handleWebTopology)
	mux.HandleFunc("/v1/telemetry/topology", h.handleWebTopology)

	// Universal Real-Time SSE Telemetry Stream for AI Agent Teams & Dashboards
	mux.HandleFunc("/api/v1/events", h.handleWebSSEStream)
	mux.HandleFunc("/v1/events", h.handleWebSSEStream)

	mux.HandleFunc("/", h.handleRoot)
	return h.corsMiddleware(mux)
}

func (h *HTTPServer) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *HTTPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "healthy",
		"service": "infrastructure-mcp-server",
		"version": "0.1.0",
	})
}

func (h *HTTPServer) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	b := make([]byte, 16)
	_, _ = rand.Read(b)
	sessionID := hex.EncodeToString(b)

	session := &Session{
		ID:        sessionID,
		Messages:  make(chan []byte, 64),
		CreatedAt: time.Now(),
	}

	h.mu.Lock()
	h.sessions[sessionID] = session
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.sessions, sessionID)
		h.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Send endpoint event with URI for POST messages
	endpoint := fmt.Sprintf("%s/message?sessionid=%s", h.basePath, sessionID)
	if strings.HasPrefix(r.URL.Path, "/v1/") && !strings.HasPrefix(endpoint, "/v1/") {
		endpoint = fmt.Sprintf("%s/v1/message?sessionid=%s", h.basePath, sessionID)
	}
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpoint)
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-session.Messages:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(msg))
			flusher.Flush()
		}
	}
}

func (h *HTTPServer) handleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("sessionid")
	if sessionID == "" {
		http.Error(w, "sessionid must be provided", http.StatusBadRequest)
		return
	}

	h.mu.RLock()
	session, exists := h.sessions[sessionID]
	h.mu.RUnlock()
	if !exists {
		http.Error(w, "invalid or expired sessionid", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid json-rpc request", http.StatusBadRequest)
		return
	}

	resp, err := h.server.HandleRequest(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if resp.JSONRPC != "" || resp.ID != nil {
		respBytes, err := json.Marshal(resp)
		if err == nil {
			select {
			case session.Messages <- respBytes:
			default:
			}
		}
	}

	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("accepted"))
}

func (h *HTTPServer) handleStreamable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: "2.0",
			Error:   &ResponseError{Code: -32700, Message: "parse error"},
		})
		return
	}

	resp, err := h.server.HandleRequest(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *HTTPServer) handleWebToolsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tools, err := h.server.provider.Tools(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"service": "infrastructure-mcp-server",
		"count":   len(tools),
		"tools":   tools,
	})
}

func (h *HTTPServer) handleWebToolCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	var req struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Name == "" {
		http.Error(w, "name is required with valid arguments", http.StatusBadRequest)
		return
	}

	res, err := h.server.provider.Call(r.Context(), req.Name, req.Arguments)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"tool":   req.Name,
		"result": res,
	})
}

func (h *HTTPServer) handleWebCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	args := map[string]any{
		"category":    q.Get("category"),
		"service":     q.Get("service"),
		"environment": q.Get("env"),
		"query":       q.Get("query"),
	}
	if args["environment"] == "" {
		args["environment"] = q.Get("environment")
	}
	if args["query"] == "" {
		args["query"] = q.Get("q")
	}

	res, err := h.server.provider.Call(r.Context(), "infra.api_catalog.query", args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (h *HTTPServer) handleWebFlagger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	args := map[string]any{
		"environment": q.Get("env"),
		"service":     q.Get("service"),
		"cluster":     q.Get("cluster"),
	}
	if args["environment"] == "" {
		args["environment"] = q.Get("environment")
	}

	res, err := h.server.provider.Call(r.Context(), "infra.flagger.status", args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (h *HTTPServer) handleWebCMDB(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	args := map[string]any{
		"environment": q.Get("env"),
		"provider":    q.Get("provider"),
		"kind":        q.Get("kind"),
		"status":      q.Get("status"),
	}
	if args["environment"] == "" {
		args["environment"] = q.Get("environment")
	}

	res, err := h.server.provider.Call(r.Context(), "infra.cmdb.query", args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (h *HTTPServer) handleWebTopology(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	res, err := h.server.provider.Call(r.Context(), "infra.observability.status", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

// handleWebSSEStream implements an automated, real-time Server-Sent Events stream for AI Agent Teams & Dashboards.
func (h *HTTPServer) handleWebSSEStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	envParam := r.URL.Query().Get("env")
	if envParam == "" {
		envParam = "prod"
	}

	// Send initial handshake
	fmt.Fprintf(w, "event: connected\ndata: {\"status\":\"connected\",\"environment\":%q,\"timestamp\":%q}\n\n", envParam, time.Now().UTC().Format(time.RFC3339))
	flusher.Flush()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		// Fetch latest status
		flaggerRes, _ := h.server.provider.Call(ctx, "infra.flagger.status", map[string]any{"environment": envParam})
		if len(flaggerRes.Content) > 0 {
			fmt.Fprintf(w, "event: flagger_status\ndata: %s\n\n", flaggerRes.Content[0].Text)
			flusher.Flush()
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *HTTPServer) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		h.handleStreamable(w, r)
		return
	}
	if r.URL.Path == "/" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"service": "infrastructure-mcp-server",
			"version": "0.1.0",
			"status":  "healthy",
			"mcp_protocols": map[string]string{
				"sse":     "/sse",
				"v1_sse":  "/v1/sse",
				"message": "/message",
				"mcp":     "/mcp",
				"healthz": "/healthz",
			},
			"web_rest_apis": map[string]string{
				"tools_list":    "/api/v1/tools",
				"tools_call":    "/api/v1/tools/call",
				"catalog_query": "/api/v1/catalog",
				"flagger_query": "/api/v1/flagger",
				"cmdb_query":    "/api/v1/cmdb",
				"topology":      "/api/v1/telemetry/topology",
				"sse_stream":    "/api/v1/events",
			},
		})
		return
	}
	http.NotFound(w, r)
}
