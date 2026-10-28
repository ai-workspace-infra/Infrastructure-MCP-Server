package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/config"
)

type HTTPClient struct {
	config     config.BackendConfig
	httpClient *http.Client

	mu          sync.Mutex
	nextID      int64
	initialized bool
}

func NewHTTPClient(cfg config.BackendConfig) *HTTPClient {
	transport := &http.Transport{
		MaxIdleConns:       10,
		IdleConnTimeout:    30 * time.Second,
		DisableCompression: false,
	}
	return &HTTPClient{
		config: cfg,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   15 * time.Second,
			// Avoid following redirects to HTML login pages blindly
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 2 {
					return fmt.Errorf("stopped after 2 redirects")
				}
				return nil
			},
		},
	}
}

func (c *HTTPClient) Name() string { return c.config.Name }

func (c *HTTPClient) Capability() string { return c.config.Capability }

func (c *HTTPClient) applyHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", "infrastructure-mcp-server/0.1.0")

	if c.config.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.config.BearerToken)
	}
	for k, v := range c.config.Headers {
		req.Header.Set(k, v)
	}
}

func (c *HTTPClient) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	idNum := atomic.AddInt64(&c.nextID, 1)
	idRaw, _ := json.Marshal(idNum)
	rpcReq := Request{
		JSONRPC: "2.0",
		ID:      idRaw,
		Method:  method,
	}
	if params != nil {
		rpcReq.Params, _ = json.Marshal(params)
	}

	bodyBytes, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.URL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}
	c.applyHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to %s: %w", c.config.URL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusSeeOther || resp.StatusCode == http.StatusTemporaryRedirect {
		loc := resp.Header.Get("Location")
		return nil, fmt.Errorf("endpoint redirected to %q (authentication or SSO required)", loc)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("authentication failed on %s (HTTP %d)", c.config.URL, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("server error from %s (HTTP %d): %s", c.config.URL, resp.StatusCode, string(respBody))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response from %s: %w", c.config.URL, err)
	}

	var rpcResp Response
	if err := json.Unmarshal(respBytes, &rpcResp); err != nil {
		return nil, fmt.Errorf("decode jsonrpc response from %s: %w", c.config.URL, err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("%s: %s (code %d)", method, rpcResp.Error.Message, rpcResp.Error.Code)
	}
	return json.Marshal(rpcResp.Result)
}

func (c *HTTPClient) initialize(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.initialized {
		return nil
	}

	_, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "infrastructure-mcp-server", "version": "0.1.0"},
	})
	if err != nil {
		return err
	}

	c.initialized = true
	return nil
}

func (c *HTTPClient) Tools(ctx context.Context) ([]Tool, error) {
	if err := c.initialize(ctx); err != nil {
		return nil, err
	}
	raw, err := c.request(ctx, "tools/list", map[string]any{})
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
	if err := c.initialize(ctx); err != nil {
		return CallToolResult{}, err
	}
	raw, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		return CallToolResult{}, err
	}
	var payload CallToolResult
	if err := json.Unmarshal(raw, &payload); err != nil {
		return CallToolResult{}, fmt.Errorf("unmarshal call result: %w", err)
	}
	return payload, nil
}

func (c *HTTPClient) Health(ctx context.Context) error {
	// First attempt: lightweight ping
	_, err := c.request(ctx, "ping", map[string]any{})
	if err == nil {
		return nil
	}

	// Fallback attempt: check HTTP connectivity to the endpoint
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, c.config.URL, nil)
	if reqErr != nil {
		return err
	}
	c.applyHeaders(req)
	resp, httpErr := c.httpClient.Do(req)
	if httpErr != nil {
		return fmt.Errorf("health check failed for %s: %w", c.config.URL, httpErr)
	}
	defer resp.Body.Close()

	// A status code between 200 and 404 indicates the host and port are alive and serving HTTP
	if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		return nil
	}
	return fmt.Errorf("health check returned HTTP %d for %s", resp.StatusCode, c.config.URL)
}
