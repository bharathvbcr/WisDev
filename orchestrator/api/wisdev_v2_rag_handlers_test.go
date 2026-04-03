package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
	"github.com/stretchr/testify/assert"
)

func TestWisDevV2_RAGHandlers(t *testing.T) {
	reg := search.NewProviderRegistry()
	reg.Register(&MockProvider{
		name: "mock1",
		papers: []search.Paper{
			{ID: "1", Title: "P1", DOI: "10.1/1", Source: "mock1"},
		},
	})
	
	gw := &wisdev.AgentGateway{
		Store:        wisdev.NewInMemorySessionStore(),
		PolicyConfig: policy.DefaultPolicyConfig(),
		Registry:     wisdev.NewToolRegistry(),
	}
	mux := http.NewServeMux()
	RegisterV2WisDevRoutes(mux, gw, nil)

	t.Run("POST /v2/rag/retrieve - Success", func(t *testing.T) {
		body := `{"query":"test", "domain":"biology", "limit":5}`
		req := httptest.NewRequest(http.MethodPost, "/v2/rag/retrieve", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.NotNil(t, resp["retrieval"])
	})

	t.Run("POST /v2/rag/hybrid - Success", func(t *testing.T) {
		body := `{"query":"test", "documents":[{"id":"1", "title":"T1"}]}`
		req := httptest.NewRequest(http.MethodPost, "/v2/rag/hybrid", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.NotNil(t, resp["hybrid"])
	})

	t.Run("POST /v2/rag/crag - Success", func(t *testing.T) {
		body := `{"query":"test", "documents":[]}`
		req := httptest.NewRequest(http.MethodPost, "/v2/rag/crag", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.NotNil(t, resp["crag"])
	})

	t.Run("POST /v2/rag/agentic-hybrid - Success", func(t *testing.T) {
		body := `{"query":"test", "domain":"cs", "retrievalMode":"parallel", "fusionMode":"rrf"}`
		req := httptest.NewRequest(http.MethodPost, "/v2/rag/agentic-hybrid", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.NotNil(t, resp["agenticHybrid"])
	})

	t.Run("POST /v2/wisdev/rag/evidence-gate - Success", func(t *testing.T) {
		body := `{"claims":[], "contradictionCount":0}`
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/rag/evidence-gate", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.NotNil(t, resp["evidenceGate"])
	})
}
