package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"time"
)

type Config struct {
	Server   ServerConfig    `json:"server"`
	Backends []BackendConfig `json:"backends"`
}

type ServerConfig struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type BackendConfig struct {
	Name        string            `json:"name"`
	Capability  string            `json:"capability"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	URL         string            `json:"url,omitempty"`
	Transport   string            `json:"transport,omitempty"`
	BearerToken string            `json:"bearer_token,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Enabled     *bool             `json:"enabled,omitempty"`
	// TimeoutSeconds bounds every request to this backend (stdio or HTTP).
	// Zero means the default (30s).
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// AllowedTools, when non-empty, is an allow-list of downstream tool names
	// (shell-style patterns, e.g. "query*") exposed through the gateway.
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// DisabledTools is a deny-list of downstream tool names (shell-style
	// patterns). Deny always wins over allow.
	DisabledTools []string `json:"disabled_tools,omitempty"`
}

// DefaultBackendTimeout is used when a backend does not set timeout_seconds.
const DefaultBackendTimeout = 30 * time.Second

// Timeout returns the effective per-request timeout for the backend.
func (b BackendConfig) Timeout() time.Duration {
	if b.TimeoutSeconds > 0 {
		return time.Duration(b.TimeoutSeconds) * time.Second
	}
	return DefaultBackendTimeout
}

// ToolAllowed reports whether a downstream tool may be listed or called
// through the gateway according to allowed_tools / disabled_tools.
func (b BackendConfig) ToolAllowed(tool string) bool {
	for _, pattern := range b.DisabledTools {
		if matchToolPattern(pattern, tool) {
			return false
		}
	}
	if len(b.AllowedTools) == 0 {
		return true
	}
	for _, pattern := range b.AllowedTools {
		if matchToolPattern(pattern, tool) {
			return true
		}
	}
	return false
}

func matchToolPattern(pattern, tool string) bool {
	if pattern == tool {
		return true
	}
	ok, err := path.Match(pattern, tool)
	return err == nil && ok
}

func Default() Config {
	return Config{Server: ServerConfig{Name: "infrastructure-mcp-server", Version: "0.1.0"}}
}

func Load(path string) (Config, error) {
	if path == "" {
		path = os.Getenv("INFRA_MCP_CONFIG")
	}
	if path == "" {
		candidate := "config.json"
		if _, err := os.Stat(candidate); err == nil {
			path = candidate
		}
	}
	if path == "" {
		return Default(), nil
	}

	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return Config{}, err
	}
	cfg := Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.Server.Name == "" {
		return errors.New("server.name must not be empty")
	}
	seen := make(map[string]struct{}, len(c.Backends))
	for _, backend := range c.Backends {
		if backend.Name == "" {
			return errors.New("backend.name must not be empty")
		}
		if _, ok := seen[backend.Name]; ok {
			return fmt.Errorf("duplicate backend name %q", backend.Name)
		}
		seen[backend.Name] = struct{}{}
		if backend.Command == "" && backend.URL == "" {
			return fmt.Errorf("backend %q: either command or url must be specified", backend.Name)
		}
		switch backend.Transport {
		case "", "streamable-http", "http", "sse":
		default:
			return fmt.Errorf("backend %q: unsupported transport %q (want streamable-http or sse)", backend.Name, backend.Transport)
		}
		for _, pattern := range append(append([]string{}, backend.AllowedTools...), backend.DisabledTools...) {
			if _, err := path.Match(pattern, ""); err != nil {
				return fmt.Errorf("backend %q: invalid tool pattern %q: %w", backend.Name, pattern, err)
			}
		}
	}
	return nil
}

func (b BackendConfig) IsEnabled() bool {
	return b.Enabled == nil || *b.Enabled
}
