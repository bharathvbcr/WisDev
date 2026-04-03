package wisdev

import (
	"context"
	"errors"
	"testing"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/policy"
	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestExecutor_CoordinateAgentFeedback(t *testing.T) {
	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	e := NewPlanExecutor(nil, policy.DefaultPolicyConfig(), lc, nil, nil, nil, nil)
	session := &AgentSession{OriginalQuery: "q"}

	t.Run("Success", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "REPLAN"}, nil).Once()
		res, err := e.CoordinateAgentFeedback(context.Background(), session, nil)
		assert.NoError(t, err)
		assert.Equal(t, "REPLAN", res)
	})

	t.Run("Error Fallback", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.Anything).Return(nil, errors.New("fail")).Once()
		res, err := e.CoordinateAgentFeedback(context.Background(), session, nil)
		assert.NoError(t, err)
		assert.Equal(t, "CONTINUE", res)
	})
}

func TestExecutor_Execute_EdgeCases(t *testing.T) {
	e := NewPlanExecutor(nil, policy.DefaultPolicyConfig(), nil, nil, nil, nil, nil)

	t.Run("Nil Plan", func(t *testing.T) {
		session := &AgentSession{SessionID: "s1"}
		out := make(chan PlanExecutionEvent, 10)
		e.Execute(context.Background(), session, out)
		ev := <-out
		assert.Equal(t, EventStepFailed, ev.Type)
		assert.Contains(t, ev.Message, "session has no Plan")
	})

	t.Run("Plan Done", func(t *testing.T) {
		session := &AgentSession{
			SessionID: "s1",
			Status:    SessionGeneratingTree,
			Plan: &PlanState{
				Steps:            []PlanStep{{ID: "s1"}},
				CompletedStepIDs: map[string]bool{"s1": true},
				FailedStepIDs:    make(map[string]string),
			},
		}
		out := make(chan PlanExecutionEvent, 10)
		e.Execute(context.Background(), session, out)
		ev := <-out
		assert.Equal(t, EventCompleted, ev.Type)
	})
	
	t.Run("Plan Failed", func(t *testing.T) {
		session := &AgentSession{
			SessionID: "s1",
			Status:    SessionGeneratingTree,
			Plan: &PlanState{
				Steps:            []PlanStep{{ID: "s1"}},
				CompletedStepIDs: make(map[string]bool),
				FailedStepIDs:    map[string]string{"s1": "oops"},
			},
		}
		out := make(chan PlanExecutionEvent, 10)
		e.Execute(context.Background(), session, out)
		ev := <-out
		assert.Equal(t, EventStepFailed, ev.Type)
		assert.Contains(t, ev.Message, "failed steps")
	})
}
