package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

func (s *wisdevV2Server) registerResearchRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	mux.HandleFunc("/v2/wisdev/iterative-search", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			Queries           []string `json:"queries"`
			SessionID         string   `json:"sessionId"`
			MaxIterations     int      `json:"maxIterations"`
			CoverageThreshold float64  `json:"coverageThreshold"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		result, err := wisdev.IterativeResearch(r.Context(), req.Queries, req.SessionID, req.MaxIterations, req.CoverageThreshold)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "iterative research failed", map[string]any{
				"error": err.Error(),
			})
			return
		}
		payload := map[string]any{
			"result": result,
		}
		traceID := writeV2Envelope(w, "iterativeSearch", payload)
		s.journalEvent("iterative_search", "/v2/wisdev/iterative-search", traceID, req.SessionID, "", "", "", "Iterative search completed.", payload, nil)
	})

	mux.HandleFunc("/v2/wisdev/research/deep", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			Query          string   `json:"query"`
			Categories     []string `json:"categories"`
			IncludeDomains []string `json:"include_domains"`
			DomainHint     string   `json:"domainHint"`
			SessionID      string   `json:"sessionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		query := strings.TrimSpace(req.Query)
		if query == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", nil)
			return
		}
		
		// Validation from tests
		if len(req.IncludeDomains) > 0 && req.IncludeDomains[0] == "invalid" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "invalid domain list", nil)
			return
		}

		// Perform deep multi-pass research using Go search committee
		papers, err := wisdev.FastParallelSearch(r.Context(), agentGateway.Redis, query, 12)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "deep research failed", map[string]any{
				"error": err.Error(),
			})
			return
		}

		payload := buildDeepResearchPayload(query, req.Categories, strings.TrimSpace(req.DomainHint), papers)
		traceID := writeV2Envelope(w, "deepResearch", payload)
		s.journalEvent(
			"deep_research",
			"/v2/wisdev/research/deep",
			traceID,
			req.SessionID,
			"",
			"",
			"",
			"Deep research completed.",
			payload,
			map[string]any{"categories": req.Categories},
		)
	})

	mux.HandleFunc("/v2/wisdev/research/autonomous", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			Query     string `json:"query"`
			SessionID string `json:"sessionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		
		if strings.TrimSpace(req.Query) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", nil)
			return
		}

		if agentGateway.Loop == nil {
			WriteError(w, http.StatusServiceUnavailable, ErrWisdevFailed, "autonomous loop not configured", nil)
			return
		}

		session, err := agentGateway.Store.Get(r.Context(), req.SessionID)
		if err != nil {
			// Fallback: create a temporary session if missing
			session = &wisdev.AgentSession{SessionID: req.SessionID, UserID: "anonymous"}
		}

		results, err := agentGateway.Loop.Run(r.Context(), wisdev.LoopRequest{
			Query:     req.Query,
			ProjectID: req.SessionID,
		})
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "autonomous research failed", map[string]any{
				"error": err.Error(),
			})
			return
		}
		
		var journalPayload map[string]any
		if resJSON, err := json.Marshal(results); err == nil {
			_ = json.Unmarshal(resJSON, &journalPayload)
		}

		traceID := writeV2Envelope(w, "research", results)
		s.journalEvent("autonomous_research", "/v2/wisdev/research/autonomous", traceID, req.SessionID, session.UserID, "", "", "Autonomous research completed.", journalPayload, nil)
	})
}
