package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
	"google.golang.org/grpc"
)

type mockExecutionRunner struct {
	mock.Mock
}

func (m *mockExecutionRunner) RunStepWithRecovery(ctx context.Context, session *wisdev.AgentSession, step wisdev.PlanStep, laneID int) wisdev.StepResult {
	args := m.Called(ctx, session, step, laneID)
	return args.Get(0).(wisdev.StepResult)
}
func (m *mockExecutionRunner) CoordinateAgentFeedback(ctx context.Context, session *wisdev.AgentSession, outcomes []wisdev.PlanOutcome) (string, error) {
	args := m.Called(ctx, session, outcomes)
	return args.String(0), args.Error(1)
}
func (m *mockExecutionRunner) Execute(ctx context.Context, session *wisdev.AgentSession, out chan<- wisdev.PlanExecutionEvent) {
	m.Called(ctx, session, out)
}

type mockLLM struct {
	mock.Mock
}

func (m *mockLLM) Generate(ctx context.Context, in *llmv1.GenerateRequest, opts ...grpc.CallOption) (*llmv1.GenerateResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*llmv1.GenerateResponse), args.Error(1)
}
func (m *mockLLM) GenerateStream(ctx context.Context, in *llmv1.GenerateRequest, opts ...grpc.CallOption) (llmv1.LLMService_GenerateStreamClient, error) {
	return nil, nil
}
func (m *mockLLM) StructuredOutput(ctx context.Context, in *llmv1.StructuredRequest, opts ...grpc.CallOption) (*llmv1.StructuredResponse, error) {
	return nil, nil
}
func (m *mockLLM) Embed(ctx context.Context, in *llmv1.EmbedRequest, opts ...grpc.CallOption) (*llmv1.EmbedResponse, error) {
	return nil, nil
}
func (m *mockLLM) EmbedBatch(ctx context.Context, in *llmv1.EmbedBatchRequest, opts ...grpc.CallOption) (*llmv1.EmbedBatchResponse, error) {
	return nil, nil
}
func (m *mockLLM) Health(ctx context.Context, in *llmv1.HealthRequest, opts ...grpc.CallOption) (*llmv1.HealthResponse, error) {
	return nil, nil
}

type failingSessionStore struct {
	base    wisdev.SessionStore
	putErr  error
	failPut bool
}

func (s *failingSessionStore) Get(ctx context.Context, sessionID string) (*wisdev.AgentSession, error) {
	return s.base.Get(ctx, sessionID)
}

func (s *failingSessionStore) Put(ctx context.Context, session *wisdev.AgentSession, ttl time.Duration) error {
	if s.failPut {
		return s.putErr
	}
	return s.base.Put(ctx, session, ttl)
}

func (s *failingSessionStore) Delete(ctx context.Context, sessionID string) error {
	return s.base.Delete(ctx, sessionID)
}

func (s *failingSessionStore) List(ctx context.Context, userID string) ([]*wisdev.AgentSession, error) {
	return s.base.List(ctx, userID)
}

type nilSessionStore struct{}

func (s *nilSessionStore) Get(context.Context, string) (*wisdev.AgentSession, error) {
	return nil, nil
}

func (s *nilSessionStore) Put(context.Context, *wisdev.AgentSession, time.Duration) error {
	return nil
}

func (s *nilSessionStore) Delete(context.Context, string) error {
	return nil
}

func (s *nilSessionStore) List(context.Context, string) ([]*wisdev.AgentSession, error) {
	return nil, nil
}

type panicExecutionRunner struct {
	panicStepID string
}

func (p *panicExecutionRunner) RunStepWithRecovery(_ context.Context, _ *wisdev.AgentSession, step wisdev.PlanStep, laneID int) wisdev.StepResult {
	if step.ID == p.panicStepID {
		panic("boom")
	}
	return wisdev.StepResult{Step: step, LaneID: laneID}
}

func (p *panicExecutionRunner) CoordinateAgentFeedback(context.Context, *wisdev.AgentSession, []wisdev.PlanOutcome) (string, error) {
	return "", nil
}

func (p *panicExecutionRunner) Execute(context.Context, *wisdev.AgentSession, chan<- wisdev.PlanExecutionEvent) {
}

func TestWisDev_ExecuteHandlers(t *testing.T) {
	me := &mockExecutionRunner{}
	mllm := &mockLLM{}
	lc := llm.NewClient()
	lc.SetClient(mllm)

	gw := &wisdev.AgentGateway{
		Store:        wisdev.NewInMemorySessionStore(),
		PolicyConfig: policy.DefaultPolicyConfig(),
		Executor:     me,
		LLMClient:    lc,
		SessionTTL:   1 * time.Hour,
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	t.Run("POST /wisdev/execute - Capability success", func(t *testing.T) {
		body := `{"action":"test_capability","stepAction":"confirm_and_execute","editedPayload":{"query":"edited"},"payload":{"key":"val","providers":["openalex"],"sourceMix":["academic"],"selectedParallelStepIds":["step-peer"]},"context":{"currentStepId":"step-cap","sourcePreferences":["semantic_scholar"],"selectedParallelStepIds":["step-shadow"]}}`
		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", strings.NewReader(body))
		rec := httptest.NewRecorder()

		mllm.On("Generate", mock.MatchedBy(func(ctx context.Context) bool {
			if ctx == nil {
				return false
			}
			_, ok := ctx.Deadline()
			return ok
		}), mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return req != nil &&
				req.Model == llm.ResolveStandardModel() &&
				req.RequestClass == "standard" &&
				req.RetryProfile == "standard" &&
				req.ServiceTier == "standard" &&
				req.GetThinkingBudget() == 1024 &&
				req.GetLatencyBudgetMs() > 0
		})).Return(&llmv1.GenerateResponse{Text: " output "}, nil).Once()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		exec := resp["execution"].(map[string]any)
		assert.NotNil(t, exec)
		data := exec["data"].(map[string]any)
		assert.ElementsMatch(t, []any{"openalex", "semantic_scholar"}, data["providers"].([]any))
		assert.ElementsMatch(t, []any{"academic"}, data["sourceMix"].([]any))
		assert.Equal(t, "step-cap", data["stepId"])
		assert.ElementsMatch(t, []any{"step-cap", "step-peer", "step-shadow"}, data["selectedParallelStepIds"].([]any))
		assert.Equal(t, "go", data["controlPlane"])
	})

	t.Run("POST /wisdev/execute - Capability empty output", func(t *testing.T) {
		body := `{"action":"empty_capability","payload":{}}`
		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", strings.NewReader(body))
		rec := httptest.NewRecorder()

		mllm.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "   "}, nil).Once()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		exec := resp["execution"].(map[string]any)
		assert.False(t, exec["applied"].(bool))
		assert.Contains(t, exec["message"].(string), "empty text")
	})

	t.Run("POST /wisdev/execute - Capability honors always_ask policy in Go", func(t *testing.T) {
		mllm.ExpectedCalls = nil
		mllm.Calls = nil
		body := `{"action":"test_capability","payload":{"toolInvocationPolicy":"always_ask"}}`
		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", strings.NewReader(body))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		exec := resp["execution"].(map[string]any)
		assert.False(t, exec["applied"].(bool))
		assert.True(t, exec["requiresConfirmation"].(bool))
		assert.Equal(t, "user_policy_confirmation_required", exec["guardrailReason"])
		assert.Equal(t, "Tool policy requires explicit approval before execution.", exec["message"])
		assert.Equal(t, []any{"approve", "skip", "edit_payload", "reject_and_replan"}, exec["nextActions"])
		mllm.AssertNotCalled(t, "Generate", mock.Anything, mock.Anything)
	})

	t.Run("POST /wisdev/execute - Capability succeeds for planless session", func(t *testing.T) {
		mllm.ExpectedCalls = nil
		mllm.Calls = nil
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		assert.Nil(t, session.Plan)

		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", strings.NewReader(`{"sessionId":"`+session.SessionID+`","action":"research.buildClaimEvidenceTable","payload":{"claims":[{"claim":"q1"}]}}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		exec := resp["execution"].(map[string]any)
		assert.False(t, exec["applied"].(bool))
		assert.True(t, exec["requiresConfirmation"].(bool))
		evidence := exec["evidence"].(map[string]any)
		assert.Equal(t, float64(1), evidence["claimCount"])
		assert.Equal(t, float64(0), evidence["linkedClaimCount"])
		assert.Equal(t, float64(1), evidence["unlinkedClaimCount"])

		updated, _ := gw.Store.Get(context.Background(), session.SessionID)
		assert.Nil(t, updated.Plan)
		mllm.AssertNotCalled(t, "Generate", mock.Anything, mock.Anything)
	})

	t.Run("POST /wisdev/execute - Claim evidence table is evaluated in Go without LLM", func(t *testing.T) {
		localGateway := &wisdev.AgentGateway{
			Store:        wisdev.NewInMemorySessionStore(),
			PolicyConfig: policy.DefaultPolicyConfig(),
			Executor:     &mockExecutionRunner{},
			SessionTTL:   time.Hour,
		}
		localMux := http.NewServeMux()
		RegisterWisDevRoutes(localMux, localGateway, nil, nil)

		session, _ := localGateway.CreateSession(context.Background(), "u1", "q1")
		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", strings.NewReader(`{"sessionId":"`+session.SessionID+`","action":"research.buildClaimEvidenceTable","payload":{"claims":[{"claim":"q1","source":"query_1"},{"claim":"q2","sourceIds":["paper-1"]}],"heuristicClaimCount":2,"heuristicLinkedClaimCount":2}}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
		rec := httptest.NewRecorder()

		localMux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		exec := resp["execution"].(map[string]any)
		assert.True(t, exec["applied"].(bool))
		assert.False(t, exec["requiresConfirmation"].(bool))
		evidence := exec["evidence"].(map[string]any)
		assert.Equal(t, float64(2), evidence["claimCount"])
		assert.Equal(t, float64(2), evidence["linkedClaimCount"])
		assert.Equal(t, float64(0), evidence["unlinkedClaimCount"])
	})

	t.Run("POST /wisdev/execute - Capability error", func(t *testing.T) {
		body := `{"action":"fail", "payload":{}}`
		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mllm.On("Generate", mock.Anything, mock.Anything).Return(nil, errors.New("llm fail")).Once()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.NotNil(t, resp["execution"])
		exec := resp["execution"].(map[string]any)
		assert.False(t, exec["applied"].(bool))
	})

	t.Run("POST /wisdev/execute - Plan step success", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		session.Plan = &wisdev.PlanState{
			PlanID: "p1",
			Steps: []wisdev.PlanStep{
				{ID: "s1", Action: "search"},
			},
			CompletedStepIDs: make(map[string]bool),
			FailedStepIDs:    make(map[string]string),
		}
		session.UpdatedAt = 1
		_ = gw.Store.Put(context.Background(), session, gw.SessionTTL)

		body := map[string]any{
			"sessionId": session.SessionID,
			"stepId":    "s1",
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", bytes.NewReader(jsonBody))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
		rec := httptest.NewRecorder()

		me.On("RunStepWithRecovery", mock.Anything, mock.Anything, mock.Anything, 1).Return(wisdev.StepResult{Step: session.Plan.Steps[0], LaneID: 1}).Once()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		updated, _ := gw.Store.Get(context.Background(), session.SessionID)
		assert.True(t, updated.Plan.CompletedStepIDs["s1"])
		assert.Equal(t, wisdev.SessionExecutingPlan, updated.Status)
		assert.Greater(t, updated.UpdatedAt, int64(1))
	})

	t.Run("POST /wisdev/execute - Plan step error", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		session.Plan = &wisdev.PlanState{
			PlanID: "p2",
			Steps: []wisdev.PlanStep{
				{ID: "s2", Action: "search"},
			},
			CompletedStepIDs: make(map[string]bool),
			FailedStepIDs:    make(map[string]string),
		}
		session.UpdatedAt = 1
		_ = gw.Store.Put(context.Background(), session, gw.SessionTTL)

		body := map[string]any{
			"sessionId": session.SessionID,
			"stepId":    "s2",
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", bytes.NewReader(jsonBody))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
		rec := httptest.NewRecorder()

		me.On("RunStepWithRecovery", mock.Anything, mock.Anything, mock.Anything, 1).Return(wisdev.StepResult{Err: errors.New("fail"), LaneID: 1}).Once()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		updated, _ := gw.Store.Get(context.Background(), session.SessionID)
		assert.Equal(t, "fail", updated.Plan.FailedStepIDs["s2"])
		assert.Equal(t, wisdev.SessionExecutingPlan, updated.Status)
		assert.Greater(t, updated.UpdatedAt, int64(1))
	})

	t.Run("POST /wisdev/execute - Plan step selected parallel steps execute together", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		session.Plan = &wisdev.PlanState{
			PlanID: "p-batch-success",
			Steps: []wisdev.PlanStep{
				{ID: "s4", Action: "search", Parallelizable: true, ParallelGroup: "retrieval"},
				{ID: "s5", Action: "search", Parallelizable: true, ParallelGroup: "retrieval"},
			},
			CompletedStepIDs: make(map[string]bool),
			FailedStepIDs:    make(map[string]string),
		}
		_ = gw.Store.Put(context.Background(), session, gw.SessionTTL)

		body := map[string]any{
			"sessionId": session.SessionID,
			"stepId":    "s4",
			"payload": map[string]any{
				"selectedParallelStepIds": []string{"s5"},
			},
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", bytes.NewReader(jsonBody))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
		rec := httptest.NewRecorder()

		me.On("RunStepWithRecovery", mock.Anything, mock.Anything, session.Plan.Steps[0], 1).Return(wisdev.StepResult{
			Step:   session.Plan.Steps[0],
			LaneID: 1,
		}).Once()
		me.On("RunStepWithRecovery", mock.Anything, mock.Anything, session.Plan.Steps[1], 2).Return(wisdev.StepResult{
			Step:   session.Plan.Steps[1],
			LaneID: 2,
		}).Once()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		exec := resp["execution"].(map[string]any)
		data := exec["data"].(map[string]any)
		assert.True(t, data["parallelExecution"].(bool))
		assert.Equal(t, float64(2), data["laneCount"].(float64))
		assert.ElementsMatch(t, []any{"s4", "s5"}, data["executedStepIds"].([]any))
		assert.ElementsMatch(t, []any{"s4", "s5"}, data["completedStepIds"].([]any))

		updated, _ := gw.Store.Get(context.Background(), session.SessionID)
		assert.True(t, updated.Plan.CompletedStepIDs["s4"])
		assert.True(t, updated.Plan.CompletedStepIDs["s5"])
	})

	t.Run("POST /wisdev/execute - Plan step selected parallel failure preserves completed peer", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		session.Plan = &wisdev.PlanState{
			PlanID: "p-batch-failure",
			Steps: []wisdev.PlanStep{
				{ID: "s6", Action: "search", Parallelizable: true, ParallelGroup: "retrieval"},
				{ID: "s7", Action: "search", Parallelizable: true, ParallelGroup: "retrieval"},
			},
			CompletedStepIDs: make(map[string]bool),
			FailedStepIDs:    make(map[string]string),
		}
		_ = gw.Store.Put(context.Background(), session, gw.SessionTTL)

		body := map[string]any{
			"sessionId": session.SessionID,
			"stepId":    "s6",
			"payload": map[string]any{
				"selectedParallelStepIds": []string{"s7"},
			},
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", bytes.NewReader(jsonBody))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
		rec := httptest.NewRecorder()

		me.On("RunStepWithRecovery", mock.Anything, mock.Anything, session.Plan.Steps[0], 1).Return(wisdev.StepResult{
			Step:   session.Plan.Steps[0],
			LaneID: 1,
		}).Once()
		me.On("RunStepWithRecovery", mock.Anything, mock.Anything, session.Plan.Steps[1], 2).Return(wisdev.StepResult{
			Step:   session.Plan.Steps[1],
			Err:    errors.New("peer failed"),
			LaneID: 2,
		}).Once()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		updated, _ := gw.Store.Get(context.Background(), session.SessionID)
		assert.True(t, updated.Plan.CompletedStepIDs["s6"])
		assert.Equal(t, "peer failed", updated.Plan.FailedStepIDs["s7"])
	})

	t.Run("POST /wisdev/execute - Plan step confirmation persists pending approval state", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		session.Plan = &wisdev.PlanState{
			PlanID: "p-batch-confirmation",
			Steps: []wisdev.PlanStep{
				{ID: "s8", Action: "search", Parallelizable: true, ParallelGroup: "retrieval"},
				{ID: "s9", Action: "search", Parallelizable: true, ParallelGroup: "retrieval"},
			},
			CompletedStepIDs: make(map[string]bool),
			FailedStepIDs:    make(map[string]string),
		}
		_ = gw.Store.Put(context.Background(), session, gw.SessionTTL)

		body := map[string]any{
			"sessionId": session.SessionID,
			"stepId":    "s8",
			"payload": map[string]any{
				"selectedParallelStepIds": []string{"s9"},
			},
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", bytes.NewReader(jsonBody))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
		rec := httptest.NewRecorder()

		me.On("RunStepWithRecovery", mock.Anything, mock.Anything, session.Plan.Steps[0], 1).Return(wisdev.StepResult{
			Step:   session.Plan.Steps[0],
			Err:    errors.New("CONFIRMATION_REQUIRED:review outbound request"),
			LaneID: 1,
		}).Once()
		me.On("RunStepWithRecovery", mock.Anything, mock.Anything, session.Plan.Steps[1], 2).Return(wisdev.StepResult{
			Step:   session.Plan.Steps[1],
			LaneID: 2,
		}).Once()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		exec := resp["execution"].(map[string]any)
		assert.False(t, exec["applied"].(bool))
		assert.True(t, exec["requiresConfirmation"].(bool))
		assert.Equal(t, "awaiting_confirmation", exec["status"])

		data := exec["data"].(map[string]any)
		assert.ElementsMatch(t, []any{"s8"}, data["confirmationRequiredStepIds"].([]any))
		reasons := data["confirmationRequiredReasons"].(map[string]any)
		assert.Equal(t, "review outbound request", reasons["s8"])
		assert.Equal(t, "s8", data["pendingApprovalStepId"])
		assert.ElementsMatch(t, []any{"s9"}, data["completedStepIds"].([]any))
		hitl := data["hitl"].(map[string]any)
		assert.Equal(t, "s8", hitl["stepId"])
		assert.NotEmpty(t, hitl["approvalId"])
		assert.NotEmpty(t, hitl["approvalToken"])

		updated, _ := gw.Store.Get(context.Background(), session.SessionID)
		assert.Equal(t, wisdev.SessionPaused, updated.Status)
		assert.True(t, updated.Plan.CompletedStepIDs["s9"])
		assert.False(t, updated.Plan.CompletedStepIDs["s8"])
		assert.Equal(t, "s8", updated.Plan.PendingApprovalStepID)
		assert.NotEmpty(t, updated.Plan.PendingApprovalID)
		assert.NotEmpty(t, updated.Plan.PendingApprovalTokenHash)
		assert.Positive(t, updated.Plan.PendingApprovalExpiresAt)
	})

	t.Run("POST /wisdev/execute - Plan step persistence failure returns server error", func(t *testing.T) {
		baseStore := wisdev.NewInMemorySessionStore()
		localExecutor := &mockExecutionRunner{}
		localGateway := &wisdev.AgentGateway{
			Store:        baseStore,
			PolicyConfig: policy.DefaultPolicyConfig(),
			Executor:     localExecutor,
			LLMClient:    lc,
			SessionTTL:   time.Hour,
		}
		localMux := http.NewServeMux()
		RegisterWisDevRoutes(localMux, localGateway, nil, nil)

		session, _ := localGateway.CreateSession(context.Background(), "u1", "q1")
		session.Plan = &wisdev.PlanState{
			PlanID: "p-store-fail",
			Steps: []wisdev.PlanStep{
				{ID: "s10", Action: "search"},
			},
			CompletedStepIDs: make(map[string]bool),
			FailedStepIDs:    make(map[string]string),
		}
		assert.NoError(t, baseStore.Put(context.Background(), session, localGateway.SessionTTL))
		localGateway.Store = &failingSessionStore{
			base:    baseStore,
			failPut: true,
			putErr:  errors.New("persist failed"),
		}

		body := map[string]any{
			"sessionId": session.SessionID,
			"stepId":    "s10",
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", bytes.NewReader(jsonBody))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
		rec := httptest.NewRecorder()

		localExecutor.On("RunStepWithRecovery", mock.Anything, mock.Anything, session.Plan.Steps[0], 1).Return(wisdev.StepResult{
			Step:   session.Plan.Steps[0],
			LaneID: 1,
		}).Once()

		localMux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrWisdevFailed, resp.Error.Code)
		assert.Contains(t, resp.Error.Message, "failed to persist session")
	})

	t.Run("POST /wisdev/execute - Plan step recovered panic becomes tracked failure", func(t *testing.T) {
		localGateway := &wisdev.AgentGateway{
			Store:        wisdev.NewInMemorySessionStore(),
			PolicyConfig: policy.DefaultPolicyConfig(),
			Executor:     &panicExecutionRunner{panicStepID: "s11"},
			LLMClient:    lc,
			SessionTTL:   time.Hour,
		}
		localMux := http.NewServeMux()
		RegisterWisDevRoutes(localMux, localGateway, nil, nil)

		session, _ := localGateway.CreateSession(context.Background(), "u1", "q1")
		session.Plan = &wisdev.PlanState{
			PlanID: "p-panic",
			Steps: []wisdev.PlanStep{
				{ID: "s11", Action: "search"},
			},
			CompletedStepIDs: make(map[string]bool),
			FailedStepIDs:    make(map[string]string),
		}
		assert.NoError(t, localGateway.Store.Put(context.Background(), session, localGateway.SessionTTL))

		body := map[string]any{
			"sessionId": session.SessionID,
			"stepId":    "s11",
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", bytes.NewReader(jsonBody))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
		rec := httptest.NewRecorder()

		localMux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		updated, _ := localGateway.Store.Get(context.Background(), session.SessionID)
		assert.Contains(t, updated.Plan.FailedStepIDs["s11"], "step execution panic: boom")
	})

	t.Run("POST /wisdev/execute - Plan step requires configured executor", func(t *testing.T) {
		localGateway := &wisdev.AgentGateway{
			Store:        wisdev.NewInMemorySessionStore(),
			PolicyConfig: policy.DefaultPolicyConfig(),
			LLMClient:    lc,
			SessionTTL:   time.Hour,
		}
		localMux := http.NewServeMux()
		RegisterWisDevRoutes(localMux, localGateway, nil, nil)

		session, _ := localGateway.CreateSession(context.Background(), "u1", "q1")
		session.Plan = &wisdev.PlanState{
			PlanID: "p-no-executor",
			Steps: []wisdev.PlanStep{
				{ID: "s12", Action: "search"},
			},
			CompletedStepIDs: make(map[string]bool),
			FailedStepIDs:    make(map[string]string),
		}
		assert.NoError(t, localGateway.Store.Put(context.Background(), session, localGateway.SessionTTL))

		body := map[string]any{
			"sessionId": session.SessionID,
			"stepId":    "s12",
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", bytes.NewReader(jsonBody))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
		rec := httptest.NewRecorder()

		localMux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrWisdevFailed, resp.Error.Code)
		assert.Contains(t, resp.Error.Message, "execution runtime unavailable")

		updated, _ := localGateway.Store.Get(context.Background(), session.SessionID)
		assert.Empty(t, updated.Plan.FailedStepIDs["s12"])
	})

	t.Run("POST /wisdev/execute - Capability conflicts with pending approval", func(t *testing.T) {
		mllm.ExpectedCalls = nil
		mllm.Calls = nil
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		session.Status = wisdev.SessionPaused
		session.Plan = &wisdev.PlanState{
			PlanID: "p-capability-pending",
			Steps: []wisdev.PlanStep{
				{ID: "s13", Action: "search"},
			},
			CompletedStepIDs:         make(map[string]bool),
			FailedStepIDs:            make(map[string]string),
			PendingApprovalID:        "approval-capability",
			PendingApprovalStepID:    "s13",
			PendingApprovalTokenHash: wisdev.HashApprovalToken("tok-capability"),
			PendingApprovalExpiresAt: wisdev.NowMillis() + 60_000,
		}
		assert.NoError(t, gw.Store.Put(context.Background(), session, gw.SessionTTL))

		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", strings.NewReader(`{"sessionId":"`+session.SessionID+`","action":"test_capability","payload":{}}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusConflict, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrConflict, resp.Error.Code)
		assert.Contains(t, resp.Error.Message, "pending approval")
		mllm.AssertNotCalled(t, "Generate", mock.Anything, mock.Anything)

		updated, _ := gw.Store.Get(context.Background(), session.SessionID)
		assert.Equal(t, "approval-capability", updated.Plan.PendingApprovalID)
		assert.Equal(t, "s13", updated.Plan.PendingApprovalStepID)
	})

	t.Run("POST /wisdev/execute - Capability clears expired pending approval before execution", func(t *testing.T) {
		mllm.ExpectedCalls = nil
		mllm.Calls = nil
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		session.Status = wisdev.SessionPaused
		session.Plan = &wisdev.PlanState{
			PlanID: "p-capability-expired",
			Steps: []wisdev.PlanStep{
				{ID: "s13e", Action: "search"},
			},
			CompletedStepIDs:         make(map[string]bool),
			FailedStepIDs:            make(map[string]string),
			PendingApprovalID:        "approval-capability-expired",
			PendingApprovalStepID:    "s13e",
			PendingApprovalTokenHash: wisdev.HashApprovalToken("tok-capability-expired"),
			PendingApprovalExpiresAt: wisdev.NowMillis() - 1,
		}
		assert.NoError(t, gw.Store.Put(context.Background(), session, gw.SessionTTL))

		mllm.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "executed"}, nil).Once()

		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", strings.NewReader(`{"sessionId":"`+session.SessionID+`","action":"test_capability","payload":{}}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		updated, _ := gw.Store.Get(context.Background(), session.SessionID)
		assert.Empty(t, updated.Plan.PendingApprovalID)
		assert.Empty(t, updated.Plan.PendingApprovalStepID)
		assert.Equal(t, wisdev.SessionPaused, updated.Status)
	})

	t.Run("POST /wisdev/execute - Plan step conflicts with pending approval", func(t *testing.T) {
		me.ExpectedCalls = nil
		me.Calls = nil
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		session.Status = wisdev.SessionPaused
		session.Plan = &wisdev.PlanState{
			PlanID: "p-step-pending",
			Steps: []wisdev.PlanStep{
				{ID: "s14", Action: "search"},
			},
			CompletedStepIDs:         make(map[string]bool),
			FailedStepIDs:            make(map[string]string),
			PendingApprovalID:        "approval-step",
			PendingApprovalStepID:    "s14",
			PendingApprovalTokenHash: wisdev.HashApprovalToken("tok-step"),
			PendingApprovalExpiresAt: wisdev.NowMillis() + 60_000,
		}
		assert.NoError(t, gw.Store.Put(context.Background(), session, gw.SessionTTL))

		body := map[string]any{
			"sessionId": session.SessionID,
			"stepId":    "s14",
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", bytes.NewReader(jsonBody))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusConflict, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrConflict, resp.Error.Code)
		assert.Contains(t, resp.Error.Message, "pending approval")
		me.AssertNotCalled(t, "RunStepWithRecovery", mock.Anything, mock.Anything, mock.Anything, mock.Anything)

		updated, _ := gw.Store.Get(context.Background(), session.SessionID)
		assert.Equal(t, "approval-step", updated.Plan.PendingApprovalID)
		assert.Equal(t, "s14", updated.Plan.PendingApprovalStepID)
		assert.Equal(t, wisdev.SessionPaused, updated.Status)
	})

	t.Run("POST /wisdev/execute - Plan step clears expired pending approval before execution", func(t *testing.T) {
		me.ExpectedCalls = nil
		me.Calls = nil
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		session.Status = wisdev.SessionPaused
		session.Plan = &wisdev.PlanState{
			PlanID: "p-step-expired",
			Steps: []wisdev.PlanStep{
				{ID: "s14e", Action: "search"},
			},
			CompletedStepIDs:         make(map[string]bool),
			FailedStepIDs:            make(map[string]string),
			PendingApprovalID:        "approval-step-expired",
			PendingApprovalStepID:    "s14e",
			PendingApprovalTokenHash: wisdev.HashApprovalToken("tok-step-expired"),
			PendingApprovalExpiresAt: wisdev.NowMillis() - 1,
		}
		assert.NoError(t, gw.Store.Put(context.Background(), session, gw.SessionTTL))

		body := map[string]any{
			"sessionId": session.SessionID,
			"stepId":    "s14e",
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", bytes.NewReader(jsonBody))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
		rec := httptest.NewRecorder()

		me.On("RunStepWithRecovery", mock.Anything, mock.Anything, session.Plan.Steps[0], 1).Return(wisdev.StepResult{
			Step:   session.Plan.Steps[0],
			LaneID: 1,
		}).Once()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		updated, _ := gw.Store.Get(context.Background(), session.SessionID)
		assert.Empty(t, updated.Plan.PendingApprovalID)
		assert.Empty(t, updated.Plan.PendingApprovalStepID)
		assert.True(t, updated.Plan.CompletedStepIDs["s14e"])
		assert.Equal(t, wisdev.SessionExecutingPlan, updated.Status)
	})

	t.Run("POST /wisdev/execute - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", strings.NewReader(`{bad`))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /wisdev/execute - missing session", func(t *testing.T) {
		body := map[string]any{
			"sessionId": "missing",
			"stepId":    "s1",
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", bytes.NewReader(jsonBody))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("POST /wisdev/execute - nil session from store is not found", func(t *testing.T) {
		localGateway := &wisdev.AgentGateway{
			Store:        &nilSessionStore{},
			PolicyConfig: policy.DefaultPolicyConfig(),
			Executor:     &mockExecutionRunner{},
			LLMClient:    llm.NewClient(),
			SessionTTL:   time.Hour,
		}
		localMux := http.NewServeMux()
		RegisterWisDevRoutes(localMux, localGateway, nil, nil)

		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", strings.NewReader(`{"sessionId":"nil-session","action":"test_capability","payload":{}}`))
		rec := httptest.NewRecorder()

		localMux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("POST /wisdev/execute - Plan step forbids non-owner", func(t *testing.T) {
		localExecutor := &mockExecutionRunner{}
		localGateway := &wisdev.AgentGateway{
			Store:        wisdev.NewInMemorySessionStore(),
			PolicyConfig: policy.DefaultPolicyConfig(),
			Executor:     localExecutor,
			LLMClient:    lc,
			SessionTTL:   time.Hour,
		}
		localMux := http.NewServeMux()
		RegisterWisDevRoutes(localMux, localGateway, nil, nil)

		session, _ := localGateway.CreateSession(context.Background(), "u1", "q1")
		session.Plan = &wisdev.PlanState{
			PlanID: "p3",
			Steps: []wisdev.PlanStep{
				{ID: "s3", Action: "search"},
			},
			CompletedStepIDs: make(map[string]bool),
			FailedStepIDs:    make(map[string]string),
		}
		_ = localGateway.Store.Put(context.Background(), session, localGateway.SessionTTL)

		body := map[string]any{
			"sessionId": session.SessionID,
			"stepId":    "s3",
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", bytes.NewReader(jsonBody))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u2"))
		rec := httptest.NewRecorder()

		localMux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrUnauthorized, resp.Error.Code)
		localExecutor.AssertNotCalled(t, "RunStepWithRecovery", mock.Anything, mock.Anything, mock.Anything, 0)
	})

	t.Run("POST /wisdev/execute - Capability requires configured llm client", func(t *testing.T) {
		localGateway := &wisdev.AgentGateway{
			Store:        wisdev.NewInMemorySessionStore(),
			PolicyConfig: policy.DefaultPolicyConfig(),
			Executor:     &mockExecutionRunner{},
			SessionTTL:   time.Hour,
		}
		localMux := http.NewServeMux()
		RegisterWisDevRoutes(localMux, localGateway, nil, nil)

		req := httptest.NewRequest(http.MethodPost, "/wisdev/execute", strings.NewReader(`{"action":"test_capability","payload":{}}`))
		rec := httptest.NewRecorder()

		localMux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrWisdevFailed, resp.Error.Code)
	})
}
