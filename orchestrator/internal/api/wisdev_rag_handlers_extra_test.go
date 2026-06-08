package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/rag"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type mockEngine struct {
	mock.Mock
}

func (m *mockEngine) GenerateAnswer(ctx context.Context, req rag.AnswerRequest) (*rag.AnswerResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*rag.AnswerResponse), args.Error(1)
}
func (m *mockEngine) MultiAgentExecute(ctx context.Context, req rag.AnswerRequest) (*rag.AnswerResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*rag.AnswerResponse), args.Error(1)
}
func (m *mockEngine) SelectSectionContext(ctx context.Context, req rag.SectionContextRequest) (*rag.SectionContextResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*rag.SectionContextResponse), args.Error(1)
}
func (m *mockEngine) GetRaptor() *rag.RaptorService {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*rag.RaptorService)
}
func (m *mockEngine) GetBM25() *rag.BM25 {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*rag.BM25)
}
func (m *mockEngine) GetEngine() *rag.Engine { return nil }

func TestRAGHandlers_DirectCalls(t *testing.T) {
	engine := new(mockEngine)
	h := NewRAGHandler(engine)

	t.Run("HandleAnswer", func(t *testing.T) {
		body := `{"query":"test","context":"ctx"}`
		req := httptest.NewRequest(http.MethodPost, "/rag/answer", strings.NewReader(body))
		rec := httptest.NewRecorder()

		engine.On("GenerateAnswer", mock.Anything, mock.Anything).Return(&rag.AnswerResponse{Answer: "ans"}, nil).Once()

		h.HandleAnswer(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleSectionContext", func(t *testing.T) {
		body := `{"sectionName":"intro","fullText":"content"}`
		req := httptest.NewRequest(http.MethodPost, "/rag/section-context", strings.NewReader(body))
		rec := httptest.NewRecorder()

		engine.On("SelectSectionContext", mock.Anything, mock.Anything).Return(&rag.SectionContextResponse{SectionName: "intro"}, nil).Once()

		h.HandleSectionContext(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleRaptorBuild - No Engine", func(t *testing.T) {
		body := `{"documents":[]}`
		req := httptest.NewRequest(http.MethodPost, "/rag/raptor/build", strings.NewReader(body))
		rec := httptest.NewRecorder()

		engine.On("GetRaptor").Return(nil).Once()

		h.HandleRaptorBuild(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}
