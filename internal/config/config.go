package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	Name       string            `json:"name"`
	Capability string            `json:"capability"`
	Command    string            `json:"command"`
	Args       []string          `json:"args"`
	Env        map[string]string `json:"env"`
	Enabled    *bool             `json:"enabled,omitempty"`
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
		if backend.Command == "" {
			return fmt.Errorf("backend %q: command must not be empty", backend.Name)
		}
	}
	return nil
}

func (b BackendConfig) IsEnabled() bool {
	return b.Enabled == nil || *b.Enabled
}
