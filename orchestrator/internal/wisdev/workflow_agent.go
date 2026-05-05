package wisdev

import (
	"fmt"
	"iter"
	"log/slog"
	"time"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// WisDevWorkflowAgent is an ADK-native agent that manages the execution of a research DAG.
// It replaces the high-level loop of the PlanExecutor to provide better framework alignment,
// including native support for ADK sessions, telemetry, and multi-agent coordination.
type WisDevWorkflowAgent struct {
	gateway  *AgentGateway
	executor *PlanExecutor
}

// NewWisDevWorkflowAgent creates a new workflow agent that wraps the existing PlanExecutor.
func NewWisDevWorkflowAgent(gateway *AgentGateway, executor *PlanExecutor, subAgents []agent.Agent) (agent.Agent, error) {
	wa := &WisDevWorkflowAgent{
		gateway:  gateway,
		executor: executor,
	}

	return agent.New(agent.Config{
		Name:        "wisdev-workflow",
		Description: "Research orchestration agent that executes structured DAG plans.",
		SubAgents:   subAgents,
		Run:         wa.Run,
	})
}

// Run implements the agent.Agent interface.
func (wa *WisDevWorkflowAgent) Run(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		if wa == nil || wa.gateway == nil || wa.executor == nil {
			yield(nil, fmt.Errorf("workflow agent runtime is not fully initialized"))
			return
		}

		sessionID := ctx.Session().ID()
		userID := ctx.Session().UserID()

		slog.Info("WisDevWorkflowAgent execution started",
			"sessionId", sessionID,
			"userId", userID)

		// 1. Resolve or create the WisDev session state
		// We bridge ADK session to WisDev AgentSession
		wisdevSession, err := wa.gateway.GetSession(ctx, sessionID)
		if err != nil || wisdevSession == nil {
			// Fallback to ensuring a session exists
			query := ""
			if ctx.UserContent() != nil && len(ctx.UserContent().Parts) > 0 {
				query = ctx.UserContent().Parts[0].Text
			}
			wisdevSession = wa.gateway.ensureADKSessionWithContext(ctx, sessionID, query, "")
		}

		if wisdevSession.Plan == nil {
			yield(nil, fmt.Errorf("no execution plan found for session %s", sessionID))
			return
		}

		// 2. Execute the plan using the existing PlanExecutor logic
		// but wrapped in the ADK agent turn.
		eventsCh := make(chan PlanExecutionEvent, 16)

		// Run executor in a goroutine
		go wa.executor.Execute(ctx, wisdevSession, eventsCh)

		// 3. Map PlanExecutionEvents to ADK session.Events and yield them
		for event := range eventsCh {
			adkEvent := wa.mapToADKEvent(ctx, event)
			if adkEvent != nil {
				if !yield(adkEvent, nil) {
					return
				}
			}
		}

		slog.Info("WisDevWorkflowAgent execution completed", "sessionId", sessionID)
	}
}

// mapToADKEvent converts WisDev internal execution events to ADK session events.
func (wa *WisDevWorkflowAgent) mapToADKEvent(ctx agent.InvocationContext, event PlanExecutionEvent) *session.Event {
	adkEvent := session.NewEvent(ctx.InvocationID())
	adkEvent.Author = "wisdev-workflow"
	adkEvent.Timestamp = workflowEventTimestamp(event.CreatedAt)
	if metadata := workflowEventMetadata(event); len(metadata) > 0 {
		adkEvent.CustomMetadata = metadata
	}

	switch event.Type {
	case EventStepStarted:
		adkEvent.Content = &genai.Content{
			Parts: []*genai.Part{
				{Text: fmt.Sprintf("Starting step: %s (%s)", event.StepID, event.Message)},
			},
		}
	case EventStepCompleted:
		adkEvent.Content = &genai.Content{
			Parts: []*genai.Part{
				{Text: fmt.Sprintf("Step completed: %s", event.StepID)},
			},
		}
		adkEvent.Actions.StateDelta = cloneAnyMap(event.Payload)
	case EventStepFailed:
		adkEvent.Content = &genai.Content{
			Parts: []*genai.Part{
				{Text: fmt.Sprintf("Step failed: %s. Error: %s", event.StepID, event.Message)},
			},
		}
		adkEvent.ErrorMessage = event.Message
	case EventPaperFound:
		// Papers are surfaced as individual events in the ADK stream
		title := ""
		if t, ok := event.Payload["title"].(string); ok {
			title = t
		}
		adkEvent.Content = &genai.Content{
			Parts: []*genai.Part{
				{Text: fmt.Sprintf("Evidence found: %s", title)},
			},
		}
	case EventConfirmationNeed:
		adkEvent.Content = &genai.Content{
			Parts: []*genai.Part{
				{Text: fmt.Sprintf("Confirmation required: %s", event.Message)},
			},
		}
		// In a real ADK implementation, we would use RequestedToolConfirmations
		// but for now we maintain compatibility with the WisDev UI via CustomMetadata.
	case EventPlanRevised:
		adkEvent.Content = &genai.Content{
			Parts: []*genai.Part{
				{Text: fmt.Sprintf("Plan revised: %s", event.Message)},
			},
		}
	case EventCompleted:
		adkEvent.Content = &genai.Content{
			Parts: []*genai.Part{
				{Text: "Research plan execution completed successfully."},
			},
		}
		adkEvent.TurnComplete = true
	case EventProgress:
		// Skip low-signal progress updates in the main event stream
		return nil
	default:
		return nil
	}

	return adkEvent
}

func workflowEventTimestamp(createdAt int64) time.Time {
	if createdAt <= 0 {
		createdAt = NowMillis()
	}
	return time.UnixMilli(createdAt)
}

func workflowEventMetadata(event PlanExecutionEvent) map[string]any {
	metadata := cloneAnyMap(event.Payload)
	if metadata == nil {
		metadata = make(map[string]any, 6)
	}
	if _, exists := metadata["eventType"]; !exists {
		metadata["eventType"] = string(event.Type)
	}
	if event.TraceID != "" {
		if _, exists := metadata["traceId"]; !exists {
			metadata["traceId"] = event.TraceID
		}
	}
	if event.SessionID != "" {
		if _, exists := metadata["sessionId"]; !exists {
			metadata["sessionId"] = event.SessionID
		}
	}
	if event.PlanID != "" {
		if _, exists := metadata["planId"]; !exists {
			metadata["planId"] = event.PlanID
		}
	}
	if event.StepID != "" {
		if _, exists := metadata["stepId"]; !exists {
			metadata["stepId"] = event.StepID
		}
	}
	if event.Message != "" {
		if _, exists := metadata["message"]; !exists {
			metadata["message"] = event.Message
		}
	}
	if event.Owner != "" {
		metadata["owner"] = event.Owner
	}
	if event.SubAgent != "" {
		metadata["subAgent"] = event.SubAgent
	}
	if event.OwningComponent != "" {
		metadata["owningComponent"] = event.OwningComponent
	}
	if event.ResultOrigin != "" {
		metadata["resultOrigin"] = event.ResultOrigin
	}
	if _, exists := metadata["resultConfidence"]; !exists && event.ResultConfidence != 0 {
		metadata["resultConfidence"] = event.ResultConfidence
	}
	if event.ResultFusionIntent != "" {
		metadata["resultFusionIntent"] = event.ResultFusionIntent
	}
	return metadata
}
