package api

import (
	"encoding/json"
	"net/http"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
	"strings"
	"time"
)

func (h *SearchHandler) HandleAggressiveExpansion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req wisdev.AggressiveExpansionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
			"field": "query",
		})
		return
	}

	// Apply defaults
	maxVariations := req.MaxVariations
	if maxVariations <= 0 {
		maxVariations = 15
	}
	includeMeSH := req.IncludeMeSH
	if !includeMeSH && req.MaxVariations == 0 {
		includeMeSH = true
	}
	targetAPIs := req.TargetAPIs
	if len(targetAPIs) == 0 {
		targetAPIs = []string{"semanticscholar", "openalex", "pubmed", "arxiv"}
	}

	result := wisdev.GenerateAggressiveExpansion(h.redis, query, maxVariations, includeMeSH, req.IncludeAbbreviations, req.IncludeTemporal, targetAPIs)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *SearchHandler) HandleSPLADEExpansion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req wisdev.SPLADEExpansionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
			"field": "query",
		})
		return
	}

	start := time.Now()
	enhanced := wisdev.ExpandQuery(query)

	resp := wisdev.SPLADEExpansionResponse{
		Original:  enhanced.Original,
		Expanded:  enhanced.Expanded,
		Intent:    enhanced.Intent,
		Keywords:  enhanced.Keywords,
		Synonyms:  enhanced.Synonyms,
		LatencyMs: time.Since(start).Milliseconds(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
