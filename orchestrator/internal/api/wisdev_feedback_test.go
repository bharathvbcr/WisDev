package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func TestWisDev_FeedbackHandlers(t *testing.T) {
	gw := &wisdev.AgentGateway{
		Store:        wisdev.NewInMemorySessionStore(),
		PolicyConfig: policy.DefaultPolicyConfig(),
		Idempotency:  wisdev.NewIdempotencyStore(1 * time.Hour),
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	t.Run("POST /feedback/save - Success and Idempotency", func(t *testing.T) {
		body := `{"userId":"u1", "sessionId":"s1", "rating":5}`
		req := httptest.NewRequest(http.MethodPost, "/feedback/save", bytes.NewReader([]byte(body)))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		// Second call - Idempotency hit
		req2 := httptest.NewRequest(http.MethodPost, "/feedback/save", bytes.NewReader([]byte(body)))
		req2 = req2.WithContext(ctx)
		rec2 := httptest.NewRecorder()
		mux.ServeHTTP(rec2, req2)
		assert.Equal(t, http.StatusOK, rec2.Code)
	})

	t.Run("POST /memory/profile/learn - Success", func(t *testing.T) {
		body := `{"userId":"u1", "conversation":{"detectedDomain":"cs"}}`
		req := httptest.NewRequest(http.MethodPost, "/memory/profile/learn", bytes.NewReader([]byte(body)))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}
