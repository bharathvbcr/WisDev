package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/rag"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/resilience"

	"github.com/stretchr/testify/assert"
)

type flexibleEngine struct {
	genAnswerFn  func(context.Context, rag.AnswerRequest) (*rag.AnswerResponse, error)
	multiAgentFn func(context.Context, rag.AnswerRequest) (*rag.AnswerResponse, error)
	selectCtxFn  func(context.Context, rag.SectionContextRequest) (*rag.SectionContextResponse, error)
}

func (f *flexibleEngine) GenerateAnswer(ctx context.Context, r rag.AnswerRequest) (*rag.AnswerResponse, error) {
	return f.genAnswerFn(ctx, r)
}
func (f *flexibleEngine) MultiAgentExecute(ctx context.Context, r rag.AnswerRequest) (*rag.AnswerResponse, error) {
	return f.multiAgentFn(ctx, r)
}
func (f *flexibleEngine) SelectSectionContext(ctx context.Context, r rag.SectionContextRequest) (*rag.SectionContextResponse, error) {
	return f.selectCtxFn(ctx, r)
}
func (f *flexibleEngine) GetBM25() *rag.BM25        { return nil }
func (f *flexibleEngine) GetRaptor() *rag.RaptorService { return nil }

func TestRAGHandler(t *testing.T) {
	fe := &flexibleEngine{}
	h := NewRAGHandler(fe)

	t.Run("HandleAnswer - Degraded", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/answer", nil)
		req = req.WithContext(resilience.SetDegraded(req.Context(), true))
		rec := httptest.NewRecorder()
		h.HandleAnswer(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("HandleAnswer - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/answer", nil)
		rec := httptest.NewRecorder()
		h.HandleAnswer(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleAnswer - Success", func(t *testing.T) {
		body := `{"query":"test"}`
		req := httptest.NewRequest(http.MethodPost, "/answer", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()

		fe.genAnswerFn = func(ctx context.Context, r rag.AnswerRequest) (*rag.AnswerResponse, error) {
			return &rag.AnswerResponse{Answer: "done"}, nil
		}

		h.HandleAnswer(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp rag.AnswerResponse
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Equal(t, "done", resp.Answer)
		assert.NotEmpty(t, resp.TraceID)
		assert.NotNil(t, resp.Metadata)
	})

	t.Run("HandleAnswer - Invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/answer", bytes.NewReader([]byte("{invalid")))
		rec := httptest.NewRecorder()
		h.HandleAnswer(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleAnswer - Missing Query", func(t *testing.T) {
		body := `{"query":""}`
		req := httptest.NewRequest(http.MethodPost, "/answer", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		h.HandleAnswer(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("HandleAnswer - Engine Error", func(t *testing.T) {
		body := `{"query":"test"}`
		req := httptest.NewRequest(http.MethodPost, "/answer", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		fe.genAnswerFn = func(ctx context.Context, r rag.AnswerRequest) (*rag.AnswerResponse, error) {
			return nil, errors.New("engine fail")
		}
		h.HandleAnswer(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrRagFailed, resp.Error.Code)
	})

	t.Run("HandleMultiAgent - Success", func(t *testing.T) {
		body := `{"query":"test"}`
		req := httptest.NewRequest(http.MethodPost, "/multi", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		fe.multiAgentFn = func(ctx context.Context, r rag.AnswerRequest) (*rag.AnswerResponse, error) {
			return &rag.AnswerResponse{Answer: "multi done"}, nil
		}
		h.HandleMultiAgent(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleMultiAgent - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/multi", nil)
		rec := httptest.NewRecorder()
		h.HandleMultiAgent(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("HandleMultiAgent - Degraded", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/multi", nil)
		req = req.WithContext(resilience.SetDegraded(req.Context(), true))
		rec := httptest.NewRecorder()
		h.HandleMultiAgent(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("HandleMultiAgent - Invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/multi", bytes.NewReader([]byte("{invalid")))
		rec := httptest.NewRecorder()
		h.HandleMultiAgent(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("HandleMultiAgent - Engine Error", func(t *testing.T) {
		body := `{"query":"test"}`
		req := httptest.NewRequest(http.MethodPost, "/multi", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		fe.multiAgentFn = func(ctx context.Context, r rag.AnswerRequest) (*rag.AnswerResponse, error) {
			return nil, errors.New("multi fail")
		}
		h.HandleMultiAgent(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("HandleSectionContext - Success", func(t *testing.T) {
		body := `{"sectionName":"S1"}`
		req := httptest.NewRequest(http.MethodPost, "/section", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		fe.selectCtxFn = func(ctx context.Context, r rag.SectionContextRequest) (*rag.SectionContextResponse, error) {
			return &rag.SectionContextResponse{SectionName: "S1"}, nil
		}
		h.HandleSectionContext(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleSectionContext - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/section", nil)
		rec := httptest.NewRecorder()
		h.HandleSectionContext(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("HandleSectionContext - Degraded", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/section", nil)
		req = req.WithContext(resilience.SetDegraded(req.Context(), true))
		rec := httptest.NewRecorder()
		h.HandleSectionContext(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("HandleSectionContext - Invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/section", bytes.NewReader([]byte("{invalid")))
		rec := httptest.NewRecorder()
		h.HandleSectionContext(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("HandleSectionContext - Engine Error", func(t *testing.T) {
		body := `{"sectionName":"S1"}`
		req := httptest.NewRequest(http.MethodPost, "/section", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		fe.selectCtxFn = func(ctx context.Context, r rag.SectionContextRequest) (*rag.SectionContextResponse, error) {
			return nil, errors.New("section fail")
		}
		h.HandleSectionContext(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}
