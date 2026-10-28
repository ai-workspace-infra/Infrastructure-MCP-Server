package cmdb

import (
	"context"
	"testing"

	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/graph"
)

func TestCMDBStoreSyncAndResources(t *testing.T) {
	gStore := graph.NewStore()
	cStore := NewStore(gStore, "/Users/shenlan/workspaces/ai-workspace-infra")

	report, err := cStore.Sync(context.Background())
	if err != nil {
		t.Fatalf("unexpected sync error: %v", err)
	}
	if report.TotalSynced == 0 {
		t.Fatal("expected synced resources, got 0")
	}

	sitRes := cStore.Resources("sit", "", "", "")
	if len(sitRes) == 0 {
		t.Fatal("expected SIT resources")
	}

	uatRes := cStore.Resources("uat", "", "", "")
	if len(uatRes) == 0 {
		t.Fatal("expected UAT resources")
	}

	prodRes := cStore.Resources("prod", "", "", "")
	if len(prodRes) == 0 {
		t.Fatal("expected PROD resources")
	}

	summary := cStore.Summary()
	if len(summary) != 3 {
		t.Fatalf("expected 3 environment summaries, got: %d", len(summary))
	}
}
