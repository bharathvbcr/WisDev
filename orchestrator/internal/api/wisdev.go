package api

import (
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
	"strings"
	"time"
)

type WisDevHandler struct {
	sessions   *wisdev.SessionManager
	guided     *wisdev.GuidedFlow
	autonomous *wisdev.AutonomousWorker
	gateway    *wisdev.AgentGateway
	brainCaps  *wisdev.BrainCapabilities
	compiler   *wisdev.Paper2SkillCompiler
	rag        *RAGHandler
}

func NewWisDevHandler(sessions *wisdev.SessionManager, guided *wisdev.GuidedFlow, autonomous *wisdev.AutonomousWorker, gateway *wisdev.AgentGateway, brainCaps *wisdev.BrainCapabilities, compiler *wisdev.Paper2SkillCompiler, rag *RAGHandler) *WisDevHandler {
	return &WisDevHandler{
		sessions:   sessions,
		guided:     guided,
		autonomous: autonomous,
		gateway:    gateway,
		brainCaps:  brainCaps,
		compiler:   compiler,
		rag:        rag,
	}
}

func (h *WisDevHandler) journalEvent(
	eventType string,
	path string,
	traceID string,
	sessionID string,
	userID string,
	planID string,
	stepID string,
	summary string,
	payload map[string]any,
	metadata map[string]any,
) {
	if h.gateway == nil || h.gateway.Journal == nil {
		return
	}
	h.gateway.Journal.Append(wisdev.RuntimeJournalEntry{
		EventID:   wisdev.NewTraceID(),
		TraceID:   traceID,
		SessionID: strings.TrimSpace(sessionID),
		UserID:    strings.TrimSpace(userID),
		PlanID:    strings.TrimSpace(planID),
		StepID:    strings.TrimSpace(stepID),
		EventType: eventType,
		Path:      path,
		Status:    "ok",
		CreatedAt: time.Now().UnixMilli(),
		Summary:   summary,
		Payload:   cloneAnyMap(payload),
		Metadata:  cloneAnyMap(metadata),
	})
}
