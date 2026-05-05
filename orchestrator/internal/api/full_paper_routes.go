package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func (s *wisdevServer) registerFullPaperRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	mux.HandleFunc("/full-paper/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			UserID            string         `json:"userId"`
			Query             string         `json:"query"`
			PlanID            string         `json:"planId"`
			SessionID         string         `json:"sessionId"`
			Options           map[string]any `json:"options"`
			OrchestrationPlan map[string]any `json:"orchestrationPlan"`
			Metadata          map[string]any `json:"metadata"`
			TraceID           string         `json:"traceId,omitempty"`
			LegacyTraceID     string         `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		query := strings.TrimSpace(req.Query)
		if query == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", nil)
			return
		}
		userID, err := resolveAuthorizedUserID(r, strings.TrimSpace(req.UserID))
		if err != nil {
			WriteError(w, http.StatusForbidden, ErrUnauthorized, err.Error(), nil)
			return
		}
		sessionID := strings.TrimSpace(req.SessionID)
		if _, ok := requireSessionBindingAccess(w, r, agentGateway, sessionID, userID); !ok {
			return
		}
		traceID := resolveWisdevRouteOptionalTraceID(r, req.TraceID, req.LegacyTraceID)

		jobID := newWisDevJobID("job")
		papers := extractFullPaperStartPapers(req.Options, req.OrchestrationPlan, req.Metadata)
		pipeline := wisdev.NewManuscriptPipeline(wisdev.ResolvePythonBase())
		result, err := pipeline.Run(context.Background(), jobID, query, papers)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to assemble manuscript workspace", map[string]any{"error": err.Error()})
			return
		}

		workspace := buildFullPaperWorkspace(jobID, sessionID, query, result, papers)
		artifacts := sliceAnyMap(workspace["artifacts"])
		pendingCheckpoint := map[string]any{
			"stageId":   "peer_reviewer",
			"stageName": "peer_reviewer",
			"label":     "Review Manuscript And Visuals",
			"surface":   "manuscript",
			"artifactIds": []string{
				wisdev.AsOptionalString(mapAny(workspace["latestManuscriptArtifact"])["artifactId"]),
				wisdev.AsOptionalString(mapAny(workspace["latestVisualArtifact"])["artifactId"]),
				firstArtifactIDByType(artifacts, "critique_report"),
			},
			"actions": []string{"approve", "request_revision", "reject", "skip"},
		}
		workspace["pendingReviewTarget"] = pendingCheckpoint

		now := time.Now().UnixMilli()
		job := map[string]any{
			"jobId":             jobID,
			"userId":            userID,
			"query":             query,
			"sessionId":         sessionID,
			"status":            "awaiting_approval",
			"progress":          0.85,
			"currentStage":      "peer_reviewer",
			"currentStageId":    "peer_reviewer",
			"pendingCheckpoint": pendingCheckpoint,
			"stages":            result.StageStates,
			"artifactIds":       artifactIDs(artifacts),
			"workspace":         workspace,
			"artifacts":         artifacts,
			"evidenceDossier":   workspace["evidenceDossier"],
			"createdAt":         now,
			"updatedAt":         now,
		}
		if agentGateway != nil {
			if err := saveFullPaperJobState(agentGateway, job); err != nil {
				WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to start job", map[string]any{"error": err.Error()})
				return
			}
		}
		w.Header().Set("X-Trace-Id", traceID)
		writeEnvelopeWithTraceID(w, traceID, "job", job)
	})

	mux.HandleFunc("/full-paper/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			JobID         string `json:"jobId"`
			TraceID       string `json:"traceId,omitempty"`
			LegacyTraceID string `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		jobID := strings.TrimSpace(req.JobID)
		traceID := resolveWisdevRouteOptionalTraceID(r, req.TraceID, req.LegacyTraceID)
		if jobID == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "jobId is required", nil)
			return
		}
		job, ok := loadOwnedFullPaperJobState(w, r, agentGateway, jobID)
		if !ok {
			return
		}
		w.Header().Set("X-Trace-Id", traceID)
		writeEnvelopeWithTraceID(w, traceID, "job", job)
	})

	mux.HandleFunc("/full-paper/artifacts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			JobID         string `json:"jobId"`
			TraceID       string `json:"traceId,omitempty"`
			LegacyTraceID string `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		jobID := strings.TrimSpace(req.JobID)
		traceID := resolveWisdevRouteOptionalTraceID(r, req.TraceID, req.LegacyTraceID)
		if jobID == "" || jobID == "invalid" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "invalid job id", nil)
			return
		}
		job, ok := loadOwnedFullPaperJobState(w, r, agentGateway, jobID)
		if !ok {
			return
		}
		w.Header().Set("X-Trace-Id", traceID)
		writeEnvelopeWithTraceID(w, traceID, "artifacts", job["artifacts"])
	})

	mux.HandleFunc("/full-paper/workspace", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			JobID         string `json:"jobId"`
			TraceID       string `json:"traceId,omitempty"`
			LegacyTraceID string `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		jobID := strings.TrimSpace(req.JobID)
		traceID := resolveWisdevRouteOptionalTraceID(r, req.TraceID, req.LegacyTraceID)
		if jobID == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "jobId is required", nil)
			return
		}
		job, ok := loadOwnedFullPaperJobState(w, r, agentGateway, jobID)
		if !ok {
			return
		}
		w.Header().Set("X-Trace-Id", traceID)
		writeEnvelopeWithTraceID(w, traceID, "workspace", job["workspace"])
	})

	mux.HandleFunc("/full-paper/checkpoint", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			JobID             string `json:"jobId"`
			StageID           string `json:"stageId"`
			Action            string `json:"action"`
			ExpectedUpdatedAt int64  `json:"expectedUpdatedAt"`
			Feedback          any    `json:"feedback"`
			TraceID           string `json:"traceId,omitempty"`
			LegacyTraceID     string `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		jobID := strings.TrimSpace(req.JobID)
		action := strings.TrimSpace(req.Action)
		stageID := strings.TrimSpace(req.StageID)
		traceID := resolveWisdevRouteOptionalTraceID(r, req.TraceID, req.LegacyTraceID)
		if jobID == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "jobId is required", nil)
			return
		}
		if action == "" || action == "invalid" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "invalid action", nil)
			return
		}
		job, ok := loadOwnedFullPaperJobState(w, r, agentGateway, jobID)
		if !ok {
			return
		}
		if !assertExpectedUpdatedAt(w, req.ExpectedUpdatedAt, job) {
			return
		}
		if err := isAllowedFullPaperCheckpointAction(job, stageID, action); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), nil)
			return
		}
		applyLegacyFullPaperCheckpointAction(job, stageID, action, req.Feedback)
		if err := saveFullPaperJobState(agentGateway, job); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist checkpoint action", map[string]any{"error": err.Error()})
			return
		}
		w.Header().Set("X-Trace-Id", traceID)
		writeEnvelopeWithTraceID(w, traceID, "job", job)
	})

	mux.HandleFunc("/full-paper/control", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			JobID             string `json:"jobId"`
			Action            string `json:"action"`
			StageID           string `json:"stageId"`
			ExpectedUpdatedAt int64  `json:"expectedUpdatedAt"`
			TraceID           string `json:"traceId,omitempty"`
			LegacyTraceID     string `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		jobID := strings.TrimSpace(req.JobID)
		action := strings.TrimSpace(req.Action)
		traceID := resolveWisdevRouteOptionalTraceID(r, req.TraceID, req.LegacyTraceID)
		if jobID == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "jobId is required", nil)
			return
		}
		if action == "" || action == "invalid" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "invalid action", nil)
			return
		}
		job, ok := loadOwnedFullPaperJobState(w, r, agentGateway, jobID)
		if !ok {
			return
		}
		if !assertExpectedUpdatedAt(w, req.ExpectedUpdatedAt, job) {
			return
		}
		if err := isAllowedFullPaperControlAction(job, action, strings.TrimSpace(req.StageID)); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), nil)
			return
		}
		applyLegacyFullPaperControlAction(job, action)
		if err := saveFullPaperJobState(agentGateway, job); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist control action", map[string]any{"error": err.Error()})
			return
		}
		w.Header().Set("X-Trace-Id", traceID)
		writeEnvelopeWithTraceID(w, traceID, "job", job)
	})

	mux.HandleFunc("/full-paper/rewrite-section", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			JobID             string `json:"jobId"`
			SectionID         string `json:"sectionId"`
			Instructions      string `json:"instructions"`
			ExpectedUpdatedAt int64  `json:"expectedUpdatedAt"`
			TraceID           string `json:"traceId,omitempty"`
			LegacyTraceID     string `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		traceID := resolveWisdevRouteOptionalTraceID(r, req.TraceID, req.LegacyTraceID)
		job, ok := loadOwnedFullPaperJobState(w, r, agentGateway, strings.TrimSpace(req.JobID))
		if !ok {
			return
		}
		if !assertExpectedUpdatedAt(w, req.ExpectedUpdatedAt, job) {
			return
		}
		result, err := rewriteFullPaperSection(job, strings.TrimSpace(req.SectionID), strings.TrimSpace(req.Instructions))
		if err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), nil)
			return
		}
		if err := saveFullPaperJobState(agentGateway, job); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist rewritten section", map[string]any{"error": err.Error()})
			return
		}
		w.Header().Set("X-Trace-Id", traceID)
		writeEnvelopeWithTraceID(w, traceID, "result", result)
	})

	mux.HandleFunc("/full-paper/regenerate-visual", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			JobID             string `json:"jobId"`
			VisualID          string `json:"visualId"`
			Instructions      string `json:"instructions"`
			ExpectedUpdatedAt int64  `json:"expectedUpdatedAt"`
			TraceID           string `json:"traceId,omitempty"`
			LegacyTraceID     string `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		traceID := resolveWisdevRouteOptionalTraceID(r, req.TraceID, req.LegacyTraceID)
		job, ok := loadOwnedFullPaperJobState(w, r, agentGateway, strings.TrimSpace(req.JobID))
		if !ok {
			return
		}
		if !assertExpectedUpdatedAt(w, req.ExpectedUpdatedAt, job) {
			return
		}
		result, err := regenerateFullPaperVisual(job, strings.TrimSpace(req.VisualID), strings.TrimSpace(req.Instructions))
		if err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), nil)
			return
		}
		if err := saveFullPaperJobState(agentGateway, job); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist regenerated visual", map[string]any{"error": err.Error()})
			return
		}
		w.Header().Set("X-Trace-Id", traceID)
		writeEnvelopeWithTraceID(w, traceID, "result", result)
	})

	mux.HandleFunc("/full-paper/sandbox-action", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			JobID         string `json:"jobId"`
			Tool          string `json:"tool"`
			TraceID       string `json:"traceId,omitempty"`
			LegacyTraceID string `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		traceID := resolveWisdevRouteOptionalTraceID(r, req.TraceID, req.LegacyTraceID)
		if strings.TrimSpace(req.Tool) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "tool is required", nil)
			return
		}
		if strings.TrimSpace(req.JobID) != "" {
			job, ok := loadOwnedFullPaperJobState(w, r, agentGateway, strings.TrimSpace(req.JobID))
			if !ok {
				return
			}
			appendWorkspaceAudit(job, "sandbox_action", "user", fmt.Sprintf("Sandbox tool requested: %s", strings.TrimSpace(req.Tool)))
			if err := saveFullPaperJobState(agentGateway, job); err != nil {
				WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist sandbox action", map[string]any{"error": err.Error()})
				return
			}
		}
		w.Header().Set("X-Trace-Id", traceID)
		writeEnvelopeWithTraceID(w, traceID, "result", map[string]any{"ok": true})
	})
}

func buildFullPaperWorkspace(jobID string, sessionID string, query string, result wisdev.ManuscriptPipelineResult, papers []search.Paper) map[string]any {
	now := time.Now().UnixMilli()
	sectionDraftMaps := toAnySliceMap(result.SectionDrafts)
	visualArtifactMaps := toAnySliceMap(result.VisualArtifacts)
	rawMaterialMap := toAnyMap(result.RawMaterials)
	blueprintMap := toAnyMap(result.Blueprint)
	dossierMap := buildWorkspaceEvidenceDossier(result)

	draftingSections := map[string]any{}
	sectionArtifactIDs := make([]string, 0, len(sectionDraftMaps))
	for _, section := range sectionDraftMaps {
		sectionID := wisdev.AsOptionalString(section["sectionId"])
		if sectionID != "" {
			draftingSections[sectionID] = section
		}
		sectionArtifactIDs = append(sectionArtifactIDs, wisdev.AsOptionalString(section["artifactId"]))
	}

	dossierArtifact := buildResearchArtifact(
		"dossier",
		"Evidence Dossier",
		"Packet-level evidence findings, contradictions, and source clusters.",
		map[string]any{
			"verifiedFindings":      dossierMap["verifiedFindings"],
			"tentativeFindings":     dossierMap["tentativeFindings"],
			"contradictoryFindings": dossierMap["contradictoryFindings"],
			"unsupportedFindings":   dossierMap["unsupportedFindings"],
			"conclusions":           dossierMap["conclusions"],
			"rawMaterialSet":        rawMaterialMap,
			"coverageMetrics":       rawMaterialMap["coverageMetrics"],
		},
		map[string]any{
			"reviewTargetSurface": "evidence",
		},
		nil,
		nil,
	)

	manuscriptArtifact := buildResearchArtifact(
		"manuscript_snapshot",
		"Grounded Manuscript Draft",
		"Blueprint-backed manuscript snapshot with section-level claim lineage.",
		map[string]any{
			"blueprint":             blueprintMap,
			"sections":              buildManuscriptSectionViews(sectionDraftMaps),
			"sectionDraftArtifacts": sectionDraftMaps,
			"claimPacketIds":        result.ClaimPacketIDs(),
		},
		map[string]any{
			"reviewTargetSurface": "manuscript",
			"sectionArtifactIds":  sectionArtifactIDs,
		},
		sectionArtifactIDs,
		nil,
	)
	manuscriptArtifact["sectionOrder"] = result.Blueprint.SectionOrder

	visualBundleArtifact := buildResearchArtifact(
		"visual_bundle",
		"Grounded Visuals",
		"Visual artifacts grounded to tables, figures, and numeric evidence.",
		map[string]any{
			"visualArtifacts": visualArtifactMaps,
		},
		map[string]any{
			"reviewTargetSurface": "visuals",
		},
		nil,
		nil,
	)

	critiqueArtifact := buildResearchArtifact(
		"critique_report",
		"Peer Review Critique",
		"Blind verification and peer review findings for the current manuscript snapshot.",
		result.CritiqueReport,
		map[string]any{
			"reviewTargetSurface": "critique",
		},
		[]string{wisdev.AsOptionalString(manuscriptArtifact["artifactId"]), wisdev.AsOptionalString(visualBundleArtifact["artifactId"])},
		nil,
	)

	revisionArtifact := buildResearchArtifact(
		"revision_queue",
		"Revision Queue",
		"Section and visual tasks generated from the critique loop.",
		map[string]any{
			"tasks": result.RevisionTasks,
		},
		map[string]any{
			"reviewTargetSurface": "queue",
		},
		[]string{wisdev.AsOptionalString(critiqueArtifact["artifactId"])},
		nil,
	)

	sourceBundleArtifact := buildResearchArtifact(
		"source_bundle",
		"Source Bundle",
		"Canonical sources attached to the raw material graph.",
		map[string]any{
			"sources": buildSourceBundleSources(result, papers),
		},
		nil,
		nil,
		nil,
	)

	trajectoryArtifact := buildResearchArtifact(
		"retrieval_trajectory",
		"Pipeline Trajectory",
		"Canonical stage progression from scouting to peer review.",
		map[string]any{
			"tasks": buildTrajectoryTasks(result.StageStates),
		},
		nil,
		nil,
		nil,
	)

	artifacts := []map[string]any{
		dossierArtifact,
		manuscriptArtifact,
		visualBundleArtifact,
		critiqueArtifact,
		revisionArtifact,
		sourceBundleArtifact,
		trajectoryArtifact,
	}

	return map[string]any{
		"workspaceId":              fmt.Sprintf("workspace_%s", jobID),
		"jobId":                    jobID,
		"sessionId":                sessionID,
		"status":                   "awaiting_approval",
		"artifacts":                artifacts,
		"rawMaterialSet":           rawMaterialMap,
		"blueprint":                blueprintMap,
		"sectionDraftArtifacts":    sectionDraftMaps,
		"visualArtifacts":          visualArtifactMaps,
		"evidenceDossier":          dossierMap,
		"critiqueReports":          []any{result.CritiqueReport},
		"revisionTasks":            result.RevisionTasks,
		"critiqueArtifacts":        []any{critiqueArtifact},
		"revisionArtifacts":        []any{revisionArtifact},
		"sandboxArtifacts":         []any{},
		"latestDossierArtifact":    dossierArtifact,
		"latestManuscriptArtifact": manuscriptArtifact,
		"latestVisualArtifact":     visualBundleArtifact,
		"latestRetrievalArtifact":  trajectoryArtifact,
		"manuscriptBundle":         manuscriptArtifact,
		"drafting": map[string]any{
			"sections":           draftingSections,
			"sectionOrder":       result.Blueprint.SectionOrder,
			"sectionArtifactIds": sectionArtifactIDs,
			"claimPacketIds":     result.ClaimPacketIDs(),
		},
		"auditLog": []any{
			map[string]any{
				"entryId":   fmt.Sprintf("audit_%d_start", now),
				"action":    "start",
				"actor":     "system",
				"detail":    fmt.Sprintf("Initialized full-paper workspace for query: %s", query),
				"timestamp": now,
				"createdAt": now,
				"stageName": "peer_reviewer",
				"result":    "applied",
			},
		},
		"controlHistory":    []any{},
		"checkpointHistory": []any{},
		"integration": map[string]any{
			"computeGo": map[string]any{
				"configuredBaseUrl": "go_orchestrator",
				"authMode":          "internal",
				"retrievalStatus":   "ready",
				"endpointStatus":    "healthy",
				"lastStageName":     "peer_reviewer",
				"lastCheckedAt":     now,
				"lastSuccessfulAt":  now,
			},
		},
		"createdAt": now,
		"updatedAt": now,
	}
}

func buildWorkspaceEvidenceDossier(result wisdev.ManuscriptPipelineResult) map[string]any {
	verified := make([]map[string]any, 0, len(result.RawMaterials.ClaimPackets))
	tentative := make([]map[string]any, 0, len(result.RawMaterials.ClaimPackets))
	contradictions := make([]map[string]any, 0)
	conclusions := make([]string, 0)
	sourceTitleByID := make(map[string]string, len(result.RawMaterials.CanonicalSources))
	for _, source := range result.RawMaterials.CanonicalSources {
		sourceTitleByID[source.CanonicalID] = source.Title
	}
	for _, packet := range toAnySliceMap(result.RawMaterials.ClaimPackets) {
		finding := map[string]any{
			"id":           packet["packetId"],
			"claim":        packet["claimText"],
			"status":       packetStatus(packet),
			"sourceIds":    sourceIDsFromPacket(packet),
			"sourceTitles": titlesFromPacket(packet, sourceTitleByID),
			"supportScore": packet["confidence"],
		}
		switch packetStatus(packet) {
		case "verified":
			verified = append(verified, finding)
			conclusions = append(conclusions, wisdev.AsOptionalString(packet["claimText"]))
		case "contradictory":
			contradictions = append(contradictions, finding)
		default:
			tentative = append(tentative, finding)
		}
	}
	return map[string]any{
		"dossierId":                       result.Dossier.DossierID,
		"bundleId":                        result.RawMaterials.RawMaterialSetID,
		"verifiedFindings":                verified,
		"tentativeFindings":               tentative,
		"contradictoryFindings":           contradictions,
		"unsupportedFindings":             []any{},
		"unresolvedGaps":                  result.Dossier.Gaps,
		"recommendedNextRetrievalActions": []string{"Review contradictions", "Attach more source papers for uncovered sections"},
		"conclusions":                     uniqueStrings(conclusions),
		"coverageMetrics":                 result.Dossier.CoverageMetrics,
	}
}

func buildSourceBundleSources(result wisdev.ManuscriptPipelineResult, papers []search.Paper) []map[string]any {
	if len(papers) > 0 {
		out := make([]map[string]any, 0, len(papers))
		for _, paper := range papers {
			out = append(out, map[string]any{
				"title":         paper.Title,
				"summary":       firstNonEmptyString(firstSentence(paper.Abstract), paper.Title),
				"year":          paper.Year,
				"citationCount": paper.CitationCount,
				"link":          paper.Link,
			})
		}
		return out
	}
	out := make([]map[string]any, 0, len(result.RawMaterials.CanonicalSources))
	for _, source := range result.RawMaterials.CanonicalSources {
		out = append(out, map[string]any{
			"title":         source.Title,
			"summary":       firstNonEmptyString(firstSentence(source.Abstract), source.Title),
			"year":          source.Year,
			"citationCount": 0,
			"link":          source.LandingURL,
		})
	}
	return out
}

func buildTrajectoryTasks(stages []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(stages))
	for _, stage := range stages {
		out = append(out, map[string]any{
			"subquestion": firstNonEmptyString(wisdev.AsOptionalString(stage["label"]), wisdev.AsOptionalString(stage["id"])),
			"rationale":   fmt.Sprintf("Stage %s completed the artifact-driven manuscript pipeline slice.", wisdev.AsOptionalString(stage["id"])),
			"status":      wisdev.AsOptionalString(stage["status"]),
			"timestamp":   time.Now().UnixMilli(),
			"results": []map[string]any{
				{"title": fmt.Sprintf("Completion: %v", stage["completion"]), "score": 1.0},
			},
		})
	}
	return out
}

func buildManuscriptSectionViews(sectionDrafts []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(sectionDrafts))
	for _, section := range sectionDrafts {
		out = append(out, map[string]any{
			"sectionId":          section["sectionId"],
			"title":              section["title"],
			"text":               section["content"],
			"content":            section["content"],
			"claimPacketIds":     section["claimPacketIds"],
			"sourceTitles":       section["sourceTitles"],
			"sourceCanonicalIds": section["sourceCanonicalIds"],
			"unresolvedIssues":   section["unresolvedIssues"],
			"reviewStatus":       section["reviewStatus"],
			"lastReviewDecision": section["lastReviewDecision"],
			"plannedVisualIds":   section["plannedVisualIds"],
			"version":            section["version"],
		})
	}
	return out
}

func buildResearchArtifact(artifactType string, title string, summary string, content any, metadata map[string]any, sourceArtifactIDs []string, exportArtifactIDs []string) map[string]any {
	now := time.Now().UnixMilli()
	artifactID := fmt.Sprintf("%s_%d", artifactType, now)
	return map[string]any{
		"artifactId":        artifactID,
		"type":              artifactType,
		"title":             title,
		"summary":           summary,
		"content":           content,
		"createdAt":         now,
		"updatedAt":         now,
		"status":            "ready",
		"sourceArtifactIds": uniqueStrings(sourceArtifactIDs),
		"exportArtifactIds": uniqueStrings(exportArtifactIDs),
		"metadata":          cloneAnyMap(metadata),
	}
}

func applyLegacyFullPaperCheckpointAction(job map[string]any, stageID string, action string, feedback any) {
	now := time.Now().UnixMilli()
	action = strings.ToLower(strings.TrimSpace(action))
	workspace := mapAny(job["workspace"])
	checkpointHistory := sliceAnyMap(workspace["checkpointHistory"])
	checkpointHistory = append(checkpointHistory, map[string]any{
		"kind":      "checkpoint",
		"action":    action,
		"createdAt": now,
		"stageName": stageID,
		"result":    "applied",
		"feedback":  feedback,
	})
	workspace["checkpointHistory"] = checkpointHistory
	job["pendingCheckpoint"] = nil
	workspace["pendingReviewTarget"] = nil
	appendWorkspaceAudit(job, "checkpoint_"+action, "user", fmt.Sprintf("Checkpoint %s applied for stage %s.", action, stageID))

	switch action {
	case "approve":
		job["status"] = "completed"
		job["currentStage"] = "completed"
		job["currentStageId"] = "completed"
		updateFullPaperStageStatus(job, "peer_reviewer", "completed", 100)
		updateFullPaperStageStatus(job, "revision_editor", "completed", 100)
	case "request_revision":
		job["status"] = "awaiting_review"
		job["currentStage"] = "revision_editor"
		job["currentStageId"] = "revision_editor"
		updateFullPaperStageStatus(job, "peer_reviewer", "completed", 100)
		updateFullPaperStageStatus(job, "revision_editor", "pending", 0)
	case "reject":
		job["status"] = "paused"
		job["currentStage"] = "revision_editor"
		job["currentStageId"] = "revision_editor"
		updateFullPaperStageStatus(job, "peer_reviewer", "completed", 100)
		updateFullPaperStageStatus(job, "revision_editor", "pending", 0)
	default:
		job["status"] = "running"
	}

	workspace["status"] = job["status"]
	workspace["updatedAt"] = now
	job["workspace"] = workspace
	job["updatedAt"] = now
}

func applyLegacyFullPaperControlAction(job map[string]any, action string) {
	now := time.Now().UnixMilli()
	action = strings.ToLower(strings.TrimSpace(action))
	workspace := mapAny(job["workspace"])
	controlHistory := sliceAnyMap(workspace["controlHistory"])
	controlHistory = append(controlHistory, map[string]any{
		"kind":      "control",
		"action":    action,
		"createdAt": now,
		"stageName": wisdev.AsOptionalString(job["currentStageId"]),
		"result":    "applied",
	})
	workspace["controlHistory"] = controlHistory
	appendWorkspaceAudit(job, "control_"+action, "user", fmt.Sprintf("Control action %s applied.", action))

	switch action {
	case "pause":
		job["status"] = "paused"
	case "resume":
		job["status"] = "running"
	case "retry_stage", "restart":
		job["status"] = "awaiting_approval"
		job["currentStage"] = "peer_reviewer"
		job["currentStageId"] = "peer_reviewer"
		if workspace["pendingReviewTarget"] != nil {
			job["pendingCheckpoint"] = workspace["pendingReviewTarget"]
		}
	case "cancel":
		job["status"] = "cancelled"
		job["pendingCheckpoint"] = nil
		workspace["pendingReviewTarget"] = nil
	}
	workspace["status"] = job["status"]
	workspace["updatedAt"] = now
	job["workspace"] = workspace
	job["updatedAt"] = now
}

func rewriteFullPaperSection(job map[string]any, sectionID string, instructions string) (map[string]any, error) {
	if sectionID == "" {
		return nil, fmt.Errorf("sectionId is required")
	}
	workspace := mapAny(job["workspace"])
	sectionDrafts := sliceAnyMap(workspace["sectionDraftArtifacts"])
	if len(sectionDrafts) == 0 {
		return nil, fmt.Errorf("workspace has no section draft artifacts")
	}

	now := time.Now().UnixMilli()
	var rewritten map[string]any
	for index, section := range sectionDrafts {
		if wisdev.AsOptionalString(section["sectionId"]) != sectionID {
			continue
		}
		rewritten = cloneAnyMap(section)
		rewritten["artifactId"] = fmt.Sprintf("%s_v%d", wisdev.AsOptionalString(section["artifactId"]), wisdev.IntValue(section["version"])+1)
		rewritten["version"] = wisdev.IntValue(section["version"]) + 1
		rewritten["content"] = strings.TrimSpace(firstNonEmptyString(wisdev.AsOptionalString(section["content"]), wisdev.AsOptionalString(section["text"]))) +
			"\n\nRevision focus: " + firstNonEmptyString(instructions, "Refresh evidence grounding and align prose with the current critique.")
		rewritten["reviewStatus"] = "ready_for_review"
		rewritten["lastReviewDecision"] = "rewritten"
		rewritten["updatedAt"] = now
		rewritten["unresolvedIssues"] = removeLineageGapIssues(section["unresolvedIssues"])
		sectionDrafts[index] = rewritten
		break
	}
	if rewritten == nil {
		return nil, fmt.Errorf("section %s not found", sectionID)
	}

	workspace["sectionDraftArtifacts"] = sectionDrafts
	drafting := mapAny(workspace["drafting"])
	sections := mapAny(drafting["sections"])
	sections[sectionID] = rewritten
	drafting["sections"] = sections
	drafting["sectionArtifactIds"] = uniqueStrings(append(sliceStrings(drafting["sectionArtifactIds"]), wisdev.AsOptionalString(rewritten["artifactId"])))
	workspace["drafting"] = drafting

	artifacts := sliceAnyMap(workspace["artifacts"])
	latestManuscript := mapAny(workspace["latestManuscriptArtifact"])
	newManuscript := cloneAnyMap(latestManuscript)
	newManuscript["artifactId"] = fmt.Sprintf("%s_v%d", wisdev.AsOptionalString(latestManuscript["artifactId"]), historyVersion(artifacts, "manuscript_snapshot")+1)
	newManuscript["createdAt"] = now
	newManuscript["updatedAt"] = now
	newContent := mapAny(newManuscript["content"])
	newContent["sections"] = buildManuscriptSectionViews(sectionDrafts)
	newContent["sectionDraftArtifacts"] = sectionDrafts
	newManuscript["content"] = newContent
	newManuscript["lastReviewAction"] = "request_revision"
	newManuscript["summary"] = "Rewritten manuscript snapshot after targeted section revision."
	artifacts = append(artifacts, newManuscript)

	updateRevisionTasksForTarget(workspace, "section", sectionID)
	pendingCheckpoint := map[string]any{
		"stageId":   "peer_reviewer",
		"stageName": "peer_reviewer",
		"label":     "Review Revised Manuscript",
		"surface":   "manuscript",
		"artifactIds": []string{
			wisdev.AsOptionalString(newManuscript["artifactId"]),
		},
		"actions": []string{"approve", "request_revision", "reject"},
	}

	workspace["artifacts"] = artifacts
	workspace["latestManuscriptArtifact"] = newManuscript
	workspace["pendingReviewTarget"] = pendingCheckpoint
	workspace["status"] = "awaiting_approval"
	workspace["updatedAt"] = now
	job["artifacts"] = artifacts
	job["workspace"] = workspace
	job["status"] = "awaiting_approval"
	job["currentStage"] = "peer_reviewer"
	job["currentStageId"] = "peer_reviewer"
	job["pendingCheckpoint"] = pendingCheckpoint
	job["artifactIds"] = artifactIDs(artifacts)
	job["updatedAt"] = now
	appendWorkspaceAudit(job, "rewrite_section", "user", fmt.Sprintf("Rewrote section %s.", sectionID))

	return map[string]any{
		"job":                job,
		"workspace":          workspace,
		"sectionArtifact":    rewritten,
		"manuscriptArtifact": newManuscript,
	}, nil
}

func regenerateFullPaperVisual(job map[string]any, visualID string, instructions string) (map[string]any, error) {
	if visualID == "" {
		return nil, fmt.Errorf("visualId is required")
	}
	workspace := mapAny(job["workspace"])
	visuals := sliceAnyMap(workspace["visualArtifacts"])
	if len(visuals) == 0 {
		return nil, fmt.Errorf("workspace has no visual artifacts")
	}

	now := time.Now().UnixMilli()
	var regenerated map[string]any
	for index, visual := range visuals {
		if wisdev.AsOptionalString(visual["artifactId"]) != visualID {
			continue
		}
		regenerated = cloneAnyMap(visual)
		regenerated["artifactId"] = fmt.Sprintf("%s_v%d", wisdev.AsOptionalString(visual["artifactId"]), wisdev.IntValue(visual["version"])+1)
		regenerated["version"] = wisdev.IntValue(visual["version"]) + 1
		regenerated["caption"] = strings.TrimSpace(wisdev.AsOptionalString(visual["caption"]) + " " + firstNonEmptyString(instructions, "Regenerated to improve packet grounding and review readiness."))
		regenerated["reviewStatus"] = "ready_for_review"
		regenerated["lastReviewDecision"] = "regenerated"
		regenerated["updatedAt"] = now
		regenerated["unresolvedIssues"] = []any{}
		specType := wisdev.AsOptionalString(regenerated["specType"])
		if specType == "mermaid" {
			regenerated["spec"] = wisdev.AsOptionalString(regenerated["spec"]) + "\n    review[\"Regenerated review-ready visual\"]"
		} else {
			spec := mapAny(regenerated["spec"])
			spec["description"] = firstNonEmptyString(instructions, "Regenerated visual with refreshed review annotations.")
			regenerated["spec"] = spec
		}
		visuals[index] = regenerated
		break
	}
	if regenerated == nil {
		return nil, fmt.Errorf("visual %s not found", visualID)
	}

	workspace["visualArtifacts"] = visuals
	artifacts := sliceAnyMap(workspace["artifacts"])
	latestVisual := mapAny(workspace["latestVisualArtifact"])
	newVisualBundle := cloneAnyMap(latestVisual)
	newVisualBundle["artifactId"] = fmt.Sprintf("%s_v%d", wisdev.AsOptionalString(latestVisual["artifactId"]), historyVersion(artifacts, "visual_bundle")+1)
	newVisualBundle["createdAt"] = now
	newVisualBundle["updatedAt"] = now
	newContent := mapAny(newVisualBundle["content"])
	newContent["visualArtifacts"] = visuals
	newVisualBundle["content"] = newContent
	newVisualBundle["summary"] = "Regenerated visual bundle after targeted visual refresh."
	artifacts = append(artifacts, newVisualBundle)

	updateRevisionTasksForTarget(workspace, "visual", visualID)
	pendingCheckpoint := map[string]any{
		"stageId":   "peer_reviewer",
		"stageName": "peer_reviewer",
		"label":     "Review Regenerated Visual",
		"surface":   "visuals",
		"artifactIds": []string{
			wisdev.AsOptionalString(newVisualBundle["artifactId"]),
		},
		"actions": []string{"approve", "request_revision", "reject"},
	}

	workspace["artifacts"] = artifacts
	workspace["latestVisualArtifact"] = newVisualBundle
	workspace["pendingReviewTarget"] = pendingCheckpoint
	workspace["status"] = "awaiting_approval"
	workspace["updatedAt"] = now
	job["artifacts"] = artifacts
	job["workspace"] = workspace
	job["status"] = "awaiting_approval"
	job["currentStage"] = "peer_reviewer"
	job["currentStageId"] = "peer_reviewer"
	job["pendingCheckpoint"] = pendingCheckpoint
	job["artifactIds"] = artifactIDs(artifacts)
	job["updatedAt"] = now
	appendWorkspaceAudit(job, "regenerate_visual", "user", fmt.Sprintf("Regenerated visual %s.", visualID))

	return map[string]any{
		"job":            job,
		"workspace":      workspace,
		"visualArtifact": regenerated,
		"bundleArtifact": newVisualBundle,
	}, nil
}

func appendWorkspaceAudit(job map[string]any, action string, actor string, detail string) {
	now := time.Now().UnixMilli()
	workspace := mapAny(job["workspace"])
	auditLog := sliceAnyMap(workspace["auditLog"])
	auditLog = append(auditLog, map[string]any{
		"entryId":   fmt.Sprintf("audit_%d_%s", now, action),
		"action":    action,
		"actor":     actor,
		"detail":    detail,
		"timestamp": now,
		"createdAt": now,
		"stageName": wisdev.AsOptionalString(job["currentStageId"]),
		"result":    "applied",
	})
	workspace["auditLog"] = auditLog
	job["workspace"] = workspace
}

func updateFullPaperStageStatus(job map[string]any, stageID string, status string, completion int) {
	stages := sliceAnyMap(job["stages"])
	for index, stage := range stages {
		if wisdev.AsOptionalString(stage["id"]) != stageID {
			continue
		}
		stage["status"] = status
		stage["completion"] = completion
		stages[index] = stage
	}
	job["stages"] = stages
}

func updateRevisionTasksForTarget(workspace map[string]any, targetType string, targetID string) {
	tasks := sliceAnyMap(workspace["revisionTasks"])
	for index, task := range tasks {
		if wisdev.AsOptionalString(task["targetType"]) == targetType && wisdev.AsOptionalString(task["targetId"]) == targetID {
			task["status"] = "completed"
			tasks[index] = task
		}
	}
	workspace["revisionTasks"] = tasks
	revisionArtifacts := sliceAnyMap(workspace["revisionArtifacts"])
	if len(revisionArtifacts) == 0 {
		return
	}
	revisionArtifact := cloneAnyMap(revisionArtifacts[len(revisionArtifacts)-1])
	content := mapAny(revisionArtifact["content"])
	content["tasks"] = tasks
	revisionArtifact["content"] = content
	revisionArtifacts[len(revisionArtifacts)-1] = revisionArtifact
	workspace["revisionArtifacts"] = revisionArtifacts
}

func historyVersion(artifacts []map[string]any, artifactType string) int {
	count := 0
	for _, artifact := range artifacts {
		if wisdev.AsOptionalString(artifact["type"]) == artifactType {
			count++
		}
	}
	return count
}

func removeLineageGapIssues(value any) []string {
	issues := sliceStrings(value)
	out := make([]string, 0, len(issues))
	for _, issue := range issues {
		lower := strings.ToLower(issue)
		if strings.Contains(lower, "no grounded") || strings.Contains(lower, "blind verifier") {
			continue
		}
		out = append(out, issue)
	}
	return uniqueStrings(out)
}

func extractFullPaperStartPapers(options map[string]any, orchestrationPlan map[string]any, metadata map[string]any) []search.Paper {
	for _, candidate := range []any{
		options["papers"],
		options["sources"],
		metadata["papers"],
		orchestrationPlan["papers"],
	} {
		papers := decodeSearchPapers(candidate)
		if len(papers) > 0 {
			return papers
		}
	}
	return nil
}

func decodeSearchPapers(value any) []search.Paper {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var papers []search.Paper
	if err := json.Unmarshal(raw, &papers); err != nil {
		return nil
	}
	out := make([]search.Paper, 0, len(papers))
	for _, paper := range papers {
		if strings.TrimSpace(paper.Title) == "" {
			continue
		}
		out = append(out, paper)
	}
	return out
}

func toAnyMap(value any) map[string]any {
	raw, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func toAnySliceMap(value any) []map[string]any {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func sourceIDsFromPacket(packet map[string]any) []string {
	spans := sliceAnyMap(packet["evidenceSpans"])
	out := make([]string, 0, len(spans))
	for _, span := range spans {
		out = append(out, wisdev.AsOptionalString(span["sourceCanonicalId"]))
	}
	return uniqueStrings(out)
}

func titlesFromPacket(packet map[string]any, titleIndex map[string]string) []string {
	sourceIDs := sourceIDsFromPacket(packet)
	out := make([]string, 0, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		if title := titleIndex[sourceID]; title != "" {
			out = append(out, title)
		}
	}
	return uniqueStrings(out)
}

func packetStatus(packet map[string]any) string {
	if len(sliceStrings(packet["contradictionPacketIds"])) > 0 {
		return "contradictory"
	}
	status := wisdev.AsOptionalString(packet["verifierStatus"])
	switch status {
	case "verified":
		return "verified"
	case "rejected":
		return "unsupported"
	default:
		return "tentative"
	}
}

func artifactIDs(artifacts []map[string]any) []string {
	out := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		out = append(out, wisdev.AsOptionalString(artifact["artifactId"]))
	}
	return uniqueStrings(out)
}

func firstArtifactIDByType(artifacts []map[string]any, artifactType string) string {
	for _, artifact := range artifacts {
		if wisdev.AsOptionalString(artifact["type"]) == artifactType {
			return wisdev.AsOptionalString(artifact["artifactId"])
		}
	}
	return ""
}

func firstSentence(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if idx := strings.IndexAny(value, ".!?"); idx >= 0 {
		return strings.TrimSpace(value[:idx+1])
	}
	return value
}
