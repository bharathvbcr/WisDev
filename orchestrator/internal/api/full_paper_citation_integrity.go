package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
)

const defaultCitationIntegrityMaxRounds = 2

func runFullPaperCitationIntegrityVerify(job map[string]any) map[string]any {
	workspace := mapAny(job["workspace"])
	outcomes, _ := verifyFullPaperWorkspaceCitations(workspace)
	return map[string]any{
		"summary":            summarizeCitationVerifyOutcomes(outcomes),
		"ungrounded":         ungroundedFromOutcomes(outcomes),
		"workspaceUpdatedAt": wisdev.IntValue64(workspace["updatedAt"]),
	}
}

func runFullPaperCitationIntegrityReground(
	ctx context.Context,
	job map[string]any,
	instructions string,
	maxRounds int,
) (map[string]any, error) {
	if maxRounds <= 0 {
		maxRounds = defaultCitationIntegrityMaxRounds
	}
	reviseInstructions := strings.TrimSpace(instructions)
	if reviseInstructions == "" {
		reviseInstructions = defaultCitationRegroundInstructions()
	}

	var lastSummary map[string]any
	var lastUngrounded []map[string]any
	previousFingerprint := ""

	for round := 0; round < maxRounds; round++ {
		workspace := mapAny(job["workspace"])
		outcomes, _ := verifyFullPaperWorkspaceCitations(workspace)
		summary := summarizeCitationVerifyOutcomes(outcomes)
		ungrounded := ungroundedFromOutcomes(outcomes)
		lastSummary = summary
		lastUngrounded = ungrounded

		if wisdev.IntValue(summary["ungrounded"]) == 0 {
			break
		}

		fingerprint := citationIntegrityFingerprint(outcomes)
		if fingerprint == previousFingerprint {
			break
		}
		previousFingerprint = fingerprint

		changed := false
		for _, sectionID := range failingSectionIDs(outcomes) {
			if sectionUserEdited(workspace, sectionID) {
				continue
			}
			if _, err := rewriteFullPaperSection(ctx, job, sectionID, reviseInstructions); err != nil {
				return nil, fmt.Errorf("failed to reground section %s: %w", sectionID, err)
			}
			changed = true
		}
		if !changed {
			break
		}
	}

	workspace := mapAny(job["workspace"])
	appendWorkspaceAudit(job, "citation_integrity_reground", "user", "Re-grounded failing manuscript citations.")
	bumpUpdatedAt(job)
	workspace["updatedAt"] = job["updatedAt"]
	job["workspace"] = workspace

	outcomes, _ := verifyFullPaperWorkspaceCitations(mapAny(job["workspace"]))
	return map[string]any{
		"job":                job,
		"summary":            summarizeCitationVerifyOutcomes(outcomes),
		"ungrounded":         ungroundedFromOutcomes(outcomes),
		"workspaceUpdatedAt": wisdev.IntValue64(mapAny(job["workspace"])["updatedAt"]),
		"rounds":             maxRounds,
		"lastPassSummary":    lastSummary,
		"lastPassUngrounded": lastUngrounded,
	}, nil
}

func handleFullPaperCitationIntegrity(
	w http.ResponseWriter,
	r *http.Request,
	agentGateway *wisdev.AgentGateway,
) {
	var req struct {
		JobID             string `json:"jobId"`
		Action            string `json:"action"`
		Instructions      string `json:"instructions"`
		MaxRounds         int    `json:"maxRounds"`
		ExpectedUpdatedAt int64  `json:"expectedUpdatedAt"`
		TraceID           string `json:"traceId,omitempty"`
		TraceIDLegacy     string `json:"trace_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
		return
	}

	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = "verify"
	}
	if action != "verify" && action != "reground" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "invalid action", map[string]any{
			"allowedActions": []string{"verify", "reground"},
		})
		return
	}

	traceID := resolveWisdevRouteTraceID(r, req.TraceID, req.TraceIDLegacy)
	job, ok := loadOwnedFullPaperJobState(w, r, agentGateway, strings.TrimSpace(req.JobID))
	if !ok {
		return
	}
	if action == "reground" {
		if fullPaperHasTerminalStatus(wisdev.AsOptionalString(job["status"])) {
			WriteError(w, http.StatusConflict, ErrInvalidParameters, "job is finalized", nil)
			return
		}
		if !assertExpectedUpdatedAt(w, req.ExpectedUpdatedAt, job) {
			return
		}
	}

	var payload map[string]any
	switch action {
	case "verify":
		payload = runFullPaperCitationIntegrityVerify(job)
	case "reground":
		result, err := runFullPaperCitationIntegrityReground(r.Context(), job, req.Instructions, req.MaxRounds)
		if err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), nil)
			return
		}
		if err := saveFullPaperJobState(agentGateway, job); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist regrounded manuscript", map[string]any{"error": err.Error()})
			return
		}
		payload = result
	}

	w.Header().Set("X-Trace-Id", traceID)
	writeEnvelopeWithTraceID(w, traceID, "result", payload)
}
