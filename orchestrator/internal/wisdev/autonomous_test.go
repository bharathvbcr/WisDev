package wisdev

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestAutonomousCooldownBackpressureControls(t *testing.T) {
	assert.False(t, shouldUseDynamicProviderSelectionForCooldown(string(WisDevModeYOLO), ResearchExecutionPlaneDeep, true, 30*time.Second))
	assert.True(t, shouldUseDynamicProviderSelectionForCooldown(string(WisDevModeYOLO), ResearchExecutionPlaneDeep, true, 0))
	assert.True(t, shouldUseDynamicProviderSelectionForCooldown("", ResearchExecutionPlaneDeep, true, 0))
	assert.False(t, shouldUseDynamicProviderSelectionForCooldown("", ResearchExecutionPlaneDeep, false, 0))

	assert.Equal(t, 1, resolveCooldownAwareParallelism(6, time.Second))
	assert.Equal(t, 6, resolveCooldownAwareParallelism(6, 0))
	assert.Equal(t, 1, resolveCooldownAwareParallelism(0, 0))

	assert.Equal(t, 2, maxBeliefRebuttalQueriesPerIteration(time.Second))
	assert.Equal(t, 8, maxBeliefRebuttalQueriesPerIteration(0))
}

func allowAutonomousHypothesisProposals(msc *mockLLMServiceClient, payload string) {
	if payload == "" {
		payload = `{"hypotheses":[{"claim":"Autonomous hypothesis A","falsifiabilityCondition":"Test A","confidenceThreshold":0.74},{"claim":"Autonomous hypothesis B","falsifiabilityCondition":"Test B","confidenceThreshold":0.68},{"claim":"Autonomous hypothesis C","falsifiabilityCondition":"Test C","confidenceThreshold":0.62}]}`
	}
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Propose 3-5 hypotheses for query")
	})).Return(&llmv1.StructuredResponse{JsonResult: payload}, nil).Maybe()
	allowAutonomousCommittee(msc)
}

func failAutonomousHypothesisProposals(msc *mockLLMServiceClient, err error) {
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Propose 3-5 hypotheses for query")
	})).Return(nil, err).Maybe()
	allowAutonomousCommittee(msc)
}

func allowAutonomousCommittee(msc *mockLLMServiceClient) {
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			(strings.Contains(req.Prompt, "Role: FactChecker") ||
				strings.Contains(req.Prompt, "Role: Synthesizer") ||
				strings.Contains(req.Prompt, "Role: ContradictionAnalyst") ||
				strings.Contains(req.Prompt, "Role: Supervisor")) &&
			req.GetThinkingBudget() == 1024 &&
			req.RequestClass == "standard" &&
			req.RetryProfile == "standard" &&
			req.ServiceTier == "standard" &&
			req.LatencyBudgetMs > 0
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"verdict":"approve","reason":"covered by test evidence"}`}, nil).Maybe()
}

func allowAutonomousHypothesisEvaluation(msc *mockLLMServiceClient, payload string) {
	if payload == "" {
		payload = `{"score":0.78,"verdict":"supported","reasoning":"Grounded evidence is consistent.","missingEvidence":[],"suggestedQueries":["replication study"]}`
	}
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			(strings.Contains(req.Prompt, "You are evaluating a research hypothesis") ||
				strings.Contains(req.Prompt, "Evaluate ALL of the following research hypotheses"))
	})).Return(&llmv1.StructuredResponse{JsonResult: payload}, nil).Maybe()
}

func allowAutonomousSufficiency(msc *mockLLMServiceClient, payload string) {
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			strings.Contains(req.Prompt, "Evaluate if the following papers") &&
			req.Model == llm.ResolveStandardModel() &&
			req.GetThinkingBudget() == 1024 &&
			req.RequestClass == "standard" &&
			req.RetryProfile == "standard" &&
			req.ServiceTier == "standard" &&
			req.LatencyBudgetMs > 0
	})).Return(&llmv1.StructuredResponse{JsonResult: payload}, nil).Maybe()
}

func failAutonomousSufficiency(msc *mockLLMServiceClient, err error) {
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			strings.Contains(req.Prompt, "Evaluate if the following papers") &&
			req.Model == llm.ResolveStandardModel() &&
			req.GetThinkingBudget() == 1024 &&
			req.RequestClass == "standard" &&
			req.RetryProfile == "standard" &&
			req.ServiceTier == "standard" &&
			req.LatencyBudgetMs > 0
	})).Return(nil, err).Maybe()
}

func allowAutonomousDossier(msc *mockLLMServiceClient, payload string) {
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			strings.Contains(req.Prompt, "Extract the top 2-3") &&
			req.Model == llm.ResolveStandardModel() &&
			req.GetThinkingBudget() == 1024 &&
			req.RequestClass == "standard" &&
			req.RetryProfile == "standard" &&
			req.ServiceTier == "standard" &&
			req.LatencyBudgetMs > 0
	})).Return(&llmv1.StructuredResponse{JsonResult: payload}, nil).Maybe()
}

func failAutonomousDossier(msc *mockLLMServiceClient, err error) {
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			strings.Contains(req.Prompt, "Extract the top 2-3") &&
			req.Model == llm.ResolveStandardModel() &&
			req.GetThinkingBudget() == 1024 &&
			req.RequestClass == "standard" &&
			req.RetryProfile == "standard" &&
			req.ServiceTier == "standard" &&
			req.LatencyBudgetMs > 0
	})).Return(nil, err).Maybe()
}

func allowAutonomousCritique(msc *mockLLMServiceClient, payload string) {
	if payload == "" {
		payload = `{"needsRevision":false,"reasoning":"Grounded coverage is sufficient.","confidence":0.86}`
	}
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			strings.Contains(req.Prompt, "Critique the following research draft") &&
			req.Model == llm.ResolveStandardModel() &&
			req.GetThinkingBudget() == 1024 &&
			req.RequestClass == "standard" &&
			req.RetryProfile == "standard" &&
			req.ServiceTier == "standard" &&
			req.LatencyBudgetMs > 0
	})).Return(&llmv1.StructuredResponse{JsonResult: payload}, nil).Maybe()

	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil && strings.Contains(req.Prompt, "Perform a harsh, critical analysis of the current evidence")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": false, "reasoning": "mock harsh critique", "nextQuery": ""}`}, nil).Maybe()
}

func failAutonomousCritique(msc *mockLLMServiceClient, err error) {
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			strings.Contains(req.Prompt, "Critique the following research draft") &&
			req.Model == llm.ResolveStandardModel() &&
			req.GetThinkingBudget() == 1024 &&
			req.RequestClass == "standard" &&
			req.RetryProfile == "standard" &&
			req.ServiceTier == "standard" &&
			req.LatencyBudgetMs > 0
	})).Return(nil, err).Maybe()
}

func TestAutonomousLoop_Run(t *testing.T) {
	// Mock Search
	reg := search.NewProviderRegistry()
	mockP := &mockSearchProvider{
		name: "mock",
		papers: []search.Paper{
			{ID: "p1", Title: "Paper 1", Abstract: "Abstract 1"},
		},
	}
	reg.Register(mockP)

	// Mock LLM
	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	allowAutonomousHypothesisProposals(msc, "")
	allowAutonomousHypothesisEvaluation(msc, "")
	allowAutonomousCritique(msc, "")

	l := NewAutonomousLoop(reg, lc)
	ctx := context.Background()

	t.Run("Success Converged", func(t *testing.T) {
		// 1. evaluateSufficiency (Balanced)
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Evaluate if the following papers")
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": true, "reasoning": "done", "nextQuery": ""}`}, nil).Once()

		// 2. assembleDossier (Balanced)
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Extract the top 2-3")
		})).Return(&llmv1.StructuredResponse{JsonResult: `[{"claim": "c1", "snippet": "s1", "confidence": 0.9}]`}, nil).Once()

		// 3. synthesizeWithEvidence (Heavy)
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
		})).Return(&llmv1.GenerateResponse{Text: "Final Synthesis"}, nil).Once()

		res, err := l.Run(ctx, LoopRequest{Query: "test", MaxIterations: 2})
		assert.NoError(t, err)
		assert.True(t, res.Converged)
		assert.Equal(t, 1, res.Iterations)
		assert.Equal(t, "Final Synthesis", res.FinalAnswer)
		assert.Len(t, res.Evidence, 1)
	})

	t.Run("Multi-iteration Success", func(t *testing.T) {
		// Iteration 1: not sufficient
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Evaluate if the following papers")
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": false, "reasoning": "need more evidence", "nextQuery": "refined"}`}, nil).Once()

		// Iteration 2: sufficient
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Evaluate if the following papers")
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": true, "reasoning": "enough", "nextQuery": ""}`}, nil).Once()

		// assembleDossier
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Extract the top 2-3")
		})).Return(&llmv1.StructuredResponse{JsonResult: `[]`}, nil).Once()

		// Final
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
		})).Return(&llmv1.GenerateResponse{Text: "Final"}, nil).Once()

		res, err := l.Run(ctx, LoopRequest{Query: "test", MaxIterations: 5})
		assert.NoError(t, err)
		assert.Equal(t, 2, res.Iterations)
	})

	t.Run("synthesis provider failure uses heuristic fallback", func(t *testing.T) {
		// evaluateSufficiency
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Evaluate if the following papers")
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": true, "reasoning": "enough", "nextQuery": ""}`}, nil).Once()

		// assembleDossier
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Extract the top 2-3")
		})).Return(&llmv1.StructuredResponse{JsonResult: `[]`}, nil).Once()

		// synthesize fail
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
		})).Return(nil, errors.New("fail")).Once()

		res, err := l.Run(ctx, LoopRequest{Query: "test", MaxIterations: 1})
		assert.NoError(t, err)
		if assert.NotNil(t, res) {
			assert.Contains(t, res.FinalAnswer, "Provisional research synthesis")
		}
	})
}

func TestAutonomousLoop_ParsesFencedJSONAndDegradesQuotaErrors(t *testing.T) {
	reg := search.NewProviderRegistry()
	reg.Register(&mockSearchProvider{
		name: "mock",
		papers: []search.Paper{
			{ID: "p1", Title: "Paper 1", Abstract: "Abstract 1"},
		},
	})

	t.Run("evaluateSufficiency parses native structured JSON", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		allowAutonomousHypothesisProposals(msc, "")
		allowAutonomousHypothesisEvaluation(msc, "")
		loop := NewAutonomousLoop(reg, lc)

		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Evaluate if the following papers")
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": true, "reasoning": "enough", "nextQuery": ""}`}, nil).Once()

		analysis, err := loop.evaluateSufficiency(context.Background(), "test query", []search.Paper{
			{ID: "p1", Title: "Paper 1"},
		})
		assert.NoError(t, err)
		assert.True(t, analysis.Sufficient)
		assert.Equal(t, "enough", analysis.Reasoning)
	})

	t.Run("run uses heuristic sufficiency fallback on provider quota failure", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		allowAutonomousHypothesisProposals(msc, "")
		allowAutonomousHypothesisEvaluation(msc, "")
		allowAutonomousCritique(msc, "")
		loop := NewAutonomousLoop(reg, lc)

		failAutonomousSufficiency(msc, errors.New("429 RESOURCE_EXHAUSTED"))
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
		})).Return(&llmv1.GenerateResponse{Text: "Final Synthesis"}, nil).Maybe()

		res, err := loop.Run(context.Background(), LoopRequest{Query: "test", MaxIterations: 1, MaxSearchTerms: 1, HitsPerSearch: 1, MaxUniquePapers: 1})
		assert.NoError(t, err)
		if assert.NotNil(t, res) && assert.NotNil(t, res.GapAnalysis) {
			assert.Contains(t, res.GapAnalysis.Reasoning, "heuristic evidence coverage")
		}
	})

	t.Run("run uses heuristic sufficiency fallback on recoverable checkpoint deadline", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		allowAutonomousHypothesisProposals(msc, "")
		allowAutonomousHypothesisEvaluation(msc, "")
		allowAutonomousCritique(msc, "")
		loop := NewAutonomousLoop(reg, lc)

		failAutonomousSufficiency(msc, context.DeadlineExceeded)
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
		})).Return(&llmv1.GenerateResponse{Text: "Final Synthesis"}, nil).Maybe()

		res, err := loop.Run(context.Background(), LoopRequest{Query: "test", MaxIterations: 1, MaxSearchTerms: 1, HitsPerSearch: 1, MaxUniquePapers: 1})
		assert.NoError(t, err)
		if assert.NotNil(t, res) && assert.NotNil(t, res.GapAnalysis) {
			assert.Contains(t, res.GapAnalysis.Reasoning, "heuristic evidence coverage")
		}
	})

	t.Run("run degrades to heuristic sufficiency when llm client is absent", func(t *testing.T) {
		reg := search.NewProviderRegistry()
		reg.Register(&mockSearchProvider{
			name: "heuristic_sufficiency",
			papers: []search.Paper{{
				ID:       "p1",
				Title:    "Heuristic Coverage",
				Abstract: "Grounded evidence describes test query replication evidence.",
				Source:   "openalex",
			}},
		})
		reg.SetDefaultOrder([]string{"heuristic_sufficiency"})
		loop := NewAutonomousLoop(reg, nil)

		result, err := loop.Run(context.Background(), LoopRequest{Query: "test query", MaxIterations: 1, MaxSearchTerms: 1, HitsPerSearch: 1, MaxUniquePapers: 1})

		assert.NoError(t, err)
		if assert.NotNil(t, result) && assert.NotNil(t, result.GapAnalysis) {
			assert.Contains(t, result.GapAnalysis.Reasoning, "heuristic evidence coverage")
		}
	})

	t.Run("P5_1_regression_Run_rejects_empty_query", func(t *testing.T) {
		// Regression for P5-1: Run previously assigned currentQuery = req.Query
		// without any guard, proceeding with an empty-string query that
		// produced a hallucinated synthesis. After the fix, an empty query
		// must return an error immediately without starting the loop.
		loop := NewAutonomousLoop(reg, nil)
		result, err := loop.Run(context.Background(), LoopRequest{Query: "", MaxIterations: 3})
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "query is required")
	})

	t.Run("P5_1_regression_Run_rejects_whitespace_only_query", func(t *testing.T) {
		loop := NewAutonomousLoop(reg, nil)
		result, err := loop.Run(context.Background(), LoopRequest{Query: "   ", MaxIterations: 3})
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "query is required")
	})

	t.Run("run uses dynamic hypothesis proposals in reasoning graph", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		allowAutonomousHypothesisProposals(msc, `{"hypotheses":[{"claim":"Hypothesis alpha","falsifiabilityCondition":"alpha falsifier","confidenceThreshold":0.81},{"claim":"Hypothesis beta","falsifiabilityCondition":"beta falsifier","confidenceThreshold":0.73}]}`)
		allowAutonomousHypothesisEvaluation(msc, "")
		allowAutonomousCritique(msc, "")
		loop := NewAutonomousLoop(reg, lc)

		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Evaluate if the following papers")
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": true, "reasoning": "enough", "nextQuery": ""}`}, nil).Once()
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Extract the top 2-3")
		})).Return(&llmv1.StructuredResponse{JsonResult: `[]`}, nil).Once()
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
		})).Return(&llmv1.GenerateResponse{Text: "Final"}, nil).Once()

		res, err := loop.Run(context.Background(), LoopRequest{Query: "sleep and memory", MaxIterations: 1})
		assert.NoError(t, err)
		assert.NotNil(t, res.ReasoningGraph)

		hypothesisLabels := make([]string, 0)
		for _, node := range res.ReasoningGraph.Nodes {
			if node.Type == ReasoningNodeHypothesis {
				hypothesisLabels = append(hypothesisLabels, node.Label)
			}
		}
		assert.ElementsMatch(t, []string{"Hypothesis alpha", "Hypothesis beta"}, hypothesisLabels)
		assert.NotContains(t, hypothesisLabels, "sleep and memory")
	})

	t.Run("run suppresses hypothesis generation when disabled", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		allowAutonomousHypothesisProposals(msc, `{"hypotheses":[{"claim":"Blocked hypothesis","falsifiabilityCondition":"blocked falsifier","confidenceThreshold":0.81}]}`)
		allowAutonomousHypothesisEvaluation(msc, "")
		allowAutonomousCritique(msc, "")
		loop := NewAutonomousLoop(reg, lc)

		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Evaluate if the following papers")
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": true, "reasoning": "enough", "nextQuery": ""}`}, nil).Once()
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Extract the top 2-3")
		})).Return(&llmv1.StructuredResponse{JsonResult: `[]`}, nil).Once()
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
		})).Return(&llmv1.GenerateResponse{Text: "Final"}, nil).Once()

		res, err := loop.Run(context.Background(), LoopRequest{
			Query:                       "sleep and memory",
			MaxIterations:               1,
			DisableHypothesisGeneration: true,
		})
		assert.NoError(t, err)
		assert.NotNil(t, res.ReasoningGraph)

		for _, node := range res.ReasoningGraph.Nodes {
			if node.Type == ReasoningNodeHypothesis {
				t.Fatalf("expected no hypothesis nodes when disabled, got %#v", node)
			}
		}
		msc.AssertNotCalled(t, "StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return strings.Contains(req.Prompt, "Propose 3-5 hypotheses for query")
		}))
	})

	t.Run("run records a durable coverage ledger when gaps remain", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		allowAutonomousHypothesisProposals(msc, "")
		allowAutonomousHypothesisEvaluation(msc, "")
		allowAutonomousCritique(msc, "")
		loop := NewAutonomousLoop(reg, lc)

		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Evaluate if the following papers")
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": false, "reasoning": "still missing independent evidence", "nextQueries":["sleep memory systematic review"], "missingAspects":["independent replication evidence"], "missingSourceTypes":["systematic review"], "contradictions":["acute sleep loss effect sizes disagree"], "confidence":0.61}`}, nil).Once()
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return strings.Contains(req.Prompt, "Extract the top 2-3")
		})).Return(&llmv1.StructuredResponse{JsonResult: `[]`}, nil).Once()
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
		})).Return(&llmv1.GenerateResponse{Text: "Final"}, nil).Once()

		res, err := loop.Run(context.Background(), LoopRequest{Query: "sleep and memory", MaxIterations: 1})
		assert.NoError(t, err)
		if assert.NotNil(t, res.GapAnalysis) {
			assert.False(t, res.GapAnalysis.Sufficient)
			assert.NotEmpty(t, res.GapAnalysis.Ledger)
			assert.Greater(t, res.GapAnalysis.ObservedEvidenceCount, 0)
		}
	})
}

func TestReWeightEvidenceConfidence(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		result := ReWeightEvidenceConfidence(nil)
		assert.Nil(t, result)
	})

	t.Run("specificity boost for detailed snippets", func(t *testing.T) {
		short := EvidenceFinding{ID: "e1", Confidence: 0.5, Snippet: "Short", SourceID: "s1"}
		long := EvidenceFinding{ID: "e2", Confidence: 0.5, Snippet: strings.Repeat("word ", 100), SourceID: "s2"}
		findings := ReWeightEvidenceConfidence([]EvidenceFinding{short, long})
		assert.Greater(t, findings[1].Confidence, findings[0].Confidence,
			"Longer snippet should get higher specificity boost")
	})

	t.Run("recency boost", func(t *testing.T) {
		old := EvidenceFinding{ID: "e1", Confidence: 0.5, Year: 2010, SourceID: "s1"}
		recent := EvidenceFinding{ID: "e2", Confidence: 0.5, Year: 2024, SourceID: "s2"}
		findings := ReWeightEvidenceConfidence([]EvidenceFinding{old, recent})
		assert.Greater(t, findings[1].Confidence, findings[0].Confidence,
			"Recent paper should get higher confidence")
	})

	t.Run("source diversity penalty", func(t *testing.T) {
		findings := make([]EvidenceFinding, 5)
		for i := range findings {
			findings[i] = EvidenceFinding{
				ID: fmt.Sprintf("e%d", i), Confidence: 0.5, SourceID: "same_source",
			}
		}
		result := ReWeightEvidenceConfidence(findings)
		assert.Greater(t, result[0].Confidence, result[4].Confidence,
			"Later findings from same source should be penalized")
	})

	t.Run("original confidence dominates", func(t *testing.T) {
		high := EvidenceFinding{ID: "e1", Confidence: 0.9, SourceID: "s1"}
		low := EvidenceFinding{ID: "e2", Confidence: 0.2, SourceID: "s2"}
		findings := ReWeightEvidenceConfidence([]EvidenceFinding{high, low})
		assert.Greater(t, findings[0].Confidence, findings[1].Confidence,
			"Original confidence should remain the dominant factor")
	})

	t.Run("scores stay in 0-1 range", func(t *testing.T) {
		findings := ReWeightEvidenceConfidence([]EvidenceFinding{
			{ID: "e1", Confidence: 1.0, Year: 2025, Snippet: strings.Repeat("word ", 200), SourceID: "s1"},
			{ID: "e2", Confidence: 0.0, SourceID: "s2"},
		})
		for _, f := range findings {
			assert.GreaterOrEqual(t, f.Confidence, 0.0)
			assert.LessOrEqual(t, f.Confidence, 1.0)
		}
	})
}

func TestShouldAbortAutonomousLoopTreatsSubcallDeadlineAsRecoverable(t *testing.T) {
	assert.False(t, shouldAbortAutonomousLoop(context.DeadlineExceeded))
	assert.True(t, shouldAbortAutonomousLoop(context.Canceled))
	assert.True(t, shouldAbortAutonomousLoop(errors.New("context canceled")))
}
