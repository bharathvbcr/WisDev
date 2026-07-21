package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/paper"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/rag"

	"github.com/stretchr/testify/assert"
	"google.golang.org/genai"
)

type mockRAGEngine struct {
	rag.Engine
	bm25 *rag.BM25
}

func (m *mockRAGEngine) GetBM25() *rag.BM25 {
	return m.bm25
}

func TestRAGHandler_BM25(t *testing.T) {
	// Setup mock Python sidecar
	server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/index") {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "indexed"})
		} else if strings.Contains(r.URL.Path, "/search") {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	os.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)
	defer os.Unsetenv("PYTHON_SIDECAR_HTTP_URL")

	engine := rag.NewEngine(nil, nil)
	h := NewRAGHandler(engine)

	t.Run("Index", func(t *testing.T) {
		reqBody := `{"documents": ["doc1"], "docIds": ["id1"]}`
		req := httptest.NewRequest(http.MethodPost, "/rag/bm25/index", bytes.NewBufferString(reqBody))
		rec := httptest.NewRecorder()
		h.HandleBM25Index(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("Search", func(t *testing.T) {
		reqBody := `{"query": "doc1", "topK": 1}`
		req := httptest.NewRequest(http.MethodPost, "/rag/bm25/search", bytes.NewBufferString(reqBody))
		rec := httptest.NewRecorder()
		h.HandleBM25Search(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

func TestPaperHandler_Exports(t *testing.T) {
	h := NewPaperHandler(nil, "")

	reqBody := paper.ExportRequest{
		DraftID: "d1",
		Content: paper.DocumentContent{
			Title: "Test Title",
		},
	}
	body, _ := json.Marshal(reqBody)

	t.Run("Markdown", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/export/md", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.HandleExportMarkdown(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "text/markdown", rec.Header().Get("Content-Type"))
	})

	t.Run("HTML", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/export/html", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.HandleExportHTML(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "text/html", rec.Header().Get("Content-Type"))
	})

	t.Run("LaTeX", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/export/tex", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.HandleExportLaTeX(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/x-tex", rec.Header().Get("Content-Type"))
	})

	t.Run("InvalidJSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/export/md", bytes.NewReader([]byte("{invalid}")))
		rec := httptest.NewRecorder()
		h.HandleExportMarkdown(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

type mockImageGenerator struct {
	generateFn func(ctx context.Context, modelID, prompt string, count int, aspectRatio string) ([]genai.Image, error)
}

func (m *mockImageGenerator) GenerateImages(ctx context.Context, modelID, prompt string, count int, aspectRatio string) ([]genai.Image, error) {
	return m.generateFn(ctx, modelID, prompt, count, aspectRatio)
}

func TestImageHandler_HandleGenerate(t *testing.T) {
	mig := &mockImageGenerator{}
	h := NewImageHandler(mig)

	t.Run("Success", func(t *testing.T) {
		reqBody := `{"prompt": "test prompt", "count": 1}`
		req := httptest.NewRequest(http.MethodPost, "/generate", bytes.NewBufferString(reqBody))
		rec := httptest.NewRecorder()

		mig.generateFn = func(ctx context.Context, modelID, prompt string, count int, aspectRatio string) ([]genai.Image, error) {
			return []genai.Image{{
				ImageBytes: []byte("fake-image-data"),
				MIMEType:   "image/png",
			}}, nil
		}

		h.HandleGenerate(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("EmptyPrompt", func(t *testing.T) {
		reqBody := `{"prompt": "", "count": 1}`
		req := httptest.NewRequest(http.MethodPost, "/generate", bytes.NewBufferString(reqBody))
		rec := httptest.NewRecorder()
		h.HandleGenerate(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("InvalidCount", func(t *testing.T) {
		reqBody := `{"prompt": "test", "count": 10}`
		req := httptest.NewRequest(http.MethodPost, "/generate", bytes.NewBufferString(reqBody))
		rec := httptest.NewRecorder()
		h.HandleGenerate(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestImageHandler_HandleGenerate_Errors(t *testing.T) {
	h := NewImageHandler(nil) // vertex is nil

	t.Run("MethodNotAllowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/generate", nil)
		rec := httptest.NewRecorder()
		h.HandleGenerate(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("ServiceUnavailable", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/generate", bytes.NewReader([]byte(`{"prompt": "test"}`)))
		rec := httptest.NewRecorder()
		h.HandleGenerate(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})
}


func TestInternalOpsHelpers(t *testing.T) {
	t.Run("statusLabel", func(t *testing.T) {
		assert.Equal(t, "already_deleted", statusLabel(true))
		assert.Equal(t, "deletion_recorded", statusLabel(false))
	})

	t.Run("accountDeleteMessage", func(t *testing.T) {
		assert.Equal(t, "Account deletion already processed for user-1", accountDeleteMessage("user-1", true))
		assert.Equal(t, "Account deletion recorded for user-1", accountDeleteMessage("user-1", false))
	})

	t.Run("firstNonEmpty", func(t *testing.T) {
		assert.Equal(t, "value", firstNonEmpty(" ", "", " value ", "later"))
		assert.Equal(t, "", firstNonEmpty(" ", "\t"))
	})
}

func TestClassifyLLMError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want generateErrorKind
	}{
		{"nil", nil, ""},
		{"timeout", errors.New("context deadline exceeded"), generateErrTimeout},
		{"rate limit", errors.New("429 quota exceeded"), generateErrRateLimit},
		{"transient", errors.New("service unavailable"), generateErrTransient},
		{"permanent", errors.New("permission denied"), generateErrPermanent},
		{"default", errors.New("something else"), generateErrTransient},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, classifyLLMError(tt.err))
		})
	}
}
