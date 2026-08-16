package config

import "testing"

func TestValidateRejectsDuplicateBackends(t *testing.T) {
	cfg := Default()
	cfg.Backends = []BackendConfig{{Name: "kubernetes", Command: "one"}, {Name: "kubernetes", Command: "two"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected duplicate backend validation error")
	}
}
