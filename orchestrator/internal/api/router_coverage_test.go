package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

func TestRouter_PathAliases(t *testing.T) {
	mux := http.NewServeMux()

	// Register a target handler
	mux.HandleFunc("/target", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("target reached"))
	})

	// Test registerPathAlias
	registerPathAlias(mux, "/alias", "/target")

	t.Run("Path Alias Success", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/alias", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "target reached", w.Body.String())
	})

	t.Run("Path Alias Edge Cases", func(t *testing.T) {
		// Should not panic or do anything
		registerPathAlias(mux, "", "/target")
		registerPathAlias(mux, "/target", "/target")
	})
}

func TestRouter_JSONPostAliases(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/post-target", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(body)
	})

	registerJSONPostAlias(mux, "/json-alias", "/post-target", func(r *http.Request) map[string]any {
		return map[string]any{"user": r.URL.Query().Get("user")}
	})

	t.Run("JSON Post Alias Success", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/json-alias?user=alice", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var res map[string]any
		json.NewDecoder(w.Body).Decode(&res)
		assert.Equal(t, "alice", res["user"])
	})
}

func TestRouter_NewRouter_Minimal(t *testing.T) {
	cfg := ServerConfig{
		Version: "1.0.0",
	}
	handler := NewRouter(cfg)
	assert.NotNil(t, handler)

	t.Run("Health Check", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/health", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var res map[string]any
		json.NewDecoder(w.Body).Decode(&res)
		assert.Equal(t, "healthy", res["status"])
		assert.Equal(t, "1.0.0", res["version"])
	})
}

func TestWrapAcceptedOnSuccess(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}
	wrapped := wrapAcceptedOnSuccess(handler)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	wrapped(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "ok", w.Body.String())
}

func TestEnsureAgentGateway_WithClients(t *testing.T) {
	cfg := ServerConfig{
		LLMClient:      llm.NewClient(),
		SearchRegistry: search.NewProviderRegistry(),
	}
	gateway := ensureAgentGateway(cfg)
	assert.NotNil(t, gateway)
	assert.NotNil(t, gateway.LLMClient)
	assert.NotNil(t, gateway.Brain)
	assert.NotNil(t, gateway.Gate)
	assert.NotNil(t, gateway.Loop)
}
