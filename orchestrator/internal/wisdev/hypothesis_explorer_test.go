package wisdev

import (
	"context"
	"errors"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

func TestHypothesisExplorer_ExploreAll(t *testing.T) {
	// Setup mocks
	reg := search.NewProviderRegistry()
	mockProvider := &mockSearchProvider{
		name: "test_provider",
		SearchFunc: func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
			return []search.Paper{
				{ID: "p1", Title: "Paper 1", Abstract: "Evidence for " + query},
			}, nil
		},
	}
	reg.Register(mockProvider)
	reg.SetDefaultOrder([]string{"test_provider"})

	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)

	// Mock hypothesis query generation
	msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return req != nil && (req.Model == llm.ResolveLightModel() || req.Model == llm.ResolveStandardModel())
	})).Return(&llmv1.GenerateResponse{Text: `["query1", "query2"]`}, nil)

	// Mock evaluation
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil && req.Model == llm.ResolveLightModel()
	})).Return(&llmv1.StructuredResponse{
		JsonResult: `{"score": 0.8, "verdict": "supported", "reasoning": "consistent evidence found"}`,
	}, nil)

	brainCaps := NewBrainCapabilities(lc)
	evaluator := NewHypothesisEvaluator(brainCaps)
	explorer := NewHypothesisExplorer(reg, evaluator, brainCaps, 2)

	hypotheses := []*Hypothesis{
		{ID: "h1", Claim: "Hypothesis 1", ConfidenceScore: 0.5},
		{ID: "h2", Claim: "Hypothesis 2", ConfidenceScore: 0.4},
		{ID: "h3", Claim: "Hypothesis 3", ConfidenceScore: 0.3},
	}

	searchOpts := search.SearchOpts{Limit: 5}
	results := explorer.ExploreAll(context.Background(), hypotheses, searchOpts, 2)

	assert.Len(t, results, 3)
	for _, res := range results {
		assert.NotNil(t, res.Hypothesis)
		assert.NotEmpty(t, res.NewEvidence)
		assert.Equal(t, "supported", res.EvaluationResult.Verdict)
		assert.Equal(t, 0.8, res.Confidence)
	}
}

func TestHypothesisExplorer_ExploreHypothesis_Refinement(t *testing.T) {
	// Setup mocks
	reg := search.NewProviderRegistry()
	mockProvider := &mockSearchProvider{
		name: "test_provider",
		SearchFunc: func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
			return []search.Paper{
				{ID: "p-" + query, Title: "Paper " + query},
			}, nil
		},
	}
	reg.Register(mockProvider)
	reg.SetDefaultOrder([]string{"test_provider"})

	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)

	// Mock hypothesis query generation
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil && (req.Model == llm.ResolveLightModel() || req.Model == llm.ResolveStandardModel())
	})).Return(&llmv1.StructuredResponse{JsonResult: `["initial_query"]`}, nil).Once()

	// Mock FIRST evaluation: uncertain
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil && req.Model == llm.ResolveLightModel()
	})).Return(&llmv1.StructuredResponse{
		JsonResult: `{"score": 0.4, "verdict": "uncertain", "reasoning": "need more info", "suggestedQueries": ["refinement_query"]}`,
	}, nil).Once()

	// Mock SECOND evaluation: supported
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil && req.Model == llm.ResolveLightModel()
	})).Return(&llmv1.StructuredResponse{
		JsonResult: `{"score": 0.85, "verdict": "supported", "reasoning": "refinement confirmed"}`,
	}, nil).Once()

	brainCaps := NewBrainCapabilities(lc)
	evaluator := NewHypothesisEvaluator(brainCaps)
	explorer := NewHypothesisExplorer(reg, evaluator, brainCaps, 1)

	h := &Hypothesis{ID: "h1", Claim: "Test Hypothesis"}
	result := explorer.exploreHypothesis(context.Background(), h, search.SearchOpts{Limit: 1}, 1)

	assert.Equal(t, "supported", result.EvaluationResult.Verdict)
	assert.Equal(t, 0.85, result.Confidence)
	assert.Contains(t, result.Queries, "initial_query")
	// Verify that refinement queries were executed (new evidence should contain refinement query results)
	foundRefinement := false
	for _, p := range result.NewEvidence {
		if p.ID == "p-refinement_query" {
			foundRefinement = true
			break
		}
	}
	assert.True(t, foundRefinement)
}

func TestParseQueryArray(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{
			input:    `["q1", "q2", "q3"]`,
			expected: []string{"q1", "q2", "q3"},
		},
		{
			input:    "```json\n[\"query one\", \"query two\"]\n```",
			expected: nil,
		},
		{
			input:    `Not a valid json array`,
			expected: nil,
		},
		{
			input:    `["unclosed quote]`,
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parseQueryArray(tt.input)
			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestHypothesisExplorer_HeuristicGenerateQueries(t *testing.T) {
	explorer := NewHypothesisExplorer(nil, nil, nil, 1)

	h := &Hypothesis{
		Claim:                   "Low-carb diet improves focus",
		FalsifiabilityCondition: "RCT showing no difference in cognitive tests",
		Category:                "Nutrition",
	}

	queries := explorer.heuristicGenerateQueries(h, 5)

	assert.GreaterOrEqual(t, len(queries), 3)
	assert.Contains(t, queries, h.Claim)
	assert.Contains(t, queries, h.FalsifiabilityCondition)
	assert.Contains(t, queries, "Nutrition evidence")
}

func TestHypothesisExplorer_GenerateQueriesCooldownFallback(t *testing.T) {
	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	msc.On("StructuredOutput", mock.Anything, mock.Anything).
		Return(nil, errors.New("vertex structured output provider cooldown active; retry after 45s")).Once()

	explorer := NewHypothesisExplorer(nil, nil, NewBrainCapabilities(lc), 1)
	h := &Hypothesis{
		Claim:                   "RLHF improves instruction following",
		FalsifiabilityCondition: "ablation shows no instruction-following improvement",
		Category:                "alignment",
	}

	queries := explorer.generateHypothesisQueries(context.Background(), h, 3)

	assert.Equal(t, []string{
		"RLHF improves instruction following",
		"ablation shows no instruction-following improvement",
		"alignment evidence",
	}, queries)
	msc.AssertExpectations(t)
}

func TestDedupePapers(t *testing.T) {
	papers := []search.Paper{
		{ID: "1", Title: "Title 1"},
		{ID: "2", Title: "Title 2"},
		{ID: "1", Title: "Title 1 Duplicate"},
		{ID: "", DOI: "doi-1", Title: "Title 3"},
		{ID: "", DOI: "doi-1", Title: "Title 3 Duplicate"},
		{ID: "", Title: "Unique Title"},
	}

	deduped := dedupePapers(papers)
	assert.Len(t, deduped, 4)
}
