package api

import (
	"encoding/json"
	"net/http"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

func HandleOpenSearchHybrid(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req wisdev.OpenSearchHybridRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	result, err := wisdev.OpenSearchHybridSearch(r.Context(), req)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrDependencyFailed, "open search hybrid execution failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	writeJSONResponse(w, http.StatusOK, result)
}
