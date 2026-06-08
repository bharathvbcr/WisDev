package wisdev

import (
	"context"
	"fmt"
	"strings"
)

type ResearchMemoryBackfillResult struct {
	UserID            string `json:"userId"`
	ProjectID         string `json:"projectId,omitempty"`
	DossiersScanned   int    `json:"dossiersScanned"`
	QuestsScanned     int    `json:"questsScanned"`
	WorkspacesScanned int    `json:"workspacesScanned"`
	SessionsScanned   int    `json:"sessionsScanned"`
	PromotionsApplied int    `json:"promotionsApplied"`
	EpisodesApplied   int    `json:"episodesApplied"`
	SkippedArtifacts  int    `json:"skippedArtifacts"`
}

func (c *ResearchMemoryCompiler) BackfillHistoricalArtifacts(ctx context.Context, userID, projectID string) (*ResearchMemoryBackfillResult, error) {
	if c == nil || c.stateStore == nil {
		return &ResearchMemoryBackfillResult{}, nil
	}
	userID = strings.TrimSpace(userID)
	projectID = strings.TrimSpace(projectID)
	if userID == "" {
		return &ResearchMemoryBackfillResult{}, nil
	}
	result := &ResearchMemoryBackfillResult{UserID: userID, ProjectID: projectID}

	dossiers, err := c.stateStore.SearchEvidenceDossiers(userID)
	if err != nil {
		return nil, err
	}
	for _, dossier := range dossiers {
		if !researchMemoryMatchesProjectScope(dossier, projectID) {
			continue
		}
		result.DossiersScanned++
		preferredSources := extractBackfillPreferredSources(dossier)
		if err := c.ConsolidateDossierPayload(ctx, userID, projectIDOrArtifact(projectID, dossier), dossier, preferredSources); err != nil {
			return nil, err
		}
		result.PromotionsApplied++
		result.EpisodesApplied++
	}

	quests, err := c.stateStore.SearchQuestStates(userID)
	if err != nil {
		return nil, err
	}
	for _, quest := range quests {
		if !researchMemoryMatchesProjectScope(quest, projectID) {
			continue
		}
		result.QuestsScanned++
		promotion, episode := questBackfillInputs(userID, projectIDOrArtifact(projectID, quest), quest)
		if len(promotion.Findings) == 0 && episode.Summary == "" {
			result.SkippedArtifacts++
			continue
		}
		if len(promotion.Findings) > 0 {
			if _, err := c.PromoteFindings(ctx, promotion); err != nil {
				return nil, err
			}
			result.PromotionsApplied++
		}
		if strings.TrimSpace(episode.Summary) != "" || len(episode.AcceptedFindings) > 0 || len(episode.UnresolvedGaps) > 0 {
			if _, err := c.ConsolidateEpisode(ctx, episode); err != nil {
				return nil, err
			}
			result.EpisodesApplied++
		}
	}

	workspaces, err := c.stateStore.SearchProjectWorkspaces(userID)
	if err != nil {
		return nil, err
	}
	for _, workspace := range workspaces {
		if !researchMemoryMatchesProjectScope(workspace, projectID) {
			continue
		}
		result.WorkspacesScanned++
		episode := workspaceBackfillInput(userID, projectIDOrArtifact(projectID, workspace), workspace)
		if strings.TrimSpace(episode.Summary) == "" && len(episode.UnresolvedGaps) == 0 && len(episode.RecommendedQueries) == 0 {
			result.SkippedArtifacts++
			continue
		}
		if _, err := c.ConsolidateEpisode(ctx, episode); err != nil {
			return nil, err
		}
		result.EpisodesApplied++
	}

	summaries, err := c.stateStore.LoadSessionSummaries(userID)
	if err == nil {
		for _, summary := range summaries {
			if !researchMemoryMatchesProjectScope(summary, projectID) {
				continue
			}
			result.SessionsScanned++
			episode := sessionSummaryBackfillInput(userID, projectIDOrArtifact(projectID, summary), summary)
			if strings.TrimSpace(episode.Summary) == "" && len(episode.AcceptedFindings) == 0 {
				result.SkippedArtifacts++
				continue
			}
			if _, err := c.ConsolidateEpisode(ctx, episode); err != nil {
				return nil, err
			}
			result.EpisodesApplied++
		}
	}

	c.appendAudit(ctx, "research_memory_backfill", userID, projectID, "backfill", map[string]any{
		"dossiersScanned":   result.DossiersScanned,
		"questsScanned":     result.QuestsScanned,
		"workspacesScanned": result.WorkspacesScanned,
		"sessionsScanned":   result.SessionsScanned,
		"promotionsApplied": result.PromotionsApplied,
		"episodesApplied":   result.EpisodesApplied,
		"skippedArtifacts":  result.SkippedArtifacts,
	})
	return result, nil
}

func researchMemoryMatchesProjectScope(payload map[string]any, projectID string) bool {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return true
	}
	return strings.TrimSpace(AsOptionalString(firstNonEmptyValue(payload["projectId"], payload["sessionId"]))) == projectID
}

func projectIDOrArtifact(explicitProjectID string, payload map[string]any) string {
	if strings.TrimSpace(explicitProjectID) != "" {
		return strings.TrimSpace(explicitProjectID)
	}
	return strings.TrimSpace(AsOptionalString(firstNonEmptyValue(payload["projectId"], payload["sessionId"])))
}

func extractBackfillPreferredSources(payload map[string]any) []string {
	values := make([]string, 0, 8)
	for _, source := range firstArtifactMaps(payload["canonicalSources"]) {
		values = append(values, strings.TrimSpace(AsOptionalString(firstNonEmptyValue(source["provider"], source["sourceApi"], source["canonicalId"]))))
	}
	values = append(values, toStringSlice(payload["includeDomains"])...)
	return uniqueStrings(values)
}

func questBackfillInputs(userID, projectID string, payload map[string]any) (ResearchMemoryPromotionInput, ResearchMemoryEpisodeInput) {
	query := strings.TrimSpace(AsOptionalString(payload["query"]))
	findings := questAcceptedClaims(payload)
	rejected := firstArtifactMaps(payload["rejectedBranches"])
	contradictionText := make([]string, 0, len(rejected))
	for _, branch := range rejected {
		if text := strings.TrimSpace(AsOptionalString(firstNonEmptyValue(branch["content"], branch["summary"]))); text != "" {
			contradictionText = append(contradictionText, text)
		}
	}
	preferredSources := extractQuestSourcesFromPayload(payload)
	recommendedQueries := uniqueStrings(append(extractQuestMethodsFromPayload(payload), toStringSlice(payload["blockingIssues"])...))
	return ResearchMemoryPromotionInput{
			UserID:             userID,
			ProjectID:          projectID,
			Query:              query,
			Scope:              projectScopeOrUser(projectID),
			Findings:           findings,
			RecommendedQueries: recommendedQueries,
			PreferredSources:   preferredSources,
			UnresolvedGaps:     toStringSlice(payload["blockingIssues"]),
		}, ResearchMemoryEpisodeInput{
			UserID:             userID,
			ProjectID:          projectID,
			Query:              query,
			Scope:              projectScopeOrUser(projectID),
			Summary:            strings.TrimSpace(firstNonEmpty(AsOptionalString(payload["finalAnswer"]), fmt.Sprintf("Backfilled research quest for %s.", query))),
			AcceptedFindings:   findingClaims(findings),
			Contradictions:     uniqueStrings(contradictionText),
			UnresolvedGaps:     toStringSlice(payload["blockingIssues"]),
			RecommendedQueries: recommendedQueries,
			ReusableStrategies: preferredSources,
		}
}

func questAcceptedClaims(payload map[string]any) []EvidenceFinding {
	items := firstArtifactMaps(payload["acceptedClaims"])
	out := make([]EvidenceFinding, 0, len(items))
	for _, item := range items {
		out = append(out, mapToEvidenceFinding(item))
	}
	return out
}

func extractQuestSourcesFromPayload(payload map[string]any) []string {
	sources := firstArtifactMaps(payload["papers"])
	values := make([]string, 0, len(sources))
	for _, source := range sources {
		values = append(values, strings.TrimSpace(AsOptionalString(firstNonEmptyValue(source["sourceApi"], source["provider"], source["siteName"]))))
	}
	return uniqueStrings(values)
}

func extractQuestMethodsFromPayload(payload map[string]any) []string {
	sources := firstArtifactMaps(payload["papers"])
	values := make([]string, 0, len(sources))
	for _, source := range sources {
		values = append(values, deriveResearchMemoryTopics(strings.Join([]string{
			AsOptionalString(source["title"]),
			AsOptionalString(source["summary"]),
			strings.Join(toStringSlice(source["keywords"]), " "),
		}, " "))...)
	}
	return uniqueStrings(values)
}

func workspaceBackfillInput(userID, projectID string, payload map[string]any) ResearchMemoryEpisodeInput {
	recommendedQueries := uniqueStrings(append(toStringSlice(payload["followUpQueries"]), toStringSlice(payload["trustedSourceClusters"])...))
	summaryParts := make([]string, 0, 3)
	if len(toStringSlice(payload["unresolvedGaps"])) > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("%d unresolved gap(s)", len(toStringSlice(payload["unresolvedGaps"]))))
	}
	if len(recommendedQueries) > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("%d follow-up cue(s)", len(recommendedQueries)))
	}
	if len(firstArtifactMaps(payload["savedDossiers"])) > 0 || len(firstArtifactMaps(payload["savedTrajectories"])) > 0 {
		summaryParts = append(summaryParts, "workspace artifacts available")
	}
	return ResearchMemoryEpisodeInput{
		UserID:             userID,
		ProjectID:          projectID,
		Query:              strings.TrimSpace(firstNonEmpty(AsOptionalString(payload["query"]), AsOptionalString(payload["projectId"]), "workspace backfill")),
		Scope:              projectScopeOrUser(projectID),
		Summary:            strings.Join(summaryParts, " · "),
		UnresolvedGaps:     toStringSlice(payload["unresolvedGaps"]),
		RecommendedQueries: recommendedQueries,
		ReusableStrategies: recommendedQueries,
	}
}

func sessionSummaryBackfillInput(userID, projectID string, payload map[string]any) ResearchMemoryEpisodeInput {
	query := strings.TrimSpace(firstNonEmpty(
		AsOptionalString(payload["query"]),
		AsOptionalString(payload["correctedQuery"]),
		AsOptionalString(payload["title"]),
		"session backfill",
	))
	accepted := uniqueStrings(append(toStringSlice(payload["keyFindings"]), toStringSlice(payload["acceptedFindings"])...))
	recommended := uniqueStrings(append(toStringSlice(payload["recommendedQueries"]), toStringSlice(payload["followUpQueries"])...))
	return ResearchMemoryEpisodeInput{
		UserID:             userID,
		ProjectID:          projectID,
		Query:              query,
		Scope:              projectScopeOrUser(projectID),
		Summary:            strings.TrimSpace(firstNonEmpty(AsOptionalString(payload["summary"]), AsOptionalString(payload["sessionSummary"]))),
		AcceptedFindings:   accepted,
		UnresolvedGaps:     toStringSlice(payload["unresolvedGaps"]),
		RecommendedQueries: recommended,
		ReusableStrategies: toStringSlice(payload["trustedSources"]),
	}
}

func projectScopeOrUser(projectID string) ResearchMemoryScope {
	if strings.TrimSpace(projectID) != "" {
		return ResearchMemoryScopeProject
	}
	return ResearchMemoryScopeUser
}
