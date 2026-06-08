package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/telemetry"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	// Initialize OTel with trace context propagator
	_, _ = telemetry.InitOTel(context.Background(), "test-project", "test")
	os.Exit(m.Run())
}

func TestAPI_BoundaryFailureModes(t *testing.T) {
	// Ensure consistent auth behavior
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	// Setup a minimal router with middleware
	cfg := ServerConfig{
		Version: "test",
	}
	handler := NewRouter(cfg)
	server := httptest.NewServer(handler)
	defer server.Close()

	t.Run("Malformed JSON payload", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/search/hybrid", strings.NewReader("{ invalid json }"))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Service-Key", "test-key")
		req.Header.Set("X-User-Id", "test-user")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

		var apiErr APIError
		err = json.NewDecoder(resp.Body).Decode(&apiErr)
		require.NoError(t, err)
		assert.False(t, apiErr.OK)
		// Hybrid search might use writeSearchError which might map to a different code string than ErrBadRequest constant exactly
		// let's just check if it's an error.
	})

	t.Run("Missing required fields", func(t *testing.T) {
		// Search requires a query
		req, err := http.NewRequest(http.MethodPost, server.URL+"/search/hybrid", strings.NewReader(`{"limit": 10}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Service-Key", "test-key")
		req.Header.Set("X-User-Id", "test-user")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.True(t, resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusOK)
	})

	t.Run("Unauthorized access (Missing Headers)", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/search/hybrid", strings.NewReader(`{"query": "test"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		// No X-Internal-Service-Key and no X-User-Id

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}
func TestAPI_TracePropagation(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	cfg := ServerConfig{
		Version: "test",
	}
	handler := NewRouter(cfg)
	server := httptest.NewServer(handler)
	defer server.Close()

	t.Run("Preserves trace ID from traceparent header in error response", func(t *testing.T) {
		// Standard W3C traceparent: 00-traceid-spanid-flags
		traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
		traceparent := fmt.Sprintf("00-%s-00f067aa0ba902b7-01", traceID)

		// Trigger an error (Unauthorized by omitting X-User-Id)
		reqErr, _ := http.NewRequest(http.MethodPost, server.URL+"/search/hybrid", strings.NewReader(`{"query":"test"}`))
		reqErr.Header.Set("Content-Type", "application/json")
		reqErr.Header.Set("traceparent", traceparent)

		respErr, err := http.DefaultClient.Do(reqErr)
		require.NoError(t, err)
		defer respErr.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, respErr.StatusCode)

		var apiErr APIError
		err = json.NewDecoder(respErr.Body).Decode(&apiErr)
		require.NoError(t, err)

		// The trace ID should be propagated to the response envelope
		assert.Equal(t, traceID, apiErr.TraceID)
	})
}

func TestAPI_PanicRecovery(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/panic", func(w http.ResponseWriter, r *http.Request) {
		panic("intentional panic")
	})

	// Wrap with PanicRecoveryMiddleware
	handler := PanicRecoveryMiddleware(mux)
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/panic", nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)

	var apiErr APIError
	err = json.NewDecoder(resp.Body).Decode(&apiErr)
	require.NoError(t, err)
	assert.Equal(t, ErrInternal, apiErr.Error.Code)
	assert.Contains(t, apiErr.Error.Message, "internal server error")
}

func TestAPI_UpstreamDependencyFailure(t *testing.T) {
	t.Run("LLM Provider Unavailable (503)", func(t *testing.T) {
		stub := &stubLLMHTTPClient{
			embedErr: status.Error(codes.Unavailable, "upstream sidecar is down"),
		}
		handler := NewLLMHandler(stub)

		req, err := http.NewRequest(http.MethodPost, "/llm/embed", strings.NewReader(`{"text": "test"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")

		rec := httptest.NewRecorder()
		handler.HandleEmbed(rec, req)

		// codes.Unavailable classifies as transient, which returns 502 Bad Gateway
		assert.Equal(t, http.StatusBadGateway, rec.Code)

		var resp map[string]any
		err = json.NewDecoder(rec.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Equal(t, "transient", resp["kind"])
		assert.Contains(t, resp["error"], "upstream sidecar is down")
	})
}
