package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func TestWisDev_PolicyHandlers(t *testing.T) {
	gw := &wisdev.AgentGateway{
		Store:        wisdev.NewInMemorySessionStore(),
		PolicyConfig: policy.DefaultPolicyConfig(),
	}
	ragH := NewRAGHandler(nil)

	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, ragH, nil)

	t.Run("GET /policy/get", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/policy/get?userId=u1", nil)
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.NotNil(t, resp["policyBundle"])
	})

	t.Run("GET /wisdev/deep-agents/capabilities", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/deep-agents/capabilities", nil)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		caps, ok := resp["capabilities"].(map[string]any)
		assert.True(t, ok)
		assert.NotEmpty(t, caps["wisdevActions"])
	})

	t.Run("POST /wisdev/deep-agents/policy/resolve - guided", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/deep-agents/policy/resolve", bytes.NewReader([]byte(`{"mode":"guided"}`)))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		policy, ok := resp["policy"].(map[string]any)
		assert.True(t, ok)
		assert.Equal(t, "guided", policy["mode"])
		assert.Equal(t, true, policy["requireHumanConfirmation"])
	})

	t.Run("POST /wisdev/deep-agents/policy/resolve - yolo", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/deep-agents/policy/resolve", bytes.NewReader([]byte(`{"mode":"yolo"}`)))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		policy, ok := resp["policy"].(map[string]any)
		assert.True(t, ok)
		assert.Equal(t, "yolo", policy["mode"])
		assert.Equal(t, false, policy["requireHumanConfirmation"])
	})

	t.Run("POST /policy/canary-config/upsert", func(t *testing.T) {
		body := `{"canaryMediumHighOnly": false}`
		req := httptest.NewRequest(http.MethodPost, "/policy/canary-config/upsert", bytes.NewReader([]byte(body)))
		// requireInternalServiceAccess check
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "internal-service")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("POST /runtime/traces/get - missing session id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/runtime/traces/get", bytes.NewReader([]byte(`{}`)))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /runtime/traces/get returns session journal entries", func(t *testing.T) {
		gw.Journal = wisdev.NewRuntimeJournal(nil)
		session, err := gw.CreateSession(context.Background(), "u1", "trace query")
		assert.NoError(t, err)
		gw.Journal.Append(wisdev.RuntimeJournalEntry{
			EventID:   "evt-1",
			SessionID: session.SessionID,
			UserID:    "u1",
			EventType: "step_started",
			Status:    "ok",
			Summary:   "Planning retrieval strategy",
			CreatedAt: 123,
		})

		req := httptest.NewRequest(http.MethodPost, "/runtime/traces/get", bytes.NewReader([]byte(`{"sessionId":"`+session.SessionID+`","limit":10}`)))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp struct {
			Traces []map[string]any `json:"traces"`
		}
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		if assert.NotEmpty(t, resp.Traces) {
			found := false
			for _, trace := range resp.Traces {
				if trace["eventId"] == "evt-1" {
					found = true
					assert.Equal(t, session.SessionID, trace["sessionId"])
					assert.Equal(t, "Planning retrieval strategy", trace["summary"])
				}
			}
			assert.True(t, found, "expected evt-1 to be present in traces response")
		}
	})

	t.Run("POST /runtime/traces/get denies non-owner", func(t *testing.T) {
		session, err := gw.CreateSession(context.Background(), "u1", "trace query")
		assert.NoError(t, err)
		gw.Journal = wisdev.NewRuntimeJournal(nil)
		gw.Journal.Append(wisdev.RuntimeJournalEntry{
			EventID:   "evt-unauthorized",
			SessionID: session.SessionID,
			UserID:    "u1",
			EventType: "step_started",
			Status:    "ok",
			Summary:   "Private trace",
			CreatedAt: 124,
		})

		req := httptest.NewRequest(http.MethodPost, "/runtime/traces/get", bytes.NewReader([]byte(`{"sessionId":"`+session.SessionID+`","limit":10}`)))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u2"))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrUnauthorized, resp.Error.Code)
	})

	t.Run("GET /policy/get - owner mismatch", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/policy/get?userId=u2", nil)
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrUnauthorized, resp.Error.Code)
	})

	t.Run("GET /policy/get - anonymous caller denied", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/policy/get?userId=u1", nil)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrUnauthorized, resp.Error.Code)
	})

	t.Run("POST /policy/upsert - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/policy/upsert", bytes.NewReader([]byte(`{bad`)))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "internal-service")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /policy/upsert - non-internal caller denied", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/policy/upsert", bytes.NewReader([]byte(`{}`)))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrUnauthorized, resp.Error.Code)
	})

	t.Run("POST /policy/canary-config/upsert - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/policy/canary-config/upsert", bytes.NewReader([]byte(`{bad`)))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "internal-service")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("PUT /policy/function-bridge-config/get - method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/policy/function-bridge-config/get", nil)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /policy/function-bridge-config/upsert - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/policy/function-bridge-config/upsert", bytes.NewReader([]byte(`{bad`)))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "internal-service")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /policy/promote - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/policy/promote", bytes.NewReader([]byte(`{bad`)))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "internal-service")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /policy/rollback - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/policy/rollback", bytes.NewReader([]byte(`{bad`)))
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

func TestWisDev_PolicyHandlers_DefaultPolicyBundleWithoutGateway(t *testing.T) {
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/policy/get?userId=u1", nil)
	req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		PolicyBundle map[string]any `json:"policyBundle"`
	}
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotNil(t, resp.PolicyBundle)
	assert.Equal(t, policy.DefaultPolicyConfig().PolicyVersion, resp.PolicyBundle["policyVersion"])
}

func TestWisDev_PlanHandlers(t *testing.T) {
	gw := &wisdev.AgentGateway{
		Store:        wisdev.NewInMemorySessionStore(),
		PolicyConfig: policy.DefaultPolicyConfig(),
	}
	ragH := NewRAGHandler(nil)
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, ragH, nil)

	t.Run("POST /wisdev/plan", func(t *testing.T) {
		body := `{"query":"test query", "userId":"u1"}`
		req := httptest.NewRequest(http.MethodPost, "/wisdev/plan", bytes.NewReader([]byte(body)))
		req = withTestUserID(req, "u1")
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.NotNil(t, resp["plan"])
	})

	t.Run("POST /wisdev/plan requires authenticated owner", func(t *testing.T) {
		body := `{"query":"test query", "userId":"u1"}`
		req := httptest.NewRequest(http.MethodPost, "/wisdev/plan", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrUnauthorized, resp.Error.Code)
	})

	t.Run("POST /wisdev/plan - missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/plan", bytes.NewReader([]byte(`{"userId":"u1"}`)))
		req = withTestUserID(req, "u1")
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})
}
