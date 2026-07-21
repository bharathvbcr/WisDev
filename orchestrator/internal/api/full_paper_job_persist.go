package api

import (
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
)

// persistFullPaperJobFromManuscript builds the same job envelope as /full-paper/start
// and persists it best-effort. Returns persisted=false when the gateway is unavailable
// or save fails; callers must not fail upstream research on persist errors.
func persistFullPaperJobFromManuscript(
	agentGateway *wisdev.AgentGateway,
	jobID, userID, sessionID, query string,
	result wisdev.ManuscriptPipelineResult,
	papers []search.Paper,
) bool {
	if agentGateway == nil || strings.TrimSpace(jobID) == "" || strings.TrimSpace(userID) == "" {
		return false
	}
	job := buildPersistedFullPaperJob(jobID, userID, sessionID, query, result, papers)
	if err := saveFullPaperJobState(agentGateway, job); err != nil {
		return false
	}
	return true
}

func buildPersistedFullPaperJob(
	jobID, userID, sessionID, query string,
	result wisdev.ManuscriptPipelineResult,
	papers []search.Paper,
) map[string]any {
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
	return map[string]any{
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
}
