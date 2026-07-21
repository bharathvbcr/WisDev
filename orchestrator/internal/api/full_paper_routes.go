package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/evidence"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
)

// fullPaperRunTimeout is the synchronous budget for a /full-paper/start run. The
// default 5 minutes preserves prior behavior; FULL_PAPER_TIMEOUT_MINUTES raises it
// for exhaustive "max mode" manuscripts (long review loops over large citation sets).
// The pipeline checkpoints each completed section, so even a timeout leaves resumable
// progress rather than discarding finished work. Clamped to a sane floor.
func fullPaperRunTimeout() time.Duration {
	minutes := wisdev.EnvInt("FULL_PAPER_TIMEOUT_MINUTES", 5)
	if minutes < 1 {
		minutes = 5
	}
	return time.Duration(minutes) * time.Minute
}

func (s *wisdevServer) registerFullPaperRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	mux.HandleFunc("/full-paper/start", func(w http.ResponseWriter, r *http.Request) {
		logAPIRouteLifecycle(r, "api", "inline", "request_received", "", "result", "accepted")
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
			TraceIDLegacy     string         `json:"trace_id,omitempty"`
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
		traceID := resolveWisdevRouteTraceID(r, req.TraceID, req.TraceIDLegacy)

		jobID := newWisDevJobID("job")
		docGenControls := withSmartDocGenDefaults(extractDocGenControls(req.Options, req.Metadata))
		papers := extractFullPaperStartPapers(req.Options, req.OrchestrationPlan, req.Metadata)
		if len(papers) == 0 {
			papers = hydrateFullPaperStartPapers(r.Context(), agentGateway, query, req.OrchestrationPlan, req.Metadata, docGenControls.minCitations, traceID)
		}
		pipeline := wisdev.NewManuscriptPipeline(wisdev.ResolvePythonBase())
		// Checkpoint each completed section draft to disk so a crashed or timed-out
		// run re-issued with the same jobID resumes instead of re-drafting sections.
		pipeline.Checkpoints = wisdev.NewFileCheckpointStore("")
		// Apply the optional DocGen customization knobs (target length, citation breadth,
		// genre, section flow, review depth, and free-text author instructions) the client
		// passes under `options` / `metadata`. Previously these were decoded but dropped, so
		// the full-paper UI could not steer generation; wire them through the same controls
		// the YOLO/deep-research paths use.
		applyFullPaperDocGenControls(pipeline, docGenControls)
		// Run under the caller's context with a hard budget so a disconnected or timed-out client
		// stops the review/refine loop and all downstream sidecar LLM calls (which honor ctx.Err())
		// instead of orphaning them and burning tokens. Mirrors attachManuscriptDocGen (yolo.go),
		// which already threads the caller ctx. The handler is synchronous, so r.Context() is live
		// for the whole run.
		ctx, cancel := context.WithTimeout(r.Context(), fullPaperRunTimeout())
		defer cancel()
		result, err := pipeline.Run(ctx, jobID, query, papers)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to assemble manuscript workspace", map[string]any{"error": err.Error()})
			return
		}

		job := buildPersistedFullPaperJob(jobID, userID, sessionID, query, result, papers)
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
		logAPIRouteLifecycle(r, "api", "inline", "request_received", "", "result", "accepted")
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			JobID         string `json:"jobId"`
			TraceID       string `json:"traceId,omitempty"`
			TraceIDLegacy string `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		jobID := strings.TrimSpace(req.JobID)
		traceID := resolveWisdevRouteTraceID(r, req.TraceID, req.TraceIDLegacy)
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
		logAPIRouteLifecycle(r, "api", "inline", "request_received", "", "result", "accepted")
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			JobID         string `json:"jobId"`
			TraceID       string `json:"traceId,omitempty"`
			TraceIDLegacy string `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		jobID := strings.TrimSpace(req.JobID)
		traceID := resolveWisdevRouteTraceID(r, req.TraceID, req.TraceIDLegacy)
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
		logAPIRouteLifecycle(r, "api", "inline", "request_received", "", "result", "accepted")
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			JobID         string `json:"jobId"`
			TraceID       string `json:"traceId,omitempty"`
			TraceIDLegacy string `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		jobID := strings.TrimSpace(req.JobID)
		traceID := resolveWisdevRouteTraceID(r, req.TraceID, req.TraceIDLegacy)
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
		logAPIRouteLifecycle(r, "api", "inline", "request_received", "", "result", "accepted")
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
			TraceIDLegacy     string `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		jobID := strings.TrimSpace(req.JobID)
		action := strings.TrimSpace(req.Action)
		stageID := strings.TrimSpace(req.StageID)
		traceID := resolveWisdevRouteTraceID(r, req.TraceID, req.TraceIDLegacy)
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
		applyFullPaperCheckpointAction(job, stageID, action, req.Feedback)
		if err := saveFullPaperJobState(agentGateway, job); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist checkpoint action", map[string]any{"error": err.Error()})
			return
		}
		w.Header().Set("X-Trace-Id", traceID)
		writeEnvelopeWithTraceID(w, traceID, "job", job)
	})

	mux.HandleFunc("/full-paper/control", func(w http.ResponseWriter, r *http.Request) {
		logAPIRouteLifecycle(r, "api", "inline", "request_received", "", "result", "accepted")
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
			TraceIDLegacy     string `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		jobID := strings.TrimSpace(req.JobID)
		action := strings.TrimSpace(req.Action)
		traceID := resolveWisdevRouteTraceID(r, req.TraceID, req.TraceIDLegacy)
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
		applyFullPaperControlAction(job, action)
		if err := saveFullPaperJobState(agentGateway, job); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist control action", map[string]any{"error": err.Error()})
			return
		}
		w.Header().Set("X-Trace-Id", traceID)
		writeEnvelopeWithTraceID(w, traceID, "job", job)
	})

	mux.HandleFunc("/full-paper/rewrite-section", func(w http.ResponseWriter, r *http.Request) {
		logAPIRouteLifecycle(r, "api", "inline", "request_received", "", "result", "accepted")
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
			TraceIDLegacy     string `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		traceID := resolveWisdevRouteTraceID(r, req.TraceID, req.TraceIDLegacy)
		job, ok := loadOwnedFullPaperJobState(w, r, agentGateway, strings.TrimSpace(req.JobID))
		if !ok {
			return
		}
		if !assertExpectedUpdatedAt(w, req.ExpectedUpdatedAt, job) {
			return
		}
		result, err := rewriteFullPaperSection(r.Context(), job, strings.TrimSpace(req.SectionID), strings.TrimSpace(req.Instructions))
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

	mux.HandleFunc("/full-paper/edit-section", func(w http.ResponseWriter, r *http.Request) {
		logAPIRouteLifecycle(r, "api", "inline", "request_received", "", "result", "accepted")
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			JobID             string `json:"jobId"`
			SectionID         string `json:"sectionId"`
			ContentHTML       string `json:"contentHtml"`
			ExpectedUpdatedAt int64  `json:"expectedUpdatedAt"`
			TraceID           string `json:"traceId,omitempty"`
			TraceIDLegacy     string `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		if strings.TrimSpace(req.SectionID) == "" || strings.TrimSpace(req.ContentHTML) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sectionId and contentHtml are required", nil)
			return
		}
		traceID := resolveWisdevRouteTraceID(r, req.TraceID, req.TraceIDLegacy)
		job, ok := loadOwnedFullPaperJobState(w, r, agentGateway, strings.TrimSpace(req.JobID))
		if !ok {
			return
		}
		if fullPaperHasTerminalStatus(wisdev.AsOptionalString(job["status"])) {
			WriteError(w, http.StatusConflict, ErrInvalidParameters, "job is finalized", nil)
			return
		}
		if !assertExpectedUpdatedAt(w, req.ExpectedUpdatedAt, job) {
			return
		}
		userID := wisdev.AsOptionalString(job["userId"])
		result, err := editFullPaperSection(job, strings.TrimSpace(req.SectionID), req.ContentHTML, userID)
		if err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), nil)
			return
		}
		if err := saveFullPaperJobState(agentGateway, job); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist edited section", map[string]any{"error": err.Error()})
			return
		}
		w.Header().Set("X-Trace-Id", traceID)
		writeEnvelopeWithTraceID(w, traceID, "result", result)
	})

	mux.HandleFunc("/full-paper/regenerate-visual", func(w http.ResponseWriter, r *http.Request) {
		logAPIRouteLifecycle(r, "api", "inline", "request_received", "", "result", "accepted")
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
			TraceIDLegacy     string `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		traceID := resolveWisdevRouteTraceID(r, req.TraceID, req.TraceIDLegacy)
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

	mux.HandleFunc("/full-paper/citation-integrity", func(w http.ResponseWriter, r *http.Request) {
		logAPIRouteLifecycle(r, "api", "inline", "request_received", "", "result", "accepted")
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		handleFullPaperCitationIntegrity(w, r, agentGateway)
	})

	mux.HandleFunc("/full-paper/sandbox-action", func(w http.ResponseWriter, r *http.Request) {
		logAPIRouteLifecycle(r, "api", "inline", "request_received", "", "result", "accepted")
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			JobID         string `json:"jobId"`
			Tool          string `json:"tool"`
			TraceID       string `json:"traceId,omitempty"`
			TraceIDLegacy string `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		traceID := resolveWisdevRouteTraceID(r, req.TraceID, req.TraceIDLegacy)
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
			"unresolvedGaps":        dossierMap["unresolvedGaps"],
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
	unsupported := make([]map[string]any, 0)
	conclusionIndex := make(map[string]map[string]any)
	conclusionOrder := make([]string, 0)
	sourceTitleByID := make(map[string]string, len(result.RawMaterials.CanonicalSources))
	for _, source := range result.RawMaterials.CanonicalSources {
		sourceTitleByID[source.CanonicalID] = source.Title
	}
	for _, packet := range toAnySliceMap(result.RawMaterials.ClaimPackets) {
		sourceIDs := sourceIDsFromPacket(packet)
		finding := map[string]any{
			"id":               packet["packetId"],
			"claim":            packet["claimText"],
			"status":           packetStatus(packet),
			"sourceIds":        sourceIDs,
			"sourceTitles":     titlesForSourceIDs(sourceIDs, sourceTitleByID),
			"sourceClusterId":  packet["sourceClusterId"],
			"supportScore":     packet["confidence"],
			"evidenceSnippets": evidenceSnippetsFromPacket(packet),
		}
		switch packetStatus(packet) {
		case "verified":
			verified = append(verified, finding)
			mergeWorkspaceConclusion(conclusionIndex, &conclusionOrder, finding, sourceTitleByID)
		case "contradictory":
			contradictions = append(contradictions, finding)
		case "unsupported":
			unsupported = append(unsupported, finding)
		default:
			tentative = append(tentative, finding)
		}
	}
	conclusions := make([]map[string]any, 0, len(conclusionOrder))
	for _, claim := range conclusionOrder {
		conclusions = append(conclusions, conclusionIndex[claim])
	}
	return map[string]any{
		"dossierId":                       result.Dossier.DossierID,
		"bundleId":                        result.RawMaterials.RawMaterialSetID,
		"verifiedFindings":                verified,
		"tentativeFindings":               tentative,
		"contradictoryFindings":           contradictions,
		"unsupportedFindings":             unsupported,
		"unresolvedGaps":                  result.Dossier.Gaps,
		"recommendedNextRetrievalActions": []string{"Review contradictions", "Attach more source papers for uncovered sections"},
		"conclusions":                     conclusions,
		"coverageMetrics":                 result.Dossier.CoverageMetrics,
	}
}

func authorObjects(authors []string) []map[string]any {
	if len(authors) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(authors))
	for _, author := range authors {
		name := strings.TrimSpace(author)
		if name == "" {
			continue
		}
		out = append(out, map[string]any{"name": name})
	}
	return out
}

func resolveCanonicalSourceCitationCount(source evidence.CanonicalCitationRecord, papers []search.Paper) int {
	if source.CitationCount > 0 {
		return source.CitationCount
	}
	sourceTitle := strings.ToLower(strings.TrimSpace(source.Title))
	sourceDOI := strings.ToLower(strings.TrimSpace(source.SourceIDs.DOI))
	for _, paper := range papers {
		if paper.CitationCount <= 0 {
			continue
		}
		record := evidence.BuildCanonicalRecord(paper)
		if source.CanonicalID != "" && record.CanonicalID == source.CanonicalID {
			return paper.CitationCount
		}
		if sourceDOI != "" && strings.EqualFold(strings.TrimSpace(paper.DOI), sourceDOI) {
			return paper.CitationCount
		}
		if sourceTitle != "" && strings.EqualFold(strings.TrimSpace(paper.Title), sourceTitle) {
			return paper.CitationCount
		}
	}
	return 0
}

func buildSourceBundleSources(result wisdev.ManuscriptPipelineResult, papers []search.Paper) []map[string]any {
	if len(result.RawMaterials.CanonicalSources) > 0 {
		out := make([]map[string]any, 0, len(result.RawMaterials.CanonicalSources))
		for _, source := range result.RawMaterials.CanonicalSources {
			paperID := firstNonEmptyString(source.CanonicalID)
			entry := map[string]any{
				"title":         source.Title,
				"summary":       firstNonEmptyString(firstSentence(source.Abstract), source.Title),
				"year":          source.Year,
				"citationCount": resolveCanonicalSourceCitationCount(source, papers),
				"link":          source.LandingURL,
				"canonicalId":   source.CanonicalID,
				"paperId":       paperID,
				"authors":       authorObjects(source.Authors),
				"publication":   source.Venue,
				"journal":       source.Venue,
			}
			if doi := strings.TrimSpace(source.SourceIDs.DOI); doi != "" {
				entry["doi"] = doi
			}
			out = append(out, entry)
		}
		return out
	}
	if len(papers) > 0 {
		out := make([]map[string]any, 0, len(papers))
		for _, paper := range papers {
			record := evidence.BuildCanonicalRecord(paper)
			canonicalID := record.CanonicalID
			paperID := firstNonEmptyString(strings.TrimSpace(paper.ID), canonicalID)
			entry := map[string]any{
				"title":         paper.Title,
				"summary":       firstNonEmptyString(firstSentence(paper.Abstract), paper.Title),
				"year":          paper.Year,
				"citationCount": paper.CitationCount,
				"link":          paper.Link,
				"canonicalId":   canonicalID,
				"paperId":       paperID,
				"authors":       authorObjects(paper.Authors),
				"publication":   paper.Venue,
				"journal":       paper.Venue,
			}
			if doi := strings.TrimSpace(paper.DOI); doi != "" {
				entry["doi"] = doi
			}
			out = append(out, entry)
		}
		return out
	}
	return nil
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
			// Carry the manual-edit provenance into the view the UI renders so the
			// editor can badge user-owned sections and the client-side merge rule
			// can avoid overwriting a section the user edited (P4).
			"userEdited": section["userEdited"],
			"editedBy":   section["editedBy"],
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

func rewriteFullPaperSection(ctx context.Context, job map[string]any, sectionID string, instructions string) (map[string]any, error) {
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
		originalContent := strings.TrimSpace(firstNonEmptyString(wisdev.AsOptionalString(section["content"]), wisdev.AsOptionalString(section["text"])))
		// Prefer a real LLM-backed, instruction-guided rewrite (ScholarLM does the
		// heavy lifting). Fall back to a deterministic "Revision focus" annotation only
		// when the sidecar is unavailable or errors, so the endpoint still succeeds
		// offline exactly as before.
		newContent := llmRewriteFullPaperSectionContent(ctx, workspace, sectionDrafts, index, sectionID, originalContent, instructions)
		if newContent == "" {
			newContent = originalContent +
				"\n\nRevision focus: " + firstNonEmptyString(instructions, "Refresh evidence grounding and align prose with the current critique.")
		}
		rewritten["content"] = newContent
		rewritten["text"] = newContent
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

// llmRewriteFullPaperSectionContent asks ScholarLM (via the manuscript sidecar) to
// rewrite one section under the user's free-text instructions, reusing the grounded
// claim packets stored on the workspace so the rewrite stays cited and attributed. It
// returns "" (never an error) on any problem — missing materials, no sidecar, empty
// result — so the caller falls back to the deterministic annotation and the endpoint
// never fails just because generation was unavailable.
func llmRewriteFullPaperSectionContent(
	ctx context.Context,
	workspace map[string]any,
	sectionDrafts []map[string]any,
	index int,
	sectionID string,
	originalContent string,
	instructions string,
) string {
	if strings.TrimSpace(originalContent) == "" {
		return ""
	}
	// Grounded claim packets for this section: the workspace's raw material set filtered
	// to the ids this section drafted against, so the rewrite cites the same evidence.
	packetIDs := map[string]struct{}{}
	for _, id := range sliceStrings(sectionDrafts[index]["claimPacketIds"]) {
		packetIDs[id] = struct{}{}
	}
	rawMaterials := mapAny(workspace["rawMaterialSet"])
	sectionPackets := make([]map[string]any, 0, len(packetIDs))
	for _, packet := range sliceAnyMap(rawMaterials["claimPackets"]) {
		if _, ok := packetIDs[wisdev.AsOptionalString(packet["packetId"])]; ok {
			sectionPackets = append(sectionPackets, packet)
		}
	}
	if len(sectionPackets) == 0 {
		return "" // no grounded evidence to keep the rewrite cited — stay deterministic
	}
	// The other sections, so the rewrite coheres with them and avoids repetition.
	prior := make([]map[string]any, 0, len(sectionDrafts))
	for i, section := range sectionDrafts {
		if i == index {
			continue
		}
		text := strings.TrimSpace(firstNonEmptyString(wisdev.AsOptionalString(section["content"]), wisdev.AsOptionalString(section["text"])))
		if text == "" {
			continue
		}
		prior = append(prior, map[string]any{
			"title": firstNonEmptyString(wisdev.AsOptionalString(section["title"]), wisdev.AsOptionalString(section["sectionId"])),
			"text":  text,
		})
	}
	pipeline := wisdev.NewManuscriptPipeline(wisdev.ResolvePythonBase())
	revised, err := pipeline.ReviseSectionWithInstructions(ctx, sectionID, originalContent, sectionPackets, prior, instructions)
	if err != nil || strings.TrimSpace(revised) == "" {
		if err != nil {
			slog.Warn("full paper section LLM rewrite unavailable — falling back to deterministic edit",
				"component", "api.full_paper", "operation", "rewrite_section",
				"section_id", sectionID, "error", err.Error())
		}
		return ""
	}
	return strings.TrimSpace(revised)
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

	raw, hasRaw := loadRawMaterialSetFromJob(job)

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
		regenerated["lastReviewDecision"] = "regenerated"
		regenerated["updatedAt"] = now

		kind := strings.ToLower(wisdev.AsOptionalString(regenerated["kind"]))
		title := wisdev.AsOptionalString(regenerated["title"])
		specType := wisdev.AsOptionalString(regenerated["specType"])
		isEvidenceSummary := kind == "table_summary" || title == "Evidence Summary" || (specType == "table" && kind == "table_summary")

		var newSpecType string
		var newSpec any
		sourcePacketIDs := sliceStrings(regenerated["sourcePacketIds"])

		if hasRaw && isEvidenceSummary {
			table, drawn := wisdev.BuildEvidenceSummaryTable(raw)
			newSpecType = "table"
			newSpec = table
			sourcePacketIDs = drawn
		} else if hasRaw {
			if matched := findMatchingVisualEvidence(raw, regenerated); matched != nil {
				packetIndex := packetIndexFromRaw(raw)
				newSpecType, newSpec = wisdev.BuildVisualSpec(*matched, packetIndex)
				if len(matched.SourcePacketIDs) > 0 {
					sourcePacketIDs = matched.SourcePacketIDs
				}
			}
		}

		if newSpecType != "" {
			regenerated["specType"] = newSpecType
			regenerated["spec"] = newSpec
		} else {
			fallbackType, fallbackSpec := refreshVisualSpecFallback(regenerated, instructions)
			regenerated["specType"] = fallbackType
			regenerated["spec"] = fallbackSpec
		}

		applyVisualReviewStatus(regenerated, sourcePacketIDs)

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

func loadRawMaterialSetFromJob(job map[string]any) (evidence.ManuscriptRawMaterialSet, bool) {
	workspace := mapAny(job["workspace"])
	candidates := []any{
		job["rawMaterialSet"],
		workspace["rawMaterialSet"],
	}
	if dossier := mapAny(job["evidenceDossier"]); dossier != nil {
		candidates = append(candidates, dossier["rawMaterialSet"])
	}
	if dossier := mapAny(workspace["evidenceDossier"]); dossier != nil {
		candidates = append(candidates, dossier["rawMaterialSet"])
	}
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		raw, ok := parseRawMaterialSet(candidate)
		if ok {
			return raw, true
		}
	}
	return evidence.ManuscriptRawMaterialSet{}, false
}

func parseRawMaterialSet(value any) (evidence.ManuscriptRawMaterialSet, bool) {
	switch typed := value.(type) {
	case evidence.ManuscriptRawMaterialSet:
		return typed, hasRawMaterialContent(typed)
	case *evidence.ManuscriptRawMaterialSet:
		if typed == nil {
			return evidence.ManuscriptRawMaterialSet{}, false
		}
		return *typed, hasRawMaterialContent(*typed)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return evidence.ManuscriptRawMaterialSet{}, false
	}
	var raw evidence.ManuscriptRawMaterialSet
	if err := json.Unmarshal(data, &raw); err != nil {
		return evidence.ManuscriptRawMaterialSet{}, false
	}
	return raw, hasRawMaterialContent(raw)
}

func hasRawMaterialContent(raw evidence.ManuscriptRawMaterialSet) bool {
	return len(raw.ClaimPackets) > 0 || len(raw.VisualEvidence) > 0 || len(raw.CanonicalSources) > 0 || len(raw.SourceClusters) > 0
}

func packetIndexFromRaw(raw evidence.ManuscriptRawMaterialSet) map[string]evidence.EvidencePacket {
	index := make(map[string]evidence.EvidencePacket, len(raw.ClaimPackets))
	for _, packet := range raw.ClaimPackets {
		index[packet.PacketID] = packet
	}
	return index
}

func findMatchingVisualEvidence(raw evidence.ManuscriptRawMaterialSet, artifact map[string]any) *evidence.VisualEvidence {
	artifactVisualID := firstNonEmptyString(
		wisdev.AsOptionalString(artifact["visualId"]),
		wisdev.AsOptionalString(artifact["sourceVisualId"]),
	)
	title := strings.TrimSpace(wisdev.AsOptionalString(artifact["title"]))
	for i := range raw.VisualEvidence {
		candidate := &raw.VisualEvidence[i]
		if artifactVisualID != "" && candidate.VisualID == artifactVisualID {
			return candidate
		}
		if title != "" && strings.EqualFold(strings.TrimSpace(candidate.Title), title) {
			return candidate
		}
	}
	return nil
}

func refreshVisualSpecFallback(visual map[string]any, instructions string) (string, any) {
	specType := wisdev.AsOptionalString(visual["specType"])
	switch specType {
	case "table":
		spec := mapAny(visual["spec"])
		table := wisdev.ManuscriptTableSpec{
			Headers: sliceStrings(spec["headers"]),
			Rows:    tableRowsFromSpec(spec["rows"]),
		}
		if len(table.Headers) == 0 && len(table.Rows) == 0 {
			table = wisdev.ManuscriptTableSpec{
				Headers: []string{"Item", "Summary"},
				Rows:    [][]string{{wisdev.AsOptionalString(visual["title"]), wisdev.AsOptionalString(visual["caption"])}},
			}
		}
		return "table", table
	case "mermaid":
		spec := wisdev.AsOptionalString(visual["spec"])
		note := firstNonEmptyString(instructions, "Regenerated review-ready visual")
		return "mermaid", strings.TrimSpace(spec + "\n    review[\"" + note + "\"]")
	default:
		return specType, visual["spec"]
	}
}

func tableRowsFromSpec(value any) [][]string {
	rows := make([][]string, 0)
	switch typed := value.(type) {
	case [][]string:
		return typed
	case []any:
		for _, row := range typed {
			switch rowTyped := row.(type) {
			case []string:
				if len(rowTyped) > 0 {
					rows = append(rows, rowTyped)
				}
			case []any:
				parsed := make([]string, 0, len(rowTyped))
				for _, cell := range rowTyped {
					if text := strings.TrimSpace(wisdev.AsOptionalString(cell)); text != "" {
						parsed = append(parsed, text)
					}
				}
				if len(parsed) > 0 {
					rows = append(rows, parsed)
				}
			}
		}
	}
	return rows
}

func applyVisualReviewStatus(visual map[string]any, sourcePacketIDs []string) {
	sourcePacketIDs = uniqueStrings(sourcePacketIDs)
	if len(sourcePacketIDs) == 0 {
		visual["reviewStatus"] = "needs_revision"
		visual["unresolvedIssues"] = []any{"visual is not grounded to any claim packets"}
		return
	}
	visual["reviewStatus"] = "ready_for_review"
	visual["unresolvedIssues"] = []any{}
	visual["sourcePacketIds"] = sourcePacketIDs
}

// scriptTagPattern is a lightweight defense-in-depth guard. Section content is
// already sanitized client-side (sanitizeRichContent); this only removes any
// <script>…</script> that slipped through. It intentionally does not reformat.
var scriptTagPattern = regexp.MustCompile(`(?is)<script.*?</script>`)

func stripScriptTags(html string) string {
	return scriptTagPattern.ReplaceAllString(html, "")
}

// editFullPaperSection persists a user's MANUAL edit of a section's content.
// Unlike rewriteFullPaperSection (an AI revision that requests peer review), a
// manual edit is authoritative: it marks the section userEdited and does NOT
// force awaiting_approval or a peer-review checkpoint. The userEdited flag is what
// the regeneration stages honor so the still-running pipeline can't clobber it.
func editFullPaperSection(job map[string]any, sectionID string, contentHTML string, userID string) (map[string]any, error) {
	if sectionID == "" {
		return nil, fmt.Errorf("sectionId is required")
	}
	content := strings.TrimSpace(stripScriptTags(contentHTML))
	if content == "" {
		return nil, fmt.Errorf("contentHtml is required")
	}
	workspace := mapAny(job["workspace"])
	sectionDrafts := sliceAnyMap(workspace["sectionDraftArtifacts"])
	if len(sectionDrafts) == 0 {
		return nil, fmt.Errorf("workspace has no section draft artifacts")
	}

	now := time.Now().UnixMilli()
	var edited map[string]any
	for index, section := range sectionDrafts {
		if wisdev.AsOptionalString(section["sectionId"]) != sectionID {
			continue
		}
		edited = cloneAnyMap(section)
		edited["artifactId"] = fmt.Sprintf("%s_v%d", wisdev.AsOptionalString(section["artifactId"]), wisdev.IntValue(section["version"])+1)
		edited["version"] = wisdev.IntValue(section["version"]) + 1
		edited["content"] = content
		edited["text"] = content
		edited["userEdited"] = true
		edited["editedBy"] = userID
		edited["lastReviewDecision"] = "user_edited"
		edited["updatedAt"] = now
		sectionDrafts[index] = edited
		break
	}
	if edited == nil {
		return nil, fmt.Errorf("section %s not found", sectionID)
	}

	workspace["sectionDraftArtifacts"] = sectionDrafts
	drafting := mapAny(workspace["drafting"])
	sections := mapAny(drafting["sections"])
	sections[sectionID] = edited
	drafting["sections"] = sections
	drafting["sectionArtifactIds"] = uniqueStrings(append(sliceStrings(drafting["sectionArtifactIds"]), wisdev.AsOptionalString(edited["artifactId"])))
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
	newManuscript["lastReviewAction"] = "user_edited"
	newManuscript["summary"] = "Manuscript snapshot after a manual section edit."
	artifacts = append(artifacts, newManuscript)

	// Manual edit is authoritative — no forced checkpoint / status change.
	workspace["artifacts"] = artifacts
	workspace["latestManuscriptArtifact"] = newManuscript
	workspace["updatedAt"] = now
	job["artifacts"] = artifacts
	job["workspace"] = workspace
	job["artifactIds"] = artifactIDs(artifacts)
	job["updatedAt"] = now
	appendWorkspaceAudit(job, "edit_section", firstNonEmptyString(userID, "user"), fmt.Sprintf("Edited section %s.", sectionID))

	return map[string]any{
		"job":                job,
		"workspace":          workspace,
		"sectionArtifact":    edited,
		"manuscriptArtifact": newManuscript,
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

func hydrateFullPaperStartPapers(ctx context.Context, agentGateway *wisdev.AgentGateway, query string, orchestrationPlan map[string]any, metadata map[string]any, minCitations int, traceID string) []search.Paper {
	if agentGateway == nil || agentGateway.SearchRegistry == nil {
		return nil
	}
	budget := resolveFullPaperHydrationBudget(minCitations)
	queries := normalizeFullPaperQueries(query, sliceStrings(orchestrationPlan["queries"]))
	if len(queries) == 0 {
		return nil
	}
	if len(queries) > budget.maxQueries {
		queries = queries[:budget.maxQueries]
	}

	searchCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	opts := wisdev.SearchOptions{
		Limit:       budget.limitPerQuery,
		QualitySort: true,
		Domain:      strings.TrimSpace(wisdev.AsOptionalString(metadata["detectedDomain"])),
		TraceID:     strings.TrimSpace(traceID),
	}
	allPapers := make([]search.Paper, 0, len(queries)*opts.Limit)
	for _, planQuery := range queries {
		sources, _, err := wisdev.RetrieveCanonicalPapersWithOptions(searchCtx, nil, agentGateway.SearchRegistry, planQuery, opts)
		if err != nil {
			slog.Warn("full paper start evidence hydration query failed",
				"component", "api.full_paper",
				"operation", "full_paper_start",
				"stage", "evidence_hydration_query_failed",
				"query_preview", firstSentence(planQuery),
				"trace_id", traceID,
				"min_citations", minCitations,
				"error", err,
			)
			continue
		}
		allPapers = append(allPapers, convertWisdevSourcesToSearchPapers(sources)...)
	}
	deduped := search.Deduplicate(allPapers)
	slog.Info("full paper start evidence hydration completed",
		"component", "api.full_paper",
		"operation", "full_paper_start",
		"stage", "evidence_hydration_completed",
		"query_count", len(queries),
		"limit_per_query", opts.Limit,
		"paper_count", len(deduped),
		"dedup_cap", budget.dedupCap,
		"min_citations", minCitations,
		"trace_id", traceID,
		"result", map[bool]string{true: "hydrated", false: "empty"}[len(deduped) > 0],
	)
	if len(deduped) > budget.dedupCap {
		return deduped[:budget.dedupCap]
	}
	return deduped
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
	return titlesForSourceIDs(sourceIDsFromPacket(packet), titleIndex)
}

func titlesForSourceIDs(sourceIDs []string, titleIndex map[string]string) []string {
	out := make([]string, 0, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		if title := titleIndex[sourceID]; title != "" {
			out = append(out, title)
		}
	}
	return out
}

func evidenceSnippetsFromPacket(packet map[string]any) []string {
	spans := sliceAnyMap(packet["evidenceSpans"])
	out := make([]string, 0, len(spans))
	for _, span := range spans {
		snippet := strings.TrimSpace(wisdev.AsOptionalString(span["snippet"]))
		if snippet != "" {
			out = append(out, snippet)
		}
	}
	return out
}

func mergeWorkspaceConclusion(index map[string]map[string]any, order *[]string, finding map[string]any, sourceTitleByID map[string]string) {
	claim := strings.TrimSpace(wisdev.AsOptionalString(finding["claim"]))
	if claim == "" {
		return
	}
	findingID := wisdev.AsOptionalString(finding["id"])
	sourceIDs := sliceStrings(finding["sourceIds"])
	supportScore := finding["supportScore"]

	if existing, ok := index[claim]; ok {
		existing["findingIds"] = uniqueStrings(append(sliceStrings(existing["findingIds"]), findingID))
		mergedSourceIDs := uniqueStrings(append(sliceStrings(existing["sourceIds"]), sourceIDs...))
		existing["sourceIds"] = mergedSourceIDs
		existing["sourceTitles"] = titlesForSourceIDs(mergedSourceIDs, sourceTitleByID)
		existing["supportScore"] = maxSupportScore(existing["supportScore"], supportScore)
		return
	}

	entry := map[string]any{
		"claim":        claim,
		"findingIds":   uniqueStrings([]string{findingID}),
		"sourceIds":    sourceIDs,
		"sourceTitles": titlesForSourceIDs(sourceIDs, sourceTitleByID),
	}
	if supportScore != nil {
		entry["supportScore"] = supportScore
	}
	index[claim] = entry
	*order = append(*order, claim)
}

func maxSupportScore(left any, right any) any {
	leftScore, leftOK := supportScoreValue(left)
	rightScore, rightOK := supportScoreValue(right)
	switch {
	case leftOK && rightOK:
		if rightScore > leftScore {
			return rightScore
		}
		return leftScore
	case rightOK:
		return rightScore
	case leftOK:
		return leftScore
	default:
		return nil
	}
}

func supportScoreValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
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

func normalizeFullPaperQueries(query string, planQueries []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(planQueries)+1)
	for _, candidate := range append([]string{query}, planQueries...) {
		normalized := strings.TrimSpace(candidate)
		if normalized == "" {
			continue
		}
		key := strings.ToLower(normalized)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, normalized)
	}
	return out
}
