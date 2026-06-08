package wisdev

import (
	"context"
	"iter"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"google.golang.org/adk/agent"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestWorkflowAgent_MapToADKEvent(t *testing.T) {
	wa := &WisDevWorkflowAgent{}
	ctx := &mockInvocationContext{}
	ctx.On("InvocationID").Return("test-inv-id")
	ctx.On("Session").Return(mockSession{})

	now := time.Now().UnixMilli()

	tests := []struct {
		name  string
		event PlanExecutionEvent
	}{
		{
			name: "step started",
			event: PlanExecutionEvent{
				Type:      EventStepStarted,
				StepID:    "s1",
				Message:   "msg",
				CreatedAt: now,
			},
		},
		{
			name: "step completed",
			event: PlanExecutionEvent{
				Type:      EventStepCompleted,
				StepID:    "s1",
				Payload:   map[string]any{"foo": "bar"},
				CreatedAt: now,
			},
		},
		{
			name: "step failed",
			event: PlanExecutionEvent{
				Type:      EventStepFailed,
				StepID:    "s1",
				Message:   "err",
				CreatedAt: now,
			},
		},
		{
			name: "paper found",
			event: PlanExecutionEvent{
				Type:      EventPaperFound,
				Payload:   map[string]any{"title": "Title"},
				CreatedAt: now,
			},
		},
		{
			name: "confirmation needed",
			event: PlanExecutionEvent{
				Type:      EventConfirmationNeed,
				Message:   "confirm",
				CreatedAt: now,
			},
		},
		{
			name: "plan revised",
			event: PlanExecutionEvent{
				Type:      EventPlanRevised,
				Message:   "replan",
				CreatedAt: now,
			},
		},
		{
			name: "completed",
			event: PlanExecutionEvent{
				Type:      EventCompleted,
				CreatedAt: now,
			},
		},
		{
			name: "progress (skip)",
			event: PlanExecutionEvent{
				Type:      EventProgress,
				CreatedAt: now,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wa.mapToADKEvent(ctx, tt.event)
			if tt.name == "progress (skip)" {
				assert.Nil(t, got)
			} else {
				assert.NotNil(t, got)
				assert.Equal(t, "wisdev-workflow", got.Author)

				if tt.name == "step failed" {
					assert.Equal(t, "err", got.ErrorMessage)
				}
				if tt.name == "completed" {
					assert.True(t, got.TurnComplete)
				}
			}
		})
	}
}

type mockInvocationContext struct {
	context.Context
	mock.Mock
}

func (m *mockInvocationContext) Agent() agent.Agent { return nil }

func (m *mockInvocationContext) Artifacts() agent.Artifacts { return nil }

func (m *mockInvocationContext) Memory() agent.Memory { return nil }

func (m *mockInvocationContext) Session() adksession.Session {
	args := m.Called()
	return args.Get(0).(adksession.Session)
}

func (m *mockInvocationContext) InvocationID() string {
	args := m.Called()
	return args.String(0)
}

func (m *mockInvocationContext) Branch() string { return "" }

func (m *mockInvocationContext) UserContent() *genai.Content { return nil }

func (m *mockInvocationContext) RunConfig() *agent.RunConfig { return nil }

func (m *mockInvocationContext) EndInvocation() {}

func (m *mockInvocationContext) Ended() bool { return false }

func (m *mockInvocationContext) WithContext(ctx context.Context) agent.InvocationContext {
	if ctx == nil {
		ctx = context.Background()
	}
	return &mockInvocationContext{Context: ctx}
}

type mockSession struct{}

func (mockSession) ID() string                { return "" }
func (mockSession) AppName() string           { return "" }
func (mockSession) UserID() string            { return "" }
func (mockSession) State() adksession.State   { return mockState{} }
func (mockSession) Events() adksession.Events { return mockEvents{} }
func (mockSession) LastUpdateTime() time.Time { return time.Time{} }

type mockState struct{}

func (mockState) Get(string) (any, error) { return nil, adksession.ErrStateKeyNotExist }

func (mockState) Set(string, any) error { return nil }

func (mockState) All() iter.Seq2[string, any] {
	return func(func(string, any) bool) {}
}

type mockEvents struct{}

func (mockEvents) All() iter.Seq[*adksession.Event] {
	return func(func(*adksession.Event) bool) {}
}

func (mockEvents) Len() int { return 0 }

func (mockEvents) At(int) *adksession.Event { return nil }
