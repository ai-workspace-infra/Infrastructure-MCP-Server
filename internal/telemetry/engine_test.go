package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type mockMCPCaller struct {
	calledTools map[string]int
}

func (m *mockMCPCaller) Call(ctx context.Context, toolName string, arguments map[string]any) ([]byte, error) {
	if m.calledTools == nil {
		m.calledTools = make(map[string]int)
	}
	m.calledTools[toolName]++

	switch toolName {
	case "observability-metrics.query":
		return json.Marshal([]MetricVector{
			{
				Metric: map[string]string{"environment": "stage", "cluster": "STAGE-K3S-V6"},
				Value:  []any{1794132000, "1"},
			},
		})
	case "observability-logs.query":
		return json.Marshal([]LogRecord{
			{Time: time.Now(), Message: "test log from MCP"},
		})
	}
	return nil, nil
}

func TestEngineDiscoverTopology(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path

		if path == "/api/v1/label/environment/values" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data":   []string{"prod", "sit", "dev", "perf"},
			})
			return
		}

		if path == "/api/v1/label/cluster/values" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data":   []string{"PROD-K3S-V6", "SIT-K3S-V6", "DEV-K3S-V5"},
			})
			return
		}

		if path == "/api/v1/query" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{
					"resultType": "vector",
					"result": []map[string]any{
						{
							"metric": map[string]string{"environment": "prod", "cluster": "PROD-K3S-V6"},
							"value":  []any{1794132000, "10"},
						},
						{
							"metric": map[string]string{"environment": "sit", "cluster": "SIT-K3S-V6"},
							"value":  []any{1794132000, "10"},
						},
					},
				},
			})
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	engine := NewEngine(Config{
		BaseURL:    server.URL,
		MetricsURL: server.URL,
	}, nil)

	ctx := context.Background()
	meta, err := engine.DiscoverTopology(ctx)
	if err != nil {
		t.Fatalf("discover topology failed: %v", err)
	}

	if len(meta.Environments) != 4 {
		t.Fatalf("expected 4 environments, got %d (%v)", len(meta.Environments), meta.Environments)
	}
	if len(meta.Clusters) != 3 {
		t.Fatalf("expected 3 clusters, got %d (%v)", len(meta.Clusters), meta.Clusters)
	}
	if meta.EnvClusterMap["prod"] != "PROD-K3S-V6" || meta.EnvClusterMap["sit"] != "SIT-K3S-V6" {
		t.Fatalf("unexpected env cluster map: %#v", meta.EnvClusterMap)
	}
}

func TestEngineWithMCPBackend(t *testing.T) {
	mockCaller := &mockMCPCaller{}
	engine := NewEngine(Config{
		BaseURL: "http://127.0.0.1:9999",
	}, mockCaller)

	ctx := context.Background()
	metrics, err := engine.QueryMetrics(ctx, "probe_success")
	if err != nil {
		t.Fatalf("query metrics failed: %v", err)
	}
	if len(metrics) != 1 || metrics[0].Metric["environment"] != "stage" {
		t.Fatalf("expected stage metric from MCP backend, got: %#v", metrics)
	}
	if mockCaller.calledTools["observability-metrics.query"] != 1 {
		t.Fatalf("expected MCP caller tool to be invoked")
	}
}
