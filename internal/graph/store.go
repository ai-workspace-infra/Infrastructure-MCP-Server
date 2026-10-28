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
	s := &Store{nodes: make(map[string]Node), edges: make(map[string]Edge)}
	s.SeedDefaults()
	return s
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

func (s *Store) GetNode(id string) (Node, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.nodes[id]
	return n, ok
}

func (s *Store) EdgesFor(id string) []Edge {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var edges []Edge
	for _, e := range s.edges {
		if e.From == id || e.To == id {
			edges = append(edges, e)
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From == edges[j].From {
			return edges[i].To < edges[j].To
		}
		return edges[i].From < edges[j].From
	})
	if edges == nil {
		edges = []Edge{}
	}
	return edges
}

func (s *Store) Subgraph(id string) (Node, []Edge, []Node, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	center, ok := s.nodes[id]
	if !ok {
		return Node{}, nil, nil, false
	}
	edges := make([]Edge, 0)
	nodeMap := make(map[string]Node)
	for _, e := range s.edges {
		if e.From == id || e.To == id {
			edges = append(edges, e)
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From == edges[j].From {
			return edges[i].To < edges[j].To
		}
		return edges[i].From < edges[j].From
	})
	for _, e := range edges {
		if e.From != id {
			if n, exists := s.nodes[e.From]; exists {
				nodeMap[n.ID] = n
			}
		}
		if e.To != id {
			if n, exists := s.nodes[e.To]; exists {
				nodeMap[n.ID] = n
			}
		}
	}
	nodes := make([]Node, 0, len(nodeMap))
	for _, n := range nodeMap {
		nodes = append(nodes, n)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	return center, edges, nodes, true
}
