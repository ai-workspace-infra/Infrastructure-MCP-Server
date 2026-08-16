package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/config"
)

type ProcessClient struct {
	config config.BackendConfig

	mu          sync.Mutex
	command     *exec.Cmd
	stdin       io.WriteCloser
	reader      *bufio.Reader
	nextID      int64
	initialized bool
}

func NewProcessClient(cfg config.BackendConfig) *ProcessClient {
	return &ProcessClient{config: cfg}
}

func (c *ProcessClient) Name() string { return c.config.Name }

func (c *ProcessClient) Capability() string { return c.config.Capability }

func (c *ProcessClient) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.startLocked()
}

func (c *ProcessClient) startLocked() error {
	if c.command != nil && c.command.Process != nil {
		return nil
	}
	cmd := exec.Command(c.config.Command, c.config.Args...)
	cmd.Env = os.Environ()
	for key, value := range c.config.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return err
	}
	c.command, c.stdin, c.reader = cmd, stdin, bufio.NewReader(stdout)
	return nil
}

func (c *ProcessClient) requestLocked(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := c.startLocked(); err != nil {
		return nil, err
	}
	c.nextID++
	id, _ := json.Marshal(c.nextID)
	request := Request{JSONRPC: "2.0", ID: id, Method: method}
	if params != nil {
		request.Params, _ = json.Marshal(params)
	}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		return nil, err
	}

	for {
		line, err := c.reader.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		var response Response
		if err := json.Unmarshal(line, &response); err != nil {
			continue
		}
		if string(response.ID) != string(id) {
			continue
		}
		if response.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, response.Error.Message)
		}
		return json.Marshal(response.Result)
	}
}

func (c *ProcessClient) initializeLocked(ctx context.Context) error {
	if c.initialized {
		return nil
	}
	_, err := c.requestLocked(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "infrastructure-mcp-server", "version": "0.1.0"},
	})
	if err != nil {
		return err
	}
	notification, _ := json.Marshal(Request{JSONRPC: "2.0", Method: "notifications/initialized"})
	if _, err := c.stdin.Write(append(notification, '\n')); err != nil {
		return err
	}
	c.initialized = true
	return nil
}

func (c *ProcessClient) Tools(ctx context.Context) ([]Tool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.initializeLocked(ctx); err != nil {
		return nil, err
	}
	result, err := c.requestLocked(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return nil, err
	}
	return payload.Tools, nil
}

func (c *ProcessClient) Call(ctx context.Context, name string, arguments map[string]any) (CallToolResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.initializeLocked(ctx); err != nil {
		return CallToolResult{}, err
	}
	result, err := c.requestLocked(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		return CallToolResult{}, err
	}
	var payload CallToolResult
	if err := json.Unmarshal(result, &payload); err != nil {
		return CallToolResult{}, err
	}
	return payload, nil
}

func (c *ProcessClient) Health(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.initializeLocked(ctx); err != nil {
		return err
	}
	_, err := c.requestLocked(ctx, "ping", map[string]any{})
	return err
}

func (c *ProcessClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.command == nil || c.command.Process == nil {
		return nil
	}
	_ = c.stdin.Close()
	err := c.command.Process.Kill()
	c.command = nil
	c.initialized = false
	return err
}
