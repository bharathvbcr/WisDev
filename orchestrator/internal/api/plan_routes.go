package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/telemetry"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

func (s *wisdevServer) registerPlanRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	mux.HandleFunc("/wisdev/decide", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req decisionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		policyConfig := policy.DefaultPolicyConfig()
		if agentGateway != nil {
			policyConfig = agentGateway.PolicyConfig
		}
		decision := buildDecisionPayload(req, policyConfig)
		selectedStepID := strings.TrimSpace(fmt.Sprintf("%v", decision["selectedStepId"]))

		traceID := writeEnvelope(w, "decision", decision)
		s.journalEvent(
			"decide",
			"/wisdev/decide",
			traceID,
			req.SessionID,
			strings.TrimSpace(req.UserID),
			req.Plan.PlanID,
			selectedStepID,
			"Step decision completed.",
			decision,
			map[string]any{
				"candidateStepIds": req.CandidateStepIDs,
				"executionMode":    decision["executionMode"],
			},
		)
	})

	mux.HandleFunc("/wisdev/critique", func(w http.ResponseWriter, r *http.Request) {
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

		var papers []wisdev.Source
		if agentGateway != nil {
			papers, _, _ = wisdev.RetrieveCanonicalPapersWithRegistry(r.Context(), agentGateway.Redis, agentGateway.SearchRegistry, query, 6)
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
		traceID := writeEnvelope(w, "critique", critique)
		s.journalEvent(
			"critique",
			"/wisdev/critique",
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

	mux.HandleFunc("/wisdev/execute", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req executeCapabilityRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		req = normalizeExecuteCapabilityRequest(req)

		if agentGateway == nil {
			// Simulate for tests
			payload := normalizeExecutionPayload(map[string]any{
				"applied": true,
				"message": "Simulated execution",
			}, req.Payload, req.Context)
			writeEnvelope(w, "execution", payload)
			return
		}

		action := strings.TrimSpace(req.Action)
		if action != "" {
			action = strings.TrimSpace(wisdev.CanonicalizeWisdevAction(action))
			if strings.TrimSpace(req.SessionID) != "" {
				if agentGateway.Store == nil {
					WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "session store unavailable", nil)
					return
				}
				session, err := agentGateway.Store.Get(r.Context(), req.SessionID)
				if err != nil || session == nil {
					WriteError(w, http.StatusNotFound, ErrNotFound, "session not found", map[string]any{
						"sessionId": req.SessionID,
					})
					return
				}
				if !requireOwnerAccess(w, r, session.UserID) {
					return
				}
				if _, err := clearExpiredPendingApproval(r.Context(), agentGateway, session); err != nil {
					WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist session", map[string]any{
						"sessionId": req.SessionID,
						"error":     err.Error(),
					})
					return
				}
				if session.Plan != nil {
					if pendingApprovalID := strings.TrimSpace(session.Plan.PendingApprovalID); pendingApprovalID != "" {
						WriteError(w, http.StatusConflict, ErrConflict, "session has a pending approval that must be resolved before new execution", map[string]any{
							"sessionId":             req.SessionID,
							"pendingApprovalId":     pendingApprovalID,
							"pendingApprovalStepId": strings.TrimSpace(session.Plan.PendingApprovalStepID),
						})
						return
					}
				}
			}
			toolInvocationPolicy := ""
			if req.Payload != nil {
				if raw := strings.TrimSpace(fmt.Sprintf("%v", req.Payload["toolInvocationPolicy"])); raw != "" && raw != "<nil>" {
					toolInvocationPolicy = strings.ToLower(raw)
				}
			}
			if !req.Confirm && !req.DryRun {
				switch toolInvocationPolicy {
				case "never_auto":
					payload := normalizeExecutionPayload(map[string]any{
						"applied":              false,
						"requiresConfirmation": true,
						"risk":                 "low",
						"message":              "Tool policy is set to never auto-run. Review and confirm to proceed.",
						"guardrailReason":      "user_policy_confirmation_required",
						"nextActions":          wisdev.ConfirmationActions(),
						"data": map[string]any{
							"toolInvocationPolicy": toolInvocationPolicy,
							"controlPlane":         "go",
						},
					}, req.Payload, req.Context)
					traceID := writeEnvelope(w, "execution", payload)
					s.journalEvent(
						"execute",
						"/wisdev/execute",
						traceID,
						req.SessionID,
						"",
						"",
						req.StepID,
						"Capability execution requires user confirmation due to policy.",
						payload,
						map[string]any{"action": action, "toolInvocationPolicy": toolInvocationPolicy},
					)
					return
				case "always_ask":
					payload := normalizeExecutionPayload(map[string]any{
						"applied":              false,
						"requiresConfirmation": true,
						"risk":                 "medium",
						"message":              "Tool policy requires explicit approval before execution.",
						"guardrailReason":      "user_policy_confirmation_required",
						"nextActions":          wisdev.ConfirmationActions(),
						"data": map[string]any{
							"toolInvocationPolicy": toolInvocationPolicy,
							"controlPlane":         "go",
						},
					}, req.Payload, req.Context)
					traceID := writeEnvelope(w, "execution", payload)
					s.journalEvent(
						"execute",
						"/wisdev/execute",
						traceID,
						req.SessionID,
						"",
						"",
						req.StepID,
						"Capability execution requires explicit approval due to policy.",
						payload,
						map[string]any{"action": action, "toolInvocationPolicy": toolInvocationPolicy},
					)
					return
				}
			}
			if action == wisdev.ActionResearchBuildClaimEvidenceTable {
				payload := normalizeExecutionPayload(buildClaimEvidenceTableExecution(req), req.Payload, req.Context)
				evidence, _ := payload["evidence"].(map[string]any)
				traceID := writeEnvelope(w, "execution", payload)
				s.journalEvent(
					"execute",
					"/wisdev/execute",
					traceID,
					req.SessionID,
					"",
					"",
					req.StepID,
					"Claim evidence gate evaluated.",
					payload,
					map[string]any{
						"action":               action,
						"claimCount":           evidence["claimCount"],
						"linkedClaimCount":     evidence["linkedClaimCount"],
						"unlinkedClaimCount":   evidence["unlinkedClaimCount"],
						"requiresConfirmation": payload["requiresConfirmation"],
					},
				)
				return
			}
			if agentGateway.LLMClient == nil {
				WriteError(w, http.StatusServiceUnavailable, ErrWisdevFailed, "LLM client not configured", nil)
				return
			}
			prompt := fmt.Sprintf("Execute capability %s with payload %v", action, req.Payload)
			generatePolicy := llm.ResolveRequestPolicy(llm.RequestPolicyInput{
				RequestedTier: "standard",
				TaskType:      "reasoning",
			})
			execCtx, execCancel := context.WithTimeout(r.Context(), generatePolicy.OuterDeadline)
			defer execCancel()
			logger := telemetry.FromCtx(execCtx)
			logger.InfoContext(execCtx, "wisdev capability execution llm request start",
				"component", "api.wisdev",
				"operation", "wisdev_execute_capability",
				"stage", "llm_request_start",
				"action", action,
				"session_id", req.SessionID,
				"step_id", req.StepID,
				"service_tier", generatePolicy.ServiceTier,
				"request_class", string(generatePolicy.RequestClass),
				"retry_profile", string(generatePolicy.RetryProfile),
				"transport_timeout_ms", generatePolicy.TransportTimeout.Milliseconds(),
				"outer_deadline_ms", generatePolicy.OuterDeadline.Milliseconds(),
				"latency_budget_ms", generatePolicy.LatencyBudgetMs,
				"thinking_budget", generatePolicy.ThinkingBudget,
			)
			llmClient := agentGateway.LLMClient.WithTimeout(generatePolicy.TransportTimeout)
			resp, err := llmClient.Generate(execCtx, llm.ApplyGeneratePolicy(&llmv1.GenerateRequest{
				Prompt: prompt,
				Model:  llm.ResolveStandardModel(),
			}, generatePolicy))
			if err != nil {
				logger.WarnContext(execCtx, "wisdev capability execution llm request failed",
					"component", "api.wisdev",
					"operation", "wisdev_execute_capability",
					"stage", "llm_request_failed",
					"action", action,
					"session_id", req.SessionID,
					"step_id", req.StepID,
					"error", err.Error(),
				)
				payload := normalizeExecutionPayload(map[string]any{
					"applied":              false,
					"requiresConfirmation": false,
					"risk":                 "medium",
					"message":              err.Error(),
					"data":                 map[string]any{},
				}, req.Payload, req.Context)
				traceID := writeEnvelope(w, "execution", payload)
				s.journalEvent(
					"execute",
					"/wisdev/execute",
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
			logger.InfoContext(execCtx, "wisdev capability execution llm request success",
				"component", "api.wisdev",
				"operation", "wisdev_execute_capability",
				"stage", "llm_request_success",
				"action", action,
				"session_id", req.SessionID,
				"step_id", req.StepID,
			)
			outputText, err := normalizeGeneratedResponseText("capability execution", resp)
			if err != nil {
				payload := normalizeExecutionPayload(map[string]any{
					"applied":              false,
					"requiresConfirmation": false,
					"risk":                 "medium",
					"message":              err.Error(),
					"data":                 map[string]any{},
				}, req.Payload, req.Context)
				traceID := writeEnvelope(w, "execution", payload)
				s.journalEvent(
					"execute",
					"/wisdev/execute",
					traceID,
					req.SessionID,
					"",
					"",
					req.StepID,
					"Capability execution returned empty output.",
					payload,
					map[string]any{"action": action},
				)
				return
			}
			result := map[string]any{"raw_output": outputText, "action": action}
			payload := normalizeExecutionPayload(map[string]any{
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
			}, req.Payload, req.Context)
			traceID := writeEnvelope(w, "execution", payload)
			s.journalEvent(
				"execute",
				"/wisdev/execute",
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

		if agentGateway.Store == nil {
			WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "session store unavailable", nil)
			return
		}
		session, err := agentGateway.Store.Get(r.Context(), req.SessionID)
		if err != nil || session == nil {
			WriteError(w, http.StatusNotFound, ErrNotFound, "session not found", map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		if !requireOwnerAccess(w, r, session.UserID) {
			return
		}
		if session.Plan == nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "session has no plan", map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		if _, err := clearExpiredPendingApproval(r.Context(), agentGateway, session); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist session", map[string]any{
				"sessionId": req.SessionID,
				"error":     err.Error(),
			})
			return
		}
		if pendingApprovalID := strings.TrimSpace(session.Plan.PendingApprovalID); pendingApprovalID != "" {
			WriteError(w, http.StatusConflict, ErrConflict, "session has a pending approval that must be resolved before new execution", map[string]any{
				"sessionId":             req.SessionID,
				"pendingApprovalId":     pendingApprovalID,
				"pendingApprovalStepId": strings.TrimSpace(session.Plan.PendingApprovalStepID),
			})
			return
		}
		if agentGateway.Executor == nil {
			WriteError(w, http.StatusServiceUnavailable, ErrWisdevFailed, "execution runtime unavailable", map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		selectedParallelStepIDs := normalizeStringSlice(req.Payload["selectedParallelStepIds"])
		stepsToExecute, err := resolveExecutePlanSteps(session, req.StepID, selectedParallelStepIDs)
		if err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "no executable step found", map[string]any{
				"sessionId": req.SessionID,
				"stepId":    req.StepID,
			})
			return
		}
		results := runExecutePlanSteps(r.Context(), agentGateway, session, stepsToExecute)
		completedStepIDs, failedStepIDs, confirmationRequired := applyExecutePlanStepResults(session, results)

		primaryStep := stepsToExecute[0]
		if len(confirmationRequired) > 0 {
			confirmationRequiredStepIDs := sortedExecuteMapKeys(confirmationRequired)
			pendingApprovalStepID := primaryStep.ID
			if _, ok := confirmationRequired[pendingApprovalStepID]; !ok {
				for _, step := range stepsToExecute {
					if _, ok := confirmationRequired[step.ID]; ok {
						pendingApprovalStepID = step.ID
						primaryStep = step
						break
					}
				}
			}
			hitlPayload := createPendingApproval(session, agentGateway, primaryStep, confirmationRequired[pendingApprovalStepID])
			deferredConfirmationStepIDs := make([]string, 0, len(confirmationRequiredStepIDs))
			for _, stepID := range confirmationRequiredStepIDs {
				if stepID == pendingApprovalStepID {
					continue
				}
				deferredConfirmationStepIDs = append(deferredConfirmationStepIDs, stepID)
			}
			if err := agentGateway.Store.Put(r.Context(), session, agentGateway.SessionTTL); err != nil {
				WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist session", map[string]any{
					"sessionId": req.SessionID,
					"stepId":    pendingApprovalStepID,
					"error":     err.Error(),
				})
				return
			}
			payload := normalizeExecutionPayload(map[string]any{
				"sessionId":            req.SessionID,
				"planId":               session.Plan.PlanID,
				"stepId":               primaryStep.ID,
				"status":               "awaiting_confirmation",
				"applied":              false,
				"requiresConfirmation": true,
				"message":              "Plan step execution requires explicit approval before continuing.",
				"traceId":              wisdev.NewTraceID(),
				"nextActions":          wisdev.ConfirmationActions(),
				"data": map[string]any{
					"parallelExecution":           len(stepsToExecute) > 1,
					"laneCount":                   len(stepsToExecute),
					"executedStepIds":             planStepIDs(stepsToExecute),
					"completedStepIds":            completedStepIDs,
					"failedStepIds":               failedStepIDs,
					"confirmationRequiredStepIds": confirmationRequiredStepIDs,
					"confirmationRequiredReasons": confirmationRequired,
					"pendingApprovalStepId":       pendingApprovalStepID,
					"deferredConfirmationStepIds": deferredConfirmationStepIDs,
					"hitl":                        hitlPayload,
				},
			}, req.Payload, req.Context)
			traceID := writeEnvelope(w, "execution", payload)
			s.journalEvent(
				"execute",
				"/wisdev/execute",
				traceID,
				req.SessionID,
				session.UserID,
				session.Plan.PlanID,
				primaryStep.ID,
				"Plan step execution paused for confirmation.",
				payload,
				map[string]any{
					"parallelExecution":   len(stepsToExecute) > 1,
					"laneCount":           len(stepsToExecute),
					"pendingApprovalStep": pendingApprovalStepID,
				},
			)
			return
		}
		session.Status = wisdev.SessionExecutingPlan
		session.UpdatedAt = wisdev.NowMillis()
		if err := agentGateway.Store.Put(r.Context(), session, agentGateway.SessionTTL); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist session", map[string]any{
				"sessionId": req.SessionID,
				"stepId":    primaryStep.ID,
				"error":     err.Error(),
			})
			return
		}
		if len(failedStepIDs) > 0 {
			WriteError(w, http.StatusBadRequest, ErrWisdevFailed, "execution failed", map[string]any{
				"sessionId":         req.SessionID,
				"stepId":            primaryStep.ID,
				"executedStepIds":   planStepIDs(stepsToExecute),
				"completedStepIds":  completedStepIDs,
				"failedStepIds":     failedStepIDs,
				"parallelExecution": len(stepsToExecute) > 1,
				"laneCount":         len(stepsToExecute),
			})
			return
		}

		message := "Plan step completed."
		status := "completed"
		if len(stepsToExecute) > 1 {
			message = "Plan steps completed in parallel."
			status = "completed_parallel"
		}
		payload := normalizeExecutionPayload(map[string]any{
			"sessionId":   req.SessionID,
			"planId":      session.Plan.PlanID,
			"stepId":      primaryStep.ID,
			"status":      status,
			"applied":     true,
			"message":     message,
			"traceId":     wisdev.NewTraceID(),
			"nextActions": []string{"approve"},
			"data": map[string]any{
				"parallelExecution": len(stepsToExecute) > 1,
				"laneCount":         len(stepsToExecute),
				"executedStepIds":   planStepIDs(stepsToExecute),
				"completedStepIds":  completedStepIDs,
			},
		}, req.Payload, req.Context)
		traceID := writeEnvelope(w, "execution", payload)
		s.journalEvent(
			"execute",
			"/wisdev/execute",
			traceID,
			req.SessionID,
			session.UserID,
			session.Plan.PlanID,
			primaryStep.ID,
			message,
			payload,
			map[string]any{
				"executionTarget":   primaryStep.ExecutionTarget,
				"parallelExecution": len(stepsToExecute) > 1,
				"laneCount":         len(stepsToExecute),
			},
		)
	})

	mux.HandleFunc("/wisdev/programmatic-loop", func(w http.ResponseWriter, r *http.Request) {
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
			Mode          string         `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}

		query := strings.TrimSpace(req.Query)
		action := strings.TrimSpace(req.Action)
		if query == "" && req.Payload != nil {
			query = strings.TrimSpace(fmt.Sprintf("%v", req.Payload["query"]))
		}
		if action == "" && query != "" {
			action = "research.queryDecompose"
		}
		if action == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "action is required", nil)
			return
		}

		if agentGateway == nil {
			// Simulate for tests
			results := map[string]any{"ok": true, "message": "Simulated loop"}
			writeEnvelope(w, "loopResult", results)
			return
		}

		payload := map[string]any{}
		for key, value := range req.Payload {
			payload[key] = value
		}
		if query != "" && strings.TrimSpace(fmt.Sprintf("%v", payload["query"])) == "" {
			payload["query"] = query
		}
		if strings.TrimSpace(fmt.Sprintf("%v", payload["mode"])) == "" && strings.TrimSpace(req.Mode) != "" {
			payload["mode"] = strings.TrimSpace(req.Mode)
		}

		execFn := agentGateway.ProgrammaticLoopExecutor()
		if execFn == nil {
			WriteError(w, http.StatusServiceUnavailable, ErrWisdevFailed, "programmatic loop executor unavailable", nil)
			return
		}

		iterations := req.MaxIterations
		if iterations <= 0 {
			iterations = 1
		}
		if iterations > 8 {
			iterations = 8
		}

		var session *wisdev.AgentSession
		if sid := strings.TrimSpace(req.SessionID); sid != "" && agentGateway.Store != nil {
			loaded, err := agentGateway.Store.Get(r.Context(), sid)
			if err != nil || loaded == nil {
				WriteError(w, http.StatusNotFound, ErrNotFound, "session not found", map[string]any{
					"sessionId": sid,
				})
				return
			}
			if !requireOwnerAccess(w, r, loaded.UserID) {
				return
			}
			session = loaded
			remainingCalls := session.Budget.MaxToolCalls - session.Budget.ToolCallsUsed
			if remainingCalls > 0 && iterations > remainingCalls {
				iterations = remainingCalls
			}
		}
		if iterations <= 0 {
			iterations = 1
		}

		tree := wisdev.RunProgrammaticTreeLoop(r.Context(), execFn, session, action, payload, iterations, nil)
		if session != nil && len(tree.BranchArtifacts) > 0 && agentGateway.Store != nil {
			session.UpdatedAt = wisdev.NowMillis()
			_ = agentGateway.Store.Put(r.Context(), session, 0)
		}

		writeEnvelope(w, "loopResult", map[string]any{
			"sessionId":       req.SessionID,
			"action":          action,
			"iterations":      wisdev.TreeLoopIterationsToHTTP(tree.Iterations),
			"completed":       tree.Completed,
			"final":           tree.Final,
			"bestConfidence":  tree.BestConfidence,
			"voteSummary":     tree.VoteSummary,
			"branchArtifacts": tree.BranchArtifacts,
		})
	})
}
