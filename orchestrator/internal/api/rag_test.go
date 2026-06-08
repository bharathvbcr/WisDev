package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/rag"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/resilience"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
)

type flexibleEngine struct {
	genAnswerFn  func(context.Context, rag.AnswerRequest) (*rag.AnswerResponse, error)
	multiAgentFn func(context.Context, rag.AnswerRequest) (*rag.AnswerResponse, error)
	selectCtxFn  func(context.Context, rag.SectionContextRequest) (*rag.SectionContextResponse, error)
	bm25         *rag.BM25
	raptor       *rag.RaptorService
}

func (f *flexibleEngine) GenerateAnswer(ctx context.Context, r rag.AnswerRequest) (*rag.AnswerResponse, error) {
	return f.genAnswerFn(ctx, r)
}
func (f *flexibleEngine) MultiAgentExecute(ctx context.Context, r rag.AnswerRequest) (*rag.AnswerResponse, error) {
	return f.multiAgentFn(ctx, r)
}
func (f *flexibleEngine) SelectSectionContext(ctx context.Context, r rag.SectionContextRequest) (*rag.SectionContextResponse, error) {
	return f.selectCtxFn(ctx, r)
}
func (f *flexibleEngine) GetBM25() *rag.BM25            { return f.bm25 }
func (f *flexibleEngine) GetRaptor() *rag.RaptorService { return f.raptor }

type swarmSearchProvider struct{}

func (swarmSearchProvider) Name() string { return "swarm-search" }

func (swarmSearchProvider) Search(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
	return []search.Paper{
		{
			ID:       "paper-1",
			Title:    "Sleep and memory consolidation",
			Abstract: "Controlled studies report stronger recall after sleep.",
			Source:   "swarm-search",
			Score:    0.91,
		},
	}, nil
}

func (swarmSearchProvider) Domains() []string { return []string{"general"} }
func (swarmSearchProvider) Healthy() bool     { return true }
func (swarmSearchProvider) Tools() []string   { return nil }

func TestRAGHandler(t *testing.T) {
	fe := &flexibleEngine{
		bm25:   rag.NewBM25(),
		raptor: rag.NewRaptorService(nil),
	}
	h := NewRAGHandler(fe)

	t.Run("WithAgentGateway nil receiver", func(t *testing.T) {
		var nilHandler *RAGHandler
		assert.Nil(t, nilHandler.WithAgentGateway(&wisdev.AgentGateway{}))
	})

	t.Run("HandleBM25Index - Success", func(t *testing.T) {
		body := `{"documents":["d1"], "docIds":["id1"]}`
		req := httptest.NewRequest(http.MethodPost, "/bm25/index", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		h.HandleBM25Index(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleBM25Index - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/bm25/index", nil)
		rec := httptest.NewRecorder()
		h.HandleBM25Index(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("HandleBM25Index - Invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/bm25/index", bytes.NewReader([]byte("{invalid")))
		rec := httptest.NewRecorder()
		h.HandleBM25Index(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("HandleBM25Search - Success", func(t *testing.T) {
		body := `{"query":"test", "topK":1}`
		req := httptest.NewRequest(http.MethodPost, "/bm25/search", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		h.HandleBM25Search(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleBM25Search - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/bm25/search", nil)
		rec := httptest.NewRecorder()
		h.HandleBM25Search(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("HandleBM25Search - Invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/bm25/search", bytes.NewReader([]byte("{invalid")))
		rec := httptest.NewRecorder()
		h.HandleBM25Search(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("HandleAdaptiveChunking - Success", func(t *testing.T) {
		body := `{"text":"some long text", "paperId":"p1"}`
		req := httptest.NewRequest(http.MethodPost, "/chunking", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		h.HandleAdaptiveChunking(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleAdaptiveChunking - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/chunking", nil)
		rec := httptest.NewRecorder()
		h.HandleAdaptiveChunking(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("HandleAdaptiveChunking - Invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/chunking", bytes.NewReader([]byte("{invalid")))
		rec := httptest.NewRecorder()
		h.HandleAdaptiveChunking(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("HandleRaptorBuild - Success", func(t *testing.T) {
		body := `{"papers":[{"paper_id":"p1", "chunks":[{"content":"alpha beta","embedding":[1,0],"chunk_id":"c1"}]}], "minClusters":1}`
		req := httptest.NewRequest(http.MethodPost, "/raptor/build", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		h.HandleRaptorBuild(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		var tree rag.RaptorTree
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &tree))
		assert.NotEmpty(t, tree.ID)
		assert.NotNil(t, tree.Root)
		assert.GreaterOrEqual(t, tree.Levels, 1)
	})

	t.Run("HandleRaptorBuild - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/raptor/build", nil)
		rec := httptest.NewRecorder()
		h.HandleRaptorBuild(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("HandleRaptorBuild - Invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/raptor/build", bytes.NewReader([]byte("{invalid")))
		rec := httptest.NewRecorder()
		h.HandleRaptorBuild(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("HandleRaptorBuild - Raptor Missing", func(t *testing.T) {
		missing := NewRAGHandler(&flexibleEngine{bm25: rag.NewBM25()})
		req := httptest.NewRequest(http.MethodPost, "/raptor/build", bytes.NewReader([]byte(`{"papers":[]}`)))
		rec := httptest.NewRecorder()
		missing.HandleRaptorBuild(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("HandleRaptorQuery - Success", func(t *testing.T) {
		raptor := rag.NewRaptorService(nil)
		tree, err := raptor.BuildTree(context.Background(), []rag.PaperChunksRequest{
			{
				PaperID: "paper-1",
				Chunks: []rag.ChunkDetails{
					{ID: "chunk-1", Content: "alpha beta", Embedding: []float64{1, 0}},
					{ID: "chunk-2", Content: "beta gamma", Embedding: []float64{0.8, 0.2}},
				},
			},
		}, 1)
		assert.NoError(t, err)

		queryReq := httptest.NewRequest(http.MethodPost, "/raptor/query", bytes.NewReader([]byte(`{
			"treeId":"`+tree.ID+`",
			"queryEmbedding":[1,0],
			"topK":1,
			"levels":[0,1,2,3]
		}`)))
		rec := httptest.NewRecorder()

		hh := NewRAGHandler(&flexibleEngine{raptor: raptor, bm25: rag.NewBM25()})
		hh.HandleRaptorQuery(rec, queryReq)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		results := resp["results"].([]any)
		assert.Len(t, results, 1)
		first := results[0].(map[string]any)
		assert.NotEmpty(t, first["node_id"])
		assert.NotEmpty(t, first["score"])
	})

	t.Run("HandleRaptorQuery - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/raptor/query", nil)
		rec := httptest.NewRecorder()
		h.HandleRaptorQuery(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("HandleRaptorQuery - Invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/raptor/query", bytes.NewReader([]byte("{invalid")))
		rec := httptest.NewRecorder()
		h.HandleRaptorQuery(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("HandleRaptorQuery - Tree Missing", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/raptor/query", bytes.NewReader([]byte(`{"treeId":"missing","queryEmbedding":[0.1],"topK":1}`)))
		rec := httptest.NewRecorder()
		h.HandleRaptorQuery(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("HandleAnswer - Degraded", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/answer", nil)
		req = req.WithContext(resilience.SetDegraded(req.Context(), true))
		rec := httptest.NewRecorder()
		h.HandleAnswer(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("HandleAnswer - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/answer", nil)
		rec := httptest.NewRecorder()
		h.HandleAnswer(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleAnswer - Success", func(t *testing.T) {
		body := `{"query":"test"}`
		req := httptest.NewRequest(http.MethodPost, "/answer", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()

		fe.genAnswerFn = func(ctx context.Context, r rag.AnswerRequest) (*rag.AnswerResponse, error) {
			return &rag.AnswerResponse{Answer: "done"}, nil
		}

		h.HandleAnswer(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp rag.AnswerResponse
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Equal(t, "done", resp.Answer)
		assert.NotEmpty(t, resp.TraceID)
		assert.NotNil(t, resp.Metadata)
	})

	t.Run("HandleAnswer - Injects Authenticated User", func(t *testing.T) {
		body := `{"query":"test","userId":"spoofed"}`
		req := httptest.NewRequest(http.MethodPost, "/answer", bytes.NewReader([]byte(body)))
		req = withTestUserID(req, "auth-user")
		rec := httptest.NewRecorder()

		fe.genAnswerFn = func(ctx context.Context, r rag.AnswerRequest) (*rag.AnswerResponse, error) {
			assert.Equal(t, "auth-user", r.UserID)
			return &rag.AnswerResponse{Answer: "done"}, nil
		}

		h.HandleAnswer(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleAnswer - Invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/answer", bytes.NewReader([]byte("{invalid")))
		rec := httptest.NewRecorder()
		h.HandleAnswer(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleAnswer - Missing Query", func(t *testing.T) {
		body := `{"query":""}`
		req := httptest.NewRequest(http.MethodPost, "/answer", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		h.HandleAnswer(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("HandleAnswer - Engine Error", func(t *testing.T) {
		body := `{"query":"test"}`
		req := httptest.NewRequest(http.MethodPost, "/answer", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		fe.genAnswerFn = func(ctx context.Context, r rag.AnswerRequest) (*rag.AnswerResponse, error) {
			return nil, errors.New("engine fail")
		}
		h.HandleAnswer(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrRagFailed, resp.Error.Code)
	})

	t.Run("HandleMultiAgent - Success", func(t *testing.T) {
		body := `{"query":"test"}`
		req := httptest.NewRequest(http.MethodPost, "/multi", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		fe.multiAgentFn = func(ctx context.Context, r rag.AnswerRequest) (*rag.AnswerResponse, error) {
			assert.Empty(t, r.UserID)
			return &rag.AnswerResponse{Answer: "multi done"}, nil
		}
		h.HandleMultiAgent(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleMultiAgent - Injects Authenticated User", func(t *testing.T) {
		body := `{"query":"test"}`
		req := httptest.NewRequest(http.MethodPost, "/multi", bytes.NewReader([]byte(body)))
		req = withTestUserID(req, "auth-user")
		rec := httptest.NewRecorder()
		fe.multiAgentFn = func(ctx context.Context, r rag.AnswerRequest) (*rag.AnswerResponse, error) {
			assert.Equal(t, "auth-user", r.UserID)
			return &rag.AnswerResponse{Answer: "multi done"}, nil
		}
		h.HandleMultiAgent(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleMultiAgent - Gateway Swarm Path", func(t *testing.T) {
		body := `{"query":"test swarm","domainHint":"cs","maxIterations":4,"includeAnalyst":true}`
		req := httptest.NewRequest(http.MethodPost, "/multi", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()

		registry := search.NewProviderRegistry()
		registry.Register(swarmSearchProvider{})
		registry.SetDefaultOrder([]string{"swarm-search"})
		gatewayHandler := NewRAGHandler(fe).WithAgentGateway(&wisdev.AgentGateway{SearchRegistry: registry})
		fe.multiAgentFn = func(context.Context, rag.AnswerRequest) (*rag.AnswerResponse, error) {
			t.Fatalf("expected gateway-backed swarm path to bypass fallback engine")
			return nil, nil
		}

		gatewayHandler.HandleMultiAgent(rec, req)
		assert.Equalf(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

		var resp rag.AnswerResponse
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "test swarm", resp.Query)
		assert.NotEmpty(t, resp.Answer)
		assert.NotEmpty(t, resp.TraceID)
		if assert.NotNil(t, resp.Metadata) {
			assert.Equal(t, "go-wisdev-unified-runtime", resp.Metadata.Backend)
			if assert.NotNil(t, resp.Metadata.Policy) {
				assert.Equal(t, "unified_runtime_blackboard", resp.Metadata.Policy["coverageModel"])
				assert.NotEmpty(t, resp.Metadata.Policy["observedSourceFamilies"])
				if count, ok := resp.Metadata.Policy["observedEvidenceCount"].(float64); ok {
					assert.Greater(t, count, 0.0)
				} else {
					t.Fatalf("expected observedEvidenceCount to decode as number, got %T", resp.Metadata.Policy["observedEvidenceCount"])
				}
				ledger, ok := resp.Metadata.Policy["coverageLedger"].([]any)
				if !ok {
					t.Fatalf("expected coverageLedger to decode as array, got %T", resp.Metadata.Policy["coverageLedger"])
				}
				assert.NotEmpty(t, ledger)
				assert.NotEmpty(t, resp.Metadata.Policy["followUpQueries"])
			}
		}
	})

	t.Run("HandleMultiAgent - Fails When Unified Runtime Is Unavailable", func(t *testing.T) {
		body := `{"query":"test unified runtime required","domainHint":"cs","maxIterations":4,"includeAnalyst":true}`
		req := httptest.NewRequest(http.MethodPost, "/multi", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()

		gatewayHandler := NewRAGHandler(fe).WithAgentGateway(&wisdev.AgentGateway{})

		gatewayHandler.HandleMultiAgent(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.Contains(t, rec.Body.String(), "wisdev_unified_runtime_unavailable")
	})

	t.Run("HandleMultiAgent - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/multi", nil)
		rec := httptest.NewRecorder()
		h.HandleMultiAgent(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("HandleMultiAgent - Degraded", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/multi", nil)
		req = req.WithContext(resilience.SetDegraded(req.Context(), true))
		rec := httptest.NewRecorder()
		h.HandleMultiAgent(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("HandleMultiAgent - Invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/multi", bytes.NewReader([]byte("{invalid")))
		rec := httptest.NewRecorder()
		h.HandleMultiAgent(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("HandleMultiAgent - Engine Error", func(t *testing.T) {
		body := `{"query":"test"}`
		req := httptest.NewRequest(http.MethodPost, "/multi", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		fe.multiAgentFn = func(ctx context.Context, r rag.AnswerRequest) (*rag.AnswerResponse, error) {
			return nil, errors.New("multi fail")
		}
		h.HandleMultiAgent(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("HandleSectionContext - Success", func(t *testing.T) {
		body := `{"sectionName":"S1"}`
		req := httptest.NewRequest(http.MethodPost, "/section", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		fe.selectCtxFn = func(ctx context.Context, r rag.SectionContextRequest) (*rag.SectionContextResponse, error) {
			return &rag.SectionContextResponse{SectionName: "S1"}, nil
		}
		h.HandleSectionContext(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleSectionContext - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/section", nil)
		rec := httptest.NewRecorder()
		h.HandleSectionContext(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("HandleSectionContext - Degraded", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/section", nil)
		req = req.WithContext(resilience.SetDegraded(req.Context(), true))
		rec := httptest.NewRecorder()
		h.HandleSectionContext(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("HandleSectionContext - Invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/section", bytes.NewReader([]byte("{invalid")))
		rec := httptest.NewRecorder()
		h.HandleSectionContext(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("HandleSectionContext - Engine Error", func(t *testing.T) {
		body := `{"sectionName":"S1"}`
		req := httptest.NewRequest(http.MethodPost, "/section", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		fe.selectCtxFn = func(ctx context.Context, r rag.SectionContextRequest) (*rag.SectionContextResponse, error) {
			return nil, errors.New("section fail")
		}
		h.HandleSectionContext(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}
