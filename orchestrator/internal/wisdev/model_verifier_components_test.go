package wisdev

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestRerankPlanCandidatesWithVerifier(t *testing.T) {
	reordered := RerankPlanCandidatesWithVerifier(context.Background(), nil, nil, "", []PlanCandidate{
		{Hypothesis: "b", Score: 0.2},
		{Hypothesis: "a", Score: 0.9},
		{Hypothesis: "c", Score: 0.9},
		{Hypothesis: "d", Score: 0.5},
	})

	require.Len(t, reordered, 4)
	assert.Equal(t, "a", reordered[0].Hypothesis)
	assert.Equal(t, "c", reordered[1].Hypothesis)
	assert.Equal(t, "d", reordered[2].Hypothesis)
	assert.Equal(t, "b", reordered[3].Hypothesis)

	assert.Nil(t, RerankPlanCandidatesWithVerifier(context.Background(), nil, nil, "", nil))
}

func TestRerankPlanCandidatesWithVerifier_BlendsModelScores(t *testing.T) {
	msc := &mockLLMServiceClient{}
	client := llm.NewClient()
	client.SetClient(msc)

	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			strings.Contains(req.Prompt, "Research goal: improve protein folding accuracy") &&
			strings.Contains(req.Prompt, "[0] hypothesis: alpha") &&
			strings.Contains(req.Prompt, "[2] hypothesis: gamma") &&
			strings.Contains(req.JsonSchema, `"scores"`) &&
			strings.Contains(req.Prompt, wisdevStructuredOutputSchemaInstruction)
	})).Return(&llmv1.StructuredResponse{
		JsonResult: `{"scores":[{"index":0,"score":0.1,"reason":"weak coverage"},{"index":1,"score":1.0,"reason":"strong coverage"},{"index":9,"score":0.9,"reason":"out of range"}]}`,
	}, nil).Once()

	input := []PlanCandidate{
		{Hypothesis: "alpha", Score: 0.9},
		{Hypothesis: "beta", Score: 0.2, Rationale: "prior rationale"},
		{Hypothesis: "gamma", Score: 0.55},
	}
	reranked := RerankPlanCandidatesWithVerifier(context.Background(), client, nil, "improve protein folding accuracy", input)

	require.Len(t, reranked, 3)
	// beta: 0.5*0.2 + 0.5*1.0 = 0.6; gamma: untouched 0.55; alpha: 0.5*0.9 + 0.5*0.1 = 0.5
	assert.Equal(t, "beta", reranked[0].Hypothesis)
	assert.InDelta(t, 0.6, reranked[0].Score, 1e-9)
	assert.Equal(t, "gamma", reranked[1].Hypothesis)
	assert.InDelta(t, 0.55, reranked[1].Score, 1e-9)
	assert.Equal(t, "alpha", reranked[2].Hypothesis)
	assert.InDelta(t, 0.5, reranked[2].Score, 1e-9)

	// Verifier reasons are attached to Rationale without clobbering priors.
	assert.Equal(t, "prior rationale | Verifier: strong coverage", reranked[0].Rationale)
	assert.Equal(t, "Verifier: weak coverage", reranked[2].Rationale)

	// The input slice is never mutated.
	assert.Equal(t, 0.9, input[0].Score)
	assert.Equal(t, 0.2, input[1].Score)
	assert.Equal(t, 0.55, input[2].Score)
	assert.Equal(t, "prior rationale", input[1].Rationale)

	msc.AssertExpectations(t)
}

func TestRerankPlanCandidatesWithVerifier_DegradedFallbacks(t *testing.T) {
	input := []PlanCandidate{
		{Hypothesis: "low", Score: 0.2},
		{Hypothesis: "high", Score: 0.9},
		{Hypothesis: "mid", Score: 0.5},
	}

	t.Run("llm error keeps deterministic prior order", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		client := llm.NewClient()
		client.SetClient(msc)
		msc.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, errors.New("provider unavailable")).Once()

		reranked := RerankPlanCandidatesWithVerifier(context.Background(), client, nil, "goal", input)
		require.Len(t, reranked, 3)
		assert.Equal(t, "high", reranked[0].Hypothesis)
		assert.Equal(t, "mid", reranked[1].Hypothesis)
		assert.Equal(t, "low", reranked[2].Hypothesis)
	})

	t.Run("unparseable structured output keeps deterministic prior order", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		client := llm.NewClient()
		client.SetClient(msc)
		msc.On("StructuredOutput", mock.Anything, mock.Anything).Return(&llmv1.StructuredResponse{JsonResult: "not json"}, nil).Once()

		reranked := RerankPlanCandidatesWithVerifier(context.Background(), client, nil, "goal", input)
		require.Len(t, reranked, 3)
		assert.Equal(t, "high", reranked[0].Hypothesis)
		assert.Equal(t, "mid", reranked[1].Hypothesis)
		assert.Equal(t, "low", reranked[2].Hypothesis)
	})

	t.Run("single candidate skips the model entirely", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		client := llm.NewClient()
		client.SetClient(msc)

		reranked := RerankPlanCandidatesWithVerifier(context.Background(), client, nil, "goal", []PlanCandidate{{Hypothesis: "solo", Score: 0.4}})
		require.Len(t, reranked, 1)
		assert.Equal(t, "solo", reranked[0].Hypothesis)
		msc.AssertNotCalled(t, "StructuredOutput", mock.Anything, mock.Anything)
	})
}

func TestVerifyClaimLexicalHeuristicFallback(t *testing.T) {
	model := NewGoogleGenAIModel(nil, "heuristic-model", ModelTierStandard)

	t.Run("high overlap is supported with bounded confidence", func(t *testing.T) {
		supported, confidence, err := model.VerifyClaim(context.Background(),
			"Transformers improve translation quality",
			"Recent studies confirm transformers improve translation quality across benchmarks")
		require.NoError(t, err)
		assert.True(t, supported)
		assert.InDelta(t, 0.80, confidence, 1e-9)
		assert.GreaterOrEqual(t, confidence, 0.25)
		assert.LessOrEqual(t, confidence, 0.80)
	})

	t.Run("zero overlap is unsupported", func(t *testing.T) {
		supported, confidence, err := model.VerifyClaim(context.Background(),
			"quantum entanglement decay",
			"banana bread recipe instructions")
		require.NoError(t, err)
		assert.False(t, supported)
		assert.InDelta(t, 0.30, confidence, 1e-9)
	})

	t.Run("llm error path uses heuristic", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		client := llm.NewClient()
		client.SetClient(msc)
		mocked := NewGoogleGenAIModel(client, "heuristic-model", ModelTierStandard)
		msc.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, errors.New("model down")).Once()

		supported, confidence, err := mocked.VerifyClaim(context.Background(),
			"graphene conducts electricity",
			"experiments show graphene conducts electricity efficiently")
		require.NoError(t, err)
		assert.True(t, supported)
		assert.InDelta(t, 0.80, confidence, 1e-9)
	})
}

func TestGoogleGenAIModelDefaultsAndActions(t *testing.T) {
	model := NewGoogleGenAIModel(nil, "", "")

	trimmed, err := model.Generate(context.Background(), "  hello model  ")
	require.NoError(t, err)
	assert.Equal(t, "hello model", trimmed)

	assert.NotEmpty(t, model.Name())
	assert.Equal(t, ModelTierStandard, model.Tier())
	hypotheses, err := model.GenerateHypotheses(context.Background(), "research topic")
	require.NoError(t, err)
	assert.Equal(t, 3, len(hypotheses))

	claims, err := model.ExtractClaims(context.Background(), "  key claim text ")
	require.NoError(t, err)
	assert.Equal(t, []string{"key claim text"}, claims)

	// Without a client the model degrades to the lexical overlap heuristic:
	// "claim" and "evidence" share no tokens, so the claim is unsupported.
	pass, confidence, err := model.VerifyClaim(context.Background(), "claim", "evidence")
	require.NoError(t, err)
	assert.False(t, pass)
	assert.InDelta(t, 0.30, confidence, 1e-9)

	synthesis, err := model.SynthesizeFindings(context.Background(), []string{"h1", "h2"}, map[string]interface{}{"query": "x"})
	require.NoError(t, err)
	assert.Equal(t, "Synthesis: h1", synthesis)

	critique, err := model.CritiqueFindings(context.Background(), []string{"f"})
	require.NoError(t, err)
	assert.Equal(t, "Critique: evidence should be strengthened.", critique)
}

func TestGoogleGenAIModelEdgePaths(t *testing.T) {
	model := NewGoogleGenAIModel(nil, "explicit-name", ModelTierHeavy)
	assert.Equal(t, "explicit-name", model.Name())
	assert.Equal(t, ModelTierHeavy, model.Tier())

	emptyHypotheses, err := model.GenerateHypotheses(context.Background(), "")
	require.NoError(t, err)
	assert.Empty(t, emptyHypotheses)
	emptyClaims, err := model.ExtractClaims(context.Background(), "   ")
	require.NoError(t, err)
	assert.Empty(t, emptyClaims)

	pass, confidence, err := model.VerifyClaim(context.Background(), "", "")
	require.NoError(t, err)
	assert.False(t, pass)
	assert.Equal(t, 0.25, confidence)

	synthesis, err := model.SynthesizeFindings(context.Background(), nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "No hypotheses available.", synthesis)

	critique, err := model.CritiqueFindings(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "No findings available for critique.", critique)
}

func TestGoogleGenAIModel_UsesLLMClientWhenAvailable(t *testing.T) {
	msc := &mockLLMServiceClient{}
	client := llm.NewClient()
	client.SetClient(msc)
	model := NewGoogleGenAIModel(client, "google-genai-model", ModelTierStandard)

	t.Run("generate", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return req != nil &&
				req.Model == "google-genai-model" &&
				req.RequestClass == "standard" &&
				req.ServiceTier == "standard" &&
				req.RetryProfile == "standard" &&
				req.GetThinkingBudget() == 1024
		})).Return(&llmv1.GenerateResponse{Text: " refined "}, nil).Once()

		text, err := model.Generate(context.Background(), "prompt")
		require.NoError(t, err)
		assert.Equal(t, "refined", text)
	})

	t.Run("generate hypotheses", func(t *testing.T) {
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return req != nil &&
				req.Model == "google-genai-model" &&
				req.RequestClass == "structured_high_value" &&
				req.ServiceTier == "priority" &&
				req.RetryProfile == "standard" &&
				req.GetThinkingBudget() == -1 &&
				strings.Contains(req.Prompt, wisdevStructuredOutputSchemaInstruction)
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"hypotheses":["h1"," h2 ","h1"]}`}, nil).Once()

		hypotheses, err := model.GenerateHypotheses(context.Background(), "query")
		require.NoError(t, err)
		assert.Equal(t, []string{"h1", "h2"}, hypotheses)
	})

	t.Run("extract claims", func(t *testing.T) {
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return req != nil && strings.Contains(req.Prompt, "Extract the core scientific claims")
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"claims":["claim a"," claim b "]}`}, nil).Once()

		claims, err := model.ExtractClaims(context.Background(), "text")
		require.NoError(t, err)
		assert.Equal(t, []string{"claim a", "claim b"}, claims)
	})

	t.Run("verify claim", func(t *testing.T) {
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return req != nil && strings.Contains(req.Prompt, "Assess whether the evidence supports the claim")
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"supported":true,"confidence":1.4}`}, nil).Once()

		supported, confidence, err := model.VerifyClaim(context.Background(), "claim", "evidence")
		require.NoError(t, err)
		assert.True(t, supported)
		assert.Equal(t, 1.0, confidence)
	})

	t.Run("synthesize findings", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return req != nil && strings.Contains(req.Prompt, "Synthesize the following research findings")
		})).Return(&llmv1.GenerateResponse{Text: " summary "}, nil).Once()

		summary, err := model.SynthesizeFindings(context.Background(), []string{"h1"}, map[string]interface{}{"k": "v"})
		require.NoError(t, err)
		assert.Equal(t, "summary", summary)
	})

	t.Run("critique findings", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return req != nil && strings.Contains(req.Prompt, "Critique the following research findings")
		})).Return(&llmv1.GenerateResponse{Text: " critique "}, nil).Once()

		critique, err := model.CritiqueFindings(context.Background(), []string{"finding"})
		require.NoError(t, err)
		assert.Equal(t, "critique", critique)
	})

	t.Run("cooldown text generation uses caller fallback", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return req != nil && strings.Contains(req.Prompt, "cooldown prompt")
		})).Return(nil, errors.New("vertex text generation provider cooldown active; retry after 45s")).Once()

		text, err := model.Generate(context.Background(), "cooldown prompt")
		require.NoError(t, err)
		assert.Equal(t, "cooldown prompt", text)
	})

	t.Run("cooldown structured generation uses caller fallback", func(t *testing.T) {
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return req != nil && strings.Contains(req.Prompt, "cooldown query")
		})).Return(nil, errors.New("vertex structured output provider cooldown active; retry after 45s")).Once()

		hypotheses, err := model.GenerateHypotheses(context.Background(), "cooldown query")
		require.NoError(t, err)
		assert.Equal(t, []string{
			"cooldown query",
			"cooldown query with supporting evidence",
			"cooldown query with counter-evidence considered",
		}, hypotheses)
	})
}

type mockQuestStateSaver struct {
	saved QuestState
	set   bool
	err   error
}

func (m *mockQuestStateSaver) SaveQuestState(_ context.Context, quest *QuestState) error {
	m.saved = *quest
	m.set = true
	if m.err != nil {
		return m.err
	}
	return nil
}

func TestYOLOOrchestratorRun(t *testing.T) {
	orchestrator := NewYOLOOrchestrator(" job-1 ", "  query text  ", nil, nil, nil, nil, nil)

	state, err := orchestrator.Run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, "job-1", orchestrator.jobID)
	assert.Equal(t, "query text", orchestrator.query)
	assert.Equal(t, "YOLO synthesis for: query text", state.Synthesis.Sections["main"])
	assert.Equal(t, 3, len(state.Hypotheses))
	assert.Equal(t, "Hypothesis 2: query text", state.Hypotheses[1].Text)
	assert.Equal(t, "complete", state.Status)
}

func TestYOLOOrchestratorRunPersistsState(t *testing.T) {
	saver := &mockQuestStateSaver{}
	orchestrator := NewYOLOOrchestrator("", "query", nil, nil, nil, saver, nil).WithUserID(" user-1 ")

	state, err := orchestrator.Run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, state)

	assert.True(t, saver.set)
	assert.Equal(t, "query", saver.saved.Query)
	assert.Equal(t, "user-1", saver.saved.UserID)
	assert.Equal(t, QuestStatusComplete, saver.saved.Status)
}
