package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
	"google.golang.org/grpc"
)

type mockLLMServiceClient struct {
	mock.Mock
}

func (m *mockLLMServiceClient) Generate(ctx context.Context, in *llmv1.GenerateRequest, opts ...grpc.CallOption) (*llmv1.GenerateResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*llmv1.GenerateResponse), args.Error(1)
}
func (m *mockLLMServiceClient) GenerateStream(ctx context.Context, in *llmv1.GenerateRequest, opts ...grpc.CallOption) (llmv1.LLMService_GenerateStreamClient, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(llmv1.LLMService_GenerateStreamClient), args.Error(1)
}
func (m *mockLLMServiceClient) StructuredOutput(ctx context.Context, in *llmv1.StructuredRequest, opts ...grpc.CallOption) (*llmv1.StructuredResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*llmv1.StructuredResponse), args.Error(1)
}
func (m *mockLLMServiceClient) Embed(ctx context.Context, in *llmv1.EmbedRequest, opts ...grpc.CallOption) (*llmv1.EmbedResponse, error) {
	return nil, nil
}
func (m *mockLLMServiceClient) EmbedBatch(ctx context.Context, in *llmv1.EmbedBatchRequest, opts ...grpc.CallOption) (*llmv1.EmbedBatchResponse, error) {
	return nil, nil
}
func (m *mockLLMServiceClient) Health(ctx context.Context, in *llmv1.HealthRequest, opts ...grpc.CallOption) (*llmv1.HealthResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*llmv1.HealthResponse), args.Error(1)
}

func TestRequestLogger(t *testing.T) {
	is := assert.New(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	mw := RequestLogger(handler)
	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()

	mw.ServeHTTP(rec, req)
	is.Equal(http.StatusAccepted, rec.Code)
}

func TestRequestTraceContextMiddleware(t *testing.T) {
	is := assert.New(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		is.Equal("header-trace", requestTraceIDFromContext(r.Context()))
		w.WriteHeader(http.StatusOK)
	})

	mw := RequestTraceContextMiddleware(handler)
	req := httptest.NewRequest("GET", "/search?trace_id=abc123", nil)
	req.Header.Set("X-Trace-Id", "header-trace")
	rec := httptest.NewRecorder()

	mw.ServeHTTP(rec, req)
	is.Equal(http.StatusOK, rec.Code)
}

func TestRequestTraceContextMiddleware_UsesContextWhenProvided(t *testing.T) {
	is := assert.New(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		is.Equal("ctx-trace-001", requestTraceIDFromContext(r.Context()))
		w.WriteHeader(http.StatusOK)
	})

	mw := RequestTraceContextMiddleware(handler)
	req := httptest.NewRequest("GET", "/search?traceId=query-trace", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxRequestTraceID, "ctx-trace-001"))
	rec := httptest.NewRecorder()

	mw.ServeHTTP(rec, req)
	is.Equal(http.StatusOK, rec.Code)
}

func TestCORSMiddleware(t *testing.T) {
	is := assert.New(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mw := CORSMiddleware(handler)

	t.Run("Normal request", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		is.Equal("*", rec.Header().Get("Access-Control-Allow-Origin"))
	})

	t.Run("OPTIONS request", func(t *testing.T) {
		req := httptest.NewRequest("OPTIONS", "/test", nil)
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		is.Equal(http.StatusNoContent, rec.Code)
	})
}

func TestResilienceMiddleware(t *testing.T) {
	is := assert.New(t)
	msc := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(msc)

	mwFunc := ResilienceMiddleware(client)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := mwFunc(handler)

	t.Run("Skip health paths", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/health", nil)
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		is.Equal(http.StatusOK, rec.Code)
	})

	t.Run("Check needed and healthy", func(t *testing.T) {
		msc.On("Health", mock.Anything, mock.Anything).Return(&llmv1.HealthResponse{Ok: true}, nil).Once()
		req := httptest.NewRequest("GET", "/search/parallel", nil)
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		is.Equal(http.StatusOK, rec.Code)
		msc.AssertExpectations(t)
	})
}

func TestAuthMiddleware_Detailed(t *testing.T) {
	is := assert.New(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid := GetUserID(r)
		email := GetUserEmail(r)
		w.Header().Set("X-Test-UID", uid)
		w.Header().Set("X-Test-Email", email)
		w.WriteHeader(http.StatusOK)
	})

	mw := AuthMiddleware(handler)

	t.Run("Internal request with key", func(t *testing.T) {
		t.Setenv("INTERNAL_SERVICE_KEY", "secret-key")
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("X-Internal-Service-Key", "secret-key")
		req.Header.Set("X-User-Id", "user123")
		req.Header.Set("X-User-Email", "user@example.com")
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)
		is.Equal(http.StatusOK, rec.Code)
		is.Equal("user123", rec.Header().Get("X-Test-UID"))
	})

	t.Run("Internal request with local overlay key", func(t *testing.T) {
		t.Setenv("ENDPOINTS_MANIFEST_ENV", "local")
		t.Setenv("INTERNAL_SERVICE_KEY", "")
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("X-Internal-Service-Key", "dev-internal-key")
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)
		is.Equal(http.StatusOK, rec.Code)
		is.Equal("internal-service", rec.Header().Get("X-Test-UID"))
		is.Equal("service@internal", rec.Header().Get("X-Test-Email"))
	})

	t.Run("Missing auth context", func(t *testing.T) {
		t.Setenv("INTERNAL_SERVICE_KEY", "secret-key")
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("X-Trace-Id", "auth-trace-1")
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)
		is.Equal(http.StatusUnauthorized, rec.Code)
		is.Contains(rec.Body.String(), `"traceId":"auth-trace-1"`)
	})
}

func TestPanicRecoveryMiddleware(t *testing.T) {
	is := assert.New(t)

	t.Run("Normal handler passes through", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		})
		mw := PanicRecoveryMiddleware(handler)
		req := httptest.NewRequest("GET", "/test", nil)
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)
		is.Equal(http.StatusOK, rec.Code)
	})

	t.Run("Panic recovered with 500 JSON envelope", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("nil pointer dereference in search provider")
		})
		mw := PanicRecoveryMiddleware(handler)
		req := httptest.NewRequest("POST", "/wisdev/research/deep", nil)
		rec := httptest.NewRecorder()

		// Should NOT panic the test -- middleware catches it.
		mw.ServeHTTP(rec, req)
		is.Equal(http.StatusInternalServerError, rec.Code)
		is.Contains(rec.Header().Get("Content-Type"), "application/json")
		is.Contains(rec.Body.String(), "internal server error")
	})

	t.Run("Panic recovered includes trace_id in error body", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("search exploded")
		})
		mw := PanicRecoveryMiddleware(handler)
		req := httptest.NewRequest("POST", "/search?trace_id=panic-trace-1", nil)
		ctx := context.WithValue(req.Context(), ctxRequestTraceID, "panic-trace-1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)
		is.Equal(http.StatusInternalServerError, rec.Code)
		is.Contains(rec.Header().Get("Content-Type"), "application/json")
		is.Contains(rec.Body.String(), `"traceId":"panic-trace-1"`)
	})
}

func TestInternalServiceMiddleware(t *testing.T) {
	is := assert.New(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mw := InternalServiceMiddleware(handler)

	t.Run("Internal path with valid key", func(t *testing.T) {
		t.Setenv("INTERNAL_SERVICE_KEY", "key123")
		req := httptest.NewRequest("GET", "/internal/secret", nil)
		req.Header.Set("X-Internal-Service-Key", "key123")
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)
		is.Equal(http.StatusOK, rec.Code)
	})

	t.Run("Internal path with invalid key", func(t *testing.T) {
		t.Setenv("INTERNAL_SERVICE_KEY", "key123")
		req := httptest.NewRequest("GET", "/internal/secret", nil)
		req.Header.Set("X-Internal-Service-Key", "wrong")
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)
		is.Equal(http.StatusForbidden, rec.Code)
	})

	t.Run("Internal path with local overlay key", func(t *testing.T) {
		t.Setenv("ENDPOINTS_MANIFEST_ENV", "local")
		t.Setenv("INTERNAL_SERVICE_KEY", "")
		req := httptest.NewRequest("GET", "/internal/secret", nil)
		req.Header.Set("X-Internal-Service-Key", "dev-internal-key")
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)
		is.Equal(http.StatusOK, rec.Code)
	})

	t.Run("Cloud overlay does not accept local placeholder key", func(t *testing.T) {
		t.Setenv("ENDPOINTS_MANIFEST_ENV", "cloudrun")
		t.Setenv("INTERNAL_SERVICE_KEY", "")
		req := httptest.NewRequest("GET", "/internal/secret", nil)
		req.Header.Set("X-Internal-Service-Key", "dev-internal-key")
		rec := httptest.NewRecorder()

		mw.ServeHTTP(rec, req)
		is.Equal(http.StatusForbidden, rec.Code)
	})
}
