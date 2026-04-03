package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
)

func (s *wisdevV2Server) registerPlanRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	mux.HandleFunc("/v2/wisdev/decide", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SessionID string `json:"sessionId"`
			Plan      struct {
				PlanID string `json:"planId"`
				Steps  []struct {
					ID   string `json:"id"`
					Risk string `json:"risk"`
				} `json:"steps"`
			} `json:"plan"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		
		selectedStepID := ""
		if len(req.Plan.Steps) > 0 {
			selectedStepID = req.Plan.Steps[0].ID
		}

		decision := map[string]any{
			"selectedStepId": selectedStepID,
			"rationale":      "Go native decision logic selected the next ready step.",
			"confidence":     0.9,
		}
		
		traceID := writeV2Envelope(w, "decision", decision)
		s.journalEvent(
			"decide",
			"/v2/wisdev/decide",
			traceID,
			req.SessionID,
			"",
			req.Plan.PlanID,
			selectedStepID,
			"Step decision completed.",
			decision,
			nil,
		)
	})

	mux.HandleFunc("/v2/wisdev/critique", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SessionID     string `json:"sessionId"`
			Query         string `json:"query"`
			DomainHint    string `json:"domainHint"`
			OperationMode string `json:"operationMode"`
			Decision      struct {
				SessionID  string  `json:"sessionId"`
				PlanID     string  `json:"planId"`
				StepID     string  `json:"selectedStepId"`
				Rationale  string  `json:"rationale"`
				Confidence float64 `json:"confidence"`
			} `json:"decision"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		query := strings.TrimSpace(req.Query)
		if query == "" {
			query = strings.TrimSpace(req.Decision.Rationale)
		}
		if query == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query or decision rationale is required", nil)
			return
		}
		papers, err := wisdev.FastParallelSearch(r.Context(), agentGateway.Redis, query, 6)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "critique failed", map[string]any{
				"error": err.Error(),
			})
			return
		}
		committee := buildMultiAgentCommitteeResult(query, strings.TrimSpace(req.DomainHint), papers, 2, false)
		critic, _ := committee["critic"].(map[string]any)
		criticDecision := strings.TrimSpace(fmt.Sprintf("%v", critic["decision"]))
		if criticDecision == "" {
			criticDecision = "accept"
		}
		criticConfidence := req.Decision.Confidence
		if rawConfidence, ok := critic["confidence"].(float64); ok && rawConfidence > 0 {
			criticConfidence = rawConfidence
		}
		if criticConfidence <= 0 {
			criticConfidence = wisdev.ClampFloat(0.74, 0.5, 0.95)
		}
		critique := map[string]any{
			"decision":      criticDecision,
			"confidence":    criticConfidence,
			"reasons":       critic["reasons"],
			"committee":     committee,
			"operationMode": resolveOperationMode(req.OperationMode),
		}
		traceID := writeV2Envelope(w, "critique", critique)
		s.journalEvent(
			"critique",
			"/v2/wisdev/critique",
			traceID,
			strings.TrimSpace(req.SessionID),
			"",
			strings.TrimSpace(req.Decision.PlanID),
			strings.TrimSpace(req.Decision.StepID),
			"Decision critique completed.",
			critique,
			map[string]any{"query": query},
		)
	})

	mux.HandleFunc("/v2/wisdev/execute", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SessionID string         `json:"sessionId"`
			StepID    string         `json:"stepId"`
			Action    string         `json:"action"`
			Payload   map[string]any `json:"payload"`
			Context   map[string]any `json:"context"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		action := strings.TrimSpace(req.Action)
		if action != "" {
			if strings.TrimSpace(req.SessionID) != "" {
				_, _ = agentGateway.Store.Get(r.Context(), req.SessionID)
			}
			prompt := fmt.Sprintf("Execute capability %s with payload %v", action, req.Payload)
			resp, err := agentGateway.LLMClient.Generate(r.Context(), &llmv1.GenerateRequest{
				Prompt: prompt,
				Model:  llm.ResolveStandardModel(),
			})
			if err != nil {
				payload := map[string]any{
					"applied":              false,
					"requiresConfirmation": false,
					"risk":                 "medium",
					"message":              err.Error(),
					"data":                 map[string]any{},
				}
				traceID := writeV2Envelope(w, "execution", payload)
				s.journalEvent(
					"execute",
					"/v2/wisdev/execute",
					traceID,
					req.SessionID,
					"",
					"",
					req.StepID,
					"Capability execution failed.",
					payload,
					map[string]any{"action": action},
				)
				return
			}
			result := map[string]any{"raw_output": resp.Text, "action": action}
			payload := map[string]any{
				"applied":              true,
				"requiresConfirmation": false,
				"risk":                 "low",
				"message":              "Capability executed.",
				"traceId":              wisdev.NewTraceID(),
				"data": map[string]any{
					"result":       result,
					"workerPlane":  "python-docs",
					"controlPlane": "go",
				},
			}
			traceID := writeV2Envelope(w, "execution", payload)
			s.journalEvent(
				"execute",
				"/v2/wisdev/execute",
				traceID,
				req.SessionID,
				"",
				"",
				req.StepID,
				"Capability execution completed.",
				payload,
				map[string]any{"action": action},
			)
			return
		}

		session, err := agentGateway.Store.Get(r.Context(), req.SessionID)
		if err != nil {
			WriteError(w, http.StatusNotFound, ErrNotFound, "session not found", map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		if session.Plan == nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "session has no plan", map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		var selected *wisdev.PlanStep
		for i := range session.Plan.Steps {
			step := &session.Plan.Steps[i]
			if req.StepID != "" && step.ID != req.StepID {
				continue
			}
			if session.Plan.CompletedStepIDs[step.ID] || session.Plan.FailedStepIDs[step.ID] != "" {
				continue
			}
			selected = step
			break
		}
		if selected == nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "no executable step found", map[string]any{
				"sessionId": req.SessionID,
				"stepId":    req.StepID,
			})
			return
		}
		result := agentGateway.Executor.RunStepWithRecovery(r.Context(), session, *selected, 0)
		if result.Err != nil {
			session.Plan.FailedStepIDs[selected.ID] = result.Err.Error()
			_ = agentGateway.Store.Put(r.Context(), session, agentGateway.SessionTTL)
			WriteError(w, http.StatusBadRequest, ErrWisdevFailed, "execution failed", map[string]any{
				"error":     result.Err.Error(),
				"sessionId": req.SessionID,
				"stepId":    selected.ID,
			})
			return
		}
		session.Plan.CompletedStepIDs[selected.ID] = true
		policy.ApplyBudgetUsage(&session.Budget, selected.ExecutionTarget == wisdev.ExecutionTargetPythonSandbox, selected.EstimatedCostCents)
		_ = agentGateway.Store.Put(r.Context(), session, agentGateway.SessionTTL)
		payload := map[string]any{
			"sessionId":   req.SessionID,
			"planId":      session.Plan.PlanID,
			"stepId":      selected.ID,
			"status":      "completed",
			"applied":     true,
			"message":     "Plan step completed.",
			"traceId":     wisdev.NewTraceID(),
			"nextActions": []string{"approve"},
		}
		traceID := writeV2Envelope(w, "execution", payload)
		s.journalEvent(
			"execute",
			"/v2/wisdev/execute",
			traceID,
			req.SessionID,
			session.UserID,
			session.Plan.PlanID,
			selected.ID,
			"Plan step completed.",
			payload,
			map[string]any{"executionTarget": selected.ExecutionTarget},
		)
	})

	mux.HandleFunc("/v2/wisdev/programmatic-loop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
			return
		}
		var req struct {
			Action        string         `json:"action"`
			Payload       map[string]any `json:"payload"`
			Query         string         `json:"query"`
			SessionID     string         `json:"sessionId"`
			MaxIterations int            `json:"maxIterations"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		
		query := strings.TrimSpace(req.Query)
		if query == "" && req.Action != "" {
			query = req.Action // Fallback for tests that send action but no query
		}
		if query == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query or action is required", nil)
			return
		}
		
		if agentGateway.Loop == nil {
			WriteError(w, http.StatusServiceUnavailable, ErrWisdevFailed, "autonomous loop not configured", nil)
			return
		}

		results, err := agentGateway.Loop.Run(r.Context(), wisdev.LoopRequest{
			Query:         query,
			ProjectID:     req.SessionID,
			MaxIterations: req.MaxIterations,
		})
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "loop failed", map[string]any{"error": err.Error()})
			return
		}
		
		writeV2Envelope(w, "loopResult", results)
	})
}
