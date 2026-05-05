package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/rag"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func TestWisDev_RAGHandlers(t *testing.T) {
	reg := search.NewProviderRegistry()
	reg.Register(&MockProvider{
		name: "mock1",
		papers: []search.Paper{
			{ID: "1", Title: "P1", DOI: "10.1/1", Source: "mock1"},
		},
	})

	gw := &wisdev.AgentGateway{
		Store:          wisdev.NewInMemorySessionStore(),
		PolicyConfig:   policy.DefaultPolicyConfig(),
		Registry:       wisdev.NewToolRegistry(),
		SearchRegistry: reg,
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	t.Run("POST /rag/retrieve - Success", func(t *testing.T) {
		body := `{"query":"test", "domain":"biology", "limit":5}`
		req := httptest.NewRequest(http.MethodPost, "/rag/retrieve", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.NotNil(t, resp["retrieval"])
		retrieval := resp["retrieval"].(map[string]any)
		assert.Equal(t, "go-wisdev-canonical", retrieval["backend"])
		assert.Equal(t, "wisdev.mcp.paper_retrieval.v1", retrieval["contract"])
	})

	t.Run("POST /rag/hybrid - Success", func(t *testing.T) {
		body := `{"query":"test", "documents":[{"id":"1", "title":"T1"}]}`
		req := httptest.NewRequest(http.MethodPost, "/rag/hybrid", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.NotNil(t, resp["hybrid"])
		hybrid := resp["hybrid"].(map[string]any)
		results := hybrid["results"].([]any)
		assert.NotEmpty(t, results)
		assert.Equal(t, "mock1", results[0].(map[string]any)["source"])
	})

	t.Run("POST /rag/crag - Success", func(t *testing.T) {
		body := `{"query":"test", "documents":[]}`
		req := httptest.NewRequest(http.MethodPost, "/rag/crag", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.NotNil(t, resp["crag"])
		crag := resp["crag"].(map[string]any)
		results := crag["results"].([]any)
		assert.NotEmpty(t, results)
		assert.Equal(t, "mock1", results[0].(map[string]any)["source"])
	})

	t.Run("POST /rag/agentic-hybrid - Success", func(t *testing.T) {
		body := `{"query":"test", "domain":"cs", "retrievalMode":"parallel", "fusionMode":"rrf"}`
		req := httptest.NewRequest(http.MethodPost, "/rag/agentic-hybrid", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.NotNil(t, resp["agenticHybrid"])
		agentic := resp["agenticHybrid"].(map[string]any)
		assert.Equal(t, float64(1), agentic["totalFound"])
	})

	t.Run("POST /wisdev/rag/evidence-gate - Success", func(t *testing.T) {
		body := `{"claims":[], "contradictionCount":0}`
		req := httptest.NewRequest(http.MethodPost, "/wisdev/rag/evidence-gate", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.NotNil(t, resp["evidenceGate"])
	})
}

func TestWisDev_RAGHandlers_RegisterCanonicalGroundedAnswerRoutes(t *testing.T) {
	engine := new(mockEngine)
	ragHandler := NewRAGHandler(engine)
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, nil, ragHandler, nil)

	t.Run("POST /wisdev/research/grounded-answer - Success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/research/grounded-answer", strings.NewReader(`{"query":"test"}`))
		rec := httptest.NewRecorder()

		engine.On("GenerateAnswer", mock.Anything, mock.Anything).Return(&rag.AnswerResponse{Answer: "ans"}, nil).Once()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("POST /wisdev/research/section-context - Success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/research/section-context", strings.NewReader(`{"sectionName":"intro"}`))
		rec := httptest.NewRecorder()

		engine.On("SelectSectionContext", mock.Anything, mock.Anything).Return(&rag.SectionContextResponse{SectionName: "intro"}, nil).Once()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

func TestWisDev_RAGHandlers_NilGatewayStillServesSearchRoutes(t *testing.T) {
	originalParallelSearch := wisdev.ParallelSearch
	originalRetrieveCanonicalPapers := wisdev.RetrieveCanonicalPapers
	t.Cleanup(func() {
		wisdev.ParallelSearch = originalParallelSearch
		wisdev.RetrieveCanonicalPapers = originalRetrieveCanonicalPapers
	})

	wisdev.ParallelSearch = func(_ context.Context, rdb redis.UniversalClient, query string, opts wisdev.SearchOptions) (*wisdev.MultiSourceResult, error) {
		assert.Nil(t, rdb)
		assert.Equal(t, "test", query)
		return &wisdev.MultiSourceResult{
			Papers: []wisdev.Source{{ID: "p1", Title: "P1", Source: "mock"}},
		}, nil
	}
	wisdev.RetrieveCanonicalPapers = func(_ context.Context, rdb redis.UniversalClient, query string, limit int) ([]wisdev.Source, map[string]any, error) {
		assert.Nil(t, rdb)
		assert.Equal(t, "test", query)
		assert.Positive(t, limit)
		return []wisdev.Source{{ID: "p1", Title: "P1", Source: "mock"}}, nil, nil
	}

	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, nil, nil, nil)

	t.Run("POST /rag/retrieve", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/rag/retrieve", strings.NewReader(`{"query":"test","limit":5}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("POST /rag/hybrid", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/rag/hybrid", strings.NewReader(`{"query":"test","documents":[{"id":"1"}]}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("POST /rag/crag", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/rag/crag", strings.NewReader(`{"query":"test","documents":[]}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("POST /rag/agentic-hybrid", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/rag/agentic-hybrid", strings.NewReader(`{"query":"test","domain":"cs"}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}
