package rag

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBM25(t *testing.T) {
	is := assert.New(t)
	b := NewBM25()

	docs := []string{
		"The quick brown fox jumps over the lazy dog",
		"Lazy dog sleeps all day",
		"Quick brown fox is fast",
	}
	ids := []string{"doc1", "doc2", "doc3"}

	t.Run("Index and Search", func(t *testing.T) {
		b.IndexDocuments(docs, ids)
		
		results := b.Search("quick brown fox", 3)
		is.NotEmpty(results)
		is.Equal("doc3", results[0].DocID) // "Quick brown fox is fast" has more matches
		
		results2 := b.Search("lazy dog", 2)
		is.Len(results2, 2)
		is.Contains([]string{"doc1", "doc2"}, results2[0].DocID)
	})

	t.Run("Score", func(t *testing.T) {
		scores := b.Score("quick", docs)
		is.Len(scores, 3)
		is.Greater(scores[0], 0.0)
		is.Equal(0.0, scores[1])
		is.Greater(scores[2], 0.0)
	})

	t.Run("Empty Index Search", func(t *testing.T) {
		b2 := NewBM25()
		is.Nil(b2.Search("query", 1))
	})

	t.Run("Score Empty Documents", func(t *testing.T) {
		is.Nil(b.Score("query", nil))
	})
	
	t.Run("Search with no matches", func(t *testing.T) {
		results := b.Search("xyzabc", 10)
		is.Empty(results)
	})

	t.Run("Search trims to requested top-k", func(t *testing.T) {
		b := NewBM25()
		docs := []string{"alpha beta", "beta gamma", "gamma delta"}
		ids := []string{"a", "b", "c"}
		b.IndexDocuments(docs, ids)

		results := b.Search("alpha beta gamma", 1)
		is.Len(results, 1)
	})
}
