package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

func TestWisDevV2_PolicyHandlers(t *testing.T) {
	gw := &wisdev.AgentGateway{
		Store:        wisdev.NewInMemorySessionStore(),
		PolicyConfig: policy.DefaultPolicyConfig(),
	}
	ragH := NewRAGHandler(nil)

	mux := http.NewServeMux()
	RegisterV2WisDevRoutes(mux, gw, ragH)

	t.Run("GET /v2/policy/get", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/policy/get?userId=u1", nil)
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.NotNil(t, resp["policyBundle"])
	})

	t.Run("POST /v2/policy/canary-config/upsert", func(t *testing.T) {
		body := `{"canaryMediumHighOnly": false}`
		req := httptest.NewRequest(http.MethodPost, "/v2/policy/canary-config/upsert", bytes.NewReader([]byte(body)))
		// requireInternalServiceAccess check
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "internal-service")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("POST /v2/runtime/traces/get - missing session id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/runtime/traces/get", bytes.NewReader([]byte(`{}`)))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("GET /v2/policy/get - forbidden user", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/policy/get?userId=someone-else", nil)
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrUnauthorized, resp.Error.Code)
	})

	t.Run("POST /v2/policy/upsert - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/policy/upsert", bytes.NewReader([]byte(`{bad`)))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /v2/policy/canary-config/upsert - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/policy/canary-config/upsert", bytes.NewReader([]byte(`{bad`)))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "internal-service")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("PUT /v2/policy/function-bridge-config/get - method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/v2/policy/function-bridge-config/get", nil)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /v2/policy/function-bridge-config/upsert - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/policy/function-bridge-config/upsert", bytes.NewReader([]byte(`{bad`)))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "internal-service")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /v2/policy/promote - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/policy/promote", bytes.NewReader([]byte(`{bad`)))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "internal-service")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /v2/policy/rollback - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/policy/rollback", bytes.NewReader([]byte(`{bad`)))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "internal-service")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})
}

func TestWisDevV2_PlanHandlers(t *testing.T) {
	t.Skip("requires full plan execution engine with LLM and policy infrastructure")
	gw := &wisdev.AgentGateway{
		Store:        wisdev.NewInMemorySessionStore(),
		PolicyConfig: policy.DefaultPolicyConfig(),
	}
	ragH := NewRAGHandler(nil)
	mux := http.NewServeMux()
	RegisterV2WisDevRoutes(mux, gw, ragH)

	t.Run("POST /v2/wisdev/wisdev.Plan", func(t *testing.T) {
		body := `{"query":"test query", "userId":"u1"}`
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/wisdev.Plan", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.NotNil(t, resp["wisdev.Plan"])
	})

	t.Run("POST /v2/wisdev/wisdev.Plan - missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/wisdev.Plan", bytes.NewReader([]byte(`{"userId":"u1"}`)))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})
}
