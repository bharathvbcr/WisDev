package wisdev

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

func TestAutonomousLoop_CoordinateAgentDebateUsesStructuredSchemaPrompt(t *testing.T) {
	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	loop := NewAutonomousLoop(search.NewProviderRegistry(), lc)

	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assertWisdevStructuredPromptHygiene(t, req.Prompt)
		return strings.Contains(req.Prompt, "Agent A (Proposer)") &&
			strings.Contains(req.Prompt, "supplied structured output schema") &&
			req.GetJsonSchema() != ""
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"score":0.74,"verdict":"uncertain","reasoning":"mixed evidence","branchingDecision":"keep","suggestedQueries":["replication evidence"]}`}, nil).Once()

	result, err := loop.coordinateAgentDebate(context.Background(), &Hypothesis{
		ID:                      "h1",
		Claim:                   "sleep improves memory consolidation",
		FalsifiabilityCondition: "randomized sleep deprivation studies refute it",
	}, []EvidenceFinding{
		{Claim: "sleep improves recall", Confidence: 0.72, PaperTitle: "Sleep Study"},
	})

	assert.NoError(t, err)
	assert.Equal(t, "h1", result.HypothesisID)
	assert.Equal(t, "uncertain", result.Verdict)
	msc.AssertExpectations(t)
}

func TestAutonomousLoop_Extra(t *testing.T) {
	reg := search.NewProviderRegistry()
	mockP := &mockSearchProvider{
		name: "mock_extra",
	}
	reg.Register(mockP)
	reg.SetDefaultOrder([]string{"mock_extra"})

	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	allowAutonomousHypothesisProposals(msc, "")

	ctx := context.Background()

	t.Run("Run - Zero Iterations", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		allowAutonomousHypothesisProposals(msc, "")
		l := NewAutonomousLoop(reg, lc)

		// Mock for assembleDossier (even if no papers)
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Extract the top 2-3")
		})).Return(&llmv1.StructuredResponse{JsonResult: `[]`}, nil).Maybe()

		// Mock for synthesizeWithEvidence
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
		})).Return(&llmv1.GenerateResponse{Text: "Empty Result"}, nil).Once()
		allowAutonomousCritique(msc, "")

		res, err := l.Run(ctx, LoopRequest{Query: "test", MaxIterations: 0})
		assert.NoError(t, err)
		assert.False(t, res.Converged)
		assert.Equal(t, 0, res.Iterations)
		assert.Equal(t, "Empty Result", res.FinalAnswer)
	})

	t.Run("Run - Max Iterations reached", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		allowAutonomousHypothesisProposals(msc, "")
		l := NewAutonomousLoop(reg, lc)

		// evaluateSufficiency always false
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Evaluate if the following papers")
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": false, "reasoning": "need more", "nextQuery": "refined"}`}, nil)

		// assembleDossier
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Extract the top 2-3")
		})).Return(&llmv1.StructuredResponse{JsonResult: `[]`}, nil)

		// synthesizeWithEvidence
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
		})).Return(&llmv1.GenerateResponse{Text: "Final"}, nil)
		allowAutonomousCritique(msc, "")

		mockP.papers = []search.Paper{{ID: "p1", Title: "T1"}}

		res, err := l.Run(ctx, LoopRequest{Query: "test", MaxIterations: 2})
		assert.NoError(t, err)
		assert.False(t, res.Converged)
		assert.Equal(t, 2, res.Iterations)
	})

	t.Run("assembleDossier - Success", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		allowAutonomousHypothesisProposals(msc, "")
		l := NewAutonomousLoop(reg, lc)

		papers := []search.Paper{{ID: "p1", Title: "T1", Abstract: "A1"}}
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Extract the top 2-3")
		})).Return(&llmv1.StructuredResponse{JsonResult: `[{"claim": "c1", "snippet": "s1", "confidence": 0.9}]`}, nil).Once()

		res, err := l.assembleDossier(ctx, "q", papers)
		assert.NoError(t, err)
		assert.Len(t, res, 1)
		assert.NotEmpty(t, res[0].PaperID)
		assert.NotEmpty(t, res[0].Claim)
		assert.NotEmpty(t, res[0].Snippet)
		assert.Equal(t, "T1", res[0].PaperTitle)
		assert.Greater(t, res[0].Confidence, 0.0)
	})

	t.Run("synthesizeWithEvidence - Success", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		allowAutonomousHypothesisProposals(msc, "")
		l := NewAutonomousLoop(reg, lc)

		papers := []search.Paper{{ID: "p1", Title: "T1"}}
		evidence := []EvidenceItem{{Claim: "c1", Snippet: "s1"}}

		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
		})).Return(&llmv1.GenerateResponse{Text: "Final Synthesis"}, nil).Once()

		res, err := l.synthesizeWithEvidence(ctx, "q", papers, evidence)
		assert.NoError(t, err)
		if assert.NotNil(t, res) {
			assert.Equal(t, "Final Synthesis", res.PlainText)
		}
	})

	t.Run("evaluateSufficiency - Unmarshal Error", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		allowAutonomousHypothesisProposals(msc, "")
		l := NewAutonomousLoop(reg, lc)

		papers := []search.Paper{{ID: "p1", Title: "T1"}}
		msc.On("StructuredOutput", mock.Anything, mock.Anything).Return(&llmv1.StructuredResponse{JsonResult: "invalid json"}, nil).Once()

		_, err := l.evaluateSufficiency(ctx, "q", papers)
		assert.Error(t, err)
	})

	t.Run("Run - Emission", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		allowAutonomousHypothesisProposals(msc, "")
		l := NewAutonomousLoop(reg, lc)

		// Mock for assembleDossier
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Extract the top 2-3")
		})).Return(&llmv1.StructuredResponse{JsonResult: `[]`}, nil).Maybe()

		// Mock for synthesizeWithEvidence
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
		})).Return(&llmv1.GenerateResponse{Text: "Result"}, nil).Once()
		allowAutonomousCritique(msc, "")

		emitted := false
		onEvent := func(ev PlanExecutionEvent) {
			if ev.Message == "autonomous loop started" {
				emitted = true
			}
		}

		_, err := l.Run(ctx, LoopRequest{Query: "test", MaxIterations: 0}, onEvent)
		assert.NoError(t, err)
		assert.True(t, emitted)
	})

	t.Run("Run - Planned seed queries drive retrieval and hypotheses", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		failAutonomousHypothesisProposals(msc, errors.New("proposal unavailable"))
		plannedSearches := make([]string, 0, 3)
		var plannedSearchesMu sync.Mutex
		seedProvider := &mockSearchProvider{
			name: "seed_queries",
			SearchFunc: func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
				plannedSearchesMu.Lock()
				plannedSearches = append(plannedSearches, query)
				plannedSearchesMu.Unlock()
				return []search.Paper{{ID: query, Title: query, Abstract: "A"}}, nil
			},
		}
		seedRegistry := search.NewProviderRegistry()
		seedRegistry.Register(seedProvider)
		seedRegistry.SetDefaultOrder([]string{"seed_queries"})

		l := NewAutonomousLoop(seedRegistry, lc)
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Evaluate if the following papers")
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": false, "reasoning": "need more", "nextQuery": ""}`}, nil).Once()
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Evaluate if the following papers")
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": false, "reasoning": "need more", "nextQuery": ""}`}, nil).Once()
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Evaluate if the following papers")
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": true, "reasoning": "enough", "nextQuery": ""}`}, nil).Once()
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Extract the top 2-3")
		})).Return(&llmv1.StructuredResponse{JsonResult: `[]`}, nil).Times(3)
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
		})).Return(&llmv1.GenerateResponse{Text: "Final"}, nil).Once()
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return req != nil && strings.Contains(req.Prompt, "Perform a harsh, critical analysis of the current evidence")
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": false, "reasoning": "mock harsh critique", "nextQuery": ""}`}, nil).Maybe()
		allowAutonomousCritique(msc, "")

		res, err := l.Run(ctx, LoopRequest{
			Query:         "sleep and memory",
			SeedQueries:   []string{"hippocampal replay", "systems consolidation"},
			MaxIterations: 3,
		})
		assert.NoError(t, err)
		assert.ElementsMatch(t, []string{"sleep and memory", "hippocampal replay", "systems consolidation"}, plannedSearches)
		assert.Equal(t, []string{"sleep and memory", "hippocampal replay", "systems consolidation"}, res.ExecutedQueries)
		if assert.Len(t, res.QueryCoverage, 3) {
			assert.Len(t, res.QueryCoverage["sleep and memory"], 1)
			assert.Len(t, res.QueryCoverage["hippocampal replay"], 1)
			assert.Len(t, res.QueryCoverage["systems consolidation"], 1)
		}
		assert.NotNil(t, res.ReasoningGraph)
		hypothesisLabels := make([]string, 0)
		for _, node := range res.ReasoningGraph.Nodes {
			if node.Type == ReasoningNodeHypothesis {
				hypothesisLabels = append(hypothesisLabels, node.Label)
			}
		}
		assert.ElementsMatch(t, []string{"sleep and memory", "hippocampal replay", "systems consolidation"}, hypothesisLabels)
	})

	t.Run("Fallback hypotheses attach scoped evidence instead of cloning top findings", func(t *testing.T) {
		findings := []EvidenceFinding{
			{
				ID:         "ev-primary",
				Claim:      "Sleep duration predicts memory retention.",
				SourceID:   "paper-primary",
				PaperTitle: "Primary Paper",
				Confidence: 0.71,
			},
			{
				ID:         "ev-replay",
				Claim:      "Hippocampal replay events increase after learning.",
				SourceID:   "paper-replay",
				PaperTitle: "Replay Paper",
				Confidence: 0.91,
			},
			{
				ID:         "ev-systems",
				Claim:      "Systems consolidation depends on cortical integration.",
				SourceID:   "paper-systems",
				PaperTitle: "Systems Paper",
				Confidence: 0.83,
			},
		}
		querySourceIndex := buildLoopQuerySourceIndex(map[string][]search.Paper{
			"sleep and memory": {
				{ID: "paper-primary", Title: "Primary Paper"},
			},
			"hippocampal replay": {
				{ID: "paper-replay", Title: "Replay Paper"},
			},
			"systems consolidation": {
				{ID: "paper-systems", Title: "Systems Paper"},
			},
		})

		hypotheses := buildAutonomousFallbackHypotheses(
			"sleep and memory",
			[]string{"hippocampal replay", "systems consolidation"},
			findings,
			querySourceIndex,
			0,
		)
		if assert.Len(t, hypotheses, 3) {
			byClaim := make(map[string]Hypothesis, len(hypotheses))
			for _, hypothesis := range hypotheses {
				byClaim[hypothesis.Claim] = hypothesis
			}
			if assert.Len(t, byClaim["sleep and memory"].Evidence, 1) {
				assert.Equal(t, "ev-primary", byClaim["sleep and memory"].Evidence[0].ID)
			}
			if assert.Len(t, byClaim["hippocampal replay"].Evidence, 1) {
				assert.Equal(t, "ev-replay", byClaim["hippocampal replay"].Evidence[0].ID)
			}
			if assert.Len(t, byClaim["systems consolidation"].Evidence, 1) {
				assert.Equal(t, "ev-systems", byClaim["systems consolidation"].Evidence[0].ID)
			}
		}
	})

	t.Run("Capability hypotheses select claim-matched evidence instead of shared findings", func(t *testing.T) {
		findings := []EvidenceFinding{
			{
				ID:         "ev-replay",
				Claim:      "Hippocampal replay events increase after learning.",
				Snippet:    "Replay increases immediately after task acquisition.",
				SourceID:   "paper-replay",
				PaperTitle: "Replay Paper",
				Confidence: 0.9,
			},
			{
				ID:         "ev-systems",
				Claim:      "Systems consolidation depends on cortical integration.",
				Snippet:    "Cortical integration predicts long-term consolidation.",
				SourceID:   "paper-systems",
				PaperTitle: "Systems Paper",
				Confidence: 0.82,
			},
			{
				ID:         "ev-unrelated",
				Claim:      "Circadian rhythm shifts alter glucose metabolism.",
				SourceID:   "paper-unrelated",
				PaperTitle: "Metabolism Paper",
				Confidence: 0.97,
			},
		}
		proposed := []Hypothesis{
			{
				Claim:                   "Hippocampal replay strengthens memory consolidation",
				FalsifiabilityCondition: "Replay suppression should reduce consolidation gains",
				ConfidenceThreshold:     0.81,
			},
			{
				Claim:                   "Cortical integration supports systems consolidation",
				FalsifiabilityCondition: "Disrupting cortical integration should weaken consolidation",
				ConfidenceThreshold:     0.76,
			},
		}

		hypotheses := normalizeAutonomousCapabilityHypotheses(
			"sleep and memory",
			proposed,
			findings,
			nil,
			0,
		)
		if assert.Len(t, hypotheses, 2) {
			if assert.Len(t, hypotheses[0].Evidence, 1) {
				assert.Equal(t, "ev-replay", hypotheses[0].Evidence[0].ID)
			}
			if assert.Len(t, hypotheses[1].Evidence, 1) {
				assert.Equal(t, "ev-systems", hypotheses[1].Evidence[0].ID)
			}
			assert.NotEqual(t, hypotheses[0].Evidence[0].ID, hypotheses[1].Evidence[0].ID)
		}
	})

	t.Run("Run - Search budget shapes retrieval breadth and unique-paper cap", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		failAutonomousHypothesisProposals(msc, errors.New("proposal unavailable"))
		searchLimits := make([]int, 0, 1)
		budgetProvider := &mockSearchProvider{
			name: "budgeted_queries",
			SearchFunc: func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
				searchLimits = append(searchLimits, opts.Limit)
				return []search.Paper{
					{ID: "p1", Title: "Paper 1", Abstract: "A1"},
					{ID: "p2", Title: "Paper 2", Abstract: "A2"},
					{ID: "p3", Title: "Paper 3", Abstract: "A3"},
				}, nil
			},
		}
		budgetRegistry := search.NewProviderRegistry()
		budgetRegistry.Register(budgetProvider)
		budgetRegistry.SetDefaultOrder([]string{"budgeted_queries"})

		l := NewAutonomousLoop(budgetRegistry, lc)
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Evaluate if the following papers")
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": false, "reasoning": "need more", "nextQuery": "unused refinement"}`}, nil).Once()
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Extract the top 2-3")
		})).Return(&llmv1.StructuredResponse{JsonResult: `[]`}, nil).Times(2)
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
		})).Return(&llmv1.GenerateResponse{Text: "Budgeted Final"}, nil).Once()
		allowAutonomousCritique(msc, "")

		res, err := l.Run(ctx, LoopRequest{
			Query:           "budget query",
			MaxIterations:   2,
			HitsPerSearch:   4,
			MaxUniquePapers: 2,
		})
		assert.NoError(t, err)
		assert.Equal(t, "Budgeted Final", res.FinalAnswer)
		assert.Equal(t, []int{2}, searchLimits)
		assert.Len(t, res.Papers, 2)
		assert.Equal(t, []string{"budget query"}, res.ExecutedQueries)
		if assert.Len(t, res.QueryCoverage, 1) {
			assert.Len(t, res.QueryCoverage["budget query"], 2)
		}
		assert.Equal(t, 1, res.Iterations)
		assert.False(t, res.Converged)
	})

	t.Run("Run - Search-term budget caps the number of executed queries", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		failAutonomousHypothesisProposals(msc, errors.New("proposal unavailable"))
		searchedQueries := make([]string, 0, 4)
		var searchedQueriesMu sync.Mutex
		cappedProvider := &mockSearchProvider{
			name: "capped_queries",
			SearchFunc: func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
				searchedQueriesMu.Lock()
				searchedQueries = append(searchedQueries, query)
				searchedQueriesMu.Unlock()
				return []search.Paper{
					{ID: query, Title: query, Abstract: "A"},
				}, nil
			},
		}
		cappedRegistry := search.NewProviderRegistry()
		cappedRegistry.Register(cappedProvider)
		cappedRegistry.SetDefaultOrder([]string{"capped_queries"})

		l := NewAutonomousLoop(cappedRegistry, lc)
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Evaluate if the following papers")
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": false, "reasoning": "need more", "nextQuery": ""}`}, nil).Times(4)
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Extract the top 2-3")
		})).Return(&llmv1.StructuredResponse{JsonResult: `[]`}, nil).Times(4)
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
		})).Return(&llmv1.GenerateResponse{Text: "Capped Final"}, nil).Once()
		allowAutonomousCritique(msc, "")

		res, err := l.Run(ctx, LoopRequest{
			Query:          "sleep and memory",
			SeedQueries:    []string{"hippocampal replay", "systems consolidation", "slow-wave sleep", "memory reactivation"},
			MaxIterations:  5,
			MaxSearchTerms: 4,
		})
		assert.NoError(t, err)
		assert.ElementsMatch(t, []string{"sleep and memory", "hippocampal replay", "systems consolidation", "slow-wave sleep"}, searchedQueries)
		assert.Equal(t, []string{"sleep and memory", "hippocampal replay", "systems consolidation", "slow-wave sleep"}, res.ExecutedQueries)
		assert.Equal(t, 2, res.Iterations)
	})

	t.Run("Run - Gap analysis preserves structured missing-evidence ledger", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		failAutonomousHypothesisProposals(msc, errors.New("proposal unavailable"))
		allowAutonomousHypothesisEvaluation(msc, "")
		gapProvider := &mockSearchProvider{
			name: "gap_queries",
			SearchFunc: func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
				return []search.Paper{
					{ID: "paper-1", Title: "Sleep outcomes", Abstract: "Observational sleep duration predicted better memory retention.", Source: "crossref"},
				}, nil
			},
		}
		gapRegistry := search.NewProviderRegistry()
		gapRegistry.Register(gapProvider)
		gapRegistry.SetDefaultOrder([]string{"gap_queries"})

		l := NewAutonomousLoop(gapRegistry, lc)
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Evaluate if the following papers")
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": false, "reasoning": "Need intervention studies and contradiction resolution.", "nextQuery": "sleep intervention trial memory", "nextQueries": ["sleep intervention trial memory", "sleep meta analysis memory"], "missingAspects": ["interventional outcomes"], "missingSourceTypes": ["randomized trials"], "contradictions": ["Observational and intervention findings diverge on effect size."], "confidence": 0.38}`}, nil).Once()
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Extract the top 2-3")
		})).Return(&llmv1.StructuredResponse{JsonResult: `[]`}, nil).Once()
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
		})).Return(&llmv1.GenerateResponse{Text: "Gap-focused synthesis"}, nil).Once()
		allowAutonomousCritique(msc, "")

		res, err := l.Run(ctx, LoopRequest{
			Query:         "sleep and memory",
			SeedQueries:   []string{"hippocampal replay"},
			MaxIterations: 1,
		})
		assert.NoError(t, err)
		assert.Equal(t, "Gap-focused synthesis", res.FinalAnswer)
		assert.NotNil(t, res.DraftCritique)
		assert.False(t, res.DraftCritique.NeedsRevision)
		assert.NotEmpty(t, strings.TrimSpace(res.DraftCritique.Reasoning))
		if assert.NotNil(t, res.GapAnalysis) {
			assert.False(t, res.GapAnalysis.Sufficient)
			assert.Contains(t, res.GapAnalysis.Reasoning, "Need intervention studies and contradiction resolution.")
			assert.Contains(t, res.GapAnalysis.Reasoning, "Grounded coverage is sufficient.")
			assert.Contains(t, res.GapAnalysis.NextQueries, "sleep intervention trial memory")
			assert.Contains(t, res.GapAnalysis.NextQueries, "sleep meta analysis memory")
			assert.Contains(t, res.GapAnalysis.MissingAspects, "interventional outcomes")
			assert.Contains(t, res.GapAnalysis.MissingAspects, "2 hypothesis branch coverage gap(s) remain open")
			assert.Equal(t, []string{"randomized trials"}, res.GapAnalysis.MissingSourceTypes)
			assert.Equal(t, []string{"Observational and intervention findings diverge on effect size."}, res.GapAnalysis.Contradictions)
			assert.Equal(t, 2, res.GapAnalysis.Coverage.PlannedQueryCount)
			assert.Equal(t, 1, res.GapAnalysis.Coverage.ExecutedQueryCount)
			assert.Equal(t, 1, res.GapAnalysis.Coverage.CoveredQueryCount)
			assert.Equal(t, []string{"hippocampal replay"}, res.GapAnalysis.Coverage.UnexecutedPlannedQueries)
		}
	})
}

func TestRefineDraftWithCritiqueUsesBoundedStandardRequest(t *testing.T) {
	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	loop := &AutonomousLoop{llmClient: lc}

	msc.On("Generate", mock.MatchedBy(func(ctx context.Context) bool {
		deadline, ok := ctx.Deadline()
		return ok && time.Until(deadline) <= optionalCritiqueRefinementLatencyBudget
	}), mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return req.GetRequestClass() == "standard" &&
			req.GetServiceTier() == "standard" &&
			req.GetLatencyBudgetMs() == int32(optionalCritiqueRefinementLatencyBudget/time.Millisecond) &&
			strings.Contains(req.GetPrompt(), "Revise the research draft")
	})).Return(nil, context.DeadlineExceeded).Once()

	refined, err := loop.refineDraftWithCritique(
		context.Background(),
		"RLHF overview",
		"draft answer",
		&LoopDraftCritique{
			NeedsRevision:      true,
			Reasoning:          "needs qualification",
			MissingAspects:     []string{"replication"},
			MissingSourceTypes: []string{"benchmark"},
		},
		[]EvidenceItem{{Claim: "claim", Snippet: "snippet", PaperTitle: "Paper"}},
	)

	if err != nil {
		t.Fatalf("expected timeout fallback, got %v", err)
	}
	if !strings.Contains(refined, "Verification note") {
		t.Fatalf("expected heuristic refinement note, got %q", refined)
	}
	msc.AssertNumberOfCalls(t, "Generate", 1)
}
