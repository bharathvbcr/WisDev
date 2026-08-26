package wisdev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/pycompute"
	"github.com/jackc/pgx/v5/pgconn"
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

func TestRankRelevantMemoryEntriesPrefersMatchesAndReturnsEmptyWithoutOverlap(t *testing.T) {
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

	t.Run("returns empty when nothing matches", func(t *testing.T) {
		entries := []MemoryEntry{
			{ID: "oldest", Content: "alpha", CreatedAt: 10},
			{ID: "middle", Content: "beta", CreatedAt: 20},
			{ID: "latest", Content: "gamma", CreatedAt: 30},
		}

		ranked := rankRelevantMemoryEntries(entries, "quantum retrieval", 2)
		assert.Nil(t, ranked)
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
			return strings.Contains(sql, "name LIKE $2") &&
				strings.Contains(sql, "user_id = $1")
		}),
		[]any{"user-1", "%sleep memory%"},
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
			return strings.Contains(sql, "name LIKE $2") &&
				strings.Contains(sql, "user_id = $1")
		}),
		[]any{"user-2", "%sleep memory%"},
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

func TestMemoryConsolidatorConsolidateResearchQuestWritesFindingWithUserScope(t *testing.T) {
	mdb := new(coverageMockDBProvider)
	mdb.On(
		"Exec",
		mock.Anything,
		mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "INSERT INTO knowledge_entities")
		}),
		mock.MatchedBy(func(args []any) bool {
			return len(args) == 10 &&
				args[1] == "quest-1" &&
				args[2] == "user-9" &&
				args[3] == "finding" &&
				args[8] == nil // compute nil → no embedding
		}),
	).Return(pgconn.CommandTag{}, nil).Once()

	consolidator := &MemoryConsolidator{
		db:      mdb,
		kg:      NewKnowledgeGraphService(mdb),
		compute: nil,
	}
	quest := &ResearchQuest{
		QuestID: "quest-1",
		UserID:  "user-9",
		Query:   "sleep memory",
		CitationVerdict: CitationVerdict{
			Promoted: true,
		},
		AcceptedClaims: []EvidenceFinding{
			{ID: "f1", Claim: "Sleep consolidates memory", SourceID: "p1", Confidence: 0.9},
		},
	}

	err := consolidator.ConsolidateResearchQuest(context.Background(), quest)
	require.NoError(t, err)
	assert.True(t, mdb.AssertExpectations(t))
}

func TestEmbedFindingTextLogsOnEmbedFailureWithoutClaimBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "embed unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	var logBuffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	claim := "unique-claim-body-must-not-appear-in-logs"
	consolidator := &MemoryConsolidator{
		compute: pycompute.NewClientWithBaseURL(server.URL),
	}
	embedding := consolidator.embedFindingText(context.Background(), &Hypothesis{
		Text:  claim,
		Claim: claim,
	})
	assert.Nil(t, embedding)

	logs := logBuffer.String()
	assert.Contains(t, logs, `"component":"wisdev.memory_consolidation"`)
	assert.Contains(t, logs, `"operation":"embed_finding_text"`)
	assert.Contains(t, logs, `"result":"error"`)
	assert.Contains(t, logs, `"error_code":"embed_batch_failed"`)
	assert.NotContains(t, logs, claim)
}

func TestEmbedFindingTextLogsOnEmptyEmbeddingWithoutClaimBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": []any{}})
	}))
	t.Cleanup(server.Close)

	var logBuffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	claim := "empty-vector-claim-must-not-appear"
	consolidator := &MemoryConsolidator{
		compute: pycompute.NewClientWithBaseURL(server.URL),
	}
	embedding := consolidator.embedFindingText(context.Background(), &Hypothesis{
		Text:  claim,
		Claim: claim,
	})
	assert.Nil(t, embedding)

	logs := logBuffer.String()
	assert.Contains(t, logs, `"component":"wisdev.memory_consolidation"`)
	assert.Contains(t, logs, `"operation":"embed_finding_text"`)
	assert.Contains(t, logs, `"result":"empty"`)
	assert.Contains(t, logs, `"error_code":"embed_batch_empty"`)
	assert.NotContains(t, logs, claim)
}

func TestMemoryConsolidatorConsolidateResearchQuestNullEmbeddingOnEmbedFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "embed unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	mdb := new(coverageMockDBProvider)
	mdb.On(
		"Exec",
		mock.Anything,
		mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "INSERT INTO knowledge_entities")
		}),
		mock.MatchedBy(func(args []any) bool {
			return len(args) == 10 &&
				args[2] == "user-embed-fail" &&
				args[8] == nil // embed failure → NULL embedding, keyword fallback path
		}),
	).Return(pgconn.CommandTag{}, nil).Once()

	consolidator := &MemoryConsolidator{
		db:      mdb,
		kg:      NewKnowledgeGraphService(mdb),
		compute: pycompute.NewClientWithBaseURL(server.URL),
	}
	quest := &ResearchQuest{
		QuestID: "quest-embed-fail",
		UserID:  "user-embed-fail",
		Query:   "sleep memory",
		CitationVerdict: CitationVerdict{
			Promoted: true,
		},
		AcceptedClaims: []EvidenceFinding{
			{ID: "f1", Claim: "Sleep consolidates memory", SourceID: "p1", Confidence: 0.9},
		},
	}

	err := consolidator.ConsolidateResearchQuest(context.Background(), quest)
	require.NoError(t, err)
	assert.True(t, mdb.AssertExpectations(t))
}

func TestMemoryConsolidatorConsolidateQuestSkipsNilAndContinuesOnRowFailure(t *testing.T) {
	mdb := new(coverageMockDBProvider)
	mdb.On(
		"Exec",
		mock.Anything,
		mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "INSERT INTO knowledge_entities")
		}),
		mock.MatchedBy(func(args []any) bool {
			return len(args) == 10 && args[4] == "good finding"
		}),
	).Return(pgconn.CommandTag{}, nil).Once()
	mdb.On(
		"Exec",
		mock.Anything,
		mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "INSERT INTO research_dead_ends")
		}),
		mock.Anything,
	).Return(pgconn.CommandTag{}, errors.New("dead-end write failed")).Once()

	consolidator := &MemoryConsolidator{
		db:      mdb,
		kg:      NewKnowledgeGraphService(mdb),
		compute: nil,
	}
	quest := &QuestState{
		QuestID: "quest-a",
		Status:  QuestStatusComplete,
		UserID:  "user-1",
		Query:   "sleep",
		Hypotheses: []*Hypothesis{
			nil,
			{Text: "good finding", Claim: "good finding", ConfidenceScore: 0.95},
			{Text: "dead branch", Claim: "dead branch", ConfidenceScore: 0.1, EvidenceCount: 8},
		},
	}

	err := consolidator.ConsolidateQuest(context.Background(), quest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dead-end write failed")
	assert.True(t, mdb.AssertExpectations(t))
}

func TestMemoryConsolidatorConsolidateResearchQuestContinuesPastRowFailures(t *testing.T) {
	mdb := new(coverageMockDBProvider)
	mdb.On(
		"Exec",
		mock.Anything,
		mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "INSERT INTO knowledge_entities")
		}),
		mock.MatchedBy(func(args []any) bool {
			return len(args) == 10 && args[5] == "first claim"
		}),
	).Return(pgconn.CommandTag{}, errors.New("first finding failed")).Once()
	mdb.On(
		"Exec",
		mock.Anything,
		mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "INSERT INTO knowledge_entities")
		}),
		mock.MatchedBy(func(args []any) bool {
			return len(args) == 10 && args[5] == "second claim"
		}),
	).Return(pgconn.CommandTag{}, nil).Once()
	mdb.On(
		"Exec",
		mock.Anything,
		mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "INSERT INTO research_dead_ends")
		}),
		mock.Anything,
	).Return(pgconn.CommandTag{}, nil).Once()

	consolidator := &MemoryConsolidator{
		db:      mdb,
		kg:      NewKnowledgeGraphService(mdb),
		compute: nil,
	}
	quest := &ResearchQuest{
		QuestID: "quest-2",
		UserID:  "user-2",
		Query:   "memory",
		CitationVerdict: CitationVerdict{
			Promoted: true,
		},
		AcceptedClaims: []EvidenceFinding{
			{ID: "f1", Claim: "first claim", SourceID: "p1", Confidence: 0.9},
			{ID: "f2", Claim: "second claim", SourceID: "p2", Confidence: 0.85},
		},
		RejectedBranches: []QuestBranchRecord{
			{ID: "r1", Content: "rejected path"},
		},
	}

	err := consolidator.ConsolidateResearchQuest(context.Background(), quest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "first finding failed")
	assert.True(t, mdb.AssertExpectations(t))
}
