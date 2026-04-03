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
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"
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
func (m *mockLLM) GenerateImages(ctx context.Context, in *llmv1.GenerateImagesRequest, opts ...grpc.CallOption) (*llmv1.GenerateImagesResponse, error) {
	return &llmv1.GenerateImagesResponse{}, nil
}

func TestWisDevV2_ExecuteHandlers(t *testing.T) {
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
	RegisterV2WisDevRoutes(mux, gw, nil)

	t.Run("POST /v2/wisdev/execute - Capability success", func(t *testing.T) {
		body := `{"action":"test_capability", "payload":{"key":"val"}}`
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/execute", strings.NewReader(body))
		rec := httptest.NewRecorder()

		mllm.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "output"}, nil).Once()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.NotNil(t, resp["execution"])
	})

	t.Run("POST /v2/wisdev/execute - Capability error", func(t *testing.T) {
		body := `{"action":"fail", "payload":{}}`
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/execute", strings.NewReader(body))
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

	t.Run("POST /v2/wisdev/execute - Plan step success", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		session.Plan = &wisdev.PlanState{
			PlanID: "p1",
			Steps: []wisdev.PlanStep{
				{ID: "s1", Action: "search"},
			},
			CompletedStepIDs: make(map[string]bool),
			FailedStepIDs:    make(map[string]string),
		}
		_ = gw.Store.Put(context.Background(), session, gw.SessionTTL)

		body := map[string]any{
			"sessionId": session.SessionID,
			"stepId":    "s1",
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/execute", bytes.NewReader(jsonBody))
		rec := httptest.NewRecorder()

		me.On("RunStepWithRecovery", mock.Anything, mock.Anything, mock.Anything, 0).Return(wisdev.StepResult{Step: session.Plan.Steps[0]}).Once()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		updated, _ := gw.Store.Get(context.Background(), session.SessionID)
		assert.True(t, updated.Plan.CompletedStepIDs["s1"])
	})

	t.Run("POST /v2/wisdev/execute - Plan step error", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		session.Plan = &wisdev.PlanState{
			PlanID: "p2",
			Steps: []wisdev.PlanStep{
				{ID: "s2", Action: "search"},
			},
			CompletedStepIDs: make(map[string]bool),
			FailedStepIDs:    make(map[string]string),
		}
		_ = gw.Store.Put(context.Background(), session, gw.SessionTTL)

		body := map[string]any{
			"sessionId": session.SessionID,
			"stepId":    "s2",
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/execute", bytes.NewReader(jsonBody))
		rec := httptest.NewRecorder()

		me.On("RunStepWithRecovery", mock.Anything, mock.Anything, mock.Anything, 0).Return(wisdev.StepResult{Err: errors.New("fail")}).Once()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("POST /v2/wisdev/execute - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/execute", strings.NewReader(`{bad`))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /v2/wisdev/execute - missing session", func(t *testing.T) {
		body := map[string]any{
			"sessionId": "missing",
			"stepId":    "s1",
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/execute", bytes.NewReader(jsonBody))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})
}
