package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/paper"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/resilience"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type flexibleProfiler struct {
	extractFn func(context.Context, search.Paper) (*paper.Profile, error)
}

func (f *flexibleProfiler) ExtractProfile(ctx context.Context, p search.Paper) (*paper.Profile, error) {
	return f.extractFn(ctx, p)
}

type mockHTTPClient struct {
	mock.Mock
}

func (m *mockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	args := m.Called(req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*http.Response), args.Error(1)
}

func TestPaperHandler_HandleProfile(t *testing.T) {
	fp := &flexibleProfiler{}
	h := NewPaperHandler(fp, "")

	t.Run("Degraded", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/profile", nil)
		ctx := resilience.SetDegraded(req.Context(), true)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		h.HandleProfile(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("Success", func(t *testing.T) {
		p := search.Paper{ID: "p1"}
		body, _ := json.Marshal(p)
		req := httptest.NewRequest(http.MethodPost, "/profile", bytes.NewReader(body))
		rec := httptest.NewRecorder()

		fp.extractFn = func(ctx context.Context, paperData search.Paper) (*paper.Profile, error) {
			return &paper.Profile{PaperID: "p1"}, nil
		}

		h.HandleProfile(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/profile", bytes.NewReader([]byte("{")))
		rec := httptest.NewRecorder()

		h.HandleProfile(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("Profiler Error", func(t *testing.T) {
		fp.extractFn = func(ctx context.Context, paperData search.Paper) (*paper.Profile, error) {
			return nil, assert.AnError
		}
		body, _ := json.Marshal(search.Paper{ID: "p2"})
		req := httptest.NewRequest(http.MethodPost, "/profile", bytes.NewReader(body))
		rec := httptest.NewRecorder()

		h.HandleProfile(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/profile", nil)
		rec := httptest.NewRecorder()

		h.HandleProfile(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})
}

func TestPaperHandler_HandleExportFormats(t *testing.T) {
	h := NewPaperHandler(nil, "")

	t.Run("HTML Success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/export/html", bytes.NewBufferString(`{"draft_id":"draft-1","content":{"title":"Paper","sections":[{"name":"Intro","content":"Hello"}]}}`))
		rec := httptest.NewRecorder()

		h.HandleExportHTML(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "text/html", rec.Header().Get("Content-Type"))
		assert.Contains(t, rec.Body.String(), "<h1>Paper</h1>")
	})

	t.Run("HTML Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/export/html", bytes.NewBufferString(`{`))
		rec := httptest.NewRecorder()

		h.HandleExportHTML(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("LaTeX Success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/export/latex", bytes.NewBufferString(`{"draft_id":"draft-1","content":{"title":"Paper","sections":[{"name":"Intro","content":"Hello"}]}}`))
		rec := httptest.NewRecorder()

		h.HandleExportLaTeX(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/x-tex", rec.Header().Get("Content-Type"))
		assert.Contains(t, rec.Body.String(), "\\section{Intro}")
	})

	t.Run("LaTeX Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/export/latex", bytes.NewBufferString(`{`))
		rec := httptest.NewRecorder()

		h.HandleExportLaTeX(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestPaperHandler_HandleExtractPDF(t *testing.T) {
	mhc := new(mockHTTPClient)
	h := NewPaperHandler(nil, "http://python-sidecar")
	h.SetHTTPClient(mhc)

	t.Run("Multipart Upload Success", func(t *testing.T) {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		part, _ := writer.CreateFormFile("file", "test.pdf")
		part.Write([]byte("fake pdf content"))
		writer.Close()

		req := httptest.NewRequest(http.MethodPost, "/extract", body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		rec := httptest.NewRecorder()

		// Mock Python sidecar response
		respBody := `{"text": "extracted text"}`
		mhc.On("Do", mock.Anything).Return(&http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(respBody)),
		}, nil).Once()

		h.HandleExtractPDF(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "extracted text")
	})

	t.Run("URL Success", func(t *testing.T) {
		reqBody := `{"url": "http://example.com/paper.pdf"}`
		req := httptest.NewRequest(http.MethodPost, "/extract", bytes.NewBufferString(reqBody))
		rec := httptest.NewRecorder()

		// 1. Mock PDF fetch
		mhc.On("Do", mock.MatchedBy(func(r *http.Request) bool { return r.Method == "GET" })).Return(&http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString("pdf binary")),
		}, nil).Once()

		// 2. Mock Python sidecar
		respBody := `{"text": "url extracted"}`
		mhc.On("Do", mock.MatchedBy(func(r *http.Request) bool { return r.Method == "POST" })).Return(&http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(respBody)),
		}, nil).Once()

		h.HandleExtractPDF(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "url extracted")
	})

	t.Run("Upstream Failure Surfaces Snippet And Forwards Internal Key", func(t *testing.T) {
		t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")

		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		part, _ := writer.CreateFormFile("file", "test.pdf")
		part.Write([]byte("fake pdf content"))
		writer.Close()

		req := httptest.NewRequest(http.MethodPost, "/extract", body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		rec := httptest.NewRecorder()

		mhc.On("Do", mock.MatchedBy(func(r *http.Request) bool {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "test-internal-key", r.Header.Get("X-Internal-Service-Key"))
			return true
		})).Return(&http.Response{
			StatusCode: http.StatusForbidden,
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"message":"Invalid service credentials"}}`)),
		}, nil).Once()

		h.HandleExtractPDF(rec, req)
		assert.Equal(t, http.StatusBadGateway, rec.Code)

		var payload APIError
		err := json.NewDecoder(rec.Body).Decode(&payload)
		assert.NoError(t, err)
		assert.Equal(t, ErrDependencyFailed, payload.Error.Code)
		assert.Equal(t, http.StatusBadGateway, payload.Error.Status)
		assert.Equal(t, float64(http.StatusForbidden), payload.Error.Details["status"])
		assert.Contains(t, payload.Error.Details["responseBodySnippet"], "Invalid service credentials")
	})

	t.Run("Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/extract", nil)
		rec := httptest.NewRecorder()

		h.HandleExtractPDF(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("Missing Input", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/extract", bytes.NewBufferString(`{}`))
		rec := httptest.NewRecorder()

		h.HandleExtractPDF(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("Fetch Error", func(t *testing.T) {
		mhc := new(mockHTTPClient)
		h := NewPaperHandler(nil, "http://python-sidecar")
		h.SetHTTPClient(mhc)
		mhc.On("Do", mock.MatchedBy(func(r *http.Request) bool { return r.Method == "GET" })).Return(nil, assert.AnError).Once()

		req := httptest.NewRequest(http.MethodPost, "/extract", bytes.NewBufferString(`{"url":"http://example.com/paper.pdf"}`))
		rec := httptest.NewRecorder()

		h.HandleExtractPDF(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("Sidecar Bad Status", func(t *testing.T) {
		mhc := new(mockHTTPClient)
		h := NewPaperHandler(nil, "http://python-sidecar")
		h.SetHTTPClient(mhc)

		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		part, _ := writer.CreateFormFile("file", "test.pdf")
		part.Write([]byte("fake pdf content"))
		writer.Close()

		req := httptest.NewRequest(http.MethodPost, "/extract", body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		rec := httptest.NewRecorder()

		mhc.On("Do", mock.MatchedBy(func(r *http.Request) bool { return r.Method == http.MethodPost })).Return(&http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":"nope"}`)),
		}, nil).Once()

		h.HandleExtractPDF(rec, req)
		assert.Equal(t, http.StatusBadGateway, rec.Code)
	})

	t.Run("Sidecar Decode Error", func(t *testing.T) {
		mhc := new(mockHTTPClient)
		h := NewPaperHandler(nil, "http://python-sidecar")
		h.SetHTTPClient(mhc)

		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		part, _ := writer.CreateFormFile("file", "test.pdf")
		part.Write([]byte("fake pdf content"))
		writer.Close()

		req := httptest.NewRequest(http.MethodPost, "/extract", body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		rec := httptest.NewRecorder()

		mhc.On("Do", mock.MatchedBy(func(r *http.Request) bool { return r.Method == http.MethodPost })).Return(&http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(`not json`)),
		}, nil).Once()

		h.HandleExtractPDF(rec, req)
		assert.Equal(t, http.StatusBadGateway, rec.Code)
	})

	t.Run("Python Failure Falls Back Then Surfaces PDF Error", func(t *testing.T) {
		mhc := new(mockHTTPClient)
		h := NewPaperHandler(nil, "http://python-sidecar")
		h.SetHTTPClient(mhc)

		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		part, _ := writer.CreateFormFile("file", "test.pdf")
		part.Write([]byte("not a pdf"))
		writer.Close()

		req := httptest.NewRequest(http.MethodPost, "/extract", body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		rec := httptest.NewRecorder()

		mhc.On("Do", mock.MatchedBy(func(r *http.Request) bool { return r.Method == http.MethodPost })).Return(nil, assert.AnError).Once()

		h.HandleExtractPDF(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)

		var payload APIError
		err := json.NewDecoder(rec.Body).Decode(&payload)
		assert.NoError(t, err)
		assert.Equal(t, ErrDependencyFailed, payload.Error.Code)
		assert.Contains(t, payload.Error.Message, "pdf extraction failed")
	})
}

func TestPaperHandler_HandleGetPaper(t *testing.T) {
	mhc := new(mockHTTPClient)
	h := NewPaperHandler(nil, "")
	h.SetHTTPClient(mhc)

	t.Run("Success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/paper?id=p1", nil)
		rec := httptest.NewRecorder()

		mhc.On("Do", mock.Anything).Return(&http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(`{"paperId": "p1"}`)),
		}, nil).Once()

		h.HandleGetPaper(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "p1")
	})

	t.Run("DOI Prefix", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/paper?id=10.1/abc", nil)
		rec := httptest.NewRecorder()

		mhc.On("Do", mock.MatchedBy(func(r *http.Request) bool {
			return strings.Contains(r.URL.String(), "DOI%3A10.1%2Fabc")
		})).Return(&http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(`{"paperId": "doi-paper"}`)),
		}, nil).Once()

		h.HandleGetPaper(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "doi-paper")
	})

	t.Run("Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/paper?id=p1", nil)
		rec := httptest.NewRecorder()

		h.HandleGetPaper(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("Missing ID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/paper", nil)
		rec := httptest.NewRecorder()

		h.HandleGetPaper(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("Request Error", func(t *testing.T) {
		mhc := new(mockHTTPClient)
		h := NewPaperHandler(nil, "")
		h.SetHTTPClient(mhc)
		mhc.On("Do", mock.Anything).Return(nil, assert.AnError).Once()

		req := httptest.NewRequest(http.MethodGet, "/paper?id=p1", nil)
		rec := httptest.NewRecorder()

		h.HandleGetPaper(rec, req)
		assert.Equal(t, http.StatusBadGateway, rec.Code)
	})

	t.Run("Bad Status", func(t *testing.T) {
		mhc := new(mockHTTPClient)
		h := NewPaperHandler(nil, "")
		h.SetHTTPClient(mhc)
		mhc.On("Do", mock.Anything).Return(&http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(bytes.NewBufferString(`not found`)),
		}, nil).Once()

		req := httptest.NewRequest(http.MethodGet, "/paper?id=p1", nil)
		rec := httptest.NewRecorder()

		h.HandleGetPaper(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}

func TestPaperHandler_HandleCount(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		t.Setenv("SEMANTIC_SCHOLAR_API_KEY", "s2-key")
		mhc := new(mockHTTPClient)
		h := NewPaperHandler(nil, "")
		h.SetHTTPClient(mhc)

		mhc.On("Do", mock.MatchedBy(func(r *http.Request) bool {
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "s2-key", r.Header.Get("x-api-key"))
			assert.Equal(t, "RLHF reinforcement learning", r.URL.Query().Get("query"))
			assert.Equal(t, "1", r.URL.Query().Get("limit"))
			assert.Equal(t, "paperId", r.URL.Query().Get("fields"))
			return strings.Contains(r.URL.String(), "api.semanticscholar.org/graph/v1/paper/search")
		})).Return(&http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"total":1234}`)),
		}, nil).Once()

		req := httptest.NewRequest(http.MethodGet, "/papers/count?query=RLHF+reinforcement+learning", nil)
		rec := httptest.NewRecorder()

		h.HandleCount(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var payload paperCountAPIResponse
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
		assert.True(t, payload.OK)
		assert.True(t, payload.Available)
		assert.Equal(t, 1234, payload.Count)
		assert.Equal(t, "semantic_scholar", payload.Source)
		mhc.AssertExpectations(t)
	})

	t.Run("RateLimitDegradesWithoutHTTP429", func(t *testing.T) {
		t.Setenv("SEMANTIC_SCHOLAR_API_KEY", "s2-key")
		mhc := new(mockHTTPClient)
		h := NewPaperHandler(nil, "")
		h.SetHTTPClient(mhc)

		mhc.On("Do", mock.Anything).Return(&http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"2"}},
			Body:       io.NopCloser(bytes.NewBufferString(`rate limited`)),
		}, nil).Once()

		req := httptest.NewRequest(http.MethodGet, "/papers/count?query=RLHF", nil)
		rec := httptest.NewRecorder()

		h.HandleCount(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var payload paperCountAPIResponse
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
		assert.True(t, payload.OK)
		assert.False(t, payload.Available)
		assert.Equal(t, http.StatusTooManyRequests, payload.UpstreamStatus)
		assert.Equal(t, 2000, payload.RetryAfterMs)
		assert.Equal(t, "upstream_status", payload.UnavailableReason)
		mhc.AssertExpectations(t)
	})

	t.Run("CachesSuccessfulCounts", func(t *testing.T) {
		t.Setenv("SEMANTIC_SCHOLAR_API_KEY", "s2-key")
		mhc := new(mockHTTPClient)
		h := NewPaperHandler(nil, "")
		h.SetHTTPClient(mhc)

		mhc.On("Do", mock.Anything).Return(&http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"total":777}`)),
		}, nil).Once()

		req := httptest.NewRequest(http.MethodGet, "/papers/count?query=RLHF", nil)
		rec := httptest.NewRecorder()
		h.HandleCount(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		req = httptest.NewRequest(http.MethodGet, "/papers/count?query=rlhf", nil)
		rec = httptest.NewRecorder()
		h.HandleCount(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		var payload paperCountAPIResponse
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
		assert.True(t, payload.Available)
		assert.Equal(t, 777, payload.Count)
		mhc.AssertExpectations(t)
	})

	t.Run("RateLimitOpensSharedBackoff", func(t *testing.T) {
		t.Setenv("SEMANTIC_SCHOLAR_API_KEY", "s2-key")
		mhc := new(mockHTTPClient)
		h := NewPaperHandler(nil, "")
		h.SetHTTPClient(mhc)

		mhc.On("Do", mock.Anything).Return(&http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"2"}},
			Body:       io.NopCloser(bytes.NewBufferString(`rate limited`)),
		}, nil).Once()

		req := httptest.NewRequest(http.MethodGet, "/papers/count?query=RLHF", nil)
		rec := httptest.NewRecorder()
		h.HandleCount(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		req = httptest.NewRequest(http.MethodGet, "/papers/count?query=RLHF+followup", nil)
		rec = httptest.NewRecorder()
		h.HandleCount(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		var payload paperCountAPIResponse
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
		assert.False(t, payload.Available)
		assert.Equal(t, http.StatusTooManyRequests, payload.UpstreamStatus)
		assert.Equal(t, "upstream_backoff", payload.UnavailableReason)
		assert.Greater(t, payload.RetryAfterMs, 0)
		mhc.AssertExpectations(t)
	})

	t.Run("RejectsMissingQuery", func(t *testing.T) {
		h := NewPaperHandler(nil, "")
		req := httptest.NewRequest(http.MethodGet, "/papers/count", nil)
		rec := httptest.NewRecorder()

		h.HandleCount(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("RejectsWrongMethod", func(t *testing.T) {
		h := NewPaperHandler(nil, "")
		req := httptest.NewRequest(http.MethodPost, "/papers/count?query=RLHF", nil)
		rec := httptest.NewRecorder()

		h.HandleCount(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})
}

func TestPaperHandler_HandleGetNetwork(t *testing.T) {
	mhc := new(mockHTTPClient)
	h := NewPaperHandler(nil, "")
	h.SetHTTPClient(mhc)

	t.Run("Success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/network?paperId=p1", nil)
		rec := httptest.NewRecorder()

		mhc.On("Do", mock.Anything).Return(&http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(`{"paperId": "p1", "citations": []}`)),
		}, nil).Once()

		h.HandleGetNetwork(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "p1")
	})

	t.Run("Paper ID Alias", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/network?id=p1", nil)
		rec := httptest.NewRecorder()

		mhc.On("Do", mock.MatchedBy(func(r *http.Request) bool {
			return strings.Contains(r.URL.String(), "/paper/p1?fields=")
		})).Return(&http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(`{"paperId": "p1", "citations": []}`)),
		}, nil).Once()

		h.HandleGetNetwork(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "p1")
	})

	t.Run("Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/network?paperId=p1", nil)
		rec := httptest.NewRecorder()

		h.HandleGetNetwork(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("Missing ID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/network", nil)
		rec := httptest.NewRecorder()

		h.HandleGetNetwork(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("Request Error", func(t *testing.T) {
		mhc := new(mockHTTPClient)
		h := NewPaperHandler(nil, "")
		h.SetHTTPClient(mhc)
		mhc.On("Do", mock.Anything).Return(nil, assert.AnError).Once()

		req := httptest.NewRequest(http.MethodGet, "/network?paperId=p1", nil)
		rec := httptest.NewRecorder()

		h.HandleGetNetwork(rec, req)
		assert.Equal(t, http.StatusBadGateway, rec.Code)
	})

	t.Run("Bad Status", func(t *testing.T) {
		mhc := new(mockHTTPClient)
		h := NewPaperHandler(nil, "")
		h.SetHTTPClient(mhc)
		mhc.On("Do", mock.Anything).Return(&http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(bytes.NewBufferString(`not found`)),
		}, nil).Once()

		req := httptest.NewRequest(http.MethodGet, "/network?paperId=p1", nil)
		rec := httptest.NewRecorder()

		h.HandleGetNetwork(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}

func TestPaperHandlerDeadlineExceededFailsQuickly(t *testing.T) {
	t.Run("profile extraction timeout", func(t *testing.T) {
		previousTimeout := paperProfileRequestTimeout
		paperProfileRequestTimeout = 50 * time.Millisecond
		defer func() { paperProfileRequestTimeout = previousTimeout }()

		fp := &flexibleProfiler{
			extractFn: func(ctx context.Context, _ search.Paper) (*paper.Profile, error) {
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("expected profile extraction to carry a deadline")
				}
				select {
				case <-ctx.Done():
					if ctx.Err() != context.DeadlineExceeded {
						t.Fatalf("expected deadline exceeded, got %v", ctx.Err())
					}
					return nil, ctx.Err()
				case <-time.After(1 * time.Second):
					t.Fatal("expected profile extraction context cancellation")
					return nil, nil
				}
			},
		}
		h := NewPaperHandler(fp, "")

		body, _ := json.Marshal(search.Paper{ID: "p-timeout"})
		req := httptest.NewRequest(http.MethodPost, "/profile", bytes.NewReader(body))
		rec := httptest.NewRecorder()

		startedAt := time.Now()
		h.HandleProfile(rec, req)

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.Less(t, time.Since(startedAt), time.Second)
	})

	t.Run("extract pdf url fetch timeout", func(t *testing.T) {
		previousTimeout := paperExternalRequestTimeout
		paperExternalRequestTimeout = 50 * time.Millisecond
		defer func() { paperExternalRequestTimeout = previousTimeout }()

		mhc := new(mockHTTPClient)
		h := NewPaperHandler(nil, "http://python-sidecar")
		h.SetHTTPClient(mhc)

		mhc.On("Do", mock.Anything).
			Run(func(args mock.Arguments) {
				req, ok := args.Get(0).(*http.Request)
				if !ok {
					t.Fatalf("expected request argument, got %T", args.Get(0))
				}
				if _, ok := req.Context().Deadline(); !ok {
					t.Fatal("expected extract-pdf fetch request to carry a deadline")
				}
				select {
				case <-req.Context().Done():
					if req.Context().Err() != context.DeadlineExceeded {
						t.Fatalf("expected deadline exceeded, got %v", req.Context().Err())
					}
				case <-time.After(1 * time.Second):
					t.Fatal("expected extract-pdf fetch context cancellation")
				}
			}).
			Return(nil, context.DeadlineExceeded).
			Once()

		req := httptest.NewRequest(http.MethodPost, "/extract", bytes.NewBufferString(`{"url":"http://example.com/paper.pdf"}`))
		rec := httptest.NewRecorder()

		startedAt := time.Now()
		h.HandleExtractPDF(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Less(t, time.Since(startedAt), time.Second)
	})

	t.Run("get paper timeout", func(t *testing.T) {
		previousTimeout := paperExternalRequestTimeout
		paperExternalRequestTimeout = 50 * time.Millisecond
		defer func() { paperExternalRequestTimeout = previousTimeout }()

		mhc := new(mockHTTPClient)
		h := NewPaperHandler(nil, "")
		h.SetHTTPClient(mhc)

		mhc.On("Do", mock.Anything).
			Run(func(args mock.Arguments) {
				req, ok := args.Get(0).(*http.Request)
				if !ok {
					t.Fatalf("expected request argument, got %T", args.Get(0))
				}
				if _, ok := req.Context().Deadline(); !ok {
					t.Fatal("expected get-paper request to carry a deadline")
				}
				select {
				case <-req.Context().Done():
					if req.Context().Err() != context.DeadlineExceeded {
						t.Fatalf("expected deadline exceeded, got %v", req.Context().Err())
					}
				case <-time.After(1 * time.Second):
					t.Fatal("expected get-paper context cancellation")
				}
			}).
			Return(nil, context.DeadlineExceeded).
			Once()

		req := httptest.NewRequest(http.MethodGet, "/paper?id=p1", nil)
		rec := httptest.NewRecorder()

		startedAt := time.Now()
		h.HandleGetPaper(rec, req)

		assert.Equal(t, http.StatusBadGateway, rec.Code)
		assert.Less(t, time.Since(startedAt), time.Second)
	})

	t.Run("get network timeout", func(t *testing.T) {
		previousTimeout := paperExternalRequestTimeout
		paperExternalRequestTimeout = 50 * time.Millisecond
		defer func() { paperExternalRequestTimeout = previousTimeout }()

		mhc := new(mockHTTPClient)
		h := NewPaperHandler(nil, "")
		h.SetHTTPClient(mhc)

		mhc.On("Do", mock.Anything).
			Run(func(args mock.Arguments) {
				req, ok := args.Get(0).(*http.Request)
				if !ok {
					t.Fatalf("expected request argument, got %T", args.Get(0))
				}
				if _, ok := req.Context().Deadline(); !ok {
					t.Fatal("expected get-network request to carry a deadline")
				}
				select {
				case <-req.Context().Done():
					if req.Context().Err() != context.DeadlineExceeded {
						t.Fatalf("expected deadline exceeded, got %v", req.Context().Err())
					}
				case <-time.After(1 * time.Second):
					t.Fatal("expected get-network context cancellation")
				}
			}).
			Return(nil, context.DeadlineExceeded).
			Once()

		req := httptest.NewRequest(http.MethodGet, "/network?paperId=p1", nil)
		rec := httptest.NewRecorder()

		startedAt := time.Now()
		h.HandleGetNetwork(rec, req)

		assert.Equal(t, http.StatusBadGateway, rec.Code)
		assert.Less(t, time.Since(startedAt), time.Second)
	})
}
