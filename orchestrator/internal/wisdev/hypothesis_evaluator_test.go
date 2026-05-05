package wisdev

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

func TestHypothesisEvaluator_HeuristicEvaluation(t *testing.T) {
	evaluator := NewHypothesisEvaluator(nil)

	tests := []struct {
		name               string
		hypothesis         *Hypothesis
		evidence           []EvidenceFinding
		expectedVerdict    string
		expectedScoreRange [2]float64 // min, max
	}{
		{
			name: "No evidence - uncertain",
			hypothesis: &Hypothesis{
				ID:    "hyp1",
				Claim: "Test hypothesis",
			},
			evidence:           []EvidenceFinding{},
			expectedVerdict:    "uncertain",
			expectedScoreRange: [2]float64{0.0, 0.1},
		},
		{
			name: "High confidence evidence - supported",
			hypothesis: &Hypothesis{
				ID:    "hyp2",
				Claim: "Strong hypothesis",
				Evidence: []*EvidenceFinding{
					{ID: "ev1", Confidence: 0.9},
					{ID: "ev2", Confidence: 0.85},
					{ID: "ev3", Confidence: 0.8},
				},
			},
			evidence:           []EvidenceFinding{},
			expectedVerdict:    "supported",
			expectedScoreRange: [2]float64{0.7, 1.0},
		},
		{
			name: "Low confidence evidence - refuted",
			hypothesis: &Hypothesis{
				ID:    "hyp3",
				Claim: "Weak hypothesis",
				Evidence: []*EvidenceFinding{
					{ID: "ev1", Confidence: 0.2},
					{ID: "ev2", Confidence: 0.15},
				},
			},
			evidence:           []EvidenceFinding{},
			expectedVerdict:    "refuted",
			expectedScoreRange: [2]float64{0.0, 0.3},
		},
		{
			name: "Mixed evidence with contradictions",
			hypothesis: &Hypothesis{
				ID:    "hyp4",
				Claim: "Controversial hypothesis",
				Evidence: []*EvidenceFinding{
					{ID: "ev1", Confidence: 0.7},
					{ID: "ev2", Confidence: 0.6},
				},
				ContradictionCount: 2,
			},
			evidence:           []EvidenceFinding{},
			expectedVerdict:    "uncertain",
			expectedScoreRange: [2]float64{0.3, 0.7},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := evaluator.heuristicEvaluation(tt.hypothesis, tt.evidence)

			if result.Verdict != tt.expectedVerdict {
				t.Errorf("Expected verdict %s, got %s", tt.expectedVerdict, result.Verdict)
			}

			if result.Score < tt.expectedScoreRange[0] || result.Score > tt.expectedScoreRange[1] {
				t.Errorf("Score %.2f out of expected range [%.2f, %.2f]",
					result.Score, tt.expectedScoreRange[0], tt.expectedScoreRange[1])
			}

			if result.EvaluatedAt == 0 {
				t.Error("EvaluatedAt timestamp should be set")
			}
		})
	}
}

func TestHypothesisEvaluator_ParseEvaluationResponseExactJSONOnly(t *testing.T) {
	evaluator := NewHypothesisEvaluator(nil)

	result, err := evaluator.parseEvaluationResponse(`{"score":0.72,"verdict":"supported","reasoning":"grounded"}`)
	assert.NoError(t, err)
	assert.Equal(t, "supported", result.Verdict)
	assert.Equal(t, 0.72, result.Score)

	_, err = evaluator.parseEvaluationResponse("```json\n{\"score\":0.72,\"verdict\":\"supported\",\"reasoning\":\"grounded\"}\n```")
	assert.Error(t, err)

	_, err = evaluator.parseEvaluationResponse("prefix {\"score\":0.72,\"verdict\":\"supported\",\"reasoning\":\"grounded\"}")
	assert.Error(t, err)
}

func TestHypothesisEvaluator_EvaluateFallsBackWithoutLLM(t *testing.T) {
	evaluator := NewHypothesisEvaluator(nil)
	hypothesis := &Hypothesis{
		ID:    "h-fallback",
		Claim: "Fallback evaluation uses local evidence",
	}
	result, err := evaluator.Evaluate(context.Background(), hypothesis, []EvidenceFinding{
		{ID: "ev-1", Claim: "Strong supporting evidence", Confidence: 0.82},
	})

	if err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}
	if result.HypothesisID != hypothesis.ID {
		t.Fatalf("expected hypothesis id %q, got %q", hypothesis.ID, result.HypothesisID)
	}
	if result.Score <= 0 {
		t.Fatalf("expected heuristic score to be positive, got %.2f", result.Score)
	}
	if result.EvaluatedAt == 0 {
		t.Fatal("expected evaluated timestamp")
	}
}

func TestHypothesisEvaluator_EvaluateCooldownErrorUsesHeuristic(t *testing.T) {
	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	evaluator := NewHypothesisEvaluator(NewBrainCapabilities(lc))
	hypothesis := &Hypothesis{
		ID:    "h-cooldown",
		Claim: "Cooldown evaluation uses local evidence",
	}
	evidence := []EvidenceFinding{
		{ID: "ev-1", Claim: "Moderate supporting evidence", Confidence: 0.66},
	}

	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.GetPrompt(), "Cooldown evaluation uses local evidence")
	})).Return(nil, errors.New("vertex structured output provider cooldown active; retry after 45s")).Once()

	result, err := evaluator.Evaluate(context.Background(), hypothesis, evidence)

	if err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}
	if result.HypothesisID != hypothesis.ID {
		t.Fatalf("expected hypothesis id %q, got %q", hypothesis.ID, result.HypothesisID)
	}
	if result.Score <= 0 {
		t.Fatalf("expected heuristic score to be positive, got %.2f", result.Score)
	}
	msc.AssertNumberOfCalls(t, "StructuredOutput", 1)
}

func TestHypothesisEvaluator_EvaluateUsesNativeStructuredPrompt(t *testing.T) {
	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	evaluator := NewHypothesisEvaluator(NewBrainCapabilities(lc))

	hypothesis := &Hypothesis{
		ID:                      "h-native-structured",
		Claim:                   "Schema-backed evaluation avoids prose JSON recovery",
		FalsifiabilityCondition: "Structured output parsing fails on prose wrappers.",
		ConfidenceThreshold:     0.7,
		ConfidenceScore:         0.5,
	}

	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		prompt := req.GetPrompt()
		return strings.Contains(prompt, "Schema-backed evaluation avoids prose JSON recovery") &&
			strings.Contains(prompt, wisdevStructuredOutputSchemaInstruction) &&
			!strings.Contains(prompt, "respond with JSON") &&
			!strings.Contains(prompt, "\"score\": <0.0 to 1.0") &&
			!strings.Contains(prompt, "Return JSON")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"score":0.82,"verdict":"supported","reasoning":"native schema result","branchingDecision":"keep"}`}, nil).Once()

	result, err := evaluator.Evaluate(context.Background(), hypothesis, []EvidenceFinding{
		{ID: "ev-native", Claim: "Exact schema result", Confidence: 0.82},
	})

	assert.NoError(t, err)
	assert.Equal(t, hypothesis.ID, result.HypothesisID)
	assert.Equal(t, "supported", result.Verdict)
	assert.Equal(t, 0.82, result.Score)
	msc.AssertNumberOfCalls(t, "StructuredOutput", 1)
}

func TestHypothesisEvaluator_PruneHypothesesByScore(t *testing.T) {
	evaluator := NewHypothesisEvaluator(nil)

	hypotheses := []*Hypothesis{
		{ID: "h1", Claim: "High confidence", ConfidenceScore: 0.9},
		{ID: "h2", Claim: "Medium confidence", ConfidenceScore: 0.5},
		{ID: "h3", Claim: "Low confidence", ConfidenceScore: 0.2},
		{ID: "h4", Claim: "Very low confidence", ConfidenceScore: 0.1},
	}

	pruned := evaluator.PruneHypothesesByScore(hypotheses, 0.3)

	if len(pruned) != 2 {
		t.Errorf("Expected 2 hypotheses after pruning, got %d", len(pruned))
	}

	for _, h := range pruned {
		if h.ConfidenceScore < 0.3 {
			t.Errorf("Hypothesis %s with score %.2f should have been pruned", h.ID, h.ConfidenceScore)
		}
	}
}

func TestHypothesisEvaluator_EvaluateAll(t *testing.T) {
	evaluator := NewHypothesisEvaluator(nil)

	hypotheses := []*Hypothesis{
		{
			ID:    "h1",
			Claim: "Test hypothesis 1",
			Evidence: []*EvidenceFinding{
				{ID: "ev1", Confidence: 0.8},
			},
		},
		{
			ID:           "h2",
			Claim:        "Test hypothesis 2",
			IsTerminated: true, // Should be skipped
		},
		{
			ID:    "h3",
			Claim: "Test hypothesis 3",
			Evidence: []*EvidenceFinding{
				{ID: "ev2", Confidence: 0.6},
			},
		},
	}

	evidence := []EvidenceFinding{
		{ID: "ev1", Confidence: 0.8, Claim: "Supporting evidence"},
		{ID: "ev2", Confidence: 0.6, Claim: "Moderate evidence"},
	}

	results, _ := evaluator.EvaluateAll(context.Background(), hypotheses, evidence)

	// Should evaluate 2 (h2 is terminated)
	if len(results) != 2 {
		t.Errorf("Expected 2 evaluation results, got %d", len(results))
	}

	// Check that hypotheses were updated
	if hypotheses[0].ConfidenceScore == 0 {
		t.Error("Hypothesis 1 should have been updated with a confidence score")
	}

	if len(hypotheses[0].EvaluationHistory) == 0 {
		t.Error("Hypothesis 1 should have evaluation history")
	}

	// Check adaptive budget allocation
	if hypotheses[0].AllocatedQueryBudget == 0 {
		t.Error("Hypothesis 1 should have allocated query budget")
	}

	// Verify terminated hypothesis was not evaluated
	if hypotheses[1].ConfidenceScore != 0 {
		t.Error("Terminated hypothesis should not have been evaluated")
	}
}

func TestHypothesisEvaluator_EvaluateAllBatched_FallsBackWithoutLLM(t *testing.T) {
	evaluator := NewHypothesisEvaluator(nil)

	hypotheses := []*Hypothesis{
		{ID: "h1", Claim: "Hyp 1", Evidence: []*EvidenceFinding{{ID: "ev1", Confidence: 0.8}}},
		{ID: "h2", Claim: "Hyp 2", Evidence: []*EvidenceFinding{{ID: "ev2", Confidence: 0.6}}},
		{ID: "h3", Claim: "Hyp 3", IsTerminated: true},
	}
	evidence := []EvidenceFinding{
		{ID: "ev1", Confidence: 0.8, Claim: "Strong"},
		{ID: "ev2", Confidence: 0.6, Claim: "Moderate"},
	}

	results, _ := evaluator.EvaluateAllBatched(context.Background(), hypotheses, evidence, 8)

	if len(results) != 2 {
		t.Errorf("Expected 2 results (h3 terminated), got %d", len(results))
	}
	for _, r := range results {
		if r.EvaluatedAt == 0 {
			t.Error("EvaluatedAt should be set")
		}
	}
	if hypotheses[0].AllocatedQueryBudget == 0 {
		t.Error("Hypothesis should have query budget allocated")
	}
}

func TestHypothesisEvaluator_EvaluateAllBatched_RateLimitUsesHeuristicWithoutSequentialFanout(t *testing.T) {
	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	evaluator := NewHypothesisEvaluator(NewBrainCapabilities(lc))

	hypotheses := []*Hypothesis{
		{ID: "h1", Claim: "Hyp 1", Evidence: []*EvidenceFinding{{ID: "ev1", Confidence: 0.8}}},
		{ID: "h2", Claim: "Hyp 2", Evidence: []*EvidenceFinding{{ID: "ev2", Confidence: 0.6}}},
	}
	evidence := []EvidenceFinding{
		{ID: "ev1", Confidence: 0.8, Claim: "Strong"},
		{ID: "ev2", Confidence: 0.6, Claim: "Moderate"},
	}

	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.GetPrompt(), "Evaluate ALL of the following research hypotheses") &&
			strings.Contains(req.GetPrompt(), wisdevStructuredOutputSchemaInstruction) &&
			!strings.Contains(req.GetPrompt(), "Return a JSON array")
	})).Return(nil, errors.New("429 RESOURCE_EXHAUSTED")).Once()

	results, branched := evaluator.EvaluateAllBatched(context.Background(), hypotheses, evidence, 8)

	if len(results) != 2 {
		t.Fatalf("expected heuristic results for both hypotheses, got %d", len(results))
	}
	if len(branched) != 0 {
		t.Fatalf("expected no branched hypotheses during rate-limit fallback, got %d", len(branched))
	}
	msc.AssertNumberOfCalls(t, "StructuredOutput", 1)
}

func TestHypothesisEvaluator_EvaluateAllBatched_SingleHypothesisDelegatesToSequential(t *testing.T) {
	evaluator := NewHypothesisEvaluator(nil)

	hypotheses := []*Hypothesis{
		{ID: "h1", Claim: "Only hypothesis", Evidence: []*EvidenceFinding{{ID: "ev1", Confidence: 0.9}}},
	}
	evidence := []EvidenceFinding{{ID: "ev1", Confidence: 0.9}}

	results, _ := evaluator.EvaluateAllBatched(context.Background(), hypotheses, evidence, 8)
	if len(results) != 1 {
		t.Errorf("Expected 1 result, got %d", len(results))
	}
}

func TestHypothesisEvaluator_ApplyEvaluationResult_Branching(t *testing.T) {
	evaluator := NewHypothesisEvaluator(nil)
	h := &Hypothesis{ID: "h1", Claim: "Broad hypothesis", Query: "test"}

	result := &EvaluationResult{
		Score:             0.5,
		Verdict:           "uncertain",
		BranchingDecision: "branch",
		SubHypotheses:     []string{"Sub A", "Sub B"},
		EvaluatedAt:       NowMillis(),
	}

	newHyps := evaluator.applyEvaluationResult(h, result)
	if h.Status != "branched" {
		t.Errorf("Expected status 'branched', got %s", h.Status)
	}
	if len(newHyps) != 2 {
		t.Errorf("Expected 2 sub-hypotheses, got %d", len(newHyps))
	}
	for _, sub := range newHyps {
		if sub.ParentID != "h1" {
			t.Errorf("Sub-hypothesis should have parent ID h1, got %s", sub.ParentID)
		}
	}
}

func TestHypothesisEvaluator_ApplyEvaluationResult_Prune(t *testing.T) {
	evaluator := NewHypothesisEvaluator(nil)
	h := &Hypothesis{ID: "h1", Claim: "Bad hypothesis"}

	result := &EvaluationResult{
		Score:             0.1,
		Verdict:           "refuted",
		BranchingDecision: "prune",
		EvaluatedAt:       NowMillis(),
	}

	newHyps := evaluator.applyEvaluationResult(h, result)
	if !h.IsTerminated {
		t.Error("Pruned hypothesis should be terminated")
	}
	if h.Status != "pruned" {
		t.Errorf("Expected status 'pruned', got %s", h.Status)
	}
	if len(newHyps) != 0 {
		t.Errorf("Pruned hypothesis should produce no sub-hypotheses")
	}
}

func TestHypothesisEvaluator_BuildEvidenceSummary(t *testing.T) {
	evaluator := NewHypothesisEvaluator(nil)

	hypothesis := &Hypothesis{
		ID:    "h1",
		Claim: "Test hypothesis",
		Evidence: []*EvidenceFinding{
			{ID: "ev1", Confidence: 0.8, Claim: "Evidence 1", PaperTitle: "Paper 1"},
			{ID: "ev2", Confidence: 0.7, Claim: "Evidence 2", PaperTitle: "Paper 2"},
		},
	}

	summary := evaluator.buildEvidenceSummary(hypothesis, []EvidenceFinding{})

	if summary == "No evidence collected yet." {
		t.Error("Should have built summary from hypothesis-linked evidence")
	}

	if !strings.Contains(summary, "Evidence 1") {
		t.Error("Summary should contain evidence claim")
	}
}
