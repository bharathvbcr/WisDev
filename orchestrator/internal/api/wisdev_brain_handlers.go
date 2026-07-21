package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

func (h *WisDevHandler) HandlePaper2Skill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	if h.compiler == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "compiler is not initialized", nil)
		return
	}

	var req struct {
		ArxivID string `json:"arxiv_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	if strings.TrimSpace(req.ArxivID) == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "arxiv_id is required", map[string]any{
			"field": "arxiv_id",
		})
		return
	}

	schema, err := h.compiler.CompileArxivID(r.Context(), req.ArxivID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to compile skill from paper", map[string]any{
			"error":   err.Error(),
			"arxivId": req.ArxivID,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"status":   "completed",
		"arxiv_id": req.ArxivID,
		"skill":    schema,
	}); err != nil {
		slog.Warn("HandlePaper2Skill: failed to encode response", "error", err.Error())
	}
}
