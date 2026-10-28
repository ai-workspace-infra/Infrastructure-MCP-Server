package config

import "testing"

func TestValidateRejectsDuplicateBackends(t *testing.T) {
	cfg := Default()
	cfg.Backends = []BackendConfig{{Name: "kubernetes", Command: "one"}, {Name: "kubernetes", Command: "two"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected duplicate backend validation error")
	}
}

func TestValidateAcceptsURLBackend(t *testing.T) {
	cfg := Default()
	cfg.Backends = []BackendConfig{
		{Name: "observability-metrics", Capability: "observability", URL: "https://observability.svc.plus/mcp/v1/metrics/mcp"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid url backend: %v", err)
	}
}

func TestValidateRejectsEmptyCommandAndURL(t *testing.T) {
	cfg := Default()
	cfg.Backends = []BackendConfig{
		{Name: "empty-backend", Capability: "runtime"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for backend without command or url")
	}
}
