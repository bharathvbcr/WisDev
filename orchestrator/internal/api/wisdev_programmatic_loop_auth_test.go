package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func TestWisDevProgrammaticLoopRequiresSessionOwner(t *testing.T) {
	gw := &wisdev.AgentGateway{
		Store:      wisdev.NewInMemorySessionStore(),
		SessionTTL: time.Hour,
		PythonExecute: func(_ context.Context, _ string, _ map[string]any, _ *wisdev.AgentSession) (map[string]any, error) {
			return map[string]any{
				"tasks": []any{
					map[string]any{"name": "branch"},
				},
			}, nil
		},
	}
	session, err := gw.CreateSession(context.Background(), "u1", "sleep and memory")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	body, _ := json.Marshal(map[string]any{
		"action":    "research.queryDecompose",
		"query":     "sleep memory",
		"sessionId": session.SessionID,
	})
	req := httptest.NewRequest(http.MethodPost, "/wisdev/programmatic-loop", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u2"))
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	var resp APIError
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, ErrUnauthorized, resp.Error.Code)
}
