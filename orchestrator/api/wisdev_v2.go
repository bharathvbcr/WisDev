package api

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

type wisdevV2Server struct {
	gateway *wisdev.AgentGateway
	rag     *RAGHandler
}

func (s *wisdevV2Server) journalEvent(
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
	if s.gateway == nil || s.gateway.Journal == nil {
		return
	}
	s.gateway.Journal.Append(wisdev.RuntimeJournalEntry{
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

func RegisterV2WisDevRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway, ragHandler *RAGHandler) {
	s := &wisdevV2Server{
		gateway: agentGateway,
		rag:     ragHandler,
	}

	agentID := "unknown"
	if agentGateway != nil && agentGateway.ADKRuntime != nil {
		agentID = agentGateway.ADKRuntime.Config.Runtime.AgentID
	}
	slog.Info("Registering WisDev V2 routes", "agentId", agentID)

	mux.HandleFunc("/v2/wisdev/multi-agent", s.rag.HandleMultiAgent)

	if agentGateway == nil {
		registerV2StubRoutes(mux)
		return
	}

	s.registerSessionRoutes(mux, agentGateway)
	s.registerQuestioningRoutes(mux, agentGateway)
	s.registerPlanRoutes(mux, agentGateway)
	s.registerToolRoutes(mux, agentGateway)
	s.registerSearchRoutes(mux, agentGateway)
	s.registerRAGRoutes(mux, agentGateway)
	s.registerResearchRoutes(mux, agentGateway)
	s.registerObserveRoutes(mux, agentGateway)
	s.registerDraftingRoutes(mux, agentGateway)
	s.registerFullPaperRoutes(mux, agentGateway)
	s.registerHealthRoutes(mux, agentGateway)
	s.registerPolicyRoutes(mux, agentGateway)
	s.registerExtraRoutes(mux, agentGateway)
}

func registerV2StubRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v2/wisdev/analyze-query", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "result", map[string]any{"complexity": 0.5})
	})
	mux.HandleFunc("/v2/wisdev/traces", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "traces", []any{})
	})
	mux.HandleFunc("/v2/wisdev/programmatic-loop", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "result", map[string]any{"ok": true})
	})
	mux.HandleFunc("/v2/wisdev/observe", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "result", map[string]any{"ok": true})
	})
	mux.HandleFunc("/v2/wisdev/structured-output", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "result", map[string]any{"ok": true})
	})
	mux.HandleFunc("/v2/wisdev/wisdev.Plan", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "result", map[string]any{"ok": true})
	})
	mux.HandleFunc("/v2/wisdev/wisdev.Tool-search", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "result", map[string]any{"ok": true})
	})
	mux.HandleFunc("/v2/wisdev/research/autonomous", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "autonomousResearch", map[string]any{
			"prismaReport":  map[string]any{"papers": []any{}},
			"searchResults": map[string]any{"papers": []any{}},
		})
	})
	mux.HandleFunc("/v2/wisdev/research/deep", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "deepResearch", map[string]any{"ok": true})
	})
}

func buildRagAnswerPayload(query string, domain string, papers []wisdev.Source) map[string]any {
	committee := buildMultiAgentCommitteeResult(query, domain, papers, 2, true)
	paperPayload := committee["papers"]
	citations := committee["citations"]
	answer := wisdev.AsOptionalString(committee["answer"])
	rawCitations, _ := citations.([]map[string]any)
	claims := make([]map[string]any, 0, len(rawCitations))
	for _, citation := range rawCitations {
		claims = append(claims, map[string]any{
			"claim": citation["claim"],
			"source": map[string]any{
				"id":         citation["sourceId"],
				"title":      citation["sourceTitle"],
				"confidence": citation["confidence"],
			},
		})
	}
	verification := buildEvidenceGatePayload(claims, 0)
	return map[string]any{
		"query":     query,
		"answer":    answer,
		"papers":    paperPayload,
		"citations": citations,
		"committee": committee,
		"verificationSummary": map[string]any{
			"status":         verification["verdict"],
			"strictGatePass": verification["strictGatePass"],
			"citationBacked": len(rawCitations) > 0,
			"claimCount":     verification["claimCount"],
		},
		"evidenceBundle": map[string]any{
			"canonicalSources": paperPayload,
			"findings":         citations,
		},
		"workerMetadata": map[string]any{
			"documentWorker": "python-docling",
			"sourceOfTruth":  "go-control-plane",
			"retrievalMode":  "committee_hybrid",
		},
	}
}
