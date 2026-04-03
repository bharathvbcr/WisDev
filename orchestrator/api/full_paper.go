package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
)

type FullPaperHandler struct {
	searchReg *search.ProviderRegistry
}

func NewFullPaperHandler(reg *search.ProviderRegistry) *FullPaperHandler {
	return &FullPaperHandler{searchReg: reg}
}

func (h *FullPaperHandler) HandleFullPaperRetrieval(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFullPaperRequestBodyBytes)
	var req FullPaperRetrievalRequest
	if err := decodeStrictJSONBody(r.Body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	result, err := h.runFullPaperRetrieval(r.Context(), req)
	if err != nil {
		status := http.StatusInternalServerError
		code := ErrDependencyFailed
		if err.Error() == "query is required" {
			status = http.StatusBadRequest
			code = ErrInvalidParameters
		}
		WriteError(w, status, code, "full paper retrieval failed", map[string]any{
			"error": err.Error(),
		})
		return
	}
	writeJSONResponse(w, http.StatusOK, result)
}

func (h *FullPaperHandler) runFullPaperRetrieval(ctx context.Context, req FullPaperRetrievalRequest) (*FullPaperRetrievalResponse, error) {
	if req.Query == "" {
		return nil, errors.New("query is required")
	}
	queries := normalizeFullPaperQueries(req.Query, req.PlanQueries)
	limit := boundedFullPaperLimit(req.Limit)

	opts := search.SearchOpts{
		Limit:       limit,
		QualitySort: true,
		Domain:      req.Domain,
	}

	allPapers := []search.Paper{}
	for _, q := range queries {
		res := search.ParallelSearch(ctx, h.searchReg, q, opts)
		allPapers = append(allPapers, res.Papers...)
	}

	deduped := search.Deduplicate(allPapers)

	return &FullPaperRetrievalResponse{
		JobID:              req.JobID,
		SessionID:          req.SessionID,
		Query:              req.Query,
		NormalizedQueries:  queries,
		DeduplicatedPapers: deduped,
		GeneratedAt:        time.Now().UnixMilli(),
	}, nil
}

// ... more types and helpers ...

const (
	maxFullPaperRequestBodyBytes = 1 << 20
)

type FullPaperRetrievalRequest struct {
	JobID       string   `json:"jobId"`
	SessionID   string   `json:"sessionId"`
	Query       string   `json:"query"`
	PlanQueries []string `json:"planQueries,omitempty"`
	Domain      string   `json:"domain,omitempty"`
	Limit       int      `json:"limit,omitempty"`
}

type FullPaperRetrievalResponse struct {
	JobID              string         `json:"jobId"`
	SessionID          string         `json:"sessionId"`
	Query              string         `json:"query"`
	NormalizedQueries  []string       `json:"normalizedQueries"`
	DeduplicatedPapers []search.Paper `json:"deduplicatedPapers"`
	GeneratedAt        int64          `json:"generatedAt"`
}

func normalizeFullPaperQueries(query string, planQueries []string) []string {
	out := []string{query}
	out = append(out, planQueries...)
	return out
}

func boundedFullPaperLimit(limit int) int {
	if limit <= 0 {
		return 10
	}
	if limit > 50 {
		return 50
	}
	return limit
}
