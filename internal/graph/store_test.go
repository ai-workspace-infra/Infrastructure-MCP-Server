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
