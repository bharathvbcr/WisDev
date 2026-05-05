package wisdev

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResearchQuestRuntime_Getters(t *testing.T) {
	is := assert.New(t)
	tmpDir := t.TempDir()
	t.Setenv("WISDEV_STATE_DIR", tmpDir)

	store := NewRuntimeStateStore(nil, nil)
	rt := NewResearchQuestRuntime(nil)
	rt.stateStore = store

	ctx := context.Background()
	questID := "q1"

	t.Run("GetEvents and GetArtifacts - not found", func(t *testing.T) {
		events, err := rt.GetEvents(ctx, questID)
		is.Error(err)
		is.Nil(events)

		artifacts, err := rt.GetArtifacts(ctx, questID)
		is.Error(err)
		is.Nil(artifacts)
	})

	t.Run("GetEvents and GetArtifacts - found", func(t *testing.T) {
		quest := &ResearchQuest{
			QuestID: questID,
			// Query must be non-empty after the P6-5 fix: questFromMap
			// now rejects empty-query quests with an error.
			Query:        "test research query",
			CurrentStage: "init",
			Events: []QuestEvent{
				{EventID: "e1", Type: "start"},
			},
			Artifacts: map[string]any{
				"paper1": "data1",
			},
		}

		// Save state
		payload, _ := questToMap(quest)
		err := store.SaveQuestState(questID, payload)
		require.NoError(t, err)

		path := filepath.Join(tmpDir, "quest_state_"+questID+".json")
		if _, err := os.Stat(path); err != nil {
			t.Logf("DEBUG: state file NOT found at %s: %v", path, err)
			// Check if it's in a subdirectory?
			files, _ := filepath.Glob(filepath.Join(tmpDir, "*"))
			t.Logf("DEBUG: files in tmpDir: %v", files)
		} else {
			t.Logf("DEBUG: state file found at %s", path)
		}

		events, err := rt.GetEvents(ctx, questID)
		is.NoError(err)
		if is.Len(events, 1) {
			is.Equal("e1", events[0].EventID)
		}

		artifacts, err := rt.GetArtifacts(ctx, questID)
		is.NoError(err)
		is.Equal("data1", artifacts["paper1"])
	})
}

func TestResearchQuest_InternalHelpers(t *testing.T) {
	is := assert.New(t)

	t.Run("extractQuestMethods", func(t *testing.T) {
		papers := []Source{
			{Title: "A Randomized Controlled Trial", Summary: "benchmark results"},
			{Title: "Systematic Review of X", Publication: "Ablation study included"},
		}
		methods := extractQuestMethods(papers)
		is.Contains(methods, "randomized")
		is.Contains(methods, "benchmark")
		is.Contains(methods, "systematic review")
		is.Contains(methods, "ablation")
		is.NotContains(methods, "meta-analysis")
	})

	t.Run("buildCanonicalCitationsFromSources", func(t *testing.T) {
		papers := []Source{
			{ID: "p1", Title: "T1", DOI: "10.123"},
			{ID: "p2", Title: "T2", ArxivID: "2101.12345"},
			{ID: "p3", Title: "T3"},
		}
		citations := buildCanonicalCitationsFromSources(papers)
		if is.Len(citations, 3) {
			is.Equal("10.123", citations[0].DOI)
			is.True(citations[0].Verified)
			is.Equal("2101.12345", citations[1].ArxivID)
			is.True(citations[1].Verified)
			is.False(citations[2].Verified)
		}
	})

	t.Run("buildAcceptedClaims", func(t *testing.T) {
		papers := []Source{
			{ID: "p1", Title: "T1", Summary: "Claim 1", Score: 0.9},
			{ID: "p2", Title: "T2", Summary: "", Score: 0.1}, // score clamped
		}
		findings := buildAcceptedClaims(papers)
		if is.Len(findings, 2) {
			is.Equal("Claim 1", findings[0].Claim)
			is.InDelta(0.91, findings[0].Confidence, 0.001)
			is.Equal("Claim 1", findings[0].Snippet)
			is.Equal("T2", findings[1].Claim)      // falls back to title
			is.Equal(0.45, findings[1].Confidence) // clamped
		}
	})

	t.Run("buildAcceptedClaims extracts richer snippets from full text", func(t *testing.T) {
		papers := []Source{{
			ID:       "p3",
			Title:    "Memory Study",
			Summary:  "Sleep improves declarative memory recall in healthy adults.",
			FullText: "Independent replication confirms hippocampal replay during overnight consolidation.",
			Source:   "crossref",
			Score:    0.88,
		}}
		findings := buildAcceptedClaims(papers)
		if is.Len(findings, 2) {
			is.Contains(findings[0].Claim, "Sleep improves declarative memory recall")
			is.Contains(findings[1].Claim, "hippocampal replay")
		}
	})

	t.Run("buildAcceptedClaims deduplicates duplicate source claims while keeping stronger confidence", func(t *testing.T) {
		papers := []Source{
			{ID: "p1", Title: "Shared Study", Summary: "Shared claim", Score: 0.55, Keywords: []string{"sleep"}},
			{ID: "p1", Title: "Shared Study", Summary: "Shared claim", Score: 0.92, Keywords: []string{"memory"}},
		}
		findings := buildAcceptedClaims(papers)
		if is.Len(findings, 1) {
			is.Equal("Shared claim", findings[0].Claim)
			is.InDelta(0.92, findings[0].Confidence, 0.08)
			is.Equal([]string{"memory", "sleep"}, findings[0].Keywords)
		}
	})

	t.Run("defaultQuestDraft and defaultQuestCritique produce structured outputs", func(t *testing.T) {
		quest := &ResearchQuest{
			Query: "sleep consolidation",
			AcceptedClaims: []EvidenceFinding{
				{Claim: "Sleep spindles predict overnight retention.", PaperTitle: "Spindle Paper", Confidence: 0.88},
			},
			RejectedBranches: []QuestBranchRecord{
				{ID: "branch-1", Content: "Replay alone explains all consolidation gains"},
			},
			CitationVerdict: CitationVerdict{
				Status:        "promoted",
				Promoted:      true,
				VerifiedCount: 2,
			},
			CoverageLedger: []CoverageLedgerEntry{
				{ID: "gap-1", Status: coverageLedgerStatusOpen, Title: "Need causal intervention evidence", SupportingQueries: []string{"sleep spindle intervention trial"}},
			},
			Papers: []Source{
				{Title: "Spindle Paper", Summary: "Sleep spindles predict overnight retention."},
			},
		}

		draft, err := defaultQuestDraft(context.Background(), quest, quest.Papers, map[string]any{"summary": "Reasoning stayed consistent across spindle studies."})
		require.NoError(t, err)
		is.Contains(draft, "Research quest: sleep consolidation")
		is.Contains(draft, "Evidence base: 1 grounded claim(s) from 1 source(s).")
		is.Contains(draft, "Sleep spindles predict overnight retention. (Spindle Paper)")
		is.Contains(draft, "Open gaps:")

		critique, err := defaultQuestCritique(context.Background(), quest, quest.Papers, quest.CitationVerdict)
		require.NoError(t, err)
		is.Contains(critique, "Quest critique: sleep consolidation")
		is.Contains(critique, "Citation status: promoted, 2 verified")
		is.Contains(critique, "Strengths:")
		is.Contains(critique, "Risks:")
		is.Contains(critique, "Unsupported branch: Replay alone explains all consolidation gains")
		is.Contains(critique, "Next actions:")
		is.Contains(critique, "sleep spindle intervention trial")
	})

	t.Run("defaultQuestBranchReasoning marks unsupported hypotheses as rejected", func(t *testing.T) {
		papers := []Source{
			{ID: "p1", Title: "Spindle Paper", Summary: "Sleep spindles predict overnight retention.", Score: 0.9},
		}
		outcome, err := defaultQuestBranchReasoning(context.Background(), nil, papers, []Hypothesis{
			{ID: "h1", Claim: "Sleep spindles predict retention"},
			{ID: "h2", Claim: "Replay alone explains all consolidation gains"},
		})
		require.NoError(t, err)
		is.Len(outcome.AcceptedClaims, 1)
		is.Len(outcome.RejectedBranches, 1)
		is.Equal("Replay alone explains all consolidation gains", outcome.RejectedBranches[0].Content)
		payload := outcome.Payload
		is.Equal(2, payload["hypothesisCount"])
		is.Equal(1, payload["rejectedBranches"])
	})
}
