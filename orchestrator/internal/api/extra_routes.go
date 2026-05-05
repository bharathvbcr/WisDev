package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func (s *wisdevServer) registerExtraRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	outcomesRecent := func(w http.ResponseWriter, r *http.Request) {
		userID := strings.TrimSpace(r.URL.Query().Get("userId"))
		if userID == "" {
			WriteError(w, http.StatusForbidden, ErrUnauthorized, "userId is required", nil)
			return
		}
		if !requireOwnerAccess(w, r, userID) {
			return
		}

		maxResults := 20
		if raw := strings.TrimSpace(r.URL.Query().Get("maxResults")); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil {
				maxResults = parsed
			}
		}
		maxResults = boundedInt(maxResults, 20, 1, 200)

		outcomes := map[string]any{
			"avgReward":       0.0,
			"failedTools":     []string{},
			"successfulTools": []string{},
			"totalOutcomes":   0,
		}
		if agentGateway != nil && agentGateway.Journal != nil {
			outcomes = agentGateway.Journal.SummarizeRecentOutcomes(userID, maxResults)
		}
		writeEnvelope(w, "outcomes", outcomes)
	}
	mux.HandleFunc("/outcomes/recent", outcomesRecent)

	feedbackSave := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID    string `json:"userId"`
			SessionID string `json:"sessionId"`
			Feedback  string `json:"feedback"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		if strings.TrimSpace(req.UserID) == "" || strings.TrimSpace(req.SessionID) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "userId and sessionId are required", nil)
			return
		}
		if !requireOwnerAccess(w, r, strings.TrimSpace(req.UserID)) {
			return
		}
		writeEnvelope(w, "result", map[string]any{"ok": true})
	}
	mux.HandleFunc("/feedback/save", feedbackSave)

	feedbackGet := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID    string `json:"userId"`
			SessionID string `json:"sessionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		if strings.TrimSpace(req.UserID) == "" || strings.TrimSpace(req.SessionID) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "userId and sessionId are required", nil)
			return
		}
		if !requireOwnerAccess(w, r, strings.TrimSpace(req.UserID)) {
			return
		}
		writeEnvelope(w, "feedback", map[string]any{})
	}
	mux.HandleFunc("/feedback/get", feedbackGet)

	feedbackAnalytics := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID string `json:"userId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		if strings.TrimSpace(req.UserID) == "" {
			WriteError(w, http.StatusForbidden, ErrUnauthorized, "userId is required", nil)
			return
		}
		if !requireOwnerAccess(w, r, strings.TrimSpace(req.UserID)) {
			return
		}
		writeEnvelope(w, "analytics", map[string]any{})
	}
	mux.HandleFunc("/feedback/analytics", feedbackAnalytics)

	memoryProfileLearn := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID       string         `json:"userId"`
			Conversation map[string]any `json:"conversation"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		if strings.TrimSpace(req.UserID) == "" {
			WriteError(w, http.StatusForbidden, ErrUnauthorized, "userId is required", nil)
			return
		}
		if !requireOwnerAccess(w, r, strings.TrimSpace(req.UserID)) {
			return
		}

		if agentGateway == nil || agentGateway.Journal == nil {
			writeEnvelope(w, "result", map[string]any{
				"updated": false,
				"reason":  "runtime_journal_unavailable",
			})
			return
		}

		summary := summarizeRuntimeResearchProfile(agentGateway, req.UserID)
		currentProfile := asAnyMap(summary["profile"])
		if len(currentProfile) == 0 {
			currentProfile = defaultRuntimeResearchProfile(req.UserID)
		}
		updatedProfile := buildUpdatedRuntimeResearchProfile(req.UserID, req.Conversation, currentProfile)
		traceID := wisdev.NewTraceID()
		agentGateway.Journal.Append(wisdev.RuntimeJournalEntry{
			EventID:   wisdev.NewTraceID(),
			TraceID:   traceID,
			UserID:    strings.TrimSpace(req.UserID),
			SessionID: strings.TrimSpace(wisdev.AsOptionalString(req.Conversation["sessionId"])),
			EventType: wisdev.EventProfileLearn,
			Path:      "/memory/profile/learn",
			Status:    "ok",
			CreatedAt: updatedProfile["lastUpdated"].(int64),
			Summary:   "Research profile learned from session conversation.",
			Payload:   cloneAnyMap(updatedProfile),
			Metadata: map[string]any{
				"source": "memory_profile_learn",
			},
		})
		writeEnvelopeWithTraceID(w, traceID, "result", map[string]any{
			"updated": true,
			"profile": updatedProfile,
		})
	}
	mux.HandleFunc("/memory/profile/learn", memoryProfileLearn)

	memoryProfileGet := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID string `json:"userId"`
		}
		// Try to decode if body exists, otherwise use query
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.UserID == "" {
			req.UserID = r.URL.Query().Get("userId")
		}

		if strings.TrimSpace(req.UserID) == "" {
			WriteError(w, http.StatusForbidden, ErrUnauthorized, "userId is required", nil)
			return
		}
		if !requireOwnerAccess(w, r, strings.TrimSpace(req.UserID)) {
			return
		}
		writeEnvelope(w, "profile", summarizeRuntimeResearchProfile(agentGateway, req.UserID))
	}
	mux.HandleFunc("/memory/profile/get", memoryProfileGet)

	memorySessionSummariesUpsert := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID    string           `json:"userId"`
			Summaries []map[string]any `json:"summaries"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		if strings.TrimSpace(req.UserID) == "" {
			WriteError(w, http.StatusForbidden, ErrUnauthorized, "userId is required", nil)
			return
		}
		if !requireOwnerAccess(w, r, strings.TrimSpace(req.UserID)) {
			return
		}
		if agentGateway == nil || agentGateway.StateStore == nil {
			WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "runtime state store unavailable", nil)
			return
		}
		if err := agentGateway.StateStore.SaveSessionSummaries(req.UserID, req.Summaries); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to persist session summaries", map[string]any{"error": err.Error()})
			return
		}
		writeEnvelope(w, "result", map[string]any{"ok": true})
	}
	mux.HandleFunc("/memory/session-summaries/upsert", memorySessionSummariesUpsert)

	memorySessionSummariesGet := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID string `json:"userId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.UserID == "" {
			req.UserID = r.URL.Query().Get("userId")
		}
		if strings.TrimSpace(req.UserID) == "" {
			WriteError(w, http.StatusForbidden, ErrUnauthorized, "userId is required", nil)
			return
		}
		if !requireOwnerAccess(w, r, strings.TrimSpace(req.UserID)) {
			return
		}
		if agentGateway == nil || agentGateway.StateStore == nil {
			WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "runtime state store unavailable", nil)
			return
		}
		summaries, err := agentGateway.StateStore.LoadSessionSummaries(req.UserID)
		if err != nil {
			WriteError(w, http.StatusNotFound, ErrNotFound, "session summaries not found", nil)
			return
		}
		writeEnvelope(w, "summaries", summaries)
	}
	mux.HandleFunc("/memory/session-summaries/get", memorySessionSummariesGet)

	memoryProjectWorkspaceUpsert := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID    string         `json:"userId"`
			ProjectID string         `json:"projectId"`
			Workspace map[string]any `json:"workspace"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		if strings.TrimSpace(req.UserID) == "" || strings.TrimSpace(req.ProjectID) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "userId and projectId are required", nil)
			return
		}
		if !requireOwnerAccess(w, r, strings.TrimSpace(req.UserID)) {
			return
		}
		if agentGateway == nil || agentGateway.StateStore == nil {
			WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "runtime state store unavailable", nil)
			return
		}
		if len(req.Workspace) == 0 {
			req.Workspace = map[string]any{}
		}
		req.Workspace["projectId"] = req.ProjectID
		if err := agentGateway.StateStore.SaveProjectWorkspace(req.UserID, req.ProjectID, req.Workspace); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to persist project workspace", map[string]any{"error": err.Error()})
			return
		}
		writeEnvelope(w, "result", map[string]any{"ok": true})
	}
	mux.HandleFunc("/memory/project-workspace/upsert", memoryProjectWorkspaceUpsert)

	memoryProjectWorkspaceGet := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID    string `json:"userId"`
			ProjectID string `json:"projectId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.UserID == "" {
			req.UserID = r.URL.Query().Get("userId")
		}
		if req.ProjectID == "" {
			req.ProjectID = r.URL.Query().Get("projectId")
		}
		if strings.TrimSpace(req.UserID) == "" || strings.TrimSpace(req.ProjectID) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "userId and projectId are required", nil)
			return
		}
		if !requireOwnerAccess(w, r, strings.TrimSpace(req.UserID)) {
			return
		}
		if agentGateway == nil || agentGateway.StateStore == nil {
			WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "runtime state store unavailable", nil)
			return
		}
		workspace, err := agentGateway.StateStore.LoadProjectWorkspace(req.UserID, req.ProjectID)
		if err != nil {
			WriteError(w, http.StatusNotFound, ErrNotFound, "project workspace not found", nil)
			return
		}
		writeEnvelope(w, "workspace", workspace)
	}
	mux.HandleFunc("/memory/project-workspace/get", memoryProjectWorkspaceGet)

	researchMemoryQuery := func(w http.ResponseWriter, r *http.Request) {
		var req wisdev.ResearchMemoryQueryRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.UserID == "" {
			req.UserID = r.URL.Query().Get("userId")
		}
		if req.ProjectID == "" {
			req.ProjectID = r.URL.Query().Get("projectId")
		}
		if req.Query == "" {
			req.Query = r.URL.Query().Get("query")
		}
		if strings.TrimSpace(req.UserID) == "" || strings.TrimSpace(req.Query) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "userId and query are required", nil)
			return
		}
		if !requireOwnerAccess(w, r, strings.TrimSpace(req.UserID)) {
			return
		}
		if agentGateway == nil || agentGateway.ResearchMemory == nil {
			writeEnvelope(w, "researchMemory", &wisdev.ResearchMemoryQueryResponse{})
			return
		}
		result, err := agentGateway.ResearchMemory.Query(r.Context(), req)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to query research memory", map[string]any{"error": err.Error()})
			return
		}
		writeEnvelope(w, "researchMemory", result)
	}
	mux.HandleFunc("/research/memory/query", researchMemoryQuery)

	researchMemoryBackfill := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID    string `json:"userId"`
			ProjectID string `json:"projectId,omitempty"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.UserID == "" {
			req.UserID = r.URL.Query().Get("userId")
		}
		if req.ProjectID == "" {
			req.ProjectID = r.URL.Query().Get("projectId")
		}
		if strings.TrimSpace(req.UserID) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "userId is required", nil)
			return
		}
		if !requireOwnerAccess(w, r, strings.TrimSpace(req.UserID)) {
			return
		}
		if agentGateway == nil || agentGateway.ResearchMemory == nil {
			writeEnvelope(w, "result", &wisdev.ResearchMemoryBackfillResult{UserID: strings.TrimSpace(req.UserID), ProjectID: strings.TrimSpace(req.ProjectID)})
			return
		}
		result, err := agentGateway.ResearchMemory.BackfillHistoricalArtifacts(r.Context(), req.UserID, req.ProjectID)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to backfill research memory", map[string]any{"error": err.Error()})
			return
		}
		writeEnvelope(w, "result", result)
	}
	mux.HandleFunc("/research/memory/backfill", researchMemoryBackfill)

	telemetryDelete := func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, "result", map[string]any{"ok": true})
	}
	mux.HandleFunc("/telemetry/delete-session", telemetryDelete)

	retentionRun := func(w http.ResponseWriter, r *http.Request) {
		if agentGateway == nil || agentGateway.StateStore == nil {
			WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "runtime state store unavailable", nil)
			return
		}
		var req struct {
			RetentionDays int `json:"retentionDays"`
		}
		if r.Method == http.MethodPost && r.Body != nil && r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
				return
			}
		}
		if req.RetentionDays <= 0 {
			req.RetentionDays = wisdev.EnvInt("WISDEV_STATE_RETENTION_DAYS", 0)
		}
		if req.RetentionDays <= 0 {
			req.RetentionDays = 30
		}
		policyRemoved, jobRemoved := agentGateway.StateStore.EnforceRetention(req.RetentionDays)
		result := map[string]any{
			"ok":            true,
			"retentionDays": req.RetentionDays,
			"policyRemoved": policyRemoved,
			"jobRemoved":    jobRemoved,
		}
		if agentGateway.ResearchMemory != nil {
			if researchMemory, err := agentGateway.ResearchMemory.EnforceRetention(r.Context(), req.RetentionDays); err == nil && researchMemory != nil {
				result["researchMemory"] = researchMemory
			}
		}
		writeEnvelope(w, "result", result)
	}
	mux.HandleFunc("/runtime/retention/run", retentionRun)

	evaluateReplay := func(w http.ResponseWriter, r *http.Request) {
		result := map[string]any{
			"ok":         true,
			"evalReport": map[string]any{},
		}
		if report, err := wisdev.RunEvalHarnessScenarios(nil); err == nil {
			result["evalReport"] = report
		} else {
			result["evalHarnessError"] = err.Error()
		}
		if transcript, ok := loadReplayBenchmarkTranscript(); ok {
			result["latestTranscript"] = transcript
			result["transcriptSummary"] = summarizeReplayTranscript(transcript)
		}
		writeEnvelope(w, "result", result)
	}
	mux.HandleFunc("/evaluate/replay", evaluateReplay)

	evaluateShadow := func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, "result", map[string]any{"ok": true})
	}
	mux.HandleFunc("/evaluate/shadow", evaluateShadow)

	evaluateCanary := func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, "result", map[string]any{"ok": true})
	}
	mux.HandleFunc("/evaluate/canary", evaluateCanary)

	evaluateRubric := func(w http.ResponseWriter, r *http.Request) {
		result := map[string]any{
			"ok": true,
			"rubrics": []map[string]any{
				{"id": "grounding", "description": "Answers remain grounded in verified evidence and promoted citations.", "minScore": 0.8},
				{"id": "abstention", "description": "The agent blocks unsupported claims and requests review when evidence is weak.", "minScore": 0.9},
				{"id": "retrieval_coverage", "description": "Retrieved evidence covers the major concepts in the query and supporting passages.", "minScore": 0.7},
			},
		}
		if report, ok := loadRubricBenchmarkReport(); ok {
			result["latestReport"] = report
		}
		writeEnvelope(w, "result", result)
	}
	mux.HandleFunc("/evaluate/rubric", evaluateRubric)

	evaluateReplayReport := func(w http.ResponseWriter, r *http.Request) {
		result := map[string]any{"ok": true}
		if report, err := wisdev.RunEvalHarnessScenarios(nil); err == nil {
			result["evalReport"] = report
		} else {
			result["evalHarnessError"] = err.Error()
		}
		if report, ok := loadRubricBenchmarkReport(); ok {
			result["latestReport"] = report
		}
		if transcript, ok := loadReplayBenchmarkTranscript(); ok {
			result["latestTranscript"] = transcript
			result["transcriptSummary"] = summarizeReplayTranscript(transcript)
		}
		writeEnvelope(w, "result", result)
	}
	mux.HandleFunc("/evaluate/replay/report", evaluateReplayReport)

	blindVerifierContract := func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, "contract", map[string]any{
			"mode":              "lineage_only",
			"independent":       true,
			"usesWriterContent": false,
			"verificationSignals": []string{
				"claim_packet_lineage",
				"citation_lineage",
				"claim_packet_verification_status",
				"contradiction_links",
			},
			"statuses": []string{"verified", "needs_review", "rejected"},
			"escalationRules": []string{
				"missing claim packet lineage",
				"missing grounded citations",
				"non-verified claim packets",
				"unresolved contradiction links",
			},
		})
	}
	mux.HandleFunc("/verifier/blind-contract", blindVerifierContract)

	agentRegistrationContract := func(w http.ResponseWriter, r *http.Request) {
		card := map[string]any{}
		runtimeMeta := map[string]any{}
		if agentGateway != nil && agentGateway.ADKRuntime != nil {
			card = cloneAnyMap(agentGateway.ADKRuntime.BuildA2ACard())
			runtimeMeta = cloneAnyMap(agentGateway.RuntimeMetadata())
		}
		externalConsumerFit := "not_configured"
		switch {
		case runtimeMeta["ready"] == true:
			externalConsumerFit = "ready_for_smoke_validation"
		case strings.TrimSpace(wisdev.AsOptionalString(runtimeMeta["initError"])) != "":
			externalConsumerFit = "blocked_by_runtime_init"
		case runtimeMeta["configured"] == true:
			externalConsumerFit = "initializing"
		}
		writeEnvelope(w, "contract", map[string]any{
			"protocol": firstNonEmptyString(wisdev.AsOptionalString(card["protocol"]), "a2a-http-json"),
			"discoveryEndpoints": []string{
				"/.well-known/agent-card.json",
				"/agent/card",
				"/agent/tools",
				"/agent/sessions",
			},
			"auth": map[string]any{
				"mode":             "bearer_or_internal_header",
				"supportsBearer":   true,
				"supportsLocalDev": true,
				"requiredHeaders":  []string{"Authorization", "X-User-Id"},
			},
			"registration": map[string]any{
				"agentName":           firstNonEmptyString(wisdev.AsOptionalString(card["name"]), "WisDev"),
				"version":             firstNonEmptyString(wisdev.AsOptionalString(card["version"]), "dev"),
				"preferredTransport":  "http-json",
				"externalConsumerFit": externalConsumerFit,
			},
			"smokeChecks": []string{
				"fetch well-known agent card",
				"fetch agent card",
				"list tool catalog",
				"create session",
				"verify blind contract endpoint",
			},
		})
	}
	mux.HandleFunc("/agent/registration-contract", agentRegistrationContract)

	questReview := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
			return
		}
		var req struct {
			QuestID string `json:"questId"`
			UserID  string `json:"userId"`
			Limit   int    `json:"limit"`
		}
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
				return
			}
		}
		if strings.TrimSpace(req.QuestID) == "" {
			req.QuestID = strings.TrimSpace(r.URL.Query().Get("questId"))
		}
		if strings.TrimSpace(req.UserID) == "" {
			req.UserID = strings.TrimSpace(r.URL.Query().Get("userId"))
		}
		if req.Limit <= 0 {
			req.Limit = 10
		}
		runtime := resolveQuestRuntime(agentGateway)
		if runtime == nil {
			WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "quest runtime unavailable", nil)
			return
		}
		quest, err := runtime.LoadQuest(r.Context(), strings.TrimSpace(req.QuestID))
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to load quest", map[string]any{"error": err.Error(), "questId": req.QuestID})
			return
		}
		if quest == nil {
			WriteError(w, http.StatusNotFound, ErrNotFound, "quest not found", map[string]any{"questId": req.QuestID})
			return
		}
		if !requireOwnerAccess(w, r, quest.UserID) {
			return
		}
		promotionGate := cloneAnyMap(quest.CitationVerdict.PromotionGate)
		if len(promotionGate) == 0 {
			promotionGate = map[string]any{
				"promoted":       quest.CitationVerdict.Promoted,
				"blockingIssues": append([]string(nil), quest.CitationVerdict.BlockingIssues...),
			}
		}
		agentRoles := make([]string, 0, len(quest.AgentAssignments))
		for _, assignment := range quest.AgentAssignments {
			agentRoles = append(agentRoles, assignment.Role)
		}
		eventLimit := req.Limit
		if eventLimit > len(quest.Events) {
			eventLimit = len(quest.Events)
		}
		recentEvents := make([]wisdev.QuestEvent, 0, eventLimit)
		if eventLimit > 0 {
			recentEvents = append(recentEvents, quest.Events[len(quest.Events)-eventLimit:]...)
		}
		writeEnvelope(w, "review", map[string]any{
			"quest": map[string]any{
				"questId":        quest.QuestID,
				"sessionId":      quest.SessionID,
				"query":          quest.Query,
				"status":         quest.Status,
				"currentStage":   quest.CurrentStage,
				"updatedAt":      quest.UpdatedAt,
				"acceptedClaims": len(quest.AcceptedClaims),
				"retrievedCount": quest.RetrievedCount,
				"blockingIssues": append([]string(nil), quest.BlockingIssues...),
				"reviewerNotes":  append([]string(nil), quest.ReviewerNotes...),
				"heavyModel":     quest.HeavyModelRequired,
				"decisionModel":  quest.DecisionModelTier,
				"qualityMode":    quest.QualityMode,
				"agentRoles":     agentRoles,
				"citationVerdict": map[string]any{
					"status":              quest.CitationVerdict.Status,
					"promoted":            quest.CitationVerdict.Promoted,
					"verifiedCount":       quest.CitationVerdict.VerifiedCount,
					"ambiguousCount":      quest.CitationVerdict.AmbiguousCount,
					"rejectedCount":       quest.CitationVerdict.RejectedCount,
					"requiresHumanReview": quest.CitationVerdict.RequiresHumanReview,
					"blockingIssues":      append([]string(nil), quest.CitationVerdict.BlockingIssues...),
				},
			},
			"promotionGate":    promotionGate,
			"rejectedBranches": quest.RejectedBranches,
			"artifactMemory":   quest.Memory.ArtifactMemory,
			"recentEvents":     recentEvents,
			"artifacts":        cloneAnyMap(quest.Artifacts),
		})
	}
	mux.HandleFunc("/quest/review", questReview)

	policyGatesGet := func(w http.ResponseWriter, r *http.Request) {
		if !requireInternalPolicyAccess(w, r) {
			return
		}
		writeEnvelope(w, "gates", []any{})
	}
	mux.HandleFunc("/policy/gates/get", policyGatesGet)

	policyCanaryConfigGet := func(w http.ResponseWriter, r *http.Request) {
		if !requireInternalPolicyAccess(w, r) {
			return
		}
		writeEnvelope(w, "config", map[string]any{})
	}
	mux.HandleFunc("/policy/canary-config/get", policyCanaryConfigGet)

}

func loadRubricBenchmarkReport() (map[string]any, bool) {
	candidates := []string{
		filepath.Join("tests", "benchmarks", "wisdev_benchmark_report.json"),
		filepath.Join("..", "..", "..", "tests", "benchmarks", "wisdev_benchmark_report.json"),
	}
	for _, candidate := range candidates {
		payload, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		var report map[string]any
		if err := json.Unmarshal(payload, &report); err != nil {
			return nil, false
		}
		return report, true
	}
	return nil, false
}

func loadReplayBenchmarkTranscript() (map[string]any, bool) {
	candidates := []string{
		filepath.Join("tests", "benchmarks", "wisdev_benchmark_transcript.json"),
		filepath.Join("..", "..", "..", "tests", "benchmarks", "wisdev_benchmark_transcript.json"),
	}
	for _, candidate := range candidates {
		payload, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		var transcript map[string]any
		if err := json.Unmarshal(payload, &transcript); err != nil {
			return nil, false
		}
		return transcript, true
	}
	return nil, false
}

func summarizeReplayTranscript(transcript map[string]any) map[string]any {
	summary := map[string]any{}
	for _, key := range []string{"baseline", "candidate"} {
		items := sliceAnyMap(transcript[key])
		if len(items) == 0 {
			continue
		}
		sampleIDs := make([]any, 0, wisdev.MinInt(len(items), 3))
		for _, item := range items[:wisdev.MinInt(len(items), 3)] {
			sampleIDs = append(sampleIDs, firstNonEmptyString(wisdev.AsOptionalString(item["sampleId"]), wisdev.AsOptionalString(item["id"])))
		}
		summary[key] = map[string]any{
			"count":     len(items),
			"sampleIds": sampleIDs,
		}
	}
	return summary
}
