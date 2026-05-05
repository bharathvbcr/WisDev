package wisdev

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestNewWisDevWorkflowAgent(t *testing.T) {
	gw := &AgentGateway{}
	exec := &PlanExecutor{}

	a, err := NewWisDevWorkflowAgent(gw, exec, nil)
	assert.NoError(t, err)
	assert.NotNil(t, a)
	assert.Equal(t, "wisdev-workflow", a.Name())
}

func TestWorkflowAgent_Run(t *testing.T) {
	gw := &AgentGateway{
		Store: NewInMemorySessionStore(),
	}
	exec := &PlanExecutor{}
	wa := &WisDevWorkflowAgent{
		gateway:  gw,
		executor: exec,
	}

	t.Run("no plan found", func(t *testing.T) {
		sessionID := "s1"
		userID := "u1"

		sess, _ := gw.CreateSession(context.Background(), userID, "query")
		sess.SessionID = sessionID
		sess.Plan = nil // No plan
		_ = gw.Store.Put(context.Background(), sess, 0)

		mctx := &mockInvocationContext{Context: context.Background()}
		msess := &mockSessionWithID{id: sessionID, userID: userID}
		mctx.On("Session").Return(msess)
		mctx.On("InvocationID").Return("inv1")
		mctx.On("UserContent").Return(&genai.Content{Parts: []*genai.Part{{Text: "query"}}})

		seq := wa.Run(mctx)
		count := 0
		var runErr error
		seq(func(ev *session.Event, err error) bool {
			count++
			runErr = err
			return true
		})

		assert.Error(t, runErr)
		assert.Contains(t, runErr.Error(), "no execution plan found")
	})

	t.Run("success execution", func(t *testing.T) {
		sessionID := "s2"
		userID := "u2"

		// Setup mock model for the executor to avoid REPLAN
		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		msc.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "CONTINUE"}, nil).Maybe()

		// Mock the gateway behavior more directly if needed, or rely on Store
		gw := &AgentGateway{
			Store: NewInMemorySessionStore(),
		}
		exec := &PlanExecutor{llmClient: lc, maxParallelLanes: 1}
		wa := &WisDevWorkflowAgent{
			gateway:  gw,
			executor: exec,
		}

		sess, _ := gw.CreateSession(context.Background(), userID, "query")
		sess.SessionID = sessionID
		sess.Status = SessionGeneratingTree
		// Empty plan means all steps are terminal immediately
		sess.Plan = &PlanState{PlanID: "p1"}
		_ = gw.Store.Put(context.Background(), sess, 0)

		mctx := &mockInvocationContext{Context: context.Background()}
		msess := &mockSessionWithID{id: sessionID, userID: userID}
		// In TestWorkflowAgent_Run we don't have control over wa.gateway easily because it's a pointer to AgentGateway struct.
		// AgentGateway doesn't have a mockable GetSession method (it's a concrete method).
		// However, it uses its Store.

		mctx.On("Session").Return(msess)
		mctx.On("InvocationID").Return("inv2")

		seq := wa.Run(mctx)
		foundCompleted := make(chan bool, 1)
		errChan := make(chan error, 1)
		go func() {
			seq(func(ev *session.Event, err error) bool {
				if err != nil {
					errChan <- err
					return false
				}
				if ev != nil && ev.ErrorMessage != "" {
					errChan <- fmt.Errorf("%s", ev.ErrorMessage)
					return false
				}
				if ev != nil && strings.Contains(ev.Content.Parts[0].Text, "successfully") {
					foundCompleted <- true
				}
				return true
			})
		}()

		select {
		case err := <-errChan:
			t.Fatalf("run failed: %v", err)
		case <-foundCompleted:
			// Success
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for completion")
		}

		updated, _ := gw.Store.Get(context.Background(), sessionID)
		assert.Equal(t, SessionGeneratingTree, updated.Status)
	})
	t.Run("yield false", func(t *testing.T) {
		sessionID := "s3"
		userID := "u3"

		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		msc.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "CONTINUE"}, nil).Maybe()

		gw := &AgentGateway{Store: NewInMemorySessionStore()}
		exec := &PlanExecutor{llmClient: lc, maxParallelLanes: 1}
		wa := &WisDevWorkflowAgent{gateway: gw, executor: exec}

		sess, _ := gw.CreateSession(context.Background(), userID, "query")
		sess.SessionID = sessionID
		sess.Status = SessionGeneratingTree
		sess.Plan = &PlanState{PlanID: "p1"}
		_ = gw.Store.Put(context.Background(), sess, 0)

		mctx := &mockInvocationContext{Context: context.Background()}
		msess := &mockSessionWithID{id: sessionID, userID: userID}
		mctx.On("Session").Return(msess)
		mctx.On("InvocationID").Return("inv3")

		seq := wa.Run(mctx)
		// Yield false on first event
		seq(func(ev *session.Event, err error) bool {
			return false
		})
	})

	t.Run("missing runtime wiring", func(t *testing.T) {
		wa := &WisDevWorkflowAgent{}
		mctx := &mockInvocationContext{Context: context.Background()}
		msess := &mockSessionWithID{id: "s-missing", userID: "u-missing"}
		mctx.On("Session").Return(msess)
		mctx.On("InvocationID").Return("inv-missing")

		seq := wa.Run(mctx)
		var runErr error
		seq(func(ev *session.Event, err error) bool {
			runErr = err
			return true
		})

		require.Error(t, runErr)
		assert.Contains(t, runErr.Error(), "not fully initialized")
	})
}

func TestWorkflowAgent_MapToADKEvent_Progress(t *testing.T) {
	wa := &WisDevWorkflowAgent{}
	mctx := &mockInvocationContext{}
	mctx.On("InvocationID").Return("inv1")
	mctx.On("Session").Return(mockSession{})

	ev := wa.mapToADKEvent(mctx, PlanExecutionEvent{Type: EventProgress})
	assert.Nil(t, ev)
}

func TestWorkflowAgent_MapToADKEvent_Full(t *testing.T) {
	wa := &WisDevWorkflowAgent{}
	mctx := &mockInvocationContext{}
	mctx.On("InvocationID").Return("inv1")
	mctx.On("Session").Return(mockSession{})

	tests := []struct {
		name     string
		event    PlanExecutionEvent
		contains string
	}{
		{"Started", PlanExecutionEvent{Type: EventStepStarted, StepID: "s1", Message: "msg"}, "Starting step"},
		{"Completed", PlanExecutionEvent{Type: EventStepCompleted, StepID: "s1"}, "Step completed"},
		{"Failed", PlanExecutionEvent{Type: EventStepFailed, StepID: "s1", Message: "err"}, "Step failed"},
		{"Paper", PlanExecutionEvent{Type: EventPaperFound, Payload: map[string]any{"title": "T1"}}, "Evidence found"},
		{"Confirm", PlanExecutionEvent{Type: EventConfirmationNeed, Message: "msg"}, "Confirmation required"},
		{"Revised", PlanExecutionEvent{Type: EventPlanRevised, Message: "msg"}, "Plan revised"},
		{"Unknown", PlanExecutionEvent{Type: "unknown"}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wa.mapToADKEvent(mctx, tt.event)
			if tt.name == "Unknown" {
				assert.Nil(t, got)
			} else {
				assert.NotNil(t, got)
				assert.Contains(t, got.Content.Parts[0].Text, tt.contains)
			}
		})
	}
}

func TestWorkflowAgent_MapToADKEvent_MetadataAndFallbackTimestamp(t *testing.T) {
	wa := &WisDevWorkflowAgent{}
	mctx := &mockInvocationContext{}
	mctx.On("InvocationID").Return("inv-meta")
	mctx.On("Session").Return(mockSession{})

	before := time.Now()
	got := wa.mapToADKEvent(mctx, PlanExecutionEvent{
		Type:      EventStepCompleted,
		TraceID:   "trace-1",
		SessionID: "session-1",
		PlanID:    "plan-1",
		StepID:    "step-1",
		Message:   "done",
		Payload:   map[string]any{"result": "ok"},
	})
	after := time.Now()

	require.NotNil(t, got)
	assert.Equal(t, map[string]any{"result": "ok"}, got.Actions.StateDelta)
	require.NotNil(t, got.CustomMetadata)
	assert.Equal(t, "step_completed", got.CustomMetadata["eventType"])
	assert.Equal(t, "trace-1", got.CustomMetadata["traceId"])
	assert.Equal(t, "session-1", got.CustomMetadata["sessionId"])
	assert.Equal(t, "plan-1", got.CustomMetadata["planId"])
	assert.Equal(t, "step-1", got.CustomMetadata["stepId"])
	assert.Equal(t, "done", got.CustomMetadata["message"])
	assert.Equal(t, "ok", got.CustomMetadata["result"])
	assert.False(t, got.Timestamp.Before(before.Add(-time.Second)))
	assert.False(t, got.Timestamp.After(after.Add(time.Second)))
}

type mockSessionWithID struct {
	mockSession
	id     string
	userID string
}

func (m *mockSessionWithID) ID() string     { return m.id }
func (m *mockSessionWithID) UserID() string { return m.userID }
