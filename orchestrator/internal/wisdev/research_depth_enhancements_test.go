package wisdev

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestExtractEvidenceSpans(t *testing.T) {
	finding := &EvidenceFinding{
		SourceID:   "p1",
		PaperTitle: "Sleep and Memory",
		Snippet: "Sleep deprivation impairs memory consolidation in adults. " +
			"The weather was sunny throughout the study period overall. " +
			"Participants with restricted sleep showed reduced memory recall and consolidation deficits.",
	}

	spans := extractEvidenceSpans("sleep deprivation impairs memory consolidation", finding)
	require.NotEmpty(t, spans)
	assert.LessOrEqual(t, len(spans), maxEvidenceSpansPerFinding)
	// Best-matching sentence first, anchored to the source paper.
	assert.Contains(t, strings.ToLower(spans[0].Quote), "memory consolidation")
	assert.Equal(t, "p1", spans[0].PaperID)
	assert.Greater(t, spans[0].MatchScore, 0.5)
	// The irrelevant weather sentence must not rank above topical ones.
	for _, span := range spans {
		assert.NotContains(t, strings.ToLower(span.Quote), "weather")
	}

	assert.Empty(t, extractEvidenceSpans("", finding))
	assert.Empty(t, extractEvidenceSpans("claim", &EvidenceFinding{}))
	assert.Empty(t, extractEvidenceSpans("claim", nil))
}

func TestMergeSimilarHypotheses(t *testing.T) {
	hyps := []Hypothesis{
		{
			ID: "h1", Claim: "Sleep deprivation impairs memory consolidation in healthy adults",
			ConfidenceScore: 0.7,
			Evidence:        []*EvidenceFinding{{ID: "ev1"}},
		},
		{
			ID: "h2", Claim: "Sleep deprivation impairs memory consolidation in healthy adults.",
			ConfidenceScore: 0.4,
			Evidence:        []*EvidenceFinding{{ID: "ev1"}, {ID: "ev2"}},
		},
		{
			ID: "h3", Claim: "Caffeine intake modulates attention networks independently of sleep",
			ConfidenceScore: 0.6,
		},
	}

	merged := MergeSimilarHypotheses(hyps)
	require.Len(t, merged, 2)

	var survivor *Hypothesis
	for i := range merged {
		if merged[i].ID == "h1" {
			survivor = &merged[i]
		}
		assert.NotEqual(t, "h2", merged[i].ID, "lower-confidence duplicate must be merged away")
	}
	require.NotNil(t, survivor, "higher-confidence duplicate must survive")
	// Evidence folded in and deduplicated by finding ID.
	assert.Equal(t, 2, survivor.EvidenceCount)

	// Distinct and short inputs pass through untouched.
	assert.Len(t, MergeSimilarHypotheses(hyps[2:]), 1)
	assert.Empty(t, MergeSimilarHypotheses(nil))
}

func TestConfirmBorderlineSufficiency(t *testing.T) {
	papers := []search.Paper{{ID: "p1", Title: "Paper 1", Abstract: "Relevant abstract content for the query."}}

	t.Run("high confidence passes without a second evaluation", func(t *testing.T) {
		l := &AutonomousLoop{}
		analysis := &sufficiencyAnalysis{Sufficient: true, Confidence: 0.9}
		assert.True(t, l.confirmBorderlineSufficiency(context.Background(), LoopRequest{Query: "q"}, analysis, papers))
	})

	t.Run("no LLM available accepts the verdict", func(t *testing.T) {
		l := &AutonomousLoop{}
		analysis := &sufficiencyAnalysis{Sufficient: true, Confidence: 0.4}
		assert.True(t, l.confirmBorderlineSufficiency(context.Background(), LoopRequest{Query: "q"}, analysis, papers))
	})

	t.Run("disagreeing second evaluation rejects convergence and carries gaps", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		l := NewAutonomousLoop(search.NewProviderRegistry(), lc)

		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return req != nil && strings.Contains(req.Prompt, "enough evidence")
		})).Return(&llmv1.StructuredResponse{
			JsonResult: `{"sufficient": false, "reasoning": "missing mechanism studies", "nextQuery": "mechanism", "missingAspects": ["causal mechanism"]}`,
		}, nil).Once()

		analysis := &sufficiencyAnalysis{Sufficient: true, Confidence: 0.5}
		confirmed := l.confirmBorderlineSufficiency(context.Background(), LoopRequest{Query: "q"}, analysis, papers)
		assert.False(t, confirmed)
		assert.Contains(t, analysis.MissingAspects, "causal mechanism")
		msc.AssertExpectations(t)
	})

	t.Run("agreeing second evaluation confirms convergence", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		l := NewAutonomousLoop(search.NewProviderRegistry(), lc)

		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return req != nil && strings.Contains(req.Prompt, "enough evidence")
		})).Return(&llmv1.StructuredResponse{
			JsonResult: `{"sufficient": true, "reasoning": "coverage is adequate", "nextQuery": "", "confidence": 0.8}`,
		}, nil).Once()

		analysis := &sufficiencyAnalysis{Sufficient: true, Confidence: 0.5}
		confirmed := l.confirmBorderlineSufficiency(context.Background(), LoopRequest{Query: "q"}, analysis, papers)
		assert.True(t, confirmed)
		assert.Equal(t, 0.8, analysis.Confidence)
		msc.AssertExpectations(t)
	})
}

func TestCallPRMJudgeAndFallback(t *testing.T) {
	t.Run("aggregate-only signals use the heuristic formula", func(t *testing.T) {
		reward, err := callPRM(context.Background(), "s", map[string]any{
			"paperCount":            4,
			"searchSuccess":         1.0,
			"citationVerifiedRatio": 1.0,
			"coverageScore":         1.0,
			"success":               true,
		})
		require.NoError(t, err)
		assert.Equal(t, 1.0, reward)

		zero, err := callPRM(context.Background(), "s", map[string]any{"paperCount": 0})
		require.NoError(t, err)
		assert.Equal(t, 0.0, zero)
	})

	t.Run("LLM judge scores steps with query and paper context", func(t *testing.T) {
		prev := GlobalLLMClient
		defer func() { GlobalLLMClient = prev }()

		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		GlobalLLMClient = lc

		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return req != nil && strings.Contains(req.Prompt, "process reward model")
		})).Return(&llmv1.StructuredResponse{
			JsonResult: `{"reward": 0.42, "relevance": 0.5, "coverage": 0.3, "grounding": 0.6, "reasoning": "papers cluster on one subtopic"}`,
		}, nil).Once()

		reward, err := callPRM(context.Background(), "s", map[string]any{
			"paperCount": 1,
			"query":      "sleep and memory",
			"queries":    []string{"sleep and memory"},
			"papers":     []Source{{Title: "P1", Summary: "abstract text"}},
			"iteration":  1,
		})
		require.NoError(t, err)
		assert.Equal(t, 0.42, reward)
		msc.AssertExpectations(t)
	})

	t.Run("LLM judge failure falls back to the heuristic", func(t *testing.T) {
		prev := GlobalLLMClient
		defer func() { GlobalLLMClient = prev }()

		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		GlobalLLMClient = lc

		msc.On("StructuredOutput", mock.Anything, mock.Anything).
			Return((*llmv1.StructuredResponse)(nil), errors.New("judge unavailable")).Once()

		reward, err := callPRM(context.Background(), "s", map[string]any{
			"paperCount":            2,
			"searchSuccess":         0.5,
			"citationVerifiedRatio": 0.5,
			"coverageScore":         0.5,
			"query":                 "sleep and memory",
			"queries":               []string{"sleep and memory"},
			"papers":                []Source{{Title: "P1"}},
			"iteration":             1,
		})
		require.NoError(t, err)
		// 0.15 + 0.35*0.5 + 0.35*0.5 + 0.15*0.5
		assert.InDelta(t, 0.575, reward, 1e-9)
	})
}

func TestEvidenceTextFromPaper(t *testing.T) {
	assert.Equal(t, "abstract", evidenceTextFromPaper(search.Paper{Abstract: "abstract"}))
	assert.Equal(t, "full body text", evidenceTextFromPaper(search.Paper{Abstract: "abstract", FullText: "full body text"}))
	long := strings.Repeat("x", maxEvidenceFullTextChars+100)
	assert.Len(t, evidenceTextFromPaper(search.Paper{FullText: long}), maxEvidenceFullTextChars)
}

func TestRegenerateLoopAgenda(t *testing.T) {
	papers := []search.Paper{{ID: "p1", Title: "Paper 1"}}
	gap := &LoopGapState{Ledger: []CoverageLedgerEntry{
		{Category: "aspect", Status: "open", Title: "Mechanistic evidence missing"},
		{Category: "aspect", Status: "closed", Title: "Already covered"},
	}}

	t.Run("nil llm or sufficient analysis returns nothing", func(t *testing.T) {
		l := &AutonomousLoop{}
		assert.Nil(t, l.regenerateLoopAgenda(context.Background(), LoopRequest{Query: "q"}, &sufficiencyAnalysis{Sufficient: false}, gap, papers, nil))
		lcLoop := NewAutonomousLoop(search.NewProviderRegistry(), llm.NewClient())
		assert.Nil(t, lcLoop.regenerateLoopAgenda(context.Background(), LoopRequest{Query: "q"}, &sufficiencyAnalysis{Sufficient: true}, gap, papers, nil))
	})

	t.Run("regenerates gap-targeted queries via LLM", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		l := NewAutonomousLoop(search.NewProviderRegistry(), lc)

		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return req != nil &&
				strings.Contains(req.Prompt, "replanning an in-progress literature research loop") &&
				strings.Contains(req.Prompt, "Mechanistic evidence missing") &&
				!strings.Contains(req.Prompt, "Already covered")
		})).Return(&llmv1.StructuredResponse{
			JsonResult: `{"queries": ["sleep deprivation hippocampal mechanism", "longitudinal sleep memory cohort"], "reasoning": "target open gaps"}`,
		}, nil).Once()

		analysis := &sufficiencyAnalysis{
			Sufficient:     false,
			MissingAspects: []string{"causal mechanism"},
		}
		queries := l.regenerateLoopAgenda(context.Background(), LoopRequest{Query: "sleep and memory"}, analysis, gap, papers, []string{"sleep and memory"})
		require.Len(t, queries, 2)
		assert.Contains(t, queries, "sleep deprivation hippocampal mechanism")
		msc.AssertExpectations(t)
	})
}
