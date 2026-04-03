package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

func (s *wisdevV2Server) registerQuestioningRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	mux.HandleFunc("/v2/agent/process-answer", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SessionID         string   `json:"sessionId"`
			QuestionID        string   `json:"questionId"`
			Values            []string `json:"values"`
			DisplayValues     []string `json:"displayValues"`
			Proceed           bool     `json:"proceed"`
			ExpectedUpdatedAt int64    `json:"expectedUpdatedAt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		if err := validateRequiredString(req.QuestionID, "questionId", 80); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "questionId",
			})
			return
		}
		if err := validateStringSlice(req.Values, "values", 8, 160); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "values",
			})
			return
		}
		if err := validateStringSlice(req.DisplayValues, "displayValues", 8, 160); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "displayValues",
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
		if !assertExpectedUpdatedAt(w, req.ExpectedUpdatedAt, session) {
			return
		}
		if status, cached, ok := enforceIdempotency(r, agentGateway, "v2_agent_answer:"+req.SessionID+":"+req.QuestionID); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(cached)
			return
		}
		if err := ensureAgentSessionMutable(session); err != nil {
			WriteError(w, http.StatusConflict, ErrInvalidParameters, err.Error(), map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		answers := mapAny(session["answers"])
		answers[strings.TrimSpace(req.QuestionID)] = map[string]any{
			"questionId":    strings.TrimSpace(req.QuestionID),
			"values":        req.Values,
			"displayValues": req.DisplayValues,
			"answeredAt":    time.Now().UTC().Format(time.RFC3339),
		}
		session["answers"] = answers
		nextIndex := wisdev.IntValue(session["currentQuestionIndex"]) + 1
		maxQuestions := wisdev.IntValue(session["maxQuestions"])
		stopReason := ""
		status := "clarifying"
		if req.Proceed {
			stopReason = "user_proceed"
			status = "ready"
		} else if maxQuestions > 0 && nextIndex >= maxQuestions {
			stopReason = "clarification_budget_reached"
			status = "ready"
		} else if strings.TrimSpace(req.QuestionID) == "depth" {
			values := uniqueStrings(req.Values)
			objectiveValues := []string{}
			if objective, ok := answers["objective"].(map[string]any); ok {
				objectiveValues = sliceStrings(objective["values"])
			}
			if len(values) > 0 && values[0] == "quick" && len(objectiveValues) > 0 && objectiveValues[0] == "survey" {
				stopReason = "evidence_sufficient"
				status = "ready"
			}
		}
		if status == "ready" {
			session["questionStopReason"] = stopReason
			session["status"] = status
		}
		session["currentQuestionIndex"] = nextIndex
		session["updatedAt"] = time.Now().UTC().Format(time.RFC3339)
		traceID := wisdev.NewTraceID()
		if err := agentGateway.StateStore.PersistAgentSessionMutation(req.SessionID, wisdev.AsOptionalString(session["userId"]), session, wisdev.RuntimeJournalEntry{
			EventID:   wisdev.NewTraceID(),
			TraceID:   traceID,
			SessionID: req.SessionID,
			UserID:    wisdev.AsOptionalString(session["userId"]),
			StepID:    req.QuestionID,
			EventType: "agent_session_answer",
			Path:      "/v2/agent/process-answer",
			Status:    "completed",
			CreatedAt: time.Now().UnixMilli(),
			Summary:   "Agent question answer recorded.",
			Payload:   cloneAnyMap(session),
			Metadata:  map[string]any{"proceed": req.Proceed},
		}); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist processed answer", map[string]any{
				"error":      err.Error(),
				"sessionId":  req.SessionID,
				"questionId": req.QuestionID,
			})
			return
		}
		body, _ := json.Marshal(buildV2EnvelopeBody(traceID, "session", session))
		storeIdempotentResponse(agentGateway, r, "v2_agent_answer:"+req.SessionID+":"+req.QuestionID, body)
		writeV2EnvelopeWithTraceID(w, traceID, "session", session)
	})

	mux.HandleFunc("/v2/agent/next-question", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SessionID           string `json:"sessionId"`
			UseAdaptiveOrdering bool   `json:"useAdaptiveOrdering"`
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
		if err := ensureAgentSessionMutable(session); err != nil {
			WriteError(w, http.StatusConflict, ErrInvalidParameters, err.Error(), map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		payload := buildAgentQuestionPayload(session, req.UseAdaptiveOrdering)
		traceID := wisdev.NewTraceID()
		if err := agentGateway.StateStore.PersistAgentSessionMutation(req.SessionID, wisdev.AsOptionalString(session["userId"]), session, wisdev.RuntimeJournalEntry{
			EventID:   wisdev.NewTraceID(),
			TraceID:   traceID,
			SessionID: req.SessionID,
			UserID:    wisdev.AsOptionalString(session["userId"]),
			StepID:    wisdev.AsOptionalString(payload["id"]),
			EventType: "agent_next_question",
			Path:      "/v2/agent/next-question",
			Status:    "completed",
			CreatedAt: time.Now().UnixMilli(),
			Summary:   "Next agent question requested.",
			Payload:   cloneAnyMap(payload),
			Metadata:  map[string]any{"adaptive": req.UseAdaptiveOrdering},
		}); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist next question state", map[string]any{
				"error":     err.Error(),
				"sessionId": req.SessionID,
			})
			return
		}
		writeV2EnvelopeWithTraceID(w, traceID, "question", payload)
	})
}
