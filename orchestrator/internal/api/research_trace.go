package api

import (
	"fmt"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func buildResearchLoopTraceEmitter(
	agentGateway *wisdev.AgentGateway,
	sessionID string,
	userID string,
	route string,
	plane wisdev.ResearchExecutionPlane,
	traceID string,
	query string,
) func(wisdev.PlanExecutionEvent) {
	return func(event wisdev.PlanExecutionEvent) {
		appendResearchLoopTraceEvent(agentGateway, sessionID, userID, route, plane, traceID, query, event)
	}
}

func appendResearchLoopTraceEvent(
	agentGateway *wisdev.AgentGateway,
	sessionID string,
	userID string,
	route string,
	plane wisdev.ResearchExecutionPlane,
	traceID string,
	query string,
	event wisdev.PlanExecutionEvent,
) {
	if agentGateway == nil || agentGateway.Journal == nil {
		return
	}
	resolvedSessionID := strings.TrimSpace(firstNonEmpty(event.SessionID, sessionID))
	if resolvedSessionID == "" {
		resolvedSessionID = strings.TrimSpace(firstNonEmpty(event.PlanID, traceID))
	}
	resolvedTraceID := strings.TrimSpace(firstNonEmpty(event.TraceID, traceID, wisdev.NewTraceID()))
	createdAt := event.CreatedAt
	if createdAt == 0 {
		createdAt = wisdev.NowMillis()
	}
	payload := cloneResearchLoopEventPayload(event.Payload)
	payload["component"] = firstNonEmpty(wisdev.AsOptionalString(payload["component"]), "api.research_routes")
	payload["operation"] = firstNonEmpty(wisdev.AsOptionalString(payload["operation"]), "unified_research_loop")
	payload["researchPlane"] = strings.TrimSpace(string(plane))
	payload["route"] = strings.TrimSpace(route)
	payload["queryPreview"] = wisdev.QueryPreview(query)
	payload["queryHash"] = searchQueryFingerprint(query)
	payload["traceId"] = resolvedTraceID
	if strings.TrimSpace(event.Owner) != "" {
		payload["owner"] = strings.TrimSpace(event.Owner)
	}
	if strings.TrimSpace(event.SubAgent) != "" {
		payload["subAgent"] = strings.TrimSpace(event.SubAgent)
	}
	if strings.TrimSpace(event.OwningComponent) != "" {
		payload["owningComponent"] = strings.TrimSpace(event.OwningComponent)
	}
	if strings.TrimSpace(event.ResultOrigin) != "" {
		payload["resultOrigin"] = strings.TrimSpace(event.ResultOrigin)
	}
	if strings.TrimSpace(event.ResultFusionIntent) != "" {
		payload["resultFusionIntent"] = strings.TrimSpace(event.ResultFusionIntent)
	}
	if event.ResultConfidence > 0 {
		payload["resultConfidence"] = event.ResultConfidence
	}
	if event.Type != "" {
		payload["eventType"] = string(event.Type)
	}

	eventID := strings.TrimSpace(event.TraceID)
	if eventID == "" {
		eventID = stableWisDevResearchTraceEventID(resolvedTraceID, event.Type, event.SubAgent, event.StepID, event.Message, createdAt)
	}

	agentGateway.Journal.Append(wisdev.RuntimeJournalEntry{
		EventID:   eventID,
		TraceID:   resolvedTraceID,
		SessionID: resolvedSessionID,
		UserID:    strings.TrimSpace(userID),
		PlanID:    strings.TrimSpace(event.PlanID),
		StepID:    strings.TrimSpace(event.StepID),
		EventType: string(event.Type),
		Path:      fmt.Sprintf("/agent/sessions/%s/events", resolvedSessionID),
		Status:    researchLoopEventStatus(event.Type),
		CreatedAt: createdAt,
		Summary:   strings.TrimSpace(event.Message),
		Payload:   payload,
		Metadata: map[string]any{
			"source":        "unified_research_runtime",
			"researchPlane": strings.TrimSpace(string(plane)),
			"route":         strings.TrimSpace(route),
		},
	})
}

func stableWisDevResearchTraceEventID(traceID string, eventType wisdev.PlanExecutionEventType, subAgent string, stepID string, message string, createdAt int64) string {
	parts := []string{
		strings.TrimSpace(firstNonEmpty(traceID, wisdev.NewTraceID())),
		strings.TrimSpace(string(eventType)),
		strings.TrimSpace(subAgent),
		strings.TrimSpace(stepID),
		strings.TrimSpace(message),
		fmt.Sprintf("%d", createdAt),
	}
	return strings.Join(parts, ":")
}

func cloneResearchLoopEventPayload(payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(payload)+12)
	for key, value := range payload {
		if strings.TrimSpace(key) == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func researchLoopEventStatus(eventType wisdev.PlanExecutionEventType) string {
	switch eventType {
	case wisdev.EventCompleted:
		return "completed"
	case wisdev.EventStepFailed:
		return "failed"
	default:
		return "running"
	}
}
