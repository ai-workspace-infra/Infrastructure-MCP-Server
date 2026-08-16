package graph

import (
	"sort"
	"strings"
	"sync"
)

type Node struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Name       string         `json:"name"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

type Edge struct {
	From       string         `json:"from"`
	To         string         `json:"to"`
	Type       string         `json:"type"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

type QueryResult struct {
	Query string `json:"query"`
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

type Store struct {
	mu    sync.RWMutex
	nodes map[string]Node
	edges map[string]Edge
}

func NewStore() *Store {
	return &Store{nodes: make(map[string]Node), edges: make(map[string]Edge)}
}

func (s *Store) UpsertNode(node Node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes[node.ID] = node
}

func (s *Store) UpsertEdge(edge Edge) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.edges[edge.From+"|"+edge.To+"|"+edge.Type] = edge
}

func (s *Store) Query(query string) QueryResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	needle := strings.ToLower(strings.TrimSpace(query))
	result := QueryResult{Query: query}
	matched := make(map[string]bool)
	for _, node := range s.nodes {
		if needle == "" || strings.Contains(strings.ToLower(node.ID+" "+node.Kind+" "+node.Name), needle) {
			result.Nodes = append(result.Nodes, node)
			matched[node.ID] = true
		}
	}
	sort.Slice(result.Nodes, func(i, j int) bool { return result.Nodes[i].ID < result.Nodes[j].ID })
	for _, edge := range s.edges {
		if matched[edge.From] || matched[edge.To] {
			result.Edges = append(result.Edges, edge)
		}
	}
	sort.Slice(result.Edges, func(i, j int) bool {
		if result.Edges[i].From == result.Edges[j].From {
			return result.Edges[i].To < result.Edges[j].To
		}
		return result.Edges[i].From < result.Edges[j].From
	})
	if result.Nodes == nil {
		result.Nodes = []Node{}
	}
	if result.Edges == nil {
		result.Edges = []Edge{}
	}
	return result
}
