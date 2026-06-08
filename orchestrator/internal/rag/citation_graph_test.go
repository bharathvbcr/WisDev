package rag

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCitationGraph(t *testing.T) {
	g := NewCitationGraph()

	n1 := &CitationNode{ID: "A", Title: "Paper A"}
	n2 := &CitationNode{ID: "B", Title: "Paper B"}
	n3 := &CitationNode{ID: "C", Title: "Paper C"}

	g.AddNode(n1)
	g.AddNode(n2)
	g.AddNode(n3)

	t.Run("Edges and Retrieval", func(t *testing.T) {
		g.AddEdge("A", "B", "A cites B")
		g.AddEdge("B", "C", "B cites C")

		citations := g.GetCitationsFor("A")
		assert.Len(t, citations, 1)
		assert.Equal(t, "B", citations[0].ID)

		citedBy := g.GetCitedBy("C")
		assert.Len(t, citedBy, 1)
		assert.Equal(t, "B", citedBy[0].ID)
	})

	t.Run("FindPath", func(t *testing.T) {
		path := g.FindPath("A", "C", 3)
		assert.Equal(t, []string{"A", "B", "C"}, path)

		pathSelf := g.FindPath("A", "A", 3)
		assert.Equal(t, []string{"A"}, pathSelf)

		pathNone := g.FindPath("C", "A", 3)
		assert.Nil(t, pathNone)

		pathDeep := g.FindPath("A", "C", 1) // max depth too shallow
		assert.Nil(t, pathDeep)
	})
}
