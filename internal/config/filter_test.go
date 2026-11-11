package config

import "testing"

func TestToolAllowed(t *testing.T) {
	b := BackendConfig{DisabledTools: []string{"write-file", "upload", "job-*"}}
	for tool, want := range map[string]bool{"execute-command": true, "write-file": false, "upload": false, "job-start": false, "list-servers": true} {
		if got := b.ToolAllowed(tool); got != want {
			t.Errorf("deny-list %s: got %v want %v", tool, got, want)
		}
	}
	b = BackendConfig{AllowedTools: []string{"query*", "alerts"}, DisabledTools: []string{"query_range"}}
	for tool, want := range map[string]bool{"query": true, "query_range": false, "alerts": true, "export": false} {
		if got := b.ToolAllowed(tool); got != want {
			t.Errorf("allow-list %s: got %v want %v", tool, got, want)
		}
	}
}

func TestValidateRejectsBadTransportAndPattern(t *testing.T) {
	cfg := Default()
	cfg.Backends = []BackendConfig{{Name: "x", URL: "http://x", Transport: "websocket"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected unsupported transport error")
	}
	cfg.Backends = []BackendConfig{{Name: "x", URL: "http://x", DisabledTools: []string{"[bad"}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid pattern error")
	}
}
