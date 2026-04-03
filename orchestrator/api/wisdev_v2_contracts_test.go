package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

// withTestUserID injects an authenticated user ID into the request context,
// bypassing AuthMiddleware for unit tests that exercise the raw mux directly.
func withTestUserID(r *http.Request, userID string) *http.Request {
	ctx := context.WithValue(r.Context(), ctxUserID, userID)
	return r.WithContext(ctx)
}

func TestWisDevV2_ContractHandlers(t *testing.T) {
	gw := &wisdev.AgentGateway{
		Store:      wisdev.NewInMemorySessionStore(),
		StateStore: wisdev.NewRuntimeStateStore(nil, nil),
		PolicyConfig: policy.DefaultPolicyConfig(),
		PythonExecute: func(ctx context.Context, action string, payload map[string]any, session *wisdev.AgentSession) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	}
	mux := http.NewServeMux()
	RegisterV2WisDevRoutes(mux, gw, nil)

	t.Run("POST /v2/wisdev/plan/revision", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/plan/revision", strings.NewReader(`{"reason":"coverage gap","stepId":"search-1"}`))
		req = withTestUserID(req, "test-user-1")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var payload map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
		result, ok := payload["result"].(map[string]any)
		if assert.True(t, ok, "result key must be a map, got: %T", payload["result"]) {
			assert.Equal(t, true, result["applied"])
		}
	})

	t.Run("POST /v2/wisdev/subtopics/generate", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/subtopics/generate", strings.NewReader(`{"query":"rlhf reward modeling for medical summarization","domain":"medicine"}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var payload map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
		result := payload["result"].(map[string]any)
		assert.NotEmpty(t, result["subtopics"])
	})

	t.Run("POST /v2/wisdev/search-coverage/evaluate", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/search-coverage/evaluate", strings.NewReader(`{"query":"transformer interpretability","queries":["transformer interpretability","mechanistic interpretability"],"results":[{"title":"Mechanistic interpretability for transformers"}]}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var payload map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
		result := payload["result"].(map[string]any)
		assert.Contains(t, result, "coverage")
		assert.Contains(t, result, "gaps")
	})

	t.Run("POST /v2/wisdev/follow-up/check", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/follow-up/check", strings.NewReader(`{"query":"clinical rlhf","coverageScore":0.2,"missingTerms":["safety"],"subtopics":["Methods"]}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var payload map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
		result := payload["result"].(map[string]any)
		assert.Equal(t, true, result["needsFollowUp"])
	})
}
