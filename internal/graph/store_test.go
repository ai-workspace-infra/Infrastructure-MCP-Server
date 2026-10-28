package graph

import "testing"

func TestQueryReturnsMatchingNodesAndRelatedEdges(t *testing.T) {
	store := NewStore()
	store.UpsertNode(Node{ID: "service/payment-api", Kind: "service", Name: "payment-api"})
	store.UpsertNode(Node{ID: "cluster/prod", Kind: "cluster", Name: "prod"})
	store.UpsertEdge(Edge{From: "service/payment-api", To: "cluster/prod", Type: "runs_on"})

	result := store.Query("payment")
	if len(result.Nodes) != 1 || result.Nodes[0].ID != "service/payment-api" {
		t.Fatalf("unexpected nodes: %#v", result.Nodes)
	}
	if len(result.Edges) != 1 || result.Edges[0].Type != "runs_on" {
		t.Fatalf("unexpected edges: %#v", result.Edges)
	}
}

func TestSeedDefaultsContainsObservabilityAndRepositories(t *testing.T) {
	store := NewStore()

	obsResult := store.Query("observability")
	if len(obsResult.Nodes) == 0 {
		t.Fatal("expected observability nodes to be seeded")
	}

	gitopsResult := store.Query("gitops")
	if len(gitopsResult.Nodes) == 0 {
		t.Fatal("expected gitops node to be seeded")
	}

	playbooksResult := store.Query("playbooks")
	if len(playbooksResult.Nodes) == 0 {
		t.Fatal("expected playbooks node to be seeded")
	}

	center, edges, connectedNodes, found := store.Subgraph("service/observability-gateway")
	if !found {
		t.Fatal("expected service/observability-gateway to be found")
	}
	if center.Name != "observability.svc.plus" {
		t.Fatalf("unexpected center node name: %s", center.Name)
	}
	if len(edges) != 15 {
		t.Fatalf("expected exactly 15 edges connected to service/observability-gateway, got %d", len(edges))
	}
	if len(connectedNodes) < 10 {
		t.Fatalf("expected at least 10 connected nodes, got %d", len(connectedNodes))
	}
}
