package wisdev

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResearchMemoryCompilerPromotesAndQueries(t *testing.T) {
	t.Setenv("WISDEV_STATE_DIR", t.TempDir())
	store := NewRuntimeStateStore(nil, nil)
	compiler := NewResearchMemoryCompiler(store, nil)

	_, err := compiler.PromoteFindings(context.Background(), ResearchMemoryPromotionInput{
		UserID:    "u1",
		ProjectID: "p1",
		Query:     "graph rag retrieval",
		Scope:     ResearchMemoryScopeProject,
		Findings: []EvidenceFinding{
			{ID: "f1", Claim: "Graph-based retrieval improves evidence coverage.", SourceID: "doi:1", PaperTitle: "Graph RAG", Confidence: 0.81, Status: "verified", Keywords: []string{"graph", "retrieval"}},
			{ID: "f2", Claim: "Graph-based retrieval improves evidence coverage.", SourceID: "doi:2", PaperTitle: "Graph RAG 2", Confidence: 0.77, Status: "verified", Keywords: []string{"graph", "retrieval"}},
		},
		PreferredSources: []string{"semantic_scholar", "openalex"},
	})
	require.NoError(t, err)

	_, err = compiler.ConsolidateEpisode(context.Background(), ResearchMemoryEpisodeInput{
		UserID:             "u1",
		ProjectID:          "p1",
		Query:              "graph rag retrieval",
		Scope:              ResearchMemoryScopeProject,
		Summary:            "Compiled graph retrieval findings.",
		AcceptedFindings:   []string{"Graph-based retrieval improves evidence coverage."},
		RecommendedQueries: []string{"graph rag contradiction benchmark"},
	})
	require.NoError(t, err)

	resp, err := compiler.Query(context.Background(), ResearchMemoryQueryRequest{
		UserID:    "u1",
		ProjectID: "p1",
		Query:     "graph retrieval coverage",
		Limit:     5,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Findings, 1)
	assert.Equal(t, 2, resp.Findings[0].SupportCount)
	assert.Greater(t, resp.Findings[0].Confidence, 0.81)
	assert.Contains(t, resp.RelatedTopics, "graph")
	assert.Contains(t, resp.RecommendedQueries, "graph rag contradiction benchmark")
	require.NotEmpty(t, resp.WorkspaceContext)
}

func TestResearchMemoryCompilerMarksContradictions(t *testing.T) {
	t.Setenv("WISDEV_STATE_DIR", t.TempDir())
	store := NewRuntimeStateStore(nil, nil)
	compiler := NewResearchMemoryCompiler(store, nil)

	_, err := compiler.PromoteFindings(context.Background(), ResearchMemoryPromotionInput{
		UserID:    "u2",
		ProjectID: "p2",
		Query:     "sleep memory consolidation",
		Scope:     ResearchMemoryScopeProject,
		Findings: []EvidenceFinding{
			{ID: "a", Claim: "Sleep improves memory consolidation.", SourceID: "s1", Confidence: 0.88, Status: "verified", Keywords: []string{"sleep", "memory"}},
			{ID: "b", Claim: "Sleep does not improve memory consolidation.", SourceID: "s2", Confidence: 0.74, Status: "verified", Keywords: []string{"sleep", "memory"}},
		},
		Contradictions: []ContradictionPair{{
			FindingA:    EvidenceFinding{Claim: "Sleep improves memory consolidation."},
			FindingB:    EvidenceFinding{Claim: "Sleep does not improve memory consolidation."},
			Severity:    ContradictionHigh,
			Explanation: "Studies disagree on the effect direction.",
		}},
	})
	require.NoError(t, err)

	resp, err := compiler.Query(context.Background(), ResearchMemoryQueryRequest{
		UserID:                "u2",
		ProjectID:             "p2",
		Query:                 "sleep memory consolidation",
		IncludeContradictions: true,
	})
	require.NoError(t, err)
	assert.Len(t, resp.ContradictedFindings, 2)
}

func TestResearchMemoryCompilerSupersedesWeakerFinding(t *testing.T) {
	t.Setenv("WISDEV_STATE_DIR", t.TempDir())
	store := NewRuntimeStateStore(nil, nil)
	compiler := NewResearchMemoryCompiler(store, nil)

	_, err := compiler.PromoteFindings(context.Background(), ResearchMemoryPromotionInput{
		UserID:    "u3",
		ProjectID: "p3",
		Query:     "graph retrieval benchmark",
		Scope:     ResearchMemoryScopeProject,
		Findings: []EvidenceFinding{
			{ID: "a", Claim: "Graph retrieval improves benchmark coverage.", SourceID: "doi:a", Confidence: 0.92, Status: "verified", Keywords: []string{"graph", "retrieval", "benchmark"}},
			{ID: "b", Claim: "Graph retrieval slightly improves benchmark coverage.", SourceID: "doi:b", Confidence: 0.61, Status: "verified", Keywords: []string{"graph", "retrieval", "benchmark"}},
		},
		RecommendedQueries: []string{"graph retrieval error analysis"},
	})
	require.NoError(t, err)

	resp, err := compiler.Query(context.Background(), ResearchMemoryQueryRequest{
		UserID:    "u3",
		ProjectID: "p3",
		Query:     "graph retrieval benchmark",
		Limit:     5,
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Findings)
	assert.Equal(t, "Graph retrieval improves benchmark coverage.", resp.Findings[0].Claim)
	assert.NotEmpty(t, resp.RelationshipSignals)
	assert.Contains(t, resp.RelatedMethods, "benchmark")
}

func TestResearchMemoryCompilerRetentionArchivesAndPrunes(t *testing.T) {
	t.Setenv("WISDEV_STATE_DIR", t.TempDir())
	store := NewRuntimeStateStore(nil, nil)
	compiler := NewResearchMemoryCompiler(store, nil)

	_, err := compiler.PromoteFindings(context.Background(), ResearchMemoryPromotionInput{
		UserID:    "u4",
		ProjectID: "p4",
		Query:     "sleep memory trial",
		Scope:     ResearchMemoryScopeProject,
		Findings: []EvidenceFinding{
			{ID: "stale", Claim: "Sleep improves consolidation in one trial.", SourceID: "doi:stale", Confidence: 0.58, Status: "tentative", Keywords: []string{"sleep", "trial"}},
		},
		RecommendedQueries: []string{"sleep memory replication"},
		PreferredSources:   []string{"openalex"},
	})
	require.NoError(t, err)
	_, err = compiler.ConsolidateEpisode(context.Background(), ResearchMemoryEpisodeInput{
		UserID:             "u4",
		ProjectID:          "p4",
		Query:              "sleep memory trial",
		Scope:              ResearchMemoryScopeProject,
		Summary:            "Initial sleep memory digest.",
		AcceptedFindings:   []string{"Sleep improves consolidation in one trial."},
		RecommendedQueries: []string{"sleep memory replication"},
		ReusableStrategies: []string{"openalex"},
	})
	require.NoError(t, err)

	state, err := compiler.loadState("u4", "p4")
	require.NoError(t, err)
	require.Len(t, state.Records, 1)
	recordOld := NowMillis() - int64(150*24*60*60*1000)
	artifactOld := NowMillis() - int64(220*24*60*60*1000)
	state.Records[0].LastConfirmedAt = recordOld
	state.Records[0].UpdatedAt = recordOld
	state.Episodes[0].CreatedAt = artifactOld
	state.Procedures[0].UpdatedAt = artifactOld
	require.NoError(t, compiler.saveState("u4", "p4", state))

	result, err := compiler.EnforceRetention(context.Background(), 90)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.GreaterOrEqual(t, result.StatesTouched, 1)
	assert.GreaterOrEqual(t, result.ArchivedRecords, 1)
	assert.GreaterOrEqual(t, result.PrunedEpisodes, 1)
	assert.GreaterOrEqual(t, result.PrunedProcedures, 1)

	updated, err := compiler.loadState("u4", "p4")
	require.NoError(t, err)
	require.Len(t, updated.Records, 1)
	assert.Equal(t, ResearchMemoryStatusArchived, updated.Records[0].LifecycleStatus)
	assert.Empty(t, updated.Episodes)
	assert.Empty(t, updated.Procedures)
}

func TestResearchMemoryCompilerBackfillsHistoricalArtifacts(t *testing.T) {
	t.Setenv("WISDEV_STATE_DIR", t.TempDir())
	store := NewRuntimeStateStore(nil, nil)
	compiler := NewResearchMemoryCompiler(store, nil)

	require.NoError(t, store.SaveEvidenceDossier("dossier_hist_1", map[string]any{
		"dossierId":      "dossier_hist_1",
		"userId":         "u5",
		"projectId":      "proj-5",
		"query":          "graph retrieval memory",
		"verifiedClaims": []map[string]any{{"claimText": "Graph retrieval improves evidence coverage.", "confidence": 0.87, "verifierStatus": "verified"}},
		"gaps":           []string{"Need contradiction benchmark."},
	}))
	require.NoError(t, store.SaveQuestState("quest_hist_1", map[string]any{
		"questId":        "quest_hist_1",
		"userId":         "u5",
		"projectId":      "proj-5",
		"query":          "graph retrieval memory",
		"finalAnswer":    "Historical quest summary.",
		"acceptedClaims": []map[string]any{{"claim": "Memory-guided retrieval reduces duplicate search work.", "confidence": 0.76, "status": "verified"}},
		"blockingIssues": []string{"Need replication study."},
	}))
	require.NoError(t, store.SaveProjectWorkspace("u5", "proj-5", map[string]any{
		"projectId":       "proj-5",
		"unresolvedGaps":  []string{"Need longitudinal benchmark."},
		"followUpQueries": []string{"graph retrieval longitudinal benchmark"},
	}))
	require.NoError(t, store.SaveSessionSummaries("u5", []map[string]any{{
		"projectId":          "proj-5",
		"query":              "graph retrieval memory",
		"summary":            "Session summary for backfill.",
		"acceptedFindings":   []string{"Graph retrieval improves evidence coverage."},
		"recommendedQueries": []string{"graph retrieval contradiction study"},
	}}))

	result, err := compiler.BackfillHistoricalArtifacts(context.Background(), "u5", "proj-5")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.DossiersScanned)
	assert.Equal(t, 1, result.QuestsScanned)
	assert.Equal(t, 1, result.WorkspacesScanned)
	assert.Equal(t, 1, result.SessionsScanned)
	assert.GreaterOrEqual(t, result.PromotionsApplied, 2)
	assert.GreaterOrEqual(t, result.EpisodesApplied, 3)

	resp, err := compiler.Query(context.Background(), ResearchMemoryQueryRequest{
		UserID:    "u5",
		ProjectID: "proj-5",
		Query:     "graph retrieval evidence coverage",
		Limit:     10,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Findings)
	assert.Contains(t, resp.RecommendedQueries, "graph retrieval contradiction study")
	assert.NotEmpty(t, resp.TopEpisodes)
	assert.NotEmpty(t, resp.WorkspaceContext)
}
