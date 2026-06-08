package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResearchEnrichmentHypothesisHelpers(t *testing.T) {
	t.Run("buildAutonomousHypothesisPayloadsFromLoop", func(t *testing.T) {
		result := &wisdev.LoopResult{
			ReasoningGraph: &wisdev.ReasoningGraph{
				Nodes: []wisdev.ReasoningNode{
					{
						ID:           "node-1",
						Type:         wisdev.ReasoningNodeHypothesis,
						RefinedQuery: "  refined claim  ",
						Confidence:   1.4,
						Metadata: map[string]any{
							"evidenceIds": []any{"ev-1", "ev-2", "ev-3", "ev-4"},
						},
					},
					{
						ID:   "ignored",
						Type: wisdev.ReasoningNodeEvidence,
					},
				},
			},
			Evidence: []wisdev.EvidenceFinding{
				{ID: "ev-1", Claim: "c1", SourceID: "s1", PaperTitle: "P1", Snippet: "S1", Confidence: 0.9},
				{ID: "ev-2", Claim: "c2", SourceID: "s2", PaperTitle: "P2", Snippet: "S2", Confidence: 0.8},
				{ID: "ev-3", Claim: "c3", SourceID: "s3", PaperTitle: "P3", Snippet: "S3", Confidence: 0.7},
				{ID: "ev-4", Claim: "c4", SourceID: "s4", PaperTitle: "P4", Snippet: "S4", Confidence: 0.6},
			},
		}

		payloads := buildAutonomousHypothesisPayloadsFromLoop("  query  ", result)
		require.Len(t, payloads, 1)

		payload := payloads[0]
		assert.Equal(t, "node-1", payload["id"])
		assert.Equal(t, "refined claim", payload["text"])
		assert.Equal(t, "refined claim", payload["claim"])
		assert.Equal(t, "validated", payload["status"])
		assert.Equal(t, 1.0, payload["confidence"])

		evidence := payload["evidence"].([]map[string]any)
		assert.Len(t, evidence, 3)
		assert.Equal(t, "ev-1", evidence[0]["id"])

		emptyPayloads := buildAutonomousHypothesisPayloadsFromLoop("query", &wisdev.LoopResult{
			ReasoningGraph: &wisdev.ReasoningGraph{
				Nodes: []wisdev.ReasoningNode{
					{Type: wisdev.ReasoningNodeHypothesis},
					{Type: wisdev.ReasoningNodeEvidence, ID: "ignored"},
				},
			},
		})
		assert.Nil(t, emptyPayloads)
	})

	t.Run("autonomousNodeEvidenceIDs", func(t *testing.T) {
		assert.Nil(t, autonomousNodeEvidenceIDs(wisdev.ReasoningNode{}))
		assert.Nil(t, autonomousNodeEvidenceIDs(wisdev.ReasoningNode{Metadata: map[string]any{"other": 1}}))
		assert.Equal(t, []string{"a", "b"}, autonomousNodeEvidenceIDs(wisdev.ReasoningNode{
			Metadata: map[string]any{"evidenceIds": []string{"a", "b"}},
		}))
		assert.Equal(t, []string{"1", "two"}, autonomousNodeEvidenceIDs(wisdev.ReasoningNode{
			Metadata: map[string]any{"evidenceIds": []any{1, " two ", ""}},
		}))
		assert.Nil(t, autonomousNodeEvidenceIDs(wisdev.ReasoningNode{
			Metadata: map[string]any{"evidenceIds": 7},
		}))
	})

	t.Run("buildAutonomousHypothesisEvidence", func(t *testing.T) {
		findings := []wisdev.EvidenceFinding{
			{ID: "ev-1", SourceID: "s1", Claim: "c1"},
			{ID: "ev-2", SourceID: "s2", Claim: "c2"},
			{ID: "ev-3", SourceID: "s3", Claim: "c3"},
			{ID: "ev-4", SourceID: "s4", Claim: "c4"},
		}

		byEvidenceIDs := buildAutonomousHypothesisEvidence(
			wisdev.ReasoningNode{Metadata: map[string]any{"evidenceIds": []any{"ev-2", "ev-4"}}},
			findings,
		)
		require.Len(t, byEvidenceIDs, 2)
		assert.Equal(t, "ev-2", byEvidenceIDs[0].ID)
		assert.Equal(t, "ev-4", byEvidenceIDs[1].ID)

		bySourceIDs := buildAutonomousHypothesisEvidence(
			wisdev.ReasoningNode{SourceIDs: []string{"s1"}},
			findings,
		)
		require.Len(t, bySourceIDs, 1)
		assert.Equal(t, "ev-1", bySourceIDs[0].ID)

		fallback := buildAutonomousHypothesisEvidence(wisdev.ReasoningNode{}, findings)
		require.Len(t, fallback, 3)
		assert.Equal(t, "ev-1", fallback[0].ID)
		noMatches := buildAutonomousHypothesisEvidence(wisdev.ReasoningNode{SourceIDs: []string{"missing"}}, findings)
		assert.Empty(t, noMatches)

		fallbackFromEvidenceIDs := buildAutonomousHypothesisEvidence(
			wisdev.ReasoningNode{Metadata: map[string]any{"evidenceIds": []any{"missing"}}},
			findings,
		)
		require.Len(t, fallbackFromEvidenceIDs, 3)
		assert.Equal(t, "ev-1", fallbackFromEvidenceIDs[0].ID)

		sourceFallback := buildAutonomousHypothesisEvidence(
			wisdev.ReasoningNode{
				Metadata:  map[string]any{"evidenceIds": []any{"missing"}},
				SourceIDs: []string{"s2"},
			},
			findings,
		)
		require.Len(t, sourceFallback, 1)
		assert.Equal(t, "ev-2", sourceFallback[0].ID)

		assert.Nil(t, buildAutonomousHypothesisEvidence(wisdev.ReasoningNode{}, nil))
	})

	t.Run("maybeBuildAutonomousHypothesisPayloads", func(t *testing.T) {
		original := proposeAutonomousHypotheses
		t.Cleanup(func() { proposeAutonomousHypotheses = original })

		proposeAutonomousHypotheses = func(context.Context, *wisdev.AgentGateway, string) ([]wisdev.Hypothesis, error) {
			return []wisdev.Hypothesis{{ID: "h1", Claim: "Claim 1"}}, nil
		}
		payloads := maybeBuildAutonomousHypothesisPayloads(context.Background(), nil, "query")
		require.Len(t, payloads, 1)
		assert.Equal(t, "h1", payloads[0]["id"])
		assert.Equal(t, "validated", payloads[0]["status"])

		proposeAutonomousHypotheses = func(context.Context, *wisdev.AgentGateway, string) ([]wisdev.Hypothesis, error) {
			return nil, errors.New("boom")
		}
		assert.Nil(t, maybeBuildAutonomousHypothesisPayloads(context.Background(), nil, "query"))
		proposeAutonomousHypotheses = func(context.Context, *wisdev.AgentGateway, string) ([]wisdev.Hypothesis, error) {
			return []wisdev.Hypothesis{}, nil
		}
		assert.Nil(t, maybeBuildAutonomousHypothesisPayloads(context.Background(), nil, "query"))
	})

	t.Run("maybeBuildAutonomousHypothesisPayloadsForQueries dedupes queries", func(t *testing.T) {
		original := proposeAutonomousHypotheses
		t.Cleanup(func() { proposeAutonomousHypotheses = original })

		proposeAutonomousHypotheses = func(_ context.Context, _ *wisdev.AgentGateway, query string) ([]wisdev.Hypothesis, error) {
			return []wisdev.Hypothesis{{ID: query, Claim: query}}, nil
		}
		payloads := maybeBuildAutonomousHypothesisPayloadsForQueries(context.Background(), nil, []string{" alpha ", "", "alpha"})
		require.Len(t, payloads, 1)
		assert.Equal(t, "alpha", payloads[0]["claim"])
	})

	t.Run("maybeBuildAutonomousHypothesisPayloads bypasses slow vertex direct via interactive sidecar", func(t *testing.T) {
		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

		var captured structuredRequestCapture
		llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/llm/structured-output", r.URL.Path)
			require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"jsonResult": `{"hypotheses":[{"id":"h1","claim":"Sleep consolidation is stage-dependent","falsifiabilityCondition":"No stage-specific effect appears","confidenceThreshold":0.6}]}`,
				"modelUsed":  "test-autonomous-hypotheses-sidecar",
			}))
		}))
		defer llmServer.Close()
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

		client := llm.NewClientWithTimeout(500 * time.Millisecond)
		setUnexportedField(t, client, "transport", "http-json")
		setUnexportedField(t, client, "httpBaseURL", llmServer.URL)

		slowDirect := &slowVertexModelsClient{}
		vertexClient := &llm.VertexClient{}
		setUnexportedField(t, vertexClient, "client", slowDirect)
		setUnexportedField(t, vertexClient, "backend", "vertex_ai")
		client.VertexDirect = vertexClient

		start := time.Now()
		payloads := maybeBuildAutonomousHypothesisPayloads(
			context.Background(),
			&wisdev.AgentGateway{Brain: wisdev.NewBrainCapabilities(client)},
			"sleep and memory",
		)
		elapsed := time.Since(start)

		require.Len(t, payloads, 1)
		assert.Equal(t, "h1", payloads[0]["id"])
		assert.Equal(t, "Sleep consolidation is stage-dependent", payloads[0]["claim"])
		assert.Less(t, elapsed, time.Second)
		assert.False(t, slowDirect.called.Load())
		assert.Equal(t, llm.ResolveStandardModel(), captured.Model)
		assert.Equal(t, "structured_high_value", captured.RequestClass)
		assert.Equal(t, "standard", captured.RetryProfile)
		assert.Equal(t, "priority", captured.ServiceTier)
		assert.Greater(t, captured.LatencyBudgetMs, int32(0))
		assertStructuredPromptHygiene(t, captured.Prompt)
	})

	t.Run("buildAutonomousGapPayloadsFromLoopAnalysis", func(t *testing.T) {
		payloads := buildAutonomousGapPayloadsFromLoopAnalysis(&wisdev.LoopGapState{
			Sufficient:         false,
			Reasoning:          "Need more interventional evidence.",
			NextQueries:        []string{"sleep intervention trial memory"},
			MissingAspects:     []string{"interventional outcomes"},
			MissingSourceTypes: []string{"randomized trials"},
			Contradictions:     []string{"Observational and intervention findings diverge."},
			Confidence:         0.41,
			Coverage: wisdev.LoopCoverageState{
				QueriesWithoutCoverage:   []string{"sleep meta analysis"},
				UnexecutedPlannedQueries: []string{"hippocampal replay"},
			},
		})

		require.Len(t, payloads, 5)
		assert.Equal(t, "coverage", payloads[0]["type"])
		assert.Equal(t, "source_diversity", payloads[1]["type"])
		assert.Equal(t, "contradiction", payloads[2]["type"])
		assert.Equal(t, "query_coverage", payloads[3]["type"])
		assert.Equal(t, "planned_query", payloads[4]["type"])
		assert.Equal(t, []string{"sleep intervention trial memory"}, payloads[0]["suggestedApproaches"])
	})

	t.Run("buildAutonomousGapPayloadsFromLoopAnalysis preserves structured ledger entries", func(t *testing.T) {
		payloads := buildAutonomousGapPayloadsFromLoopAnalysis(&wisdev.LoopGapState{
			ObservedSourceFamilies: []string{"crossref", "openalex"},
			ObservedEvidenceCount:  3,
			Ledger: []wisdev.CoverageLedgerEntry{
				{
					ID:                "ledger-1",
					Category:          "source_diversity",
					Status:            "open",
					Title:             "Need interventional trials",
					Description:       "Independent intervention evidence is still missing.",
					SupportingQueries: []string{"sleep intervention trial memory"},
					SourceFamilies:    []string{"crossref", "openalex"},
					Confidence:        0.63,
				},
			},
		})

		require.Len(t, payloads, 1)
		assert.Equal(t, "ledger-1", payloads[0]["id"])
		assert.Equal(t, "source_diversity", payloads[0]["type"])
		assert.Equal(t, "open", payloads[0]["status"])
		assert.Equal(t, 3, payloads[0]["observedEvidenceCount"])
		assert.Equal(t, []string{"crossref", "openalex"}, payloads[0]["sourceFamilies"])
	})
}

func TestResearchEnrichmentCoverageHelpers(t *testing.T) {
	t.Run("coverage payload helpers", func(t *testing.T) {
		normalized := normalizeAutonomousCoveragePayloadByQuery(map[string][]map[string]any{
			" ": nil,
			" q1 ": []map[string]any{
				{"id": "p1", "title": "Paper 1"},
			},
		})
		require.Len(t, normalized, 1)
		assert.Contains(t, normalized, "q1")

		merged := mergeAutonomousCoverageQueries(map[string][]map[string]any{
			"q1": []map[string]any{
				{"id": "p1", "title": "Paper 1"},
				{"title": "Untitled"},
				{"id": "p1", "title": "Paper 1 duplicate"},
			},
		}, []string{"q1"})
		require.Len(t, merged, 2)
		assert.Equal(t, "p1", merged[0]["id"])

		coverage := buildAutonomousCoveragePayload(
			map[string][]string{"primary": []string{" q1 ", "q2"}},
			[]string{"q1", "q2", "q3"},
			map[string][]map[string]any{
				"q1": []map[string]any{
					{"id": "p1", "title": "Paper 1"},
				},
				"q2": []map[string]any{
					{"title": "Paper 2"},
				},
			},
		)
		require.Contains(t, coverage, "primary")

		assert.Equal(t, "paper-1", autonomousCoveragePaperKey(map[string]any{"paperId": "paper-1"}))
		assert.Equal(t, "paper 2", autonomousCoveragePaperKey(map[string]any{"title": "Paper 2"}))

		sources := serializeAutonomousCoverageSourcesByQuery(map[string][]wisdev.Source{
			" q ": []wisdev.Source{
				{ID: "s1", Title: "Source 1"},
			},
		})
		require.Contains(t, sources, "q")
		require.Len(t, sources["q"], 1)

		searchPapers := serializeAutonomousCoverageSearchPapersByQuery(map[string][]search.Paper{
			" q2 ": []search.Paper{
				{ID: "sp1", Title: "Search Paper", Source: "openalex"},
			},
		})
		require.Contains(t, searchPapers, "q2")
		require.Len(t, searchPapers["q2"], 1)

		original := []map[string]any{
			{"id": "p1", "title": "Paper 1"},
		}
		cloned := cloneAutonomousPaperPayloads(original)
		require.Len(t, cloned, 1)
		cloned[0]["title"] = "Changed"
		assert.Equal(t, "Paper 1", original[0]["title"])
		assert.Equal(t, "Changed", cloned[0]["title"])
	})

	t.Run("buildAutonomousGapPayloads", func(t *testing.T) {
		assert.Nil(t, buildAutonomousGapPayloads(nil))

		payloads := buildAutonomousGapPayloads(map[string]any{
			"gaps": []any{
				"  ",
				"This is a very long gap description that should be truncated at some point because it is longer than seventy-two characters in length.",
				"Short gap",
			},
		})
		require.Len(t, payloads, 2)
		assert.Equal(t, "gap_1", payloads[0]["id"])
		assert.Contains(t, payloads[0]["title"], "...")
		assert.Equal(t, "Short gap", payloads[1]["title"])
	})
}

func TestResearchEnrichmentMoreBranches(t *testing.T) {
	t.Run("buildAutonomousHypothesisPayloadsFromLoop nil input", func(t *testing.T) {
		assert.Nil(t, buildAutonomousHypothesisPayloadsFromLoop("query", nil))
	})

	t.Run("serializeAutonomousHypothesisPayloads", func(t *testing.T) {
		assert.Nil(t, serializeAutonomousHypothesisPayloads(nil))

		payloads := serializeAutonomousHypothesisPayloads([]wisdev.Hypothesis{
			{Claim: "  ", Text: "", Query: ""},
			{Text: " derived claim ", ConfidenceScore: 1.2},
			{ID: "id-3", Claim: "claim 3", FalsifiabilityCondition: " condition "},
		})
		require.Len(t, payloads, 2)
		assert.NotEmpty(t, payloads[0]["id"])
		assert.Equal(t, "derived claim", payloads[0]["claim"])
		assert.Equal(t, "validated", payloads[0]["status"])
		assert.Equal(t, "condition", payloads[1]["falsifiabilityCondition"])
	})

	t.Run("buildAutonomousHypothesisPayloads policy branches", func(t *testing.T) {
		loopResult := &wisdev.LoopResult{
			ReasoningGraph: &wisdev.ReasoningGraph{
				Nodes: []wisdev.ReasoningNode{
					{ID: "h1", Type: wisdev.ReasoningNodeHypothesis, RefinedQuery: "Claim 1", Confidence: 0.9},
				},
			},
		}

		blockedPolicy := wisdev.DeepAgentsExecutionPolicy{}
		assert.Nil(t, buildAutonomousHypothesisPayloads(context.Background(), nil, "query", []string{"query"}, loopResult, blockedPolicy))

		generatePolicy := wisdev.DeepAgentsExecutionPolicy{
			EnableWisdevTools: true,
			AllowlistedTools:  []string{wisdev.ActionResearchGenerateHypotheses},
		}
		seeded := buildAutonomousHypothesisPayloads(context.Background(), nil, "query", []string{"alpha", "beta"}, nil, generatePolicy)
		require.Len(t, seeded, 2)
		assert.Equal(t, "planned_query", seeded[0]["category"])
		assert.Equal(t, "candidate", seeded[0]["status"])

		proposePolicy := wisdev.DeepAgentsExecutionPolicy{
			EnableWisdevTools: true,
			AllowlistedTools:  []string{wisdev.ActionResearchProposeHypotheses},
		}
		merged := buildAutonomousHypothesisPayloads(context.Background(), nil, "query", []string{"alpha"}, loopResult, proposePolicy)
		require.Len(t, merged, 1)
		assert.Equal(t, "h1", merged[0]["id"])

		original := proposeAutonomousHypotheses
		t.Cleanup(func() { proposeAutonomousHypotheses = original })
		proposeAutonomousHypotheses = func(context.Context, *wisdev.AgentGateway, string) ([]wisdev.Hypothesis, error) {
			return []wisdev.Hypothesis{}, nil
		}
		proposeAndGeneratePolicy := wisdev.DeepAgentsExecutionPolicy{
			EnableWisdevTools: true,
			AllowlistedTools: []string{
				wisdev.ActionResearchProposeHypotheses,
				wisdev.ActionResearchGenerateHypotheses,
			},
		}
		fallbackToSeed := buildAutonomousHypothesisPayloads(context.Background(), nil, "query", []string{"alpha", "beta"}, nil, proposeAndGeneratePolicy)
		require.Len(t, fallbackToSeed, 2)
		assert.Equal(t, "planned_query", fallbackToSeed[0]["category"])
	})

	t.Run("mergeAutonomousHypothesisPayloads", func(t *testing.T) {
		assert.Nil(t, mergeAutonomousHypothesisPayloads(nil))
		merged := mergeAutonomousHypothesisPayloads(
			[]map[string]any{{"id": "p1", "claim": "Claim"}},
			[]map[string]any{{"id": "p2", "claim": "Claim"}},
			[]map[string]any{{}},
		)
		require.Len(t, merged, 1)
		assert.Equal(t, "p1", merged[0]["id"])
	})

	t.Run("maybeBuildAutonomousHypothesisPayloadsForQueries", func(t *testing.T) {
		assert.Nil(t, maybeBuildAutonomousHypothesisPayloadsForQueries(context.Background(), nil, nil))
	})
}
