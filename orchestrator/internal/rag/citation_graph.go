package rag

import (
	"sync"
)

// CitationGraph tracks relationships between research papers.
type CitationGraph struct {
	mu    sync.RWMutex
	Nodes map[string]*CitationNode `json:"nodes"`
	Edges []CitationEdge           `json:"edges"`
	
	// Adjacency lists for fast traversal
	outgoing map[string][]string // citing -> cited
	incoming map[string][]string // cited -> citing
}

func NewCitationGraph() *CitationGraph {
	return &CitationGraph{
		Nodes:    make(map[string]*CitationNode),
		Edges:    make([]CitationEdge, 0),
		outgoing: make(map[string][]string),
		incoming: make(map[string][]string),
	}
}

// AddNode adds a paper to the graph if it doesn't exist.
func (g *CitationGraph) AddNode(node *CitationNode) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.Nodes[node.ID]; !exists {
		g.Nodes[node.ID] = node
	}
}

// AddEdge adds a citation link between papers.
func (g *CitationGraph) AddEdge(sourceID, targetID, context string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	
	edge := CitationEdge{
		SourceID: sourceID,
		TargetID: targetID,
		Context:  context,
	}
	g.Edges = append(g.Edges, edge)
	g.outgoing[sourceID] = append(g.outgoing[sourceID], targetID)
	g.incoming[targetID] = append(g.incoming[targetID], sourceID)
}

// GetCitationsFor returns all papers cited by the given paper.
func (g *CitationGraph) GetCitationsFor(paperID string) []*CitationNode {
	g.mu.RLock()
	defer g.mu.RUnlock()
	
	targets := g.outgoing[paperID]
	nodes := make([]*CitationNode, 0, len(targets))
	for _, id := range targets {
		if n, ok := g.Nodes[id]; ok {
			nodes = append(nodes, n)
		}
	}
	return nodes
}

// GetCitedBy returns all papers that cite the given paper.
func (g *CitationGraph) GetCitedBy(paperID string) []*CitationNode {
	g.mu.RLock()
	defer g.mu.RUnlock()
	
	sources := g.incoming[paperID]
	nodes := make([]*CitationNode, 0, len(sources))
	for _, id := range sources {
		if n, ok := g.Nodes[id]; ok {
			nodes = append(nodes, n)
		}
	}
	return nodes
}

// FindPath returns a chain of citations between two papers (BFS).
func (g *CitationGraph) FindPath(startID, endID string, maxDepth int) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if startID == endID {
		return []string{startID}
	}

	queue := [][]string{{startID}}
	visited := map[string]bool{startID: true}

	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		
		if len(path) > maxDepth {
			continue
		}

		last := path[len(path)-1]
		for _, next := range g.outgoing[last] {
			if next == endID {
				return append(path, next)
			}
			if !visited[next] {
				visited[next] = true
				newPath := make([]string, len(path)+1)
				copy(newPath, path)
				newPath[len(path)] = next
				queue = append(queue, newPath)
			}
		}
	}

	return nil
}
