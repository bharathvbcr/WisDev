package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
	"github.com/stretchr/testify/assert"
)

func TestWisDevV2_CoreHandlers(t *testing.T) {
	gw := &wisdev.AgentGateway{
		Store:        wisdev.NewInMemorySessionStore(),
		PolicyConfig: policy.DefaultPolicyConfig(),
	}
	mux := http.NewServeMux()
	RegisterV2WisDevRoutes(mux, gw, nil)

	t.Run("POST /v2/wisdev/decide - Success with session", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "test query")
		session.Plan = &wisdev.PlanState{
			PlanID: "p1",
			Steps: []wisdev.PlanStep{
				{ID: "s1", Action: "search", Risk: "low"},
			},
		}
		_ = gw.Store.Put(context.Background(), session, gw.SessionTTL)

		body := map[string]any{
			"sessionId": session.SessionID,
			"plan": map[string]any{
				"planId": "p1",
				"steps": []map[string]any{
					{"id": "s1", "risk": "low"},
				},
			},
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/decide", bytes.NewReader(jsonBody))
		rec := httptest.NewRecorder()
		
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		
		result := resp["decision"].(map[string]any)
		assert.Equal(t, "s1", result["selectedStepId"])
	})

	t.Run("POST /v2/wisdev/critique - Success", func(t *testing.T) {
		body := map[string]any{
			"query": "climate change",
			"decision": map[string]any{
				"rationale": "testing",
				"confidence": 0.8,
			},
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/critique", bytes.NewReader(jsonBody))
		rec := httptest.NewRecorder()
		
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.NotNil(t, resp["critique"])
	})

	t.Run("POST /v2/wisdev/decide - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/decide", bytes.NewReader([]byte(`{bad`)))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /v2/wisdev/critique - missing query", func(t *testing.T) {
		body := map[string]any{
			"decision": map[string]any{
				"rationale": "",
			},
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/critique", bytes.NewReader(jsonBody))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})
}
