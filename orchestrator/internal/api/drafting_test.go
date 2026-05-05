package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

type mockLLMStream struct {
	llmv1.LLMService_GenerateStreamClient
	chunks []*llmv1.GenerateChunk
	idx    int
	err    error
}

func (m *mockLLMStream) Recv() (*llmv1.GenerateChunk, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.idx >= len(m.chunks) {
		return nil, io.EOF
	}
	c := m.chunks[m.idx]
	m.idx++
	return c, nil
}

func TestWisDevServer_HandleManuscriptDraft(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)

	gateway := &wisdev.AgentGateway{
		LLMClient: client,
	}
	s := &wisdevServer{gateway: gateway}

	reqBody := `{"title":"Test Paper","findings":["Finding 1"],"traceId":"trace-draft-1"}`
	req := httptest.NewRequest(http.MethodPost, "/manuscript/draft", bytes.NewBufferString(reqBody))
	req = withTestUserID(req, "u1")
	w := httptest.NewRecorder()

	mockLLM.On("Generate", mock.Anything, mock.MatchedBy(func(in *llmv1.GenerateRequest) bool {
		return in != nil &&
			in.Metadata["trace_id"] == "trace-draft-1" &&
			in.Model == llm.ResolveHeavyModel() &&
			in.RequestClass == "heavy" &&
			in.ServiceTier == "priority" &&
			in.RetryProfile == "standard" &&
			in.GetThinkingBudget() == 8192
	})).Return(&llmv1.GenerateResponse{Text: "Draft content"}, nil)

	s.HandleManuscriptDraft(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "trace-draft-1", w.Header().Get("X-Trace-Id"))
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	assert.True(t, resp["ok"].(bool))
	assert.Equal(t, "trace-draft-1", resp["traceId"])
	data := resp["manuscriptDraft"].(map[string]any)
	assert.Equal(t, "Draft content", data["content"])
	assert.Equal(t, "trace-draft-1", data["traceId"])
	assert.Equal(t, "trace-draft-1", data["trace_id"])
}

func TestWisDevServer_HandleReviewerRebuttal(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)

	gateway := &wisdev.AgentGateway{
		LLMClient: client,
	}
	s := &wisdevServer{gateway: gateway}

	reqBody := `{"reviewer_comments":["Comment 1"],"paper_text":"Original text","traceId":"trace-rebuttal-1"}`
	req := httptest.NewRequest(http.MethodPost, "/reviewer/rebuttal", bytes.NewBufferString(reqBody))
	req = withTestUserID(req, "u1")
	w := httptest.NewRecorder()

	mockLLM.On("Generate", mock.Anything, mock.MatchedBy(func(in *llmv1.GenerateRequest) bool {
		return in != nil &&
			in.Metadata["trace_id"] == "trace-rebuttal-1" &&
			in.Model == llm.ResolveHeavyModel() &&
			in.RequestClass == "heavy" &&
			in.ServiceTier == "priority" &&
			in.RetryProfile == "standard" &&
			in.GetThinkingBudget() == 8192
	})).Return(&llmv1.GenerateResponse{Text: "Rebuttal response"}, nil)

	s.HandleReviewerRebuttal(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "trace-rebuttal-1", w.Header().Get("X-Trace-Id"))
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	assert.True(t, resp["ok"].(bool))
	assert.Equal(t, "trace-rebuttal-1", resp["traceId"])
	data := resp["reviewerRebuttal"].(map[string]any)
	assert.Equal(t, "Rebuttal response", data["rebuttal_text"])
	assert.Equal(t, "trace-rebuttal-1", data["traceId"])
	assert.Equal(t, "trace-rebuttal-1", data["trace_id"])
}

func TestWisDevServer_HandleManuscriptDraftStream(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)

	gateway := &wisdev.AgentGateway{
		LLMClient: client,
	}
	s := &wisdevServer{gateway: gateway}

	reqBody := `{"title":"Test Paper","findings":["Finding 1"],"traceId":"trace-draft-stream-1"}`
	req := httptest.NewRequest(http.MethodPost, "/manuscript/draft/stream", bytes.NewBufferString(reqBody))
	req = withTestUserID(req, "u1")
	w := httptest.NewRecorder()

	stream := &mockLLMStream{
		chunks: []*llmv1.GenerateChunk{
			{Delta: "Paragraph 1\n\n"},
			{Delta: "Paragraph 2"},
		},
	}
	mockLLM.On("GenerateStream", mock.Anything, mock.MatchedBy(func(in *llmv1.GenerateRequest) bool {
		return in != nil &&
			in.Metadata["trace_id"] == "trace-draft-stream-1" &&
			in.Model == llm.ResolveHeavyModel() &&
			in.RequestClass == "heavy" &&
			in.ServiceTier == "priority" &&
			in.RetryProfile == "standard" &&
			in.GetThinkingBudget() == 8192
	})).Return(stream, nil)

	s.HandleManuscriptDraftStream(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "trace-draft-stream-1", w.Header().Get("X-Trace-Id"))
	assert.Contains(t, w.Body.String(), "event: chunk")
	assert.Contains(t, w.Body.String(), "Paragraph 1")
	assert.Contains(t, w.Body.String(), `"traceId":"trace-draft-stream-1"`)
	assert.Contains(t, w.Body.String(), `"trace_id":"trace-draft-stream-1"`)
}

func TestWisDevServer_HandleReviewerRebuttalStream(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)

	gateway := &wisdev.AgentGateway{
		LLMClient: client,
	}
	s := &wisdevServer{gateway: gateway}

	reqBody := `{"reviewer_comments":["Comment 1"],"paper_text":"Original text","traceId":"trace-rebuttal-stream-1"}`
	req := httptest.NewRequest(http.MethodPost, "/reviewer/rebuttal/stream", bytes.NewBufferString(reqBody))
	req = withTestUserID(req, "u1")
	w := httptest.NewRecorder()

	stream := &mockLLMStream{
		chunks: []*llmv1.GenerateChunk{
			{Delta: "Response 1\n\n"},
			{Delta: "Response 2"},
		},
	}
	mockLLM.On("GenerateStream", mock.Anything, mock.MatchedBy(func(in *llmv1.GenerateRequest) bool {
		return in != nil &&
			in.Metadata["trace_id"] == "trace-rebuttal-stream-1" &&
			in.Model == llm.ResolveHeavyModel() &&
			in.RequestClass == "heavy" &&
			in.ServiceTier == "priority" &&
			in.RetryProfile == "standard" &&
			in.GetThinkingBudget() == 8192
	})).Return(stream, nil)

	s.HandleReviewerRebuttalStream(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "trace-rebuttal-stream-1", w.Header().Get("X-Trace-Id"))
	assert.Contains(t, w.Body.String(), "event: chunk")
	assert.Contains(t, w.Body.String(), "Response 1")
	assert.Contains(t, w.Body.String(), `"traceId":"trace-rebuttal-stream-1"`)
	assert.Contains(t, w.Body.String(), `"trace_id":"trace-rebuttal-stream-1"`)
}

func TestWisDevServer_HandleManuscriptDraft_AcceptsLegacyTraceID(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)

	gateway := &wisdev.AgentGateway{
		LLMClient: client,
	}
	s := &wisdevServer{gateway: gateway}

	reqBody := `{"title":"Legacy Trace","findings":["Finding 1"],"trace_id":"trace-draft-legacy-1"}`
	req := httptest.NewRequest(http.MethodPost, "/manuscript/draft", bytes.NewBufferString(reqBody))
	req = withTestUserID(req, "u1")
	w := httptest.NewRecorder()

	mockLLM.On("Generate", mock.Anything, mock.MatchedBy(func(in *llmv1.GenerateRequest) bool {
		return in != nil &&
			in.Metadata["trace_id"] == "trace-draft-legacy-1" &&
			in.Model == llm.ResolveHeavyModel() &&
			in.RequestClass == "heavy" &&
			in.ServiceTier == "priority" &&
			in.RetryProfile == "standard" &&
			in.GetThinkingBudget() == 8192
	})).Return(&llmv1.GenerateResponse{Text: "Draft content"}, nil)

	s.HandleManuscriptDraft(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "trace-draft-legacy-1", w.Header().Get("X-Trace-Id"))
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "trace-draft-legacy-1", resp["traceId"])
	data := resp["manuscriptDraft"].(map[string]any)
	assert.Equal(t, "trace-draft-legacy-1", data["traceId"])
	assert.Equal(t, "trace-draft-legacy-1", data["trace_id"])
}

func TestWisDevServer_HandleManuscriptDraft_RequiresAuth(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)

	gateway := &wisdev.AgentGateway{
		LLMClient: client,
	}
	s := &wisdevServer{gateway: gateway}

	req := httptest.NewRequest(http.MethodPost, "/manuscript/draft", bytes.NewBufferString(`{"title":"Test Paper","findings":["Finding 1"]}`))
	w := httptest.NewRecorder()

	s.HandleManuscriptDraft(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	mockLLM.AssertNotCalled(t, "Generate", mock.Anything, mock.Anything)
}

func TestWisDevServer_HandleReviewerRebuttal_RequiresAuth(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)

	gateway := &wisdev.AgentGateway{
		LLMClient: client,
	}
	s := &wisdevServer{gateway: gateway}

	req := httptest.NewRequest(http.MethodPost, "/reviewer/rebuttal", bytes.NewBufferString(`{"reviewer_comments":["Comment 1"],"paper_text":"Original text"}`))
	w := httptest.NewRecorder()

	s.HandleReviewerRebuttal(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	mockLLM.AssertNotCalled(t, "Generate", mock.Anything, mock.Anything)
}

func TestWisDevServer_HandleDraftingRejectsEmptyModelOutput(t *testing.T) {
	t.Run("manuscript draft empty response", func(t *testing.T) {
		mockLLM := new(mockLLMServiceClient)
		client := llm.NewClient()
		client.SetClient(mockLLM)
		s := &wisdevServer{gateway: &wisdev.AgentGateway{LLMClient: client}}

		req := httptest.NewRequest(http.MethodPost, "/manuscript/draft", bytes.NewBufferString(`{"title":"Test Paper","findings":["Finding 1"],"traceId":"trace-draft-empty-1"}`))
		req = withTestUserID(req, "u1")
		w := httptest.NewRecorder()

		mockLLM.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "   "}, nil).Once()

		s.HandleManuscriptDraft(w, req)
		assert.Equal(t, http.StatusBadGateway, w.Code)
	})

	t.Run("reviewer rebuttal empty response", func(t *testing.T) {
		mockLLM := new(mockLLMServiceClient)
		client := llm.NewClient()
		client.SetClient(mockLLM)
		s := &wisdevServer{gateway: &wisdev.AgentGateway{LLMClient: client}}

		req := httptest.NewRequest(http.MethodPost, "/reviewer/rebuttal", bytes.NewBufferString(`{"reviewer_comments":["Comment 1"],"paper_text":"Original text","traceId":"trace-rebuttal-empty-1"}`))
		req = withTestUserID(req, "u1")
		w := httptest.NewRecorder()

		mockLLM.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: ""}, nil).Once()

		s.HandleReviewerRebuttal(w, req)
		assert.Equal(t, http.StatusBadGateway, w.Code)
	})
}

func TestWisDevServer_HandleDraftingTimeoutFailsQuickly(t *testing.T) {
	previousTimeout := draftingLLMRequestTimeout
	draftingLLMRequestTimeout = 50 * time.Millisecond
	defer func() { draftingLLMRequestTimeout = previousTimeout }()

	t.Run("manuscript draft timeout", func(t *testing.T) {
		mockLLM := new(mockLLMServiceClient)
		client := llm.NewClient()
		client.SetClient(mockLLM)
		s := &wisdevServer{gateway: &wisdev.AgentGateway{LLMClient: client}}

		req := httptest.NewRequest(http.MethodPost, "/manuscript/draft", bytes.NewBufferString(`{"title":"Test Paper","findings":["Finding 1"],"traceId":"trace-draft-timeout-1"}`))
		req = withTestUserID(req, "u1")
		w := httptest.NewRecorder()

		mockLLM.On("Generate", mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) {
				ctx, ok := args.Get(0).(context.Context)
				if !ok {
					t.Fatalf("expected context argument, got %T", args.Get(0))
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("expected manuscript draft call to carry a deadline")
				}
				select {
				case <-ctx.Done():
					if ctx.Err() != context.DeadlineExceeded {
						t.Fatalf("expected deadline exceeded, got %v", ctx.Err())
					}
				case <-time.After(1 * time.Second):
					t.Fatal("expected manuscript draft context cancellation")
				}
			}).
			Return(nil, context.DeadlineExceeded).
			Once()

		startedAt := time.Now()
		s.HandleManuscriptDraft(w, req)

		assert.Equal(t, http.StatusBadGateway, w.Code)
		assert.Less(t, time.Since(startedAt), time.Second)
	})

	t.Run("reviewer rebuttal timeout", func(t *testing.T) {
		mockLLM := new(mockLLMServiceClient)
		client := llm.NewClient()
		client.SetClient(mockLLM)
		s := &wisdevServer{gateway: &wisdev.AgentGateway{LLMClient: client}}

		req := httptest.NewRequest(http.MethodPost, "/reviewer/rebuttal", bytes.NewBufferString(`{"reviewer_comments":["Comment 1"],"paper_text":"Original text","traceId":"trace-rebuttal-timeout-1"}`))
		req = withTestUserID(req, "u1")
		w := httptest.NewRecorder()

		mockLLM.On("Generate", mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) {
				ctx, ok := args.Get(0).(context.Context)
				if !ok {
					t.Fatalf("expected context argument, got %T", args.Get(0))
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("expected reviewer rebuttal call to carry a deadline")
				}
				select {
				case <-ctx.Done():
					if ctx.Err() != context.DeadlineExceeded {
						t.Fatalf("expected deadline exceeded, got %v", ctx.Err())
					}
				case <-time.After(1 * time.Second):
					t.Fatal("expected reviewer rebuttal context cancellation")
				}
			}).
			Return(nil, context.DeadlineExceeded).
			Once()

		startedAt := time.Now()
		s.HandleReviewerRebuttal(w, req)

		assert.Equal(t, http.StatusBadGateway, w.Code)
		assert.Less(t, time.Since(startedAt), time.Second)
	})
}
