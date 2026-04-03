package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

func (s *wisdevV2Server) registerSessionRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	mux.HandleFunc("/v2/wisdev/wisdev.Plan", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			UserID        string         `json:"userId"`
			SessionID     string         `json:"sessionId"`
			Query         string         `json:"query"`
			OperationMode string         `json:"operationMode"`
			Metadata      map[string]any `json:"metadata"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
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
		userID := strings.TrimSpace(req.UserID)
		if userID == "" {
			userID = "anonymous"
		}

		session, err := agentGateway.CreateSession(r.Context(), userID, query)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to create session", map[string]any{
				"error": err.Error(),
			})
			return
		}
		if req.SessionID != "" {
			session.SessionID = req.SessionID
		}
		synthesized := wisdev.SynthesizePlanCandidates(session, query)
		allCandidates := make([]wisdev.PlanCandidate, 0, len(synthesized.Alternates)+1)
		allCandidates = append(allCandidates, synthesized.Selected)
		allCandidates = append(allCandidates, synthesized.Alternates...)
		reranked := wisdev.RerankPlanCandidatesWithVerifier(r.Context(), agentGateway.LLMClient, session, query, allCandidates)
		if len(reranked) > 0 {
			synthesized.Selected = reranked[0]
		}
		session.Plan = synthesized.Selected.Plan
		session.Status = wisdev.SessionExecutingPlan

		traceID := wisdev.NewTraceID()
		planID := ""
		var planPayload map[string]any
		if session.Plan != nil {
			planID = session.Plan.PlanID
			if planJSON, marshalErr := json.Marshal(session.Plan); marshalErr == nil {
				_ = json.Unmarshal(planJSON, &planPayload)
			}
		}
		if planPayload == nil {
			planPayload = map[string]any{}
		}
		s.journalEvent("plan_created", "/v2/wisdev/wisdev.Plan", traceID, session.SessionID, userID, planID, "", "Research plan created and selected.", map[string]any{"plan": session.Plan}, nil)
		writeV2EnvelopeWithTraceID(w, traceID, "plan", planPayload)
	})

	mux.HandleFunc("/v2/agent/initialize-session", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			UserID           string   `json:"userId"`
			OriginalQuery    string   `json:"originalQuery"`
			CorrectedQuery   string   `json:"correctedQuery"`
			DetectedDomain   string   `json:"detectedDomain"`
			SecondaryDomains []string `json:"secondaryDomains"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		if status, cached, ok := enforceIdempotency(r, agentGateway, "v2_agent_initialize:"+strings.TrimSpace(req.UserID)+":"+strings.TrimSpace(req.OriginalQuery)); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(cached)
			return
		}
		userID, err := resolveAuthorizedUserID(r, req.UserID)
		if err != nil {
			WriteError(w, http.StatusForbidden, ErrUnauthorized, err.Error(), nil)
			return
		}
		if userID == "" || strings.TrimSpace(req.OriginalQuery) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "userId and originalQuery are required", map[string]any{
				"requiredFields": []string{"userId", "originalQuery"},
			})
			return
		}
		now := time.Now().UTC().Format(time.RFC3339)
		sessionID := fmt.Sprintf("ws_%d", time.Now().UnixMilli())
		questions := defaultAgentQuestionSequence(req.CorrectedQuery, req.DetectedDomain)
		complexityScore := wisdev.ClampFloat(0.4+float64(strings.Count(req.OriginalQuery, " "))*0.03+float64(len(req.SecondaryDomains))*0.05, 0.4, 0.9)
		expertiseLevel := "intermediate"
		if strings.Contains(strings.ToLower(req.DetectedDomain), "biology") || strings.Contains(strings.ToLower(req.DetectedDomain), "medicine") {
			expertiseLevel = "advanced"
		}
		session := map[string]any{
			"sessionId":            sessionID,
			"userId":               userID,
			"originalQuery":        strings.TrimSpace(req.OriginalQuery),
			"correctedQuery":       strings.TrimSpace(req.CorrectedQuery),
			"detectedDomain":       strings.TrimSpace(req.DetectedDomain),
			"secondaryDomains":     req.SecondaryDomains,
			"answers":              map[string]any{},
			"questions":            questions,
			"questionSequence":     []string{"objective", "constraints", "depth"},
			"currentQuestionIndex": 0,
			"minQuestions":         1,
			"maxQuestions":         len(questions),
			"complexityScore":      complexityScore,
			"clarificationBudget":  len(questions),
			"questionStopReason":   "",
			"status":               "clarifying",
			"expertiseLevel":       expertiseLevel,
			"autoRefine":           true,
			"createdAt":            now,
			"updatedAt":            now,
		}

		// Proactive Triage: Trigger complexity assessment in background to hide latency
		if query := strings.TrimSpace(req.OriginalQuery); query != "" && agentGateway.Brain != nil {
			go func(sid, uid string, q string) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				complexity, _ := agentGateway.Brain.AssessResearchComplexity(ctx, q)
				if complexity != "" {
					// Load, update, and save the session map
					if s, err := agentGateway.StateStore.LoadAgentSession(sid); err == nil {
						s["assessedComplexity"] = complexity
						agentGateway.StateStore.PersistAgentSessionMutation(sid, uid, s, wisdev.RuntimeJournalEntry{
							EventID:   wisdev.NewTraceID(),
							SessionID: sid,
							UserID:    uid,
							EventType: "agent_complexity_triage",
							Status:    "completed",
							Summary:   "Complexity triage cached.",
							Payload:   map[string]any{"assessedComplexity": complexity},
						})
					}
				}
			}(sessionID, userID, query)
		}
		traceID := wisdev.NewTraceID()
		if err := agentGateway.StateStore.PersistAgentSessionMutation(sessionID, userID, session, wisdev.RuntimeJournalEntry{
			EventID:   wisdev.NewTraceID(),
			TraceID:   traceID,
			SessionID: sessionID,
			UserID:    userID,
			EventType: "agent_session_initialize",
			Path:      "/v2/agent/initialize-session",
			Status:    "completed",
			CreatedAt: time.Now().UnixMilli(),
			Summary:   "Agent session initialized.",
			Payload:   cloneAnyMap(session),
			Metadata:  nil,
		}); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist agent session", map[string]any{
				"error": err.Error(),
			})
			return
		}
		body, _ := json.Marshal(buildV2EnvelopeBody(traceID, "session", session))
		storeIdempotentResponse(agentGateway, r, "v2_agent_initialize:"+userID+":"+strings.TrimSpace(req.OriginalQuery), body)
		writeV2EnvelopeWithTraceID(w, traceID, "session", session)
	})

	mux.HandleFunc("/v2/agent/get-session", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SessionID string `json:"sessionId"`
			UserID    string `json:"userId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		session, err := agentGateway.StateStore.LoadAgentSession(req.SessionID)
		if err != nil {
			WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(session["userId"])) {
			return
		}
		traceID := writeV2Envelope(w, "session", session)
		s.journalEvent("agent_session_get", "/v2/agent/get-session", traceID, req.SessionID, strings.TrimSpace(req.UserID), "", "", "Agent session requested.", session, nil)
	})

	mux.HandleFunc("/v2/agent/complete-session", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		session, err := agentGateway.StateStore.LoadAgentSession(req.SessionID)
		if err != nil {
			WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(session["userId"])) {
			return
		}
		if status, cached, ok := enforceIdempotency(r, agentGateway, "v2_agent_complete:"+req.SessionID); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(cached)
			return
		}
		if strings.TrimSpace(wisdev.AsOptionalString(session["status"])) == "completed" {
			WriteError(w, http.StatusConflict, ErrInvalidParameters, "agent session is already completed", map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		if _, ok := session["orchestrationPlan"].(map[string]any); !ok {
			session["orchestrationPlan"] = buildAgentOrchestrationPlan(session)
		}
		session["status"] = "completed"
		session["updatedAt"] = time.Now().UTC().Format(time.RFC3339)
		if strings.TrimSpace(wisdev.AsOptionalString(session["questionStopReason"])) == "" {
			session["questionStopReason"] = "evidence_sufficient"
		}
		traceID := wisdev.NewTraceID()
		if err := agentGateway.StateStore.PersistAgentSessionMutation(req.SessionID, wisdev.AsOptionalString(session["userId"]), session, wisdev.RuntimeJournalEntry{
			EventID:   wisdev.NewTraceID(),
			TraceID:   traceID,
			SessionID: req.SessionID,
			UserID:    wisdev.AsOptionalString(session["userId"]),
			EventType: "agent_complete_session",
			Path:      "/v2/agent/complete-session",
			Status:    "completed",
			CreatedAt: time.Now().UnixMilli(),
			Summary:   "Agent session completed.",
			Payload:   cloneAnyMap(session),
			Metadata:  nil,
		}); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist completed session", map[string]any{
				"error":     err.Error(),
				"sessionId": req.SessionID,
			})
			return
		}
		payload := map[string]any{
			"ok":                 true,
			"sessionId":          req.SessionID,
			"status":             session["status"],
			"questionStopReason": session["questionStopReason"],
			"orchestrationPlan":  session["orchestrationPlan"],
		}
		body, _ := json.Marshal(buildV2EnvelopeBody(traceID, "completion", payload))
		storeIdempotentResponse(agentGateway, r, "v2_agent_complete:"+req.SessionID, body)
		writeV2EnvelopeWithTraceID(w, traceID, "completion", payload)
	})

	mux.HandleFunc("/v2/agent/generate-search-queries", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", nil)
			return
		}
		session, err := agentGateway.StateStore.LoadAgentSession(req.SessionID)
		if err != nil {
			WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		
		queries := []string{wisdev.AsOptionalString(session["correctedQuery"])}
		writeV2Envelope(w, "queries", queries)
	})
}
