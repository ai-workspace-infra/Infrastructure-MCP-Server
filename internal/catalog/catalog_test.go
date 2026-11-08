package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCatalogDefaultQuery(t *testing.T) {
	store := NewStore("")
	ctx := context.Background()

	res := store.Query(ctx, Filter{})
	if res.Total == 0 {
		t.Fatalf("expected catalog items, got 0")
	}
	if res.Environment != "PROD" {
		t.Fatalf("expected default environment PROD, got %s", res.Environment)
	}
	if len(res.Categories) == 0 {
		t.Fatalf("expected category summaries")
	}

	// Filter by Category = frontend
	feRes := store.Query(ctx, Filter{Category: "frontend"})
	for _, item := range feRes.Items {
		if item.Type != "frontend" {
			t.Fatalf("expected frontend item, got %s", item.Type)
		}
	}

	// Filter by Service = billing
	billingRes := store.Query(ctx, Filter{Service: "billing"})
	for _, item := range billingRes.Items {
		if item.Service != "Billing" {
			t.Fatalf("expected Billing service, got %s", item.Service)
		}
	}

	// Filter by Query keyword
	searchRes := store.Query(ctx, Filter{Query: "plans"})
	if searchRes.Total == 0 {
		t.Fatalf("expected search match for 'plans'")
	}
}

func TestCatalogLiveSync(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"allRoutesAndApis": []map[string]any{
				{
					"id":             "live-1",
					"order":          1,
					"type":           "api",
					"categoryKey":    "be-custom",
					"categoryName":   "动态微服务",
					"categoryEnName": "Dynamic Microservice",
					"service":        "Console",
					"serviceDomain":  "console.svc.plus",
					"path":           "/api/custom/endpoint",
					"description":    "测试动态微服务端点",
					"method":         "POST",
					"expectedHttp":   200,
					"observedHttp":   200,
					"latencyMs":      18,
					"result":         "PASS",
				},
			},
		})
	}))
	defer server.Close()

	store := NewStore(server.URL)
	res := store.Query(context.Background(), Filter{Environment: "sit"})

	if !res.LiveSynced {
		t.Fatalf("expected live_synced true")
	}
	if res.Total != 1 {
		t.Fatalf("expected 1 item from live sync, got %d", res.Total)
	}
	if res.Items[0].ID != "live-1" {
		t.Fatalf("expected live-1, got %s", res.Items[0].ID)
	}
}
