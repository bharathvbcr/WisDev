package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
)

func TestWisDevEvidenceDossierRoute(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "wisdev_evidence_route")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	os.Setenv("WISDEV_STATE_DIR", tmpDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	stateStore := wisdev.NewRuntimeStateStore(nil, nil)
	gw := &wisdev.AgentGateway{
		Store:        wisdev.NewInMemorySessionStore(),
		StateStore:   stateStore,
		PolicyConfig: policy.DefaultPolicyConfig(),
		SessionTTL:   time.Hour,
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	payload := map[string]any{
		"dossierId": "dossier_test_1",
		"jobId":     "job_test_1",
		"userId":    "u1",
		"query":     "reward modeling",
		"coverageMetrics": map[string]any{
			"sourceCount": 1,
		},
	}
	if err := stateStore.SaveEvidenceDossier("dossier_test_1", payload); err != nil {
		t.Fatalf("failed to seed evidence dossier: %v", err)
	}

	t.Run("GET returns dossier for owner", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/evidence-dossier?dossierId=dossier_test_1", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var body map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		_, ok := body["evidenceDossier"].(map[string]any)
		assert.True(t, ok)
	})

	t.Run("GET blocks non-owner", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/evidence-dossier?dossierId=dossier_test_1", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u2"))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("GET requires dossierId", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/evidence-dossier", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}
