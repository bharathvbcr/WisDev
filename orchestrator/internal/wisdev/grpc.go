package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	wisdevpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/wisdev"
)

type agentGatewayGRPCServer struct {
	wisdevpb.UnimplementedAgentGatewayServer
	gateway *AgentGateway
}

func mapStatusToProto(status SessionStatus) wisdevpb.SessionStatus {
	switch status {
	case SessionQuestioning:
		return wisdevpb.SessionStatus_QUESTIONING
	case SessionGeneratingTree:
		return wisdevpb.SessionStatus_GENERATING_TREE
	case SessionEditingTree:
		return wisdevpb.SessionStatus_EDITING_TREE
	case SessionExecutingPlan:
		return wisdevpb.SessionStatus_EXECUTING_PLAN
	case SessionPaused:
		return wisdevpb.SessionStatus_PAUSED
	case SessionComplete:
		return wisdevpb.SessionStatus_COMPLETE
	case SessionFailed:
		return wisdevpb.SessionStatus_FAILED
	default:
		return wisdevpb.SessionStatus_SESSION_STATUS_UNSPECIFIED
	}
}

func buildProtoSession(session *AgentSession) *wisdevpb.AgentSession {
	payload, _ := json.Marshal(session)
	return &wisdevpb.AgentSession{
		SchemaVersion:        session.SchemaVersion,
		PolicyVersion:        session.PolicyVersion,
		TraceId:              NewTraceID(),
		SessionId:            session.SessionID,
		UserId:               session.UserID,
		Status:               mapStatusToProto(session.Status),
		CheckpointBlob:       payload,
		QuestionSequence:     session.QuestionSequence,
		CurrentQuestionIndex: int32(session.CurrentQuestionIndex),
		MinQuestions:         int32(session.MinQuestions),
		MaxQuestions:         int32(session.MaxQuestions),
		ComplexityScore:      float32(session.ComplexityScore),
		ClarificationBudget:  int32(session.ClarificationBudget),
		QuestionStopReason:   mapQuestionStopReasonToProto(session.QuestionStopReason),
	}
}

func mapQuestionStopReasonToProto(reason QuestionStopReason) wisdevpb.QuestionStopReason {
	switch reason {
	case QuestionStopReasonEvidenceSufficient:
		return wisdevpb.QuestionStopReason_EVIDENCE_SUFFICIENT
	case QuestionStopReasonClarificationBudgetReached:
		return wisdevpb.QuestionStopReason_CLARIFICATION_BUDGET_REACHED
	case QuestionStopReasonUserProceed:
		return wisdevpb.QuestionStopReason_USER_PROCEED
	default:
		return wisdevpb.QuestionStopReason_QUESTION_STOP_REASON_UNSPECIFIED
	}
}

func buildQuestionStateSummaryProto(session *AgentSession) *wisdevpb.QuestionStateSummary {
	state := BuildQuestionStateSummary(session)
	toInt32 := func(value any) int32 {
		switch v := value.(type) {
		case int:
			return int32(v)
		case int32:
			return v
		case int64:
			return int32(v)
		case float64:
			return int32(v)
		default:
			return 0
		}
	}
	toFloat32 := func(value any) float32 {
		switch v := value.(type) {
		case float32:
			return v
		case float64:
			return float32(v)
		case int:
			return float32(v)
		default:
			return 0
		}
	}
	remaining := []string{}
	if raw, ok := state["remainingQuestionIds"].([]string); ok {
		remaining = raw
	}
	stopReason := QuestionStopReasonNone
	if raw, ok := state["stopReason"].(QuestionStopReason); ok {
		stopReason = raw
	}
	return &wisdevpb.QuestionStateSummary{
		AnsweredCount:        toInt32(state["answeredCount"]),
		MinQuestions:         toInt32(state["minQuestions"]),
		MaxQuestions:         toInt32(state["maxQuestions"]),
		ComplexityScore:      toFloat32(state["complexityScore"]),
		RemainingQuestionIds: remaining,
		StopReason:           mapQuestionStopReasonToProto(stopReason),
	}
}

func (s *agentGatewayGRPCServer) CreateSession(ctx context.Context, req *wisdevpb.CreateSessionRequest) (*wisdevpb.CreateSessionResponse, error) {
	session, err := s.gateway.CreateSession(ctx, req.GetUserId(), req.GetQuery())
	if err != nil {
		return nil, err
	}
	return &wisdevpb.CreateSessionResponse{
		Session: buildProtoSession(session),
	}, nil
}

func (s *agentGatewayGRPCServer) GetSession(ctx context.Context, req *wisdevpb.GetSessionRequest) (*wisdevpb.GetSessionResponse, error) {
	session, err := s.gateway.Store.Get(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	return &wisdevpb.GetSessionResponse{
		Session: buildProtoSession(session),
	}, nil
}

func (s *agentGatewayGRPCServer) ResumeSession(ctx context.Context, req *wisdevpb.ResumeSessionRequest) (*wisdevpb.ResumeSessionResponse, error) {
	session, err := LoadSessionCheckpoint(ctx, s.gateway.Checkpoints, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	if err := transitionSessionStatus(session, SessionExecutingPlan); err != nil {
		return nil, err
	}
	if err := s.gateway.Store.Put(ctx, session, s.gateway.SessionTTL); err != nil {
		return nil, err
	}
	return &wisdevpb.ResumeSessionResponse{
		Session: buildProtoSession(session),
	}, nil
}

func (s *agentGatewayGRPCServer) GetNextQuestion(ctx context.Context, req *wisdevpb.GetNextQuestionRequest) (*wisdevpb.GetNextQuestionResponse, error) {
	session, err := s.gateway.Store.Get(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	questionMap := questionByID()
	nextQuestionID := FindNextQuestionID(session)
	if nextQuestionID == "" {
		return &wisdevpb.GetNextQuestionResponse{
			Done:          true,
			QuestionState: buildQuestionStateSummaryProto(session),
			StopReason:    mapQuestionStopReasonToProto(session.QuestionStopReason),
		}, nil
	}
	q := questionMap[nextQuestionID]
	opts := make([]string, 0, len(q.Options))
	for _, item := range q.Options {
		opts = append(opts, item.Value)
	}
	return &wisdevpb.GetNextQuestionResponse{
		QuestionId:    q.ID,
		Question:      q.Question,
		Options:       opts,
		Done:          false,
		QuestionState: buildQuestionStateSummaryProto(session),
		StopReason:    mapQuestionStopReasonToProto(session.QuestionStopReason),
	}, nil
}

func (s *agentGatewayGRPCServer) SubmitAnswer(ctx context.Context, req *wisdevpb.SubmitAnswerRequest) (*wisdevpb.SubmitAnswerResponse, error) {
	session, err := s.gateway.Store.Get(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	session.Answers[req.GetQuestionId()] = Answer{
		QuestionID: req.GetQuestionId(),
		Values:     req.GetValues(),
		AnsweredAt: NowMillis(),
	}
	if req.GetQuestionId() == "q1_domain" && len(req.GetValues()) > 0 {
		session.DetectedDomain = req.GetValues()[0]
	}
	session.CurrentQuestionIndex = AnsweredQuestionCount(session)
	completed, stopReason := ShouldStopQuestioning(session)
	if req.GetProceed() {
		completed = true
		stopReason = QuestionStopReasonUserProceed
	}
	session.QuestionStopReason = stopReason
	if completed {
		if err := transitionSessionStatus(session, SessionGeneratingTree); err != nil {
			return nil, err
		}
		session.Plan = BuildDefaultPlan(session)
	}
	if err := s.gateway.Store.Put(ctx, session, s.gateway.SessionTTL); err != nil {
		return nil, err
	}
	return &wisdevpb.SubmitAnswerResponse{
		Session:       buildProtoSession(session),
		Completed:     completed,
		StopReason:    mapQuestionStopReasonToProto(stopReason),
		QuestionState: buildQuestionStateSummaryProto(session),
	}, nil
}

func (s *agentGatewayGRPCServer) ExecutePlan(req *wisdevpb.ExecutePlanRequest, stream wisdevpb.AgentGateway_ExecutePlanServer) error {
	session, err := s.gateway.Store.Get(stream.Context(), req.GetSessionId())
	if err != nil {
		return err
	}
	if session.Plan == nil {
		session.Plan = BuildDefaultPlan(session)
	}
	ch := make(chan PlanExecutionEvent, 16)
	go s.gateway.Executor.Execute(stream.Context(), session, ch)
	for event := range ch {
		update := mapPlanExecutionEventToUpdate(event)
		if err := stream.Send(update); err != nil {
			return err
		}
	}
	return nil
}

func mapPlanExecutionEventToUpdate(event PlanExecutionEvent) *wisdevpb.PlanExecutionUpdate {
	update := &wisdevpb.PlanExecutionUpdate{
		TraceId:   event.TraceID,
		SessionId: event.SessionID,
		PlanId:    event.PlanID,
	}
	switch event.Type {
	case EventStepStarted:
		laneID, _ := event.Payload["laneId"].(int)
		action, _ := event.Payload["action"].(string)
		update.Update = &wisdevpb.PlanExecutionUpdate_StepStarted{
			StepStarted: &wisdevpb.StepStarted{
				StepId:  event.StepID,
				Action:  action,
				LaneId:  int32(laneID),
				Attempt: 1,
			},
		}
	case EventStepCompleted:
		laneID, _ := event.Payload["laneId"].(int)
		attempts, _ := event.Payload["attempts"].(int)
		degraded, _ := event.Payload["degraded"].(bool)
		update.Update = &wisdevpb.PlanExecutionUpdate_StepCompleted{
			StepCompleted: &wisdevpb.StepCompleted{
				StepId:   event.StepID,
				LaneId:   int32(laneID),
				Attempts: int32(attempts),
				Degraded: degraded,
			},
		}
	case EventStepFailed:
		laneID, _ := event.Payload["laneId"].(int)
		attempts, _ := event.Payload["attempts"].(int)
		degraded, _ := event.Payload["degraded"].(bool)
		errorCode, _ := event.Payload["errorCode"].(string)
		if errorCode == "" {
			if parts := strings.SplitN(event.Message, ":", 2); len(parts) > 1 {
				errorCode = parts[0]
			}
		}
		update.Update = &wisdevpb.PlanExecutionUpdate_StepFailed{
			StepFailed: &wisdevpb.StepFailed{
				StepId:    event.StepID,
				Reason:    event.Message,
				ErrorCode: errorCode,
				LaneId:    int32(laneID),
				Attempts:  int32(attempts),
				Degraded:  degraded,
			},
		}
	case EventPaperFound:
		paperId, _ := event.Payload["paperId"].(string)
		title, _ := event.Payload["title"].(string)
		Source, _ := event.Payload["source"].(string)
		update.Update = &wisdevpb.PlanExecutionUpdate_PaperFound{
			PaperFound: &wisdevpb.PaperFound{
				PaperId: paperId,
				Title:   title,
				Source:  Source,
			},
		}
	case EventConfirmationNeed:
		approvalToken, _ := event.Payload["approvalToken"].(string)
		update.Update = &wisdevpb.PlanExecutionUpdate_ConfirmationNeeded{
			ConfirmationNeeded: &wisdevpb.ConfirmationRequired{
				Action:          event.Message,
				Rationale:       event.Message,
				ApprovalToken:   approvalToken,
				AllowedActions:  []string{"approve", "skip", "edit_payload", "reject_replan"},
				StepId:          event.StepID,
				GuardrailReason: event.Message,
			},
		}
	case EventProgress:
		completed, _ := event.Payload["completed"].(int)
		total, _ := event.Payload["total"].(int)
		failed, _ := event.Payload["failed"].(int)
		update.Update = &wisdevpb.PlanExecutionUpdate_Progress{
			Progress: &wisdevpb.Progress{Completed: int32(completed), Total: int32(total), Failed: int32(failed)},
		}
	case EventPlanCancelled:
		status, _ := event.Payload["status"].(string)
		if status == "" {
			status = "cancelled"
		}
		update.Update = &wisdevpb.PlanExecutionUpdate_ExecutionCancelled{
			ExecutionCancelled: &wisdevpb.ExecutionCancelled{
				Reason: event.Message,
				Status: status,
			},
		}
	default:
		replanCount, _ := event.Payload["replanCount"].(int)
		sourceStepID, _ := event.Payload["fromStep"].(string)
		if sourceStepID == "" {
			sourceStepID, _ = event.Payload["replanFor"].(string)
		}
		update.Update = &wisdevpb.PlanExecutionUpdate_PlanRevised{
			PlanRevised: &wisdevpb.PlanRevised{Rationale: event.Message, ReplanCount: int32(replanCount), SourceStepId: sourceStepID},
		}
	}
	return update
}

func (s *agentGatewayGRPCServer) CancelPlan(ctx context.Context, req *wisdevpb.CancelPlanRequest) (*wisdevpb.CancelPlanResponse, error) {
	session, err := s.gateway.Store.Get(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	if err := transitionSessionStatus(session, SessionPaused); err != nil {
		return nil, err
	}
	if err := s.gateway.Store.Put(ctx, session, s.gateway.SessionTTL); err != nil {
		return nil, err
	}
	return &wisdevpb.CancelPlanResponse{Cancelled: true}, nil
}

func mapRiskToProto(risk RiskLevel) wisdevpb.RiskLevel {
	switch risk {
	case RiskLevelHigh:
		return wisdevpb.RiskLevel_HIGH
	case RiskLevelMedium:
		return wisdevpb.RiskLevel_MEDIUM
	case RiskLevelLow:
		return wisdevpb.RiskLevel_LOW
	default:
		return wisdevpb.RiskLevel_RISK_LEVEL_UNSPECIFIED
	}
}

func mapTargetToProto(target ExecutionTarget) wisdevpb.ExecutionTarget {
	switch target {
	case ExecutionTargetGoNative:
		return wisdevpb.ExecutionTarget_GO_NATIVE
	case ExecutionTargetPythonCapability:
		return wisdevpb.ExecutionTarget_PYTHON_CAPABILITY
	case ExecutionTargetPythonSandbox:
		return wisdevpb.ExecutionTarget_PYTHON_SANDBOX
	default:
		return wisdevpb.ExecutionTarget_EXECUTION_TARGET_UNSPECIFIED
	}
}

func (s *agentGatewayGRPCServer) ListTools(ctx context.Context, _ *wisdevpb.ListToolsRequest) (*wisdevpb.ListToolsResponse, error) {
	tools := s.gateway.Registry.List()
	resp := make([]*wisdevpb.ToolDefinition, 0, len(tools))
	for _, Tool := range tools {
		resp = append(resp, &wisdevpb.ToolDefinition{
			Name:                Tool.Name,
			Description:         Tool.Description,
			Risk:                mapRiskToProto(Tool.Risk),
			ParameterJsonSchema: string(Tool.ParameterSchema),
			ExecutionTarget:     mapTargetToProto(Tool.ExecutionTarget),
			Parallelizable:      Tool.Parallelizable,
			Dependencies:        Tool.Dependencies,
		})
	}
	return &wisdevpb.ListToolsResponse{Tools: resp}, nil
}

func (s *agentGatewayGRPCServer) InvokeTool(ctx context.Context, req *wisdevpb.InvokeToolRequest) (*wisdevpb.InvokeToolResponse, error) {
	session, err := s.gateway.Store.Get(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	Tool, err := s.gateway.Registry.Get(req.GetToolName())
	if err != nil {
		return nil, err
	}
	payload := map[string]any{}
	if req.GetPayloadJson() != "" {
		_ = json.Unmarshal([]byte(req.GetPayloadJson()), &payload)
	}
	var result map[string]any
	switch Tool.ExecutionTarget {
	case ExecutionTargetGoNative:
		// Dispatch on the specific tool name rather than treating every
		// Go-native tool as a parallel search.
		switch req.GetToolName() {
		case "parallel_search", "fast_search", "web_search", "search":
			queryUsed := strings.TrimSpace(ResolveSessionSearchQuery(session.Query, session.CorrectedQuery, session.OriginalQuery))
			if queryUsed == "" {
				return &wisdevpb.InvokeToolResponse{Ok: false, ResultJson: ""}, nil
			}
			papers, _, searchErr := RetrieveCanonicalPapersWithRegistry(ctx, s.gateway.Redis, s.gateway.SearchRegistry, queryUsed, 10)
			if searchErr != nil {
				return &wisdevpb.InvokeToolResponse{Ok: false, ResultJson: ""}, nil
			}
			result = map[string]any{
				"papers":    papers,
				"queryUsed": queryUsed,
			}
		default:
			return &wisdevpb.InvokeToolResponse{Ok: false, ResultJson: ""},
				fmt.Errorf("no Go-native handler registered for tool %q", req.GetToolName())
		}
	default:
		r, execErr := s.gateway.PythonExecute(ctx, req.GetToolName(), payload, session)
		if execErr != nil {
			return &wisdevpb.InvokeToolResponse{Ok: false, ResultJson: ""}, nil
		}
		result = r
	}
	b, _ := json.Marshal(result)
	return &wisdevpb.InvokeToolResponse{Ok: true, ResultJson: string(b)}, nil
}

func (s *agentGatewayGRPCServer) SaveCheckpoint(ctx context.Context, req *wisdevpb.SaveCheckpointRequest) (*wisdevpb.SaveCheckpointResponse, error) {
	session, err := s.gateway.Store.Get(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	if err := SaveSessionCheckpoint(ctx, s.gateway.Checkpoints, session, s.gateway.CheckpointTTL); err != nil {
		return nil, err
	}
	return &wisdevpb.SaveCheckpointResponse{Saved: true}, nil
}

func (s *agentGatewayGRPCServer) LoadCheckpoint(ctx context.Context, req *wisdevpb.LoadCheckpointRequest) (*wisdevpb.LoadCheckpointResponse, error) {
	session, err := LoadSessionCheckpoint(ctx, s.gateway.Checkpoints, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	return &wisdevpb.LoadCheckpointResponse{
		Session: buildProtoSession(session),
	}, nil
}

func (s *agentGatewayGRPCServer) ToolSearch(ctx context.Context, req *wisdevpb.ToolSearchRequest) (*wisdevpb.ToolSearchResponse, error) {
	query := strings.TrimSpace(req.GetQuery())
	if query == "" {
		return &wisdevpb.ToolSearchResponse{
			Query: query,
			Error: &wisdevpb.ExecutionError{
				Code:       "MISSING_QUERY",
				Message:    "query is required",
				HttpStatus: 400,
				Retryable:  false,
			},
		}, nil
	}

	candidates := req.GetCandidateTools()
	tools := make([]ToolDefinition, 0, len(candidates))
	if len(candidates) > 0 {
		for _, Tool := range candidates {
			if Tool == nil || strings.TrimSpace(Tool.GetName()) == "" {
				continue
			}
			tools = append(tools, ToolDefinition{
				Name:        Tool.GetName(),
				Description: Tool.GetDescription(),
				Risk: func() RiskLevel {
					switch Tool.GetRisk() {
					case wisdevpb.RiskLevel_HIGH:
						return RiskLevelHigh
					case wisdevpb.RiskLevel_MEDIUM:
						return RiskLevelMedium
					default:
						return RiskLevelLow
					}
				}(),
			})
		}
	} else {
		for _, Tool := range s.gateway.Registry.List() {
			tools = append(tools, Tool)
		}
	}

	ranked := RankTools(query, tools, int(req.GetLimit()))
	out := make([]*wisdevpb.ToolSearchResult, 0, len(ranked))
	for _, item := range ranked {
		out = append(out, &wisdevpb.ToolSearchResult{
			Name:        item.Name,
			Description: item.Description,
			Risk: func() wisdevpb.RiskLevel {
				switch strings.ToLower(strings.TrimSpace(item.Risk)) {
				case "high":
					return wisdevpb.RiskLevel_HIGH
				case "medium":
					return wisdevpb.RiskLevel_MEDIUM
				default:
					return wisdevpb.RiskLevel_LOW
				}
			}(),
			Score: float32(item.Score),
		})
	}
	guard := policy.EvaluateGuardrail(
		s.gateway.PolicyConfig,
		policy.NewBudgetState(s.gateway.PolicyConfig),
		policy.RiskLow,
		false,
		1,
	)
	return &wisdevpb.ToolSearchResponse{
		Query: query,
		Tools: out,
		Guardrail: &wisdevpb.GuardrailDecision{
			Allowed:              guard.Allowed,
			RequiresConfirmation: guard.RequiresConfirmation,
			Reason:               guard.Reason,
			MaxToolCalls:         int32(s.gateway.PolicyConfig.MaxToolCallsPerSession),
			MaxScriptRuns:        int32(s.gateway.PolicyConfig.MaxScriptRunsPerSession),
			MaxCostCents:         int32(s.gateway.PolicyConfig.MaxCostPerSessionCents),
		},
	}, nil
}

func (s *agentGatewayGRPCServer) ProgrammaticLoop(ctx context.Context, req *wisdevpb.ProgrammaticLoopRequest) (*wisdevpb.ProgrammaticLoopResponse, error) {
	action := strings.TrimSpace(req.GetAction())
	if action == "" {
		return &wisdevpb.ProgrammaticLoopResponse{
			SessionId: req.GetSessionId(),
			Action:    action,
			Completed: false,
			Error: &wisdevpb.ExecutionError{
				Code:       "MISSING_ACTION",
				Message:    "action is required",
				HttpStatus: 400,
				Retryable:  false,
			},
		}, nil
	}

	payload := map[string]any{}
	if raw := strings.TrimSpace(req.GetPayloadJson()); raw != "" {
		_ = json.Unmarshal([]byte(raw), &payload)
	}

	iterations := int(req.GetMaxIterations())
	if iterations <= 0 {
		iterations = 1
	}
	if iterations > 8 {
		iterations = 8
	}

	var session *AgentSession
	if sid := strings.TrimSpace(req.GetSessionId()); sid != "" && s.gateway != nil && s.gateway.Store != nil {
		if loaded, err := s.gateway.Store.Get(ctx, sid); err == nil {
			if loaded != nil {
				session = loaded
				remainingCalls := session.Budget.MaxToolCalls - session.Budget.ToolCallsUsed
				if remainingCalls > 0 && iterations > remainingCalls {
					iterations = remainingCalls
				}
			}
		}
	}
	if iterations <= 0 {
		iterations = 1
	}

	execFn := s.gateway.ProgrammaticLoopExecutor()
	if execFn == nil {
		return &wisdevpb.ProgrammaticLoopResponse{
			SessionId: req.GetSessionId(),
			Action:    action,
			Completed: false,
			Error: &wisdevpb.ExecutionError{
				Code:       "PROGRAMMATIC_LOOP_UNAVAILABLE",
				Message:    "programmatic loop executor is unavailable",
				HttpStatus: 503,
				Retryable:  true,
			},
		}, nil
	}

	tree := RunProgrammaticTreeLoop(ctx, execFn, session, action, payload, iterations, nil)
	if session != nil && len(tree.BranchArtifacts) > 0 && s.gateway != nil && s.gateway.Store != nil {
		session.UpdatedAt = NowMillis()
		_ = s.gateway.Store.Put(ctx, session, 0)
	}
	loop := make([]*wisdevpb.ProgrammaticLoopIteration, 0, len(tree.Iterations))
	completed := tree.Completed
	for _, item := range tree.Iterations {
		entry := &wisdevpb.ProgrammaticLoopIteration{
			Iteration: int32(item.Iteration),
			Success:   item.Success,
		}
		if item.Output != nil {
			body, _ := json.Marshal(item.Output)
			entry.OutputJson = string(body)
		}
		if item.Error != nil {
			entry.Error = &wisdevpb.ExecutionError{
				Code:       classifyErrorCode(item.Error),
				Message:    item.Error.Error(),
				HttpStatus: 400,
				Retryable:  true,
			}
		}
		loop = append(loop, entry)
	}

	return &wisdevpb.ProgrammaticLoopResponse{
		SessionId:  req.GetSessionId(),
		Action:     action,
		Iterations: loop,
		Completed:  completed,
		Guardrail: &wisdevpb.GuardrailDecision{
			Allowed:              true,
			RequiresConfirmation: false,
			Reason:               "loop_completed",
			MaxToolCalls:         int32(s.gateway.PolicyConfig.MaxToolCallsPerSession),
			MaxScriptRuns:        int32(s.gateway.PolicyConfig.MaxScriptRunsPerSession),
			MaxCostCents:         int32(s.gateway.PolicyConfig.MaxCostPerSessionCents),
		},
	}, nil
}

func (s *agentGatewayGRPCServer) StructuredOutput(_ context.Context, req *wisdevpb.StructuredOutputRequest) (*wisdevpb.StructuredOutputResponse, error) {
	normalized := map[string]any{
		"prompt": req.GetPrompt(),
	}
	validation := policy.ValidateStructuredOutput(req.GetJsonSchema(), normalized)
	resultBody, _ := json.Marshal(map[string]any{
		"normalized": validation.Normalized,
		"reason":     validation.Reason,
	})
	return &wisdevpb.StructuredOutputResponse{
		ResultJson:  string(resultBody),
		SchemaValid: validation.Valid,
		TraceId:     req.GetTraceId(),
	}, nil
}
