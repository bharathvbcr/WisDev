package wisdev

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// mockExecutor implements PlanStepRunner for testing.
type mockExecutor struct {
	mock.Mock
}

func (m *mockExecutor) RunStepWithRecovery(ctx context.Context, session *AgentSession, step PlanStep, laneID int) StepResult {
	args := m.Called(ctx, session, step, laneID)
	return args.Get(0).(StepResult)
}

func TestSelfHealer_Execute_Success(t *testing.T) {
	mLLM := new(mockLLM)
	mExec := new(mockExecutor)
	sh := NewSelfHealer(mLLM, mExec)

	step := PlanStep{ID: "step_ok", Action: "search.arxiv"}
	mExec.On("RunStepWithRecovery", mock.Anything, mock.Anything, step, 1).
		Return(StepResult{Sources: []Source{{ID: "src1"}}})

	result, err := sh.Execute(context.Background(), "sess1", step)

	assert.NoError(t, err)
	assert.Len(t, result["sources"], 1)
	mLLM.AssertNotCalled(t, "StructuredOutput")
}

func TestSelfHealer_Execute_SelfHeal(t *testing.T) {
	mLLM := new(mockLLM)
	mExec := new(mockExecutor)
	sh := NewSelfHealer(mLLM, mExec)

	step := PlanStep{ID: "step_retry", Action: "search.web"}
	retryErr := errors.New("rate limit exceeded")
	revisedJSON := `{"id":"step_retry","action":"search.web","params":{"q":"revised"}}`

	var revisedStep PlanStep
	json.Unmarshal([]byte(revisedJSON), &revisedStep)

	mExec.On("RunStepWithRecovery", mock.Anything, mock.Anything, step, 1).
		Return(StepResult{Err: retryErr}).Once()
	mLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assertWisdevStructuredPromptHygiene(t, req.Prompt)
		return strings.Contains(req.Prompt, "rate limit exceeded") &&
			req.GetThinkingBudget() == 1024 &&
			req.RequestClass == "standard" &&
			req.RetryProfile == "standard" &&
			req.ServiceTier == "standard" &&
			req.LatencyBudgetMs > 0
	})).Return(&llmv1.StructuredResponse{JsonResult: revisedJSON}, nil)
	mExec.On("RunStepWithRecovery", mock.Anything, mock.Anything, revisedStep, 1).
		Return(StepResult{Sources: []Source{{ID: "src2"}}})

	result, err := sh.Execute(context.Background(), "sess2", step)

	assert.NoError(t, err)
	assert.Len(t, result["sources"], 1)
}

func TestSelfHealer_Execute_Oscillation(t *testing.T) {
	mLLM := new(mockLLM)
	mExec := new(mockExecutor)
	sh := NewSelfHealer(mLLM, mExec)

	step := PlanStep{ID: "step_osc", Action: "search.arxiv"}
	sameErr := errors.New("timeout: upstream unavailable")
	sameStepJSON := func() string { b, _ := json.Marshal(step); return string(b) }()

	mExec.On("RunStepWithRecovery", mock.Anything, mock.Anything, mock.Anything, 1).
		Return(StepResult{Err: sameErr})
	mLLM.On("StructuredOutput", mock.Anything, mock.Anything).
		Return(&llmv1.StructuredResponse{JsonResult: sameStepJSON}, nil)

	_, err := sh.Execute(context.Background(), "sess3", step)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "infinite recovery loop")
}

func TestSelfHealer_ReplanStep_CallsLLM(t *testing.T) {
	mLLM := new(mockLLM)
	mExec := new(mockExecutor)
	sh := NewSelfHealer(mLLM, mExec)

	step := PlanStep{ID: "step_fail", Action: "search.arxiv", Params: map[string]any{"q": "original"}}
	retryErr := errors.New("rate limit exceeded")
	revisedJSON := `{"id":"step_fail","action":"search.arxiv","params":{"q":"revised query"}}`

	mExec.On("RunStepWithRecovery", mock.Anything, mock.Anything, step, 1).
		Return(StepResult{Err: retryErr}).Once()

	mLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assertWisdevStructuredPromptHygiene(t, req.Prompt)
		return strings.Contains(req.Prompt, "rate limit exceeded") &&
			req.GetThinkingBudget() == 1024 &&
			req.RequestClass == "standard" &&
			req.RetryProfile == "standard" &&
			req.ServiceTier == "standard" &&
			req.LatencyBudgetMs > 0
	})).Return(&llmv1.StructuredResponse{JsonResult: revisedJSON}, nil)

	var revisedStep PlanStep
	json.Unmarshal([]byte(revisedJSON), &revisedStep)
	mExec.On("RunStepWithRecovery", mock.Anything, mock.Anything, revisedStep, 1).
		Return(StepResult{Sources: []Source{{ID: "s1"}}})

	result, err := sh.Execute(context.Background(), "sess", step)

	assert.NoError(t, err)
	assert.Len(t, result["sources"], 1)
	mLLM.AssertExpectations(t)
}

func TestSelfHealer_ReplanStep_CooldownKeepsOriginalStep(t *testing.T) {
	mLLM := new(mockLLM)
	mExec := new(mockExecutor)
	sh := NewSelfHealer(mLLM, mExec)

	step := PlanStep{ID: "step_retry", Action: "search.arxiv", Params: map[string]any{"q": "original"}}
	retryErr := errors.New("rate limit exceeded")
	cooldownErr := errors.New("vertex structured output provider cooldown active; retry after 45s")

	mExec.On("RunStepWithRecovery", mock.Anything, mock.Anything, step, 1).
		Return(StepResult{Err: retryErr}).Once()
	mLLM.On("StructuredOutput", mock.Anything, mock.Anything).
		Return(nil, cooldownErr).Once()
	mExec.On("RunStepWithRecovery", mock.Anything, mock.Anything, step, 1).
		Return(StepResult{Sources: []Source{{ID: "s-original"}}}).Once()

	result, err := sh.Execute(context.Background(), "sess", step)

	assert.NoError(t, err)
	assert.Len(t, result["sources"], 1)
	mLLM.AssertExpectations(t)
}

func TestSelfHealer_FatalError_NoRetry(t *testing.T) {
	mLLM := new(mockLLM)
	mExec := new(mockExecutor)
	sh := NewSelfHealer(mLLM, mExec)

	step := PlanStep{ID: "step_auth", Action: "search.private"}
	fatalErr := errors.New("UNAUTHORIZED: invalid API key")

	mExec.On("RunStepWithRecovery", mock.Anything, mock.Anything, step, 1).
		Return(StepResult{Err: fatalErr})

	_, err := sh.Execute(context.Background(), "sess", step)

	assert.Error(t, err)
	mLLM.AssertNotCalled(t, "StructuredOutput")
}
