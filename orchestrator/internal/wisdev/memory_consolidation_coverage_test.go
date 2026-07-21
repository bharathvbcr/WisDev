package wisdev

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestNewMemoryConsolidatorUsesExpectedStoreFallback(t *testing.T) {
	consolidator := NewMemoryConsolidator(nil)
	assert.IsType(t, &NoopMemoryStore{}, consolidator.store)
	require.NotNil(t, consolidator.kg)
	require.NotNil(t, consolidator.compute)

	store := newCoverageTestMemoryStore()
	consolidator = NewMemoryConsolidator(nil, store)
	assert.Same(t, store, consolidator.store)
}

func TestRankRelevantMemoryEntriesPrefersMatchesAndFallsBackToLatest(t *testing.T) {
	t.Run("prefers better lexical matches", func(t *testing.T) {
		entries := []MemoryEntry{
			{ID: "latest", Content: "sleep memory replication evidence", CreatedAt: 300},
			{ID: "older", Content: "sleep memory", CreatedAt: 100},
			{ID: "ignored", Content: "cardiology biomarkers", CreatedAt: 500},
		}

		ranked := rankRelevantMemoryEntries(entries, "sleep memory", 5)
		require.Len(t, ranked, 2)
		assert.Equal(t, "latest", ranked[0].ID)
		assert.Equal(t, "older", ranked[1].ID)
	})

	t.Run("falls back to most recent entries when nothing matches", func(t *testing.T) {
		entries := []MemoryEntry{
			{ID: "oldest", Content: "alpha", CreatedAt: 10},
			{ID: "middle", Content: "beta", CreatedAt: 20},
			{ID: "latest", Content: "gamma", CreatedAt: 30},
		}

		ranked := rankRelevantMemoryEntries(entries, "quantum retrieval", 2)
		require.Len(t, ranked, 2)
		assert.Equal(t, "latest", ranked[0].ID)
		assert.Equal(t, "middle", ranked[1].ID)
	})
}

func TestDedupeMemoryEntriesUsesStableKeyForBlankIDs(t *testing.T) {
	entries := []MemoryEntry{
		{Type: "finding", Content: "Repeated evidence"},
		{Type: "finding", Content: "Repeated evidence"},
		{ID: "explicit", Type: "finding", Content: "Repeated evidence"},
	}

	deduped := dedupeMemoryEntries(entries)
	require.Len(t, deduped, 2)
	assert.Equal(t, "Repeated evidence", deduped[0].Content)
	assert.Equal(t, "explicit", deduped[1].ID)
}

func TestMemoryConsolidatorGetRelevantFindingEntriesMergesStoreAndKnowledgeGraph(t *testing.T) {
	store := newCoverageTestMemoryStore()
	store.longTerm["user-1"] = []MemoryEntry{
		{ID: "ltm-1", Type: "finding", Content: "sleep memory consolidation evidence", CreatedAt: 20},
		{ID: "ltm-2", Type: "finding", Content: "unrelated retrieval note", CreatedAt: 30},
	}
	mdb := new(coverageMockDBProvider)
	rows := &coverageFakeRows{values: [][]any{{"replication evidence"}}, index: -1}
	mdb.On(
		"Query",
		mock.Anything,
		mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "name LIKE $1")
		}),
		[]any{"%sleep memory%"},
	).Return(rows, nil).Once()

	consolidator := &MemoryConsolidator{
		store:   store,
		kg:      NewKnowledgeGraphService(mdb),
		compute: nil,
	}

	entries, err := consolidator.GetRelevantFindingEntries(context.Background(), "user-1", "sleep memory")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "sleep memory consolidation evidence", entries[0].Content)
	assert.Equal(t, "replication evidence", entries[1].Content)
	assert.True(t, rows.closed)
	assert.True(t, mdb.AssertExpectations(t))
}

func TestMemoryConsolidatorGetRelevantFindingEntriesReturnsStoreEntriesOnKnowledgeGraphError(t *testing.T) {
	store := newCoverageTestMemoryStore()
	store.longTerm["user-2"] = []MemoryEntry{{ID: "ltm-1", Type: "finding", Content: "sleep memory note", CreatedAt: 11}}
	mdb := new(coverageMockDBProvider)
	mdb.On(
		"Query",
		mock.Anything,
		mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "name LIKE $1")
		}),
		[]any{"%sleep memory%"},
	).Return(nil, errors.New("knowledge graph unavailable")).Once()

	consolidator := &MemoryConsolidator{
		store:   store,
		kg:      NewKnowledgeGraphService(mdb),
		compute: nil,
	}

	entries, err := consolidator.GetRelevantFindingEntries(context.Background(), "user-2", "sleep memory")
	require.Error(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "sleep memory note", entries[0].Content)
	assert.True(t, mdb.AssertExpectations(t))
}
