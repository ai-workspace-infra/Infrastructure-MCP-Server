package flagger

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/telemetry"
)

func TestFlaggerDynamicBaselineQuery(t *testing.T) {
	mockTel := telemetry.NewEngine(telemetry.Config{
		BaseURL: "http://127.0.0.1:9999",
	}, nil, &http.Client{Timeout: 10})

	client := NewClient("", mockTel)
	ctx := context.Background()

	// 1. PROD Query
	prodRes := client.Query(ctx, Filter{Environment: "prod"})
	if prodRes.Environment != "PROD" {
		t.Fatalf("expected PROD environment, got %s", prodRes.Environment)
	}
	if prodRes.GateDecision != "ALLOWED" {
		t.Fatalf("expected ALLOWED gate decision on PROD, got %s", prodRes.GateDecision)
	}
	if len(prodRes.Services) != 3 {
		t.Fatalf("expected 3 services, got %d", len(prodRes.Services))
	}
	for _, s := range prodRes.Services {
		if s.Phase != "Succeeded" {
			t.Fatalf("expected Succeeded on PROD for %s, got %s", s.Service, s.Phase)
		}
		if s.CandidatePercent != 0 || s.PrimaryPercent != 100 {
			t.Fatalf("expected Primary 100%% Candidate 0%%, got P:%d C:%d", s.PrimaryPercent, s.CandidatePercent)
		}
	}

	// 2. SIT Query
	sitRes := client.Query(ctx, Filter{Environment: "sit"})
	if sitRes.Environment != "SIT" {
		t.Fatalf("expected SIT environment, got %s", sitRes.Environment)
	}
	if sitRes.GateDecision != "BLOCKED" {
		t.Fatalf("expected BLOCKED gate decision on SIT due to failed canaries, got %s", sitRes.GateDecision)
	}

	// 3. Service Filter
	billingRes := client.Query(ctx, Filter{Environment: "prod", Service: "billing"})
	if len(billingRes.Services) != 1 || billingRes.Services[0].Service != "Billing" {
		t.Fatalf("expected only Billing service, got %#v", billingRes.Services)
	}
}

func TestFlaggerLivePrometheusQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")

		if q != "" && len(q) >= 20 && q[:20] == "flagger_canary_statu" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{
					"resultType": "vector",
					"result": []map[string]any{
						{
							"metric": map[string]string{"name": "web-saas-console", "cluster": "SIT-K3S-V6", "environment": "sit"},
							"value":  []any{1794132000, "0"}, // Progressing
						},
						{
							"metric": map[string]string{"name": "web-saas-accounts", "cluster": "SIT-K3S-V6", "environment": "sit"},
							"value":  []any{1794132000, "1"}, // Succeeded
						},
					},
				},
			})
			return
		}

		if q != "" && len(q) >= 20 && q[:20] == "flagger_canary_weigh" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{
					"resultType": "vector",
					"result": []map[string]any{
						{
							"metric": map[string]string{"workload": "web-saas-console", "cluster": "SIT-K3S-V6"},
							"value":  []any{1794132000, "20"},
						},
					},
				},
			})
			return
		}

		// Discovery endpoint fallback
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data":   []string{"sit", "prod"},
		})
	}))
	defer server.Close()

	tel := telemetry.NewEngine(telemetry.Config{
		MetricsURL: server.URL,
	}, nil)

	client := NewClient("", tel)
	res := client.Query(context.Background(), Filter{Environment: "sit"})

	if !res.LiveMetrics {
		t.Fatalf("expected LiveMetrics true")
	}
	if res.GateDecision != "PROGRESSING" {
		t.Fatalf("expected PROGRESSING decision, got %s", res.GateDecision)
	}
	if len(res.Services) != 2 {
		t.Fatalf("expected 2 services from live mock, got %d", len(res.Services))
	}
	for _, s := range res.Services {
		if s.Workload == "web-saas-console" {
			if s.Phase != "Progressing" || s.Weight != 20 || s.PrimaryPercent != 80 || s.CandidatePercent != 20 {
				t.Fatalf("unexpected console canary values: %#v", s)
			}
		}
	}
}
