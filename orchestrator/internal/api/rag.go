package api

import (
	"context"
	"encoding/json"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/rag"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
	"net/http"
	"strings"
)

type Engine interface {
	GenerateAnswer(ctx context.Context, req rag.AnswerRequest) (*rag.AnswerResponse, error)
	MultiAgentExecute(ctx context.Context, req rag.AnswerRequest) (*rag.AnswerResponse, error)
	SelectSectionContext(ctx context.Context, req rag.SectionContextRequest) (*rag.SectionContextResponse, error)
	GetRaptor() *rag.RaptorService
	GetBM25() *rag.BM25
}

type RAGHandler struct {
	engine  Engine
	gateway *wisdev.AgentGateway
}

func NewRAGHandler(engine Engine) *RAGHandler {
	return &RAGHandler{engine: engine}
}

func (h *RAGHandler) WithAgentGateway(gateway *wisdev.AgentGateway) *RAGHandler {
	if h == nil {
		return nil
	}
	h.gateway = gateway
	return h
}

func (h *RAGHandler) HandleRaptorBuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req rag.RaptorBuildRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	if req.MinClusters <= 0 {
		req.MinClusters = 3
	}

	raptor := h.engine.GetRaptor()
	if raptor == nil {
		WriteError(w, http.StatusInternalServerError, ErrRagFailed, "raptor engine not available", nil)
		return
	}

	tree, _ := raptor.BuildTree(r.Context(), req.Papers, req.MinClusters)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tree)
}

func (h *RAGHandler) HandleRaptorQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req rag.RaptorQueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	results, err := h.engine.GetRaptor().QueryTree(req.TreeID, req.QueryEmbedding, req.TopK, req.Levels)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrRagFailed, "raptor query failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"results": results})
}

func (h *RAGHandler) HandleBM25Index(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req rag.BM25IndexRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	h.engine.GetBM25().IndexDocuments(req.Documents, req.DocIds)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "indexed"})
}

func (h *RAGHandler) HandleBM25Search(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req rag.BM25QueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	results := h.engine.GetBM25().Search(req.Query, req.TopK)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"results": results})
}

func (h *RAGHandler) HandleAdaptiveChunking(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req struct {
		Text        string `json:"text"`
		PaperID     string `json:"paperId"`
		InitialSize int    `json:"initialSize"`
		Overlap     int    `json:"overlap"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	if req.InitialSize <= 0 {
		req.InitialSize = 2048
	}

	chunks := rag.AdaptiveChunking(req.Text, req.PaperID, req.InitialSize, req.Overlap)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"chunks": chunks})
}

func (h *RAGHandler) HandleAnswer(w http.ResponseWriter, r *http.Request) {
	if IsDegraded(r.Context()) {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "LLM sidecar is currently unavailable. Synthesis features are disabled.", nil)
		return
	}
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req rag.AnswerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	if strings.TrimSpace(req.Query) == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
			"field": "query",
		})
		return
	}
	if authUserID := strings.TrimSpace(GetUserID(r)); authUserID != "" && authUserID != "anonymous" {
		req.UserID = authUserID
	}

	resp, err := h.engine.GenerateAnswer(r.Context(), req)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrRagFailed, "rag generation failed", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if strings.TrimSpace(resp.TraceID) == "" {
		resp.TraceID = newTraceID()
	}
	if resp.Metadata == nil {
		resp.Metadata = &rag.ResponseMetadata{
			Backend:           "go-rag",
			FallbackTriggered: false,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Trace-Id", resp.TraceID)
	json.NewEncoder(w).Encode(resp)
}

func (h *RAGHandler) HandleMultiAgent(w http.ResponseWriter, r *http.Request) {
	if IsDegraded(r.Context()) {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "LLM sidecar is currently unavailable. Multi-agent research is disabled.", nil)
		return
	}
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req struct {
		Query          string `json:"query"`
		DomainHint     string `json:"domainHint"`
		MaxIterations  int    `json:"maxIterations"`
		IncludeAnalyst bool   `json:"includeAnalyst"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
			"field": "query",
		})
		return
	}
	if h.gateway == nil {
		answerReq := rag.AnswerRequest{
			Query: strings.TrimSpace(req.Query),
		}
		if authUserID := strings.TrimSpace(GetUserID(r)); authUserID != "" && authUserID != "anonymous" {
			answerReq.UserID = authUserID
		}
		engineResp, engineErr := h.engine.MultiAgentExecute(r.Context(), answerReq)
		if engineErr != nil {
			WriteError(w, http.StatusInternalServerError, ErrRagFailed, "rag multi-agent execution failed", map[string]any{
				"error": engineErr.Error(),
			})
			return
		}
		if strings.TrimSpace(engineResp.TraceID) == "" {
			engineResp.TraceID = newTraceID()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Trace-Id", engineResp.TraceID)
		json.NewEncoder(w).Encode(engineResp)
		return
	}
	resp, err := executeWisdevMultiAgentSwarm(
		r.Context(),
		h.gateway,
		req.Query,
		req.DomainHint,
		req.MaxIterations,
		req.IncludeAnalyst,
	)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrRagFailed, "rag multi-agent execution failed", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if strings.TrimSpace(resp.TraceID) == "" {
		resp.TraceID = newTraceID()
	}
	if resp.Metadata == nil {
		resp.Metadata = &rag.ResponseMetadata{
			Backend:           "go-rag-multi-agent",
			FallbackTriggered: false,
		}
	} else if strings.TrimSpace(resp.Metadata.Backend) == "" {
		resp.Metadata.Backend = "go-rag-multi-agent"
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Trace-Id", resp.TraceID)
	json.NewEncoder(w).Encode(resp)
}

func (h *RAGHandler) HandleSectionContext(w http.ResponseWriter, r *http.Request) {
	if IsDegraded(r.Context()) {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "LLM sidecar is currently unavailable. Section context selection is disabled.", nil)
		return
	}
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req rag.SectionContextRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	resp, err := h.engine.SelectSectionContext(r.Context(), req)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrRagFailed, "section context selection failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
