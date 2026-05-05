package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

func (h *WisDevHandler) HandleDecomposeTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	if h.brainCaps == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "brain capabilities are not initialized", nil)
		return
	}

	var req struct {
		Query  string `json:"query"`
		Domain string `json:"domain"`
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

	tasks, err := h.brainCaps.DecomposeTaskInteractive(r.Context(), req.Query, req.Domain, "")
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to decompose task", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"tasks": tasks}); err != nil {
		slog.Warn("HandleDecomposeTask: failed to encode response", "error", err.Error())
	}
}

func (h *WisDevHandler) HandleProposeHypotheses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	if h.brainCaps == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "brain capabilities are not initialized", nil)
		return
	}

	var req struct {
		Query  string `json:"query"`
		Intent string `json:"intent"`
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

	hypotheses, err := h.brainCaps.ProposeHypothesesInteractive(r.Context(), req.Query, req.Intent, "")
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to propose hypotheses", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"hypotheses": hypotheses}); err != nil {
		slog.Warn("HandleProposeHypotheses: failed to encode response", "error", err.Error())
	}
}

func (h *WisDevHandler) HandleCoordinateReplan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	if h.brainCaps == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "brain capabilities are not initialized", nil)
		return
	}

	var req struct {
		FailedStepID string         `json:"failedStepId"`
		Reason       string         `json:"reason"`
		Context      map[string]any `json:"context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if strings.TrimSpace(req.FailedStepID) == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "failedStepId is required", map[string]any{
			"field": "failedStepId",
		})
		return
	}

	tasks, err := h.brainCaps.CoordinateReplan(r.Context(), req.FailedStepID, req.Reason, req.Context, "")
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to coordinate replan", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"tasks": tasks}); err != nil {
		slog.Warn("HandleCoordinateReplan: failed to encode response", "error", err.Error())
	}
}

func (h *WisDevHandler) HandleAnalyzeQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req struct {
		Query   string `json:"query"`
		TraceID string `json:"traceId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	traceID := resolveWisdevRouteTraceID(r, req.TraceID)
	query := strings.TrimSpace(req.Query)
	if query == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
			"field": "query",
		})
		return
	}

	logWisdevRouteLifecycle(r, "wisdev_analyze_query_legacy", "request_received", query,
		"trace_id", traceID,
		"result", "accepted",
	)
	payload := buildAnalyzeQueryPayload(query, traceID)
	logWisdevRouteLifecycle(r, "wisdev_analyze_query_legacy", "response_ready", query,
		"trace_id", traceID,
		"entity_count", len(payload["entities"].([]string)),
		"research_question_count", len(payload["research_questions"].([]string)),
		"result", "success",
	)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Trace-Id", traceID)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Warn("HandleAnalyzeQuery: failed to encode response", "error", err.Error(), "trace_id", traceID)
	}
}

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
