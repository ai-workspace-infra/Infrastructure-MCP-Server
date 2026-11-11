package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/config"
)

// ClientProtocolVersion is the MCP protocol revision the gateway offers to
// downstream servers during initialize.
const ClientProtocolVersion = "2025-06-18"

const (
	headerSessionID       = "Mcp-Session-Id"
	headerProtocolVersion = "MCP-Protocol-Version"
	maxResponseBytes      = 16 << 20
)

var errSessionExpired = errors.New("mcp session expired")

// HTTPClient talks to a downstream MCP server over HTTP. It supports the
// Streamable HTTP transport (POST JSON-RPC, JSON or SSE-framed replies,
// Mcp-Session-Id session handling) and the legacy HTTP+SSE transport
// (GET event stream announcing a POST endpoint) when transport is "sse".
type HTTPClient struct {
	config     config.BackendConfig
	httpClient *http.Client
	streamHTTP *http.Client // no overall timeout; used for long-lived SSE streams

	nextID int64

	initMu          sync.Mutex
	mu              sync.Mutex
	initialized     bool
	sessionID       string
	protocolVersion string

	legacy *legacySSE
}

func NewHTTPClient(cfg config.BackendConfig) *HTTPClient {
	transport := &http.Transport{
		MaxIdleConns:    10,
		IdleConnTimeout: 30 * time.Second,
	}
	checkRedirect := func(req *http.Request, via []*http.Request) error {
		if len(via) >= 2 {
			return fmt.Errorf("stopped after 2 redirects")
		}
		return nil
	}
	return &HTTPClient{
		config: cfg,
		httpClient: &http.Client{
			Transport:     transport,
			Timeout:       cfg.Timeout() + 5*time.Second,
			CheckRedirect: checkRedirect,
		},
		streamHTTP: &http.Client{Transport: transport, CheckRedirect: checkRedirect},
	}
}

func (c *HTTPClient) Name() string { return c.config.Name }

func (c *HTTPClient) Capability() string { return c.config.Capability }

// Config exposes the backend configuration (used for tool filtering).
func (c *HTTPClient) Config() config.BackendConfig { return c.config }

func (c *HTTPClient) isLegacySSE() bool {
	return strings.EqualFold(c.config.Transport, "sse")
}

// SessionID returns the current downstream session id (empty if none).
func (c *HTTPClient) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.isLegacySSE() && c.legacy != nil {
		return c.legacy.sessionHint()
	}
	return c.sessionID
}

func (c *HTTPClient) applyHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "infrastructure-mcp-server/0.3.0")
	if token := c.config.ResolvedBearerToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range c.config.Headers {
		req.Header.Set(k, v)
	}
}

func (c *HTTPClient) newRequestBody(method string, params any, notification bool) ([]byte, json.RawMessage, error) {
	rpcReq := Request{JSONRPC: "2.0", Method: method}
	var idRaw json.RawMessage
	if !notification {
		idRaw, _ = json.Marshal(atomic.AddInt64(&c.nextID, 1))
		rpcReq.ID = idRaw
	}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal params: %w", err)
		}
		rpcReq.Params = raw
	}
	body, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal request: %w", err)
	}
	return body, idRaw, nil
}

// ---- Streamable HTTP -------------------------------------------------------

func (c *HTTPClient) postStreamable(ctx context.Context, method string, params any, notification bool) (json.RawMessage, error) {
	body, idRaw, err := c.newRequestBody(method, params, notification)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	c.applyHeaders(req)

	c.mu.Lock()
	sessionID, proto := c.sessionID, c.protocolVersion
	c.mu.Unlock()
	if sessionID != "" {
		req.Header.Set(headerSessionID, sessionID)
	}
	if proto != "" && method != "initialize" {
		req.Header.Set(headerProtocolVersion, proto)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to %s: %w", c.config.URL, err)
	}
	defer resp.Body.Close()

	if err := checkHTTPStatus(c.config.URL, resp); err != nil {
		if resp.StatusCode == http.StatusNotFound && sessionID != "" && method != "initialize" {
			return nil, errSessionExpired
		}
		return nil, err
	}

	if method == "initialize" {
		if sid := resp.Header.Get(headerSessionID); sid != "" {
			c.mu.Lock()
			c.sessionID = sid
			c.mu.Unlock()
		}
	}

	if notification || resp.StatusCode == http.StatusAccepted {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, nil
	}

	contentType := resp.Header.Get("Content-Type")
	if strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
		return readSSEResponse(c.config.URL, method, resp.Body, idRaw)
	}

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read response from %s: %w", c.config.URL, err)
	}
	return decodeRPCResponse(c.config.URL, method, respBytes)
}

func checkHTTPStatus(endpoint string, resp *http.Response) error {
	switch {
	case resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusSeeOther || resp.StatusCode == http.StatusTemporaryRedirect:
		return fmt.Errorf("endpoint redirected to %q (authentication or SSO required)", resp.Header.Get("Location"))
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("authentication failed on %s (HTTP %d)", endpoint, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("server error from %s (HTTP %d): %s", endpoint, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}

func decodeRPCResponse(endpoint, method string, data []byte) (json.RawMessage, error) {
	var rpcResp struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *ResponseError  `json:"error"`
	}
	if err := json.Unmarshal(data, &rpcResp); err != nil {
		return nil, fmt.Errorf("decode jsonrpc response from %s: %w", endpoint, err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("%s: %s (code %d)", method, rpcResp.Error.Message, rpcResp.Error.Code)
	}
	if len(rpcResp.Result) == 0 {
		return json.RawMessage("{}"), nil
	}
	return rpcResp.Result, nil
}

// readSSEResponse reads an SSE-framed reply to a POST and returns the
// JSON-RPC response whose id matches. Server notifications/requests that
// arrive on the same stream are skipped.
func readSSEResponse(endpoint, method string, body io.Reader, id json.RawMessage) (json.RawMessage, error) {
	var (
		result json.RawMessage
		resErr error
		found  bool
	)
	err := readSSE(io.LimitReader(body, maxResponseBytes), func(ev sseEvent) bool {
		if ev.Event != "message" || strings.TrimSpace(ev.Data) == "" {
			return true
		}
		var probe struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal([]byte(ev.Data), &probe) != nil || probe.Method != "" {
			return true
		}
		if len(id) > 0 && !sameID(probe.ID, id) {
			return true
		}
		found = true
		result, resErr = decodeRPCResponse(endpoint, method, []byte(ev.Data))
		return false
	})
	if found {
		return result, resErr
	}
	if err != nil {
		return nil, fmt.Errorf("read event stream from %s: %w", endpoint, err)
	}
	return nil, fmt.Errorf("event stream from %s ended without a response to %s", endpoint, method)
}

func sameID(a, b json.RawMessage) bool {
	return strings.TrimSpace(string(a)) == strings.TrimSpace(string(b))
}

// ---- Shared request path ---------------------------------------------------

func (c *HTTPClient) send(ctx context.Context, method string, params any, notification bool) (json.RawMessage, error) {
	if c.isLegacySSE() {
		l, err := c.legacyConn(ctx)
		if err != nil {
			return nil, err
		}
		return l.request(ctx, c, method, params, notification)
	}
	return c.postStreamable(ctx, method, params, notification)
}

func (c *HTTPClient) initialize(ctx context.Context) error {
	c.initMu.Lock()
	defer c.initMu.Unlock()
	c.mu.Lock()
	done := c.initialized
	c.mu.Unlock()
	if done {
		return nil
	}

	raw, err := c.send(ctx, "initialize", map[string]any{
		"protocolVersion": ClientProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "infrastructure-mcp-server", "version": "0.3.0"},
	}, false)
	if err != nil {
		return fmt.Errorf("initialize %s: %w", c.config.Name, err)
	}
	var initResult struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(raw, &initResult)
	c.mu.Lock()
	c.protocolVersion = initResult.ProtocolVersion
	if c.protocolVersion == "" {
		c.protocolVersion = ClientProtocolVersion
	}
	c.mu.Unlock()

	if _, err := c.send(ctx, "notifications/initialized", nil, true); err != nil {
		// Spec-compliant servers answer 202; tolerate older servers that
		// reject the notification rather than failing the whole session.
		log.Printf("mcp: backend %q rejected notifications/initialized (continuing): %v", c.config.Name, err)
	}
	c.mu.Lock()
	c.initialized = true
	c.mu.Unlock()
	return nil
}

func (c *HTTPClient) resetSession() {
	c.mu.Lock()
	c.initialized = false
	c.sessionID = ""
	c.protocolVersion = ""
	legacy := c.legacy
	c.legacy = nil
	c.mu.Unlock()
	if legacy != nil {
		legacy.close()
	}
}

// do runs a request after initialize, re-initializing once if the server
// reports the session as expired (HTTP 404 with a session id) or the legacy
// SSE stream dropped.
func (c *HTTPClient) do(ctx context.Context, method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout())
	defer cancel()
	for attempt := 0; attempt < 2; attempt++ {
		if err := c.initialize(ctx); err != nil {
			return nil, err
		}
		raw, err := c.send(ctx, method, params, false)
		if errors.Is(err, errSessionExpired) {
			c.resetSession()
			continue
		}
		return raw, err
	}
	return nil, fmt.Errorf("%s: session could not be re-established on %s", method, c.config.URL)
}

func (c *HTTPClient) Tools(ctx context.Context) ([]Tool, error) {
	raw, err := c.do(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal tools: %w", err)
	}
	return payload.Tools, nil
}

func (c *HTTPClient) Call(ctx context.Context, name string, arguments map[string]any) (CallToolResult, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	raw, err := c.do(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		return CallToolResult{}, err
	}
	var payload CallToolResult
	if err := json.Unmarshal(raw, &payload); err != nil {
		return CallToolResult{}, fmt.Errorf("unmarshal call result: %w", err)
	}
	return payload, nil
}

// Health performs a real MCP round trip (initialize + ping).
func (c *HTTPClient) Health(ctx context.Context) error {
	_, err := c.do(ctx, "ping", map[string]any{})
	return err
}

// Close drops the downstream session (legacy SSE stream if any).
func (c *HTTPClient) Close() error {
	c.resetSession()
	return nil
}

// ---- Legacy HTTP+SSE transport ---------------------------------------------

type legacySSE struct {
	cancel   context.CancelFunc
	endpoint string
	done     chan struct{}

	mu      sync.Mutex
	pending map[string]chan json.RawMessage
	err     error
}

func (l *legacySSE) sessionHint() string {
	u, err := url.Parse(l.endpoint)
	if err != nil {
		return ""
	}
	for _, key := range []string{"sessionid", "sessionId", "session_id"} {
		if v := u.Query().Get(key); v != "" {
			return v
		}
	}
	return ""
}

func (l *legacySSE) close() {
	l.cancel()
}

func (l *legacySSE) alive() bool {
	select {
	case <-l.done:
		return false
	default:
		return true
	}
}

func (c *HTTPClient) legacyConn(ctx context.Context) (*legacySSE, error) {
	c.mu.Lock()
	if c.legacy != nil && c.legacy.alive() {
		l := c.legacy
		c.mu.Unlock()
		return l, nil
	}
	if c.legacy != nil {
		// Stream dropped: the old session is gone. Let do() re-initialize.
		c.legacy = nil
		if c.initialized {
			c.initialized = false
			c.mu.Unlock()
			return nil, errSessionExpired
		}
	}
	c.mu.Unlock()

	streamCtx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, c.config.URL, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	c.applyHeaders(req)
	resp, err := c.streamHTTP.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open sse stream %s: %w", c.config.URL, err)
	}
	if err := checkHTTPStatus(c.config.URL, resp); err != nil {
		resp.Body.Close()
		cancel()
		return nil, err
	}

	l := &legacySSE{cancel: cancel, done: make(chan struct{}), pending: map[string]chan json.RawMessage{}}
	endpointCh := make(chan string, 1)
	go func() {
		defer close(l.done)
		defer resp.Body.Close()
		err := readSSE(resp.Body, func(ev sseEvent) bool {
			switch ev.Event {
			case "endpoint":
				select {
				case endpointCh <- strings.TrimSpace(ev.Data):
				default:
				}
			case "message":
				var probe struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				if json.Unmarshal([]byte(ev.Data), &probe) != nil || probe.Method != "" || len(probe.ID) == 0 {
					return true
				}
				l.mu.Lock()
				ch := l.pending[strings.TrimSpace(string(probe.ID))]
				l.mu.Unlock()
				if ch != nil {
					select {
					case ch <- json.RawMessage(ev.Data):
					default:
					}
				}
			}
			return true
		})
		if err == nil {
			err = io.EOF
		}
		l.mu.Lock()
		l.err = err
		l.mu.Unlock()
	}()

	select {
	case ep := <-endpointCh:
		resolved, err := resolveSSEEndpoint(c.config.URL, ep)
		if err != nil {
			cancel()
			return nil, err
		}
		l.endpoint = resolved
	case <-l.done:
		cancel()
		return nil, fmt.Errorf("sse stream %s closed before endpoint event", c.config.URL)
	case <-ctx.Done():
		cancel()
		return nil, fmt.Errorf("waiting for sse endpoint event from %s: %w", c.config.URL, ctx.Err())
	}

	c.mu.Lock()
	c.legacy = l
	c.mu.Unlock()
	return l, nil
}

// resolveSSEEndpoint resolves the endpoint announced by a legacy SSE server
// against the stream URL. When the gateway reaches the server through a
// path-rewriting reverse proxy (e.g. public /mcp/v1/grafana/sse -> backend
// /v1/grafana/sse) the announced absolute path lacks the public prefix; in
// that case the prefix of the stream URL is re-applied.
func resolveSSEEndpoint(streamURL, announced string) (string, error) {
	base, err := url.Parse(streamURL)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(announced)
	if err != nil {
		return "", fmt.Errorf("invalid sse endpoint %q: %w", announced, err)
	}
	if ref.IsAbs() {
		return ref.String(), nil
	}
	if strings.HasPrefix(ref.Path, "/") && !strings.HasPrefix(base.Path, ref.Path) {
		segments := strings.SplitN(strings.TrimPrefix(ref.Path, "/"), "/", 2)
		if len(segments) > 0 && segments[0] != "" {
			marker := "/" + segments[0] + "/"
			if idx := strings.Index(base.Path, marker); idx > 0 {
				ref.Path = base.Path[:idx] + ref.Path
			}
		}
	}
	return base.ResolveReference(ref).String(), nil
}

func (l *legacySSE) request(ctx context.Context, c *HTTPClient, method string, params any, notification bool) (json.RawMessage, error) {
	body, idRaw, err := c.newRequestBody(method, params, notification)
	if err != nil {
		return nil, err
	}
	var ch chan json.RawMessage
	key := strings.TrimSpace(string(idRaw))
	if !notification {
		ch = make(chan json.RawMessage, 1)
		l.mu.Lock()
		l.pending[key] = ch
		l.mu.Unlock()
		defer func() {
			l.mu.Lock()
			delete(l.pending, key)
			l.mu.Unlock()
		}()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	c.applyHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post to %s: %w", l.endpoint, err)
	}
	if err := checkHTTPStatus(l.endpoint, resp); err != nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, errSessionExpired
		}
		return nil, err
	}
	inline, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	resp.Body.Close()
	if notification {
		return nil, nil
	}
	// Some servers answer inline instead of on the stream.
	if trimmed := bytes.TrimSpace(inline); len(trimmed) > 0 && trimmed[0] == '{' {
		var probe struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(trimmed, &probe) == nil && sameID(probe.ID, idRaw) {
			return decodeRPCResponse(l.endpoint, method, trimmed)
		}
	}

	select {
	case data := <-ch:
		return decodeRPCResponse(c.config.URL, method, data)
	case <-l.done:
		return nil, errSessionExpired
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for %s response on sse stream %s: %w", method, c.config.URL, ctx.Err())
	}
}
