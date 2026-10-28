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
			"endpoints": map[string]string{
				"sse":     "/sse",
				"message": "/message",
				"mcp":     "/mcp",
				"healthz": "/healthz",
			},
		})
		return
	}
	http.NotFound(w, r)
}
