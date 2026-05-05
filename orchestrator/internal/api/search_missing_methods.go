package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func resolveRequestTraceID(r *http.Request) string {
	for _, candidate := range []string{
		r.Header.Get("X-Trace-Id"),
		r.Header.Get("X-Amzn-Trace-Id"),
		r.Header.Get("traceparent"),
		r.URL.Query().Get("traceId"),
		r.URL.Query().Get("trace_id"),
	} {
		if traceID := strings.TrimSpace(candidate); traceID != "" {
			return traceID
		}
	}
	return fmt.Sprintf("req-%d", time.Now().UnixNano())
}

func searchQueryFingerprint(query string) string {
	hash := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(query))))
	return hex.EncodeToString(hash[:])
}

func (h *SearchHandler) HandleRecordClick(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSearchError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}
	var req struct {
		Query    string `json:"query"`
		PaperID  string `json:"paperId"`
		Provider string `json:"provider"`
		Rank     int    `json:"rank"`
		UserID   string `json:"userId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeSearchError(w, http.StatusBadRequest, "INVALID_BODY", "Failed to parse request body")
		return
	}
	query := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(req.Query))), " ")
	paperID := strings.TrimSpace(req.PaperID)
	if query == "" || paperID == "" {
		writeSearchError(w, http.StatusBadRequest, "INVALID_REQUEST", "query and paperId are required")
		return
	}
	provider := strings.TrimSpace(req.Provider)
	if normalized, ok := normalizeSearchProviderHint(provider); ok {
		provider = normalized
	}
	if provider == "" {
		provider = "unknown"
	}
	rank := req.Rank
	if rank <= 0 {
		rank = 1
	}
	userID := resolveSearchClickUserID(r, req.UserID)
	if h != nil && h.registry != nil {
		if intelligence := h.registry.GetIntelligence(); intelligence != nil {
			_ = intelligence.RecordClick(r.Context(), userID, query, paperID, provider, rank)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func resolveSearchClickUserID(r *http.Request, requestedUserID string) string {
	caller, _ := r.Context().Value(contextKey("user_id")).(string)
	caller = strings.TrimSpace(caller)
	if caller == "" {
		return ""
	}
	if strings.TrimSpace(requestedUserID) != "" && caller == "admin" {
		return strings.TrimSpace(requestedUserID)
	}
	return caller
}

func (h *SearchHandler) HandleSearchTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeSearchError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"tools": []map[string]any{
			{
				"name":        "wisdevSearchPapers",
				"type":        "retrieval",
				"description": "Search scholarly papers across configured providers.",
				"aliases":     []string{"paper_search", "search_papers"},
			},
			{
				"name":        "paper_lookup",
				"type":        "retrieval",
				"description": "Look up a specific paper by stable identifier.",
				"aliases":     []string{"scholarlmSearchPapers", "scholarlmPaperLookup"},
			},
		},
	})
}

func (h *SearchHandler) HandleQueryCategories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "Method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	var req struct {
		Query    string `json:"query"`
		Keywords string `json:"keywords"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "Failed to parse request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", nil)
		return
	}
	combined := strings.TrimSpace(strings.Join([]string{query, strings.TrimSpace(req.Keywords)}, " "))
	fieldID, confidence := ClassifyQueryField(combined)
	name := fieldID
	switch fieldID {
	case "computerscience":
		name = "Computer Science"
	case "medicine":
		name = "Medicine"
	case "biology":
		name = "Biology"
	case "physics":
		name = "Physics"
	case "chemistry":
		name = "Chemistry"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"categories": []map[string]any{
			{
				"id":          fieldID,
				"name":        name,
				"confidence":  confidence,
				"description": fmt.Sprintf("Category inferred from query %q.", query),
			},
		},
	})
}
