package wisdev

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ResearchMemoryLifecycleStatus string

const (
	ResearchMemoryStatusTentative    ResearchMemoryLifecycleStatus = "tentative"
	ResearchMemoryStatusActive       ResearchMemoryLifecycleStatus = "active"
	ResearchMemoryStatusContradicted ResearchMemoryLifecycleStatus = "contradicted"
	ResearchMemoryStatusSuperseded   ResearchMemoryLifecycleStatus = "superseded"
	ResearchMemoryStatusArchived     ResearchMemoryLifecycleStatus = "archived"
)

type ResearchMemoryScope string

const (
	ResearchMemoryScopeUser    ResearchMemoryScope = "user"
	ResearchMemoryScopeProject ResearchMemoryScope = "project"
)

type ResearchMemoryEdgeType string

const (
	ResearchMemoryEdgeContradicts ResearchMemoryEdgeType = "contradicts"
	ResearchMemoryEdgeSupersedes  ResearchMemoryEdgeType = "supersedes"
	ResearchMemoryEdgeAboutTopic  ResearchMemoryEdgeType = "about_topic"
	ResearchMemoryEdgeUsesMethod  ResearchMemoryEdgeType = "uses_method"
	ResearchMemoryEdgeEvaluates   ResearchMemoryEdgeType = "evaluates"
	ResearchMemoryEdgeDependsOn   ResearchMemoryEdgeType = "depends_on"
	ResearchMemoryEdgeFollowUpOf  ResearchMemoryEdgeType = "follow_up_of"
)

type ResearchMemoryProvenance struct {
	SourceID          string  `json:"sourceId,omitempty"`
	CanonicalSourceID string  `json:"canonicalSourceId,omitempty"`
	PaperTitle        string  `json:"paperTitle,omitempty"`
	Snippet           string  `json:"snippet,omitempty"`
	Query             string  `json:"query,omitempty"`
	Support           string  `json:"support,omitempty"`
	Confidence        float64 `json:"confidence,omitempty"`
	CreatedAt         int64   `json:"createdAt"`
}

type ResearchMemoryRecord struct {
	ID                    string                        `json:"id"`
	CanonicalKey          string                        `json:"canonicalKey"`
	Scope                 ResearchMemoryScope           `json:"scope"`
	UserID                string                        `json:"userId,omitempty"`
	ProjectID             string                        `json:"projectId,omitempty"`
	Query                 string                        `json:"query,omitempty"`
	Claim                 string                        `json:"claim"`
	NormalizedClaim       string                        `json:"normalizedClaim"`
	Subject               string                        `json:"subject,omitempty"`
	Topics                []string                      `json:"topics,omitempty"`
	Methods               []string                      `json:"methods,omitempty"`
	Confidence            float64                       `json:"confidence"`
	SupportCount          int                           `json:"supportCount"`
	ContradictionCount    int                           `json:"contradictionCount"`
	LastConfirmedAt       int64                         `json:"lastConfirmedAt"`
	FreshnessHalfLifeDays int                           `json:"freshnessHalfLifeDays"`
	LifecycleStatus       ResearchMemoryLifecycleStatus `json:"lifecycleStatus"`
	SupersededBy          string                        `json:"supersededBy,omitempty"`
	Provenance            []ResearchMemoryProvenance    `json:"provenance,omitempty"`
	Metadata              map[string]any                `json:"metadata,omitempty"`
	CreatedAt             int64                         `json:"createdAt"`
	UpdatedAt             int64                         `json:"updatedAt"`
}

type ResearchMemoryEpisode struct {
	ID                     string              `json:"id"`
	Scope                  ResearchMemoryScope `json:"scope"`
	UserID                 string              `json:"userId,omitempty"`
	ProjectID              string              `json:"projectId,omitempty"`
	Query                  string              `json:"query"`
	Summary                string              `json:"summary"`
	AcceptedFindings       []string            `json:"acceptedFindings,omitempty"`
	Contradictions         []string            `json:"contradictions,omitempty"`
	UnresolvedGaps         []string            `json:"unresolvedGaps,omitempty"`
	RecommendedNextQueries []string            `json:"recommendedNextQueries,omitempty"`
	ReusableStrategies     []string            `json:"reusableStrategies,omitempty"`
	Metadata               map[string]any      `json:"metadata,omitempty"` // NEW — judge score, outcome, factors
	CreatedAt              int64               `json:"createdAt"`
}

type ResearchProcedureMemory struct {
	ID               string              `json:"id"`
	Scope            ResearchMemoryScope `json:"scope"`
	UserID           string              `json:"userId,omitempty"`
	ProjectID        string              `json:"projectId,omitempty"`
	Label            string              `json:"label"`
	PreferredSources []string            `json:"preferredSources,omitempty"`
	Confidence       float64             `json:"confidence"`
	Uses             int                 `json:"uses"`
	UpdatedAt        int64               `json:"updatedAt"`

	// NEW — ReasoningBank strategic primitive fields
	Content             string   `json:"content,omitempty"`
	ApplicableWhen      string   `json:"applicableWhen,omitempty"`
	QueryPatterns       []string `json:"queryPatterns,omitempty"`
	DomainHints         []string `json:"domainHints,omitempty"`
	SuccessRate         float64  `json:"successRate,omitempty"`
	SourceTrajectoryIDs []string `json:"sourceTrajectoryIds,omitempty"`
}

type TrajectoryOutcome string

const (
	TrajectoryOutcomeSuccess TrajectoryOutcome = "success"
	TrajectoryOutcomePartial TrajectoryOutcome = "partial"
	TrajectoryOutcomeFailure TrajectoryOutcome = "failure"
)

type ExperienceJudgeOutput struct {
	Score          float64            `json:"score"`
	Outcome        TrajectoryOutcome  `json:"outcome"`
	Reasoning      string             `json:"reasoning"`
	SuccessFactors []string           `json:"successFactors,omitempty"`
	FailureFactors []string           `json:"failureFactors,omitempty"`
	Lessons        []ExperienceLesson `json:"lessons,omitempty"`
}

type ExperienceLesson struct {
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	Content        string   `json:"content"`
	ApplicableWhen string   `json:"applicableWhen"`
	QueryPatterns  []string `json:"queryPatterns,omitempty"`
	DomainHints    []string `json:"domainHints,omitempty"`
}

func reasoningBankEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("WISDEV_ENABLE_REASONING_BANK")), "true")
}

func ReasoningBankEnabled() bool {
	return reasoningBankEnabled()
}

type ResearchMemoryEdge struct {
	ID        string                 `json:"id"`
	Type      ResearchMemoryEdgeType `json:"type"`
	FromID    string                 `json:"fromId"`
	ToID      string                 `json:"toId"`
	Weight    float64                `json:"weight,omitempty"`
	Metadata  map[string]any         `json:"metadata,omitempty"`
	CreatedAt int64                  `json:"createdAt"`
}

type ResearchMemoryState struct {
	Records    []ResearchMemoryRecord    `json:"records,omitempty"`
	Episodes   []ResearchMemoryEpisode   `json:"episodes,omitempty"`
	Procedures []ResearchProcedureMemory `json:"procedures,omitempty"`
	Edges      []ResearchMemoryEdge      `json:"edges,omitempty"`
	UpdatedAt  int64                     `json:"updatedAt"`
}

type ResearchMemoryPromotionInput struct {
	UserID             string
	ProjectID          string
	Query              string
	Scope              ResearchMemoryScope
	Findings           []EvidenceFinding
	Contradictions     []ContradictionPair
	UnresolvedGaps     []string
	RecommendedQueries []string
	PreferredSources   []string
}

type ResearchMemoryEpisodeInput struct {
	UserID             string
	ProjectID          string
	Query              string
	Scope              ResearchMemoryScope
	Summary            string
	AcceptedFindings   []string
	Contradictions     []string
	UnresolvedGaps     []string
	RecommendedQueries []string
	ReusableStrategies []string
	Metadata           map[string]any // NEW — optional judge metadata, stored on episode
}

type ResearchMemoryQueryRequest struct {
	UserID                string `json:"userId,omitempty"`
	ProjectID             string `json:"projectId,omitempty"`
	Query                 string `json:"query"`
	Limit                 int    `json:"limit,omitempty"`
	IncludeContradictions bool   `json:"includeContradictions,omitempty"`
}

type ResearchMemoryQueryResponse struct {
	Findings             []ResearchMemoryRecord    `json:"findings"`
	ContradictedFindings []ResearchMemoryRecord    `json:"contradictedFindings,omitempty"`
	RelatedTopics        []string                  `json:"relatedTopics,omitempty"`
	RelatedMethods       []string                  `json:"relatedMethods,omitempty"`
	RecommendedQueries   []string                  `json:"recommendedQueries,omitempty"`
	RelationshipSignals  []map[string]any          `json:"relationshipSignals,omitempty"`
	WorkspaceContext     map[string]any            `json:"workspaceContext,omitempty"`
	TopEpisodes          []ResearchMemoryEpisode   `json:"topEpisodes,omitempty"`
	ProceduralHints      []ResearchProcedureMemory `json:"proceduralHints,omitempty"`
	QuerySummary         string                    `json:"querySummary,omitempty"`
	Metadata             map[string]any            `json:"metadata,omitempty"`
}

type ResearchMemoryRetentionResult struct {
	StatesTouched    int `json:"statesTouched"`
	ArchivedRecords  int `json:"archivedRecords"`
	PrunedRecords    int `json:"prunedRecords"`
	PrunedEpisodes   int `json:"prunedEpisodes"`
	PrunedProcedures int `json:"prunedProcedures"`
	PrunedEdges      int `json:"prunedEdges"`
}

type ResearchMemoryCompiler struct {
	stateStore *RuntimeStateStore
	journal    *RuntimeJournal
}

func NewResearchMemoryCompiler(stateStore *RuntimeStateStore, journal *RuntimeJournal) *ResearchMemoryCompiler {
	return &ResearchMemoryCompiler{stateStore: stateStore, journal: journal}
}

func (c *ResearchMemoryCompiler) PromoteFindings(ctx context.Context, input ResearchMemoryPromotionInput) (*ResearchMemoryState, error) {
	if c == nil || c.stateStore == nil {
		return nil, nil
	}
	query := strings.TrimSpace(input.Query)
	if strings.TrimSpace(input.UserID) == "" || query == "" {
		return nil, nil
	}

	scopes := resolveResearchMemoryScopes(input.Scope, input.ProjectID)
	var lastState *ResearchMemoryState
	for _, scope := range scopes {
		state, err := c.promoteIntoScope(ctx, scope, input)
		if err != nil {
			return nil, err
		}
		lastState = state
	}
	return lastState, nil
}

func (c *ResearchMemoryCompiler) ConsolidateEpisode(ctx context.Context, input ResearchMemoryEpisodeInput) (*ResearchMemoryState, error) {
	if c == nil || c.stateStore == nil {
		return nil, nil
	}
	query := strings.TrimSpace(input.Query)
	if strings.TrimSpace(input.UserID) == "" || query == "" {
		return nil, nil
	}

	scopes := resolveResearchMemoryScopes(input.Scope, input.ProjectID)
	var lastState *ResearchMemoryState
	for _, scope := range scopes {
		projectID := scopedProjectID(scope, input.ProjectID)
		state, err := c.loadState(input.UserID, projectID)
		if err != nil {
			return nil, err
		}
		if state == nil {
			state = &ResearchMemoryState{}
		}
		now := NowMillis()
		episode := ResearchMemoryEpisode{
			ID:                     stableWisDevID("rm-episode", string(scope), input.UserID, projectID, query, fmt.Sprintf("%d", now)),
			Scope:                  scope,
			UserID:                 input.UserID,
			ProjectID:              projectID,
			Query:                  query,
			Summary:                strings.TrimSpace(input.Summary),
			AcceptedFindings:       uniqueStrings(input.AcceptedFindings),
			Contradictions:         uniqueStrings(input.Contradictions),
			UnresolvedGaps:         uniqueStrings(input.UnresolvedGaps),
			RecommendedNextQueries: uniqueStrings(input.RecommendedQueries),
			ReusableStrategies:     uniqueStrings(input.ReusableStrategies),
			CreatedAt:              now,
		}
		if input.Metadata != nil {
			episode.Metadata = input.Metadata
		}
		state.Episodes = prependResearchMemoryEpisode(state.Episodes, episode, 80)
		for _, nextQuery := range uniqueStrings(input.RecommendedQueries) {
			upsertResearchMemoryEdge(&state.Edges, ResearchMemoryEdge{
				ID:        stableWisDevID("rm-edge-follow-up", string(scope), input.UserID, projectID, query, nextQuery),
				Type:      ResearchMemoryEdgeFollowUpOf,
				FromID:    state.Episodes[0].ID,
				ToID:      stableWisDevID("rm-query", normalizeResearchMemoryText(nextQuery)),
				Weight:    0.7,
				Metadata:  map[string]any{"query": nextQuery, "sourceQuery": query},
				CreatedAt: now,
			})
		}
		state.Procedures = mergeResearchProcedures(state.Procedures, buildProceduralHints(scope, input.UserID, projectID, input.ReusableStrategies, query, now))
		state.UpdatedAt = now
		_, normalized := normalizeResearchMemoryState(state, 0, now)
		if err := c.saveState(input.UserID, projectID, state); err != nil {
			return nil, err
		}
		c.syncWorkspaceProjection(input.UserID, projectID, state)
		c.appendAudit(ctx, "research_memory_episode", input.UserID, projectID, query, map[string]any{
			"scope":         string(scope),
			"summary":       input.Summary,
			"gapCount":      len(input.UnresolvedGaps),
			"followUpCount": len(input.RecommendedQueries),
			"normalized":    normalized,
		})
		lastState = state
	}
	return lastState, nil
}

func (c *ResearchMemoryCompiler) QueryExperiencePrimitives(ctx context.Context, userID, query, domain string) ([]ResearchProcedureMemory, []ResearchMemoryEpisode, string) {
	if !reasoningBankEnabled() {
		return nil, nil, ""
	}
	state, err := c.loadState(userID, "")
	if err != nil {
		return nil, nil, ""
	}
	if state == nil {
		return nil, nil, ""
	}

	primitives := make([]ResearchProcedureMemory, 0)
	for _, p := range state.Procedures {
		if p.Content != "" {
			primitives = append(primitives, p)
		}
	}

	rankedPrimitives := rankExperienceProcedures(primitives, query, domain, 5)
	rankedEpisodes := rankResearchEpisodes(state.Episodes, query, 3)

	promptText := buildExperiencePromptBlock(rankedPrimitives, rankedEpisodes)
	return rankedPrimitives, rankedEpisodes, promptText
}

func (c *ResearchMemoryCompiler) MergeExperienceLessons(ctx context.Context, userID, trajectoryID string, lessons []ExperienceLesson) error {
	if !reasoningBankEnabled() || len(lessons) == 0 {
		return nil
	}
	state, err := c.loadState(userID, "")
	if err != nil {
		return err
	}
	if state == nil {
		state = &ResearchMemoryState{}
	}

	now := NowMillis()
	state.Procedures = mergeExperiencePrimitives(state.Procedures, lessons, ResearchMemoryScopeUser, userID, "", trajectoryID, now)
	state.UpdatedAt = now

	if err := c.saveState(userID, "", state); err != nil {
		return err
	}
	c.appendAudit(ctx, "reasoning_bank_lessons", userID, "", trajectoryID, map[string]any{
		"lessonCount": len(lessons),
	})
	return nil
}

func rankResearchProcedures(procedures []ResearchProcedureMemory, limit int) []ResearchProcedureMemory {
	return rankExperienceProcedures(procedures, "", "", limit)
}

func rankExperienceProcedures(procedures []ResearchProcedureMemory, query, domain string, limit int) []ResearchProcedureMemory {
	type scored struct {
		proc  ResearchProcedureMemory
		score float64
	}
	lowerQuery := strings.ToLower(query)
	scoredList := make([]scored, 0, len(procedures))
	for _, proc := range procedures {
		score := proc.Confidence * 1.5
		if len(proc.QueryPatterns) > 0 && lowerQuery != "" {
			for _, pattern := range proc.QueryPatterns {
				if strings.Contains(lowerQuery, strings.ToLower(pattern)) {
					score += 0.3
					break
				}
			}
		}
		if len(proc.DomainHints) > 0 && domain != "" {
			for _, hint := range proc.DomainHints {
				if strings.EqualFold(hint, domain) {
					score += 0.2
					break
				}
			}
		}
		if proc.SuccessRate > 0 {
			score += proc.SuccessRate * 0.15
		}
		scoredList = append(scoredList, scored{proc: proc, score: score})
	}
	sort.SliceStable(scoredList, func(i, j int) bool {
		return scoredList[i].score > scoredList[j].score
	})
	if len(scoredList) > limit {
		scoredList = scoredList[:limit]
	}
	out := make([]ResearchProcedureMemory, 0, len(scoredList))
	for _, item := range scoredList {
		out = append(out, item.proc)
	}
	return out
}

func mergeExperiencePrimitives(existing []ResearchProcedureMemory, lessons []ExperienceLesson, scope ResearchMemoryScope, userID, projectID, sourceTrajectoryID string, now int64) []ResearchProcedureMemory {
	for _, lesson := range lessons {
		found := false
		for i := range existing {
			e := &existing[i]
			if strings.EqualFold(e.Label, lesson.Title) {
				e.Uses++
				e.SuccessRate = (e.SuccessRate*float64(e.Uses-1) + 1.0) / float64(e.Uses)
				e.Confidence = math.Min(0.95, e.Confidence+0.05)
				e.UpdatedAt = now
				e.SourceTrajectoryIDs = uniqueStrings(append(e.SourceTrajectoryIDs, sourceTrajectoryID))
				found = true
				break
			}
		}
		if !found {
			existing = append(existing, ResearchProcedureMemory{
				ID:                  stableWisDevID("rm-primitive", string(scope), userID, lesson.Title),
				Scope:               scope,
				UserID:              userID,
				ProjectID:           projectID,
				Label:               lesson.Title,
				Confidence:          0.5,
				Uses:                1,
				UpdatedAt:           now,
				Content:             lesson.Content,
				ApplicableWhen:      lesson.ApplicableWhen,
				QueryPatterns:       lesson.QueryPatterns,
				DomainHints:         lesson.DomainHints,
				SuccessRate:         1.0,
				SourceTrajectoryIDs: []string{sourceTrajectoryID},
			})
		}
	}
	sort.SliceStable(existing, func(i, j int) bool {
		return (existing[i].Confidence * float64(existing[i].Uses)) > (existing[j].Confidence * float64(existing[j].Uses))
	})
	if len(existing) > 80 {
		existing = existing[:80]
	}
	return existing
}

func buildExperiencePromptBlock(primitives []ResearchProcedureMemory, episodes []ResearchMemoryEpisode) string {
	var sb strings.Builder
	sb.WriteString("### ReasoningBank: Past Experiences & Strategic Primitives\n")
	sb.WriteString("The following lessons and trajectories from past research sessions are provided to guide your current quest.\n\n")

	if len(primitives) > 0 {
		sb.WriteString("#### Strategic Primitives (Distilled Lessons)\n")
		for _, p := range primitives {
			sb.WriteString(fmt.Sprintf("- **%s**: %s\n", p.Label, p.Content))
			sb.WriteString(fmt.Sprintf("  * Use when: %s\n", p.ApplicableWhen))
		}
		sb.WriteString("\n")
	}

	if len(episodes) > 0 {
		sb.WriteString("#### Relevant Past Trajectories\n")
		for _, e := range episodes {
			sb.WriteString(fmt.Sprintf("- **Query**: %s\n", e.Query))
			if e.Summary != "" {
				summary := e.Summary
				if len(summary) > 200 {
					summary = summary[:197] + "..."
				}
				sb.WriteString(fmt.Sprintf("  * Summary: %s\n", summary))
			}
		}
	}

	if len(primitives) == 0 && len(episodes) == 0 {
		return "No relevant past experiences found."
	}

	return sb.String()
}

func (c *ResearchMemoryCompiler) Query(ctx context.Context, req ResearchMemoryQueryRequest) (*ResearchMemoryQueryResponse, error) {
	if c == nil || c.stateStore == nil {
		return &ResearchMemoryQueryResponse{}, nil
	}
	query := strings.TrimSpace(req.Query)
	userID := strings.TrimSpace(req.UserID)
	if userID == "" || query == "" {
		return &ResearchMemoryQueryResponse{}, nil
	}
	limit := boundedResearchMemoryInt(req.Limit, 8, 1, 20)

	userState, err := c.loadState(userID, "")
	if err != nil {
		return nil, err
	}
	projectState, err := c.loadState(userID, strings.TrimSpace(req.ProjectID))
	if err != nil {
		return nil, err
	}
	combined := combineResearchMemoryStates(userState, projectState)
	findings, contradicted := rankResearchMemoryRecords(combined.Records, query, req.IncludeContradictions, limit)
	episodes := rankResearchEpisodes(combined.Episodes, query, 3)
	topics := collectTopResearchTopics(findings, contradicted, 12)
	methods := collectResearchMethods(findings, contradicted, 8)
	recommendedQueries := collectResearchMemoryQueries(combined, findings, 8)
	graphSignals := collectResearchRelationshipSignals(combined, findings, episodes, 8)

	response := &ResearchMemoryQueryResponse{
		Findings:             findings,
		ContradictedFindings: contradicted,
		RelatedTopics:        topics,
		RelatedMethods:       methods,
		RecommendedQueries:   recommendedQueries,
		RelationshipSignals:  graphSignals,
		WorkspaceContext:     buildResearchMemoryWorkspaceContext(req.ProjectID, findings, contradicted, topics, methods, recommendedQueries, episodes, graphSignals),
		TopEpisodes:          episodes,
		ProceduralHints:      rankResearchProcedures(combined.Procedures, 4),
		QuerySummary:         buildResearchMemorySummary(findings, contradicted, recommendedQueries, methods),
		Metadata: map[string]any{
			"recordCount":        len(combined.Records),
			"episodeCount":       len(combined.Episodes),
			"edgeCount":          len(combined.Edges),
			"contradictionCount": len(contradicted),
		},
	}
	c.appendAudit(ctx, "research_memory_query", userID, strings.TrimSpace(req.ProjectID), query, map[string]any{
		"resultCount": len(findings),
		"topics":      stringSliceToAny(topics),
	})
	return response, nil
}

func (c *ResearchMemoryCompiler) EnforceRetention(ctx context.Context, retentionDays int) (*ResearchMemoryRetentionResult, error) {
	if c == nil || c.stateStore == nil || retentionDays <= 0 {
		return &ResearchMemoryRetentionResult{}, nil
	}
	if err := c.stateStore.ensureStorage(); err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(c.stateStore.pathFor("research_memory_*.json"))
	if err != nil {
		return nil, err
	}
	result := &ResearchMemoryRetentionResult{}
	now := NowMillis()
	for _, path := range paths {
		var persisted persistedResearchMemoryState
		if err := c.stateStore.readJSONFile(path, &persisted); err != nil || persisted.State == nil {
			continue
		}
		stats, changed := normalizeResearchMemoryState(persisted.State, retentionDays, now)
		if !changed {
			continue
		}
		persisted.UpdatedAt = now
		if err := c.stateStore.writeJSONFile(path, persisted); err != nil {
			return nil, err
		}
		if strings.TrimSpace(persisted.ProjectID) != "" {
			c.syncWorkspaceProjection(persisted.UserID, persisted.ProjectID, persisted.State)
		}
		result.StatesTouched++
		result.ArchivedRecords += stats.archivedRecords
		result.PrunedRecords += stats.prunedRecords
		result.PrunedEpisodes += stats.prunedEpisodes
		result.PrunedProcedures += stats.prunedProcedures
		result.PrunedEdges += stats.prunedEdges
	}
	c.appendAudit(ctx, "research_memory_retention", "", "", fmt.Sprintf("retention=%d", retentionDays), map[string]any{
		"retentionDays":    retentionDays,
		"statesTouched":    result.StatesTouched,
		"archivedRecords":  result.ArchivedRecords,
		"prunedRecords":    result.PrunedRecords,
		"prunedEpisodes":   result.PrunedEpisodes,
		"prunedProcedures": result.PrunedProcedures,
		"prunedEdges":      result.PrunedEdges,
	})
	return result, nil
}

func (c *ResearchMemoryCompiler) ConsolidateDossierPayload(ctx context.Context, userID, projectID string, dossier map[string]any, preferredSources []string) error {
	if c == nil || len(dossier) == 0 {
		return nil
	}
	query := strings.TrimSpace(AsOptionalString(dossier["query"]))
	if strings.TrimSpace(userID) == "" || query == "" {
		return nil
	}
	findings := findingsFromDossierPayload(dossier)
	contradictions := contradictionsFromDossierPayload(dossier)
	gaps := toStringSlice(dossier["gaps"])
	recommendedQueries := toStringSlice(firstNonEmptyValue(dossier["recommendedNextRetrievalActions"], dossier["recommendedQueries"]))
	if _, err := c.PromoteFindings(ctx, ResearchMemoryPromotionInput{
		UserID:             userID,
		ProjectID:          projectID,
		Query:              query,
		Scope:              ResearchMemoryScopeProject,
		Findings:           findings,
		Contradictions:     contradictions,
		UnresolvedGaps:     gaps,
		RecommendedQueries: recommendedQueries,
		PreferredSources:   preferredSources,
	}); err != nil {
		return err
	}
	_, err := c.ConsolidateEpisode(ctx, ResearchMemoryEpisodeInput{
		UserID:             userID,
		ProjectID:          projectID,
		Query:              query,
		Scope:              ResearchMemoryScopeProject,
		Summary:            crystallizeDossierSummary(query, findings, gaps),
		AcceptedFindings:   findingClaims(findings),
		Contradictions:     contradictionSummaries(contradictions),
		UnresolvedGaps:     gaps,
		RecommendedQueries: recommendedQueries,
		ReusableStrategies: preferredSources,
	})
	return err
}

func (c *ResearchMemoryCompiler) promoteIntoScope(ctx context.Context, scope ResearchMemoryScope, input ResearchMemoryPromotionInput) (*ResearchMemoryState, error) {
	state, err := c.loadState(input.UserID, scopedProjectID(scope, input.ProjectID))
	if err != nil {
		return nil, err
	}
	if state == nil {
		state = &ResearchMemoryState{}
	}
	now := NowMillis()
	recordsByKey := make(map[string]*ResearchMemoryRecord, len(state.Records))
	for i := range state.Records {
		recordsByKey[state.Records[i].CanonicalKey] = &state.Records[i]
	}

	for _, finding := range input.Findings {
		claim := strings.TrimSpace(firstNonEmpty(finding.Claim, finding.Snippet))
		if claim == "" {
			continue
		}
		normalizedClaim := normalizeResearchMemoryText(claim)
		if normalizedClaim == "" {
			continue
		}
		topics := uniqueStrings(append(deriveResearchMemoryTopics(input.Query), finding.Keywords...))
		canonicalKey := stableWisDevID("research-memory", string(scope), scopedProjectID(scope, input.ProjectID), normalizedClaim)
		record := recordsByKey[canonicalKey]
		if record == nil {
			state.Records = append(state.Records, ResearchMemoryRecord{
				ID:                    stableWisDevID("rm-record", input.UserID, scopedProjectID(scope, input.ProjectID), normalizedClaim),
				CanonicalKey:          canonicalKey,
				Scope:                 scope,
				UserID:                strings.TrimSpace(input.UserID),
				ProjectID:             scopedProjectID(scope, input.ProjectID),
				Query:                 input.Query,
				Claim:                 claim,
				NormalizedClaim:       normalizedClaim,
				Subject:               inferResearchMemorySubject(topics, claim),
				Topics:                topics,
				Methods:               inferResearchMemoryMethods(topics),
				Confidence:            ClampFloat(finding.Confidence, 0.2, 0.98),
				LastConfirmedAt:       now,
				FreshnessHalfLifeDays: 60,
				LifecycleStatus:       resolveResearchMemoryStatus(finding),
				Metadata:              map[string]any{"status": finding.Status},
				CreatedAt:             now,
				UpdatedAt:             now,
			})
			record = &state.Records[len(state.Records)-1]
			recordsByKey[canonicalKey] = record
		}
		if appendResearchMemoryProvenance(record, ResearchMemoryProvenance{
			SourceID:          strings.TrimSpace(finding.SourceID),
			CanonicalSourceID: strings.TrimSpace(finding.SourceID),
			PaperTitle:        strings.TrimSpace(finding.PaperTitle),
			Snippet:           strings.TrimSpace(finding.Snippet),
			Query:             input.Query,
			Support:           strings.TrimSpace(finding.Status),
			Confidence:        ClampFloat(finding.Confidence, 0.0, 1.0),
			CreatedAt:         now,
		}) {
			record.SupportCount++
		}
		record.Confidence = nextResearchMemoryConfidence(record.Confidence, finding.Confidence, record.SupportCount)
		record.LastConfirmedAt = now
		record.UpdatedAt = now
		record.Topics = uniqueStrings(append(record.Topics, topics...))
		record.Methods = uniqueStrings(append(record.Methods, inferResearchMemoryMethods(topics)...))
		if record.LifecycleStatus == ResearchMemoryStatusTentative && finding.Confidence >= 0.65 {
			record.LifecycleStatus = ResearchMemoryStatusActive
		}
		for _, topic := range topics {
			upsertResearchMemoryEdge(&state.Edges, ResearchMemoryEdge{
				ID:        stableWisDevID("rm-edge-topic", record.ID, topic),
				Type:      ResearchMemoryEdgeAboutTopic,
				FromID:    record.ID,
				ToID:      stableWisDevID("rm-topic", topic),
				Weight:    ClampFloat(record.Confidence, 0.2, 1.0),
				Metadata:  map[string]any{"topic": topic},
				CreatedAt: now,
			})
		}
		for _, method := range record.Methods {
			upsertResearchMemoryEdge(&state.Edges, ResearchMemoryEdge{
				ID:        stableWisDevID("rm-edge-method", record.ID, method),
				Type:      ResearchMemoryEdgeUsesMethod,
				FromID:    record.ID,
				ToID:      stableWisDevID("rm-method", method),
				Weight:    ClampFloat(record.Confidence, 0.2, 1.0),
				Metadata:  map[string]any{"method": method},
				CreatedAt: now,
			})
		}
		if sourceID := strings.TrimSpace(firstNonEmpty(finding.SourceID, record.ProvenanceKey())); sourceID != "" {
			upsertResearchMemoryEdge(&state.Edges, ResearchMemoryEdge{
				ID:        stableWisDevID("rm-edge-source", record.ID, sourceID),
				Type:      ResearchMemoryEdgeDependsOn,
				FromID:    record.ID,
				ToID:      stableWisDevID("rm-source", sourceID),
				Weight:    ClampFloat(record.Confidence, 0.2, 1.0),
				Metadata:  map[string]any{"sourceId": sourceID},
				CreatedAt: now,
			})
		}
		if topic := inferResearchMemoryEvaluationTarget(input.Query, claim, topics); topic != "" {
			upsertResearchMemoryEdge(&state.Edges, ResearchMemoryEdge{
				ID:        stableWisDevID("rm-edge-evaluates", record.ID, topic),
				Type:      ResearchMemoryEdgeEvaluates,
				FromID:    record.ID,
				ToID:      stableWisDevID("rm-topic", topic),
				Weight:    ClampFloat(record.Confidence, 0.2, 1.0),
				Metadata:  map[string]any{"topic": topic},
				CreatedAt: now,
			})
		}
	}

	applyResearchMemoryContradictions(state, input.Contradictions, now)
	applyResearchMemorySupersession(state, now)
	for _, nextQuery := range uniqueStrings(input.RecommendedQueries) {
		for _, record := range state.Records {
			if strings.TrimSpace(record.Query) != strings.TrimSpace(input.Query) {
				continue
			}
			upsertResearchMemoryEdge(&state.Edges, ResearchMemoryEdge{
				ID:        stableWisDevID("rm-edge-record-follow-up", record.ID, nextQuery),
				Type:      ResearchMemoryEdgeFollowUpOf,
				FromID:    record.ID,
				ToID:      stableWisDevID("rm-query", normalizeResearchMemoryText(nextQuery)),
				Weight:    ClampFloat(record.Confidence, 0.3, 1.0),
				Metadata:  map[string]any{"query": nextQuery, "sourceQuery": input.Query},
				CreatedAt: now,
			})
		}
	}
	state.Procedures = mergeResearchProcedures(state.Procedures, buildProceduralHints(scope, input.UserID, scopedProjectID(scope, input.ProjectID), input.PreferredSources, input.Query, now))
	state.UpdatedAt = now
	_, normalized := normalizeResearchMemoryState(state, 0, now)

	if err := c.saveState(input.UserID, scopedProjectID(scope, input.ProjectID), state); err != nil {
		return nil, err
	}
	c.syncWorkspaceProjection(input.UserID, scopedProjectID(scope, input.ProjectID), state)
	c.appendAudit(ctx, "research_memory_promoted", input.UserID, scopedProjectID(scope, input.ProjectID), input.Query, map[string]any{
		"scope":            string(scope),
		"promotedCount":    len(input.Findings),
		"recordCount":      len(state.Records),
		"preferredSources": stringSliceToAny(input.PreferredSources),
		"normalized":       normalized,
	})
	return state, nil
}

func (c *ResearchMemoryCompiler) loadState(userID, projectID string) (*ResearchMemoryState, error) {
	if c == nil || c.stateStore == nil {
		return &ResearchMemoryState{}, nil
	}
	return c.stateStore.LoadResearchMemoryState(userID, projectID)
}

func (c *ResearchMemoryCompiler) saveState(userID, projectID string, state *ResearchMemoryState) error {
	if c == nil || c.stateStore == nil || state == nil {
		return nil
	}
	return c.stateStore.SaveResearchMemoryState(userID, projectID, state)
}

func (c *ResearchMemoryCompiler) syncWorkspaceProjection(userID, projectID string, state *ResearchMemoryState) {
	if c == nil || c.stateStore == nil || state == nil || strings.TrimSpace(projectID) == "" {
		return
	}
	workspace := map[string]any{
		"projectId":             projectID,
		"trustedSourceClusters": stringSliceToAny(collectResearchMemoryQueries(state, state.Records, 12)),
		"unresolvedGaps":        stringSliceToAny(collectEpisodeGaps(state.Episodes, 12)),
		"followUpQueries":       stringSliceToAny(collectResearchMemoryQueries(state, state.Records, 12)),
		"savedDossiers":         buildWorkspaceArtifactsFromEpisodes(state.Episodes, "dossier"),
		"savedTrajectories":     buildWorkspaceArtifactsFromEpisodes(state.Episodes, "trajectory"),
		"acceptedSeedPapers":    buildWorkspaceAcceptedSeedPapers(state.Records),
	}
	_ = c.stateStore.SaveProjectWorkspace(userID, projectID, workspace)
}

func (c *ResearchMemoryCompiler) appendAudit(ctx context.Context, eventType, userID, projectID, query string, payload map[string]any) {
	if c == nil || c.journal == nil {
		return
	}
	_ = ctx
	c.journal.Append(RuntimeJournalEntry{
		EventID:   NewTraceID(),
		TraceID:   NewTraceID(),
		SessionID: strings.TrimSpace(projectID),
		UserID:    strings.TrimSpace(userID),
		EventType: eventType,
		Path:      "/research/memory",
		Status:    "ok",
		CreatedAt: time.Now().UnixMilli(),
		Summary:   fmt.Sprintf("%s: %s", eventType, query),
		Payload:   cloneAnyMap(payload),
		Metadata:  map[string]any{"query": query},
	})
}

func resolveResearchMemoryScopes(scope ResearchMemoryScope, projectID string) []ResearchMemoryScope {
	if strings.TrimSpace(projectID) != "" {
		return []ResearchMemoryScope{ResearchMemoryScopeProject, ResearchMemoryScopeUser}
	}
	if scope == ResearchMemoryScopeProject {
		return []ResearchMemoryScope{ResearchMemoryScopeUser}
	}
	return []ResearchMemoryScope{ResearchMemoryScopeUser}
}

func scopedProjectID(scope ResearchMemoryScope, projectID string) string {
	if scope == ResearchMemoryScopeProject {
		return strings.TrimSpace(projectID)
	}
	return ""
}

func normalizeResearchMemoryText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(",", " ", ".", " ", ";", " ", ":", " ", "\n", " ", "\t", " ")
	return strings.Join(strings.Fields(replacer.Replace(value)), " ")
}

func deriveResearchMemoryTopics(query string) []string {
	terms := strings.Fields(normalizeResearchMemoryText(query))
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		if len(term) < 4 {
			continue
		}
		out = append(out, term)
		if len(out) >= 10 {
			break
		}
	}
	return uniqueStrings(out)
}

func inferResearchMemoryMethods(topics []string) []string {
	methods := make([]string, 0, len(topics))
	for _, topic := range topics {
		switch topic {
		case "benchmark", "evaluation", "retrieval", "rag", "survey", "review", "experiment":
			methods = append(methods, topic)
		}
	}
	return uniqueStrings(methods)
}

func inferResearchMemorySubject(topics []string, claim string) string {
	if len(topics) > 0 {
		return topics[0]
	}
	parts := strings.Fields(normalizeResearchMemoryText(claim))
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func resolveResearchMemoryStatus(finding EvidenceFinding) ResearchMemoryLifecycleStatus {
	switch strings.ToLower(strings.TrimSpace(finding.Status)) {
	case "contradicted", "contradictory":
		return ResearchMemoryStatusContradicted
	case "tentative", "needs_review":
		return ResearchMemoryStatusTentative
	default:
		if finding.Confidence >= 0.65 {
			return ResearchMemoryStatusActive
		}
		return ResearchMemoryStatusTentative
	}
}

func appendResearchMemoryProvenance(record *ResearchMemoryRecord, provenance ResearchMemoryProvenance) bool {
	for _, existing := range record.Provenance {
		if existing.SourceID == provenance.SourceID && existing.Snippet == provenance.Snippet && existing.Query == provenance.Query {
			return false
		}
	}
	record.Provenance = append(record.Provenance, provenance)
	return true
}

func nextResearchMemoryConfidence(existing float64, incoming float64, supportCount int) float64 {
	incoming = ClampFloat(incoming, 0.0, 1.0)
	boost := 0.04 * float64(MinInt(MaxInt(supportCount-1, 0), 5))
	return ClampFloat(maxResearchMemoryFloat(existing, incoming)+boost, 0.2, 0.98)
}

func maxResearchMemoryFloat(a float64, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func boundedResearchMemoryInt(val int, def int, min int, max int) int {
	if val <= 0 {
		return def
	}
	if val < min {
		return min
	}
	if val > max {
		return max
	}
	return val
}

func applyResearchMemoryContradictions(state *ResearchMemoryState, contradictions []ContradictionPair, now int64) {
	if state == nil {
		return
	}
	byClaim := make(map[string]*ResearchMemoryRecord, len(state.Records))
	for i := range state.Records {
		byClaim[normalizeResearchMemoryText(state.Records[i].Claim)] = &state.Records[i]
	}
	for _, contradiction := range contradictions {
		left := byClaim[normalizeResearchMemoryText(firstNonEmpty(contradiction.FindingA.Claim, contradiction.FindingA.Snippet))]
		right := byClaim[normalizeResearchMemoryText(firstNonEmpty(contradiction.FindingB.Claim, contradiction.FindingB.Snippet))]
		if left == nil || right == nil {
			continue
		}
		left.LifecycleStatus = ResearchMemoryStatusContradicted
		right.LifecycleStatus = ResearchMemoryStatusContradicted
		left.ContradictionCount++
		right.ContradictionCount++
		left.UpdatedAt = now
		right.UpdatedAt = now
		upsertResearchMemoryEdge(&state.Edges, ResearchMemoryEdge{
			ID:        stableWisDevID("rm-edge-contradiction", left.ID, right.ID),
			Type:      ResearchMemoryEdgeContradicts,
			FromID:    left.ID,
			ToID:      right.ID,
			Weight:    1,
			Metadata:  map[string]any{"explanation": contradiction.Explanation, "severity": string(contradiction.Severity)},
			CreatedAt: now,
		})
	}
}

func applyResearchMemorySupersession(state *ResearchMemoryState, now int64) {
	if state == nil {
		return
	}
	for i := range state.Records {
		for j := range state.Records {
			if i == j {
				continue
			}
			left := &state.Records[i]
			right := &state.Records[j]
			if left.Subject == "" || left.Subject != right.Subject || left.Scope != right.Scope {
				continue
			}
			if left.Confidence > right.Confidence+0.12 && left.LastConfirmedAt >= right.LastConfirmedAt && right.LifecycleStatus == ResearchMemoryStatusActive {
				right.LifecycleStatus = ResearchMemoryStatusSuperseded
				right.SupersededBy = left.ID
				right.UpdatedAt = now
				upsertResearchMemoryEdge(&state.Edges, ResearchMemoryEdge{
					ID:        stableWisDevID("rm-edge-supersede", left.ID, right.ID),
					Type:      ResearchMemoryEdgeSupersedes,
					FromID:    left.ID,
					ToID:      right.ID,
					Weight:    ClampFloat(left.Confidence, 0.3, 1.0),
					CreatedAt: now,
				})
			}
		}
	}
}

func buildProceduralHints(scope ResearchMemoryScope, userID, projectID string, sources []string, query string, now int64) []ResearchProcedureMemory {
	sources = uniqueStrings(sources)
	if len(sources) == 0 {
		return nil
	}
	return []ResearchProcedureMemory{{
		ID:               stableWisDevID("rm-procedure", string(scope), userID, projectID, strings.Join(sources, "|")),
		Scope:            scope,
		UserID:           userID,
		ProjectID:        projectID,
		Label:            fmt.Sprintf("Reuse retrieval mix for %s", firstNonEmpty(query, projectID, userID)),
		PreferredSources: sources,
		Confidence:       0.65,
		Uses:             1,
		UpdatedAt:        now,
	}}
}

func mergeResearchProcedures(existing []ResearchProcedureMemory, incoming []ResearchProcedureMemory) []ResearchProcedureMemory {
	if len(incoming) == 0 {
		return existing
	}
	byID := make(map[string]*ResearchProcedureMemory, len(existing))
	for i := range existing {
		byID[existing[i].ID] = &existing[i]
	}
	for _, candidate := range incoming {
		if current := byID[candidate.ID]; current != nil {
			current.Uses++
			current.UpdatedAt = candidate.UpdatedAt
			current.Confidence = ClampFloat(current.Confidence+0.05, 0.2, 0.98)
			current.PreferredSources = uniqueStrings(append(current.PreferredSources, candidate.PreferredSources...))
			continue
		}
		existing = append(existing, candidate)
	}
	sort.SliceStable(existing, func(i, j int) bool {
		if existing[i].Confidence == existing[j].Confidence {
			return existing[i].Uses > existing[j].Uses
		}
		return existing[i].Confidence > existing[j].Confidence
	})
	if len(existing) > 40 {
		existing = existing[:40]
	}
	return existing
}

func prependResearchMemoryEpisode(existing []ResearchMemoryEpisode, episode ResearchMemoryEpisode, limit int) []ResearchMemoryEpisode {
	existing = append([]ResearchMemoryEpisode{episode}, existing...)
	if len(existing) > limit {
		existing = existing[:limit]
	}
	return existing
}

func combineResearchMemoryStates(states ...*ResearchMemoryState) *ResearchMemoryState {
	combined := &ResearchMemoryState{}
	recordIndexByKey := map[string]int{}
	seenEpisodes := map[string]struct{}{}
	seenProcedures := map[string]struct{}{}
	seenEdges := map[string]struct{}{}
	for _, state := range states {
		if state == nil {
			continue
		}
		for _, record := range state.Records {
			if idx, ok := recordIndexByKey[record.NormalizedClaim]; ok {
				combined.Records[idx] = mergeResearchMemoryRecordVariants(combined.Records[idx], record)
				continue
			}
			recordIndexByKey[record.NormalizedClaim] = len(combined.Records)
			combined.Records = append(combined.Records, record)
		}
		for _, episode := range state.Episodes {
			if _, ok := seenEpisodes[episode.ID]; ok {
				continue
			}
			seenEpisodes[episode.ID] = struct{}{}
			combined.Episodes = append(combined.Episodes, episode)
		}
		for _, procedure := range state.Procedures {
			if _, ok := seenProcedures[procedure.ID]; ok {
				continue
			}
			seenProcedures[procedure.ID] = struct{}{}
			combined.Procedures = append(combined.Procedures, procedure)
		}
		for _, edge := range state.Edges {
			if _, ok := seenEdges[edge.ID]; ok {
				continue
			}
			seenEdges[edge.ID] = struct{}{}
			combined.Edges = append(combined.Edges, edge)
		}
		if state.UpdatedAt > combined.UpdatedAt {
			combined.UpdatedAt = state.UpdatedAt
		}
	}
	return combined
}

func mergeResearchMemoryRecordVariants(existing ResearchMemoryRecord, candidate ResearchMemoryRecord) ResearchMemoryRecord {
	if candidate.Scope == ResearchMemoryScopeProject && existing.Scope != ResearchMemoryScopeProject {
		existing, candidate = candidate, existing
	}
	existing.Topics = uniqueStrings(append(existing.Topics, candidate.Topics...))
	existing.Methods = uniqueStrings(append(existing.Methods, candidate.Methods...))
	existing.Provenance = mergeResearchMemoryProvenance(existing.Provenance, candidate.Provenance)
	existing.SupportCount = MaxInt(existing.SupportCount, candidate.SupportCount)
	existing.ContradictionCount = MaxInt(existing.ContradictionCount, candidate.ContradictionCount)
	existing.Confidence = ClampFloat(maxResearchMemoryFloat(existing.Confidence, candidate.Confidence), 0.0, 1.0)
	existing.LastConfirmedAt = maxResearchMemoryInt64(existing.LastConfirmedAt, candidate.LastConfirmedAt)
	existing.CreatedAt = minResearchMemoryInt64(existing.CreatedAt, candidate.CreatedAt)
	existing.UpdatedAt = maxResearchMemoryInt64(existing.UpdatedAt, candidate.UpdatedAt)
	if existing.Query == "" {
		existing.Query = candidate.Query
	}
	if existing.Subject == "" {
		existing.Subject = candidate.Subject
	}
	if existing.ProjectID == "" {
		existing.ProjectID = candidate.ProjectID
	}
	if existing.UserID == "" {
		existing.UserID = candidate.UserID
	}
	if existing.SupersededBy == "" {
		existing.SupersededBy = candidate.SupersededBy
	}
	existing.Metadata = mergeResearchMemoryMetadata(existing.Metadata, candidate.Metadata)
	existing.LifecycleStatus = mergeResearchMemoryLifecycleStatus(existing.LifecycleStatus, candidate.LifecycleStatus)
	return existing
}

func mergeResearchMemoryProvenance(existing []ResearchMemoryProvenance, candidate []ResearchMemoryProvenance) []ResearchMemoryProvenance {
	seen := make(map[string]struct{}, len(existing))
	for _, item := range existing {
		seen[researchMemoryProvenanceKey(item)] = struct{}{}
	}
	for _, item := range candidate {
		key := researchMemoryProvenanceKey(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		existing = append(existing, item)
	}
	return existing
}

func researchMemoryProvenanceKey(item ResearchMemoryProvenance) string {
	return strings.Join([]string{
		strings.TrimSpace(item.SourceID),
		strings.TrimSpace(item.CanonicalSourceID),
		strings.TrimSpace(item.PaperTitle),
		normalizeResearchMemoryText(item.Snippet),
	}, "|")
}

func mergeResearchMemoryMetadata(existing map[string]any, candidate map[string]any) map[string]any {
	if len(existing) == 0 && len(candidate) == 0 {
		return nil
	}
	out := cloneAnyMap(existing)
	if out == nil {
		out = map[string]any{}
	}
	for key, value := range candidate {
		if _, ok := out[key]; !ok {
			out[key] = value
		}
	}
	return out
}

func mergeResearchMemoryLifecycleStatus(existing ResearchMemoryLifecycleStatus, candidate ResearchMemoryLifecycleStatus) ResearchMemoryLifecycleStatus {
	priority := map[ResearchMemoryLifecycleStatus]int{
		ResearchMemoryStatusContradicted: 4,
		ResearchMemoryStatusSuperseded:   3,
		ResearchMemoryStatusActive:       2,
		ResearchMemoryStatusTentative:    1,
		ResearchMemoryStatusArchived:     0,
	}
	if priority[candidate] > priority[existing] {
		return candidate
	}
	return existing
}

func minResearchMemoryInt64(a int64, b int64) int64 {
	if a == 0 {
		return b
	}
	if b == 0 || a < b {
		return a
	}
	return b
}

func maxResearchMemoryInt64(a int64, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func rankResearchMemoryRecords(records []ResearchMemoryRecord, query string, includeContradictions bool, limit int) ([]ResearchMemoryRecord, []ResearchMemoryRecord) {
	type ranked struct {
		record ResearchMemoryRecord
		score  float64
	}
	active := make([]ranked, 0)
	conflicts := make([]ranked, 0)
	for _, record := range records {
		score := researchMemoryScore(record, query)
		if score <= 0 || record.LifecycleStatus == ResearchMemoryStatusArchived {
			continue
		}
		if record.LifecycleStatus == ResearchMemoryStatusContradicted {
			if includeContradictions {
				conflicts = append(conflicts, ranked{record: record, score: score})
			}
			continue
		}
		if record.LifecycleStatus == ResearchMemoryStatusSuperseded {
			score *= 0.45
		}
		active = append(active, ranked{record: record, score: score})
	}
	sort.SliceStable(active, func(i, j int) bool { return active[i].score > active[j].score })
	sort.SliceStable(conflicts, func(i, j int) bool { return conflicts[i].score > conflicts[j].score })
	if len(active) > limit {
		active = active[:limit]
	}
	if len(conflicts) > MinInt(limit, 4) {
		conflicts = conflicts[:MinInt(limit, 4)]
	}
	top := make([]ResearchMemoryRecord, 0, len(active))
	for _, item := range active {
		top = append(top, item.record)
	}
	contra := make([]ResearchMemoryRecord, 0, len(conflicts))
	for _, item := range conflicts {
		contra = append(contra, item.record)
	}
	return top, contra
}

func researchMemoryScore(record ResearchMemoryRecord, query string) float64 {
	queryTerms := strings.Fields(normalizeResearchMemoryText(query))
	if len(queryTerms) == 0 {
		return 0
	}
	score := record.Confidence * 2
	blob := normalizeResearchMemoryText(strings.Join(append([]string{record.Claim, record.Subject}, record.Topics...), " "))
	for _, term := range queryTerms {
		if len(term) < 3 {
			continue
		}
		if strings.Contains(blob, term) {
			score += 0.75
		}
	}
	if record.Scope == ResearchMemoryScopeProject {
		score += 0.9
	}
	score *= researchMemoryFreshnessWeight(record, NowMillis())
	if record.LifecycleStatus == ResearchMemoryStatusTentative {
		score *= 0.8
	}
	if record.LifecycleStatus == ResearchMemoryStatusArchived {
		score *= 0.1
	}
	if record.SupportCount > 1 {
		score += float64(MinInt(record.SupportCount, 5)) * 0.15
	}
	return score
}

func researchMemoryFreshnessWeight(record ResearchMemoryRecord, now int64) float64 {
	ageDays := float64(MaxInt(0, int((now-record.LastConfirmedAt)/(24*60*60*1000))))
	halfLife := float64(MaxInt(record.FreshnessHalfLifeDays, 30))
	weight := 1.0 / (1.0 + ageDays/halfLife)
	if ageDays > halfLife*2 {
		weight *= 0.7
	}
	return ClampFloat(weight, 0.1, 1.0)
}

func collectResearchMethods(findings []ResearchMemoryRecord, contradicted []ResearchMemoryRecord, limit int) []string {
	counts := map[string]int{}
	for _, record := range append(append([]ResearchMemoryRecord(nil), findings...), contradicted...) {
		for _, method := range record.Methods {
			counts[method]++
		}
	}
	type item struct {
		label string
		count int
	}
	items := make([]item, 0, len(counts))
	for label, count := range counts {
		items = append(items, item{label: label, count: count})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].count > items[j].count })
	out := make([]string, 0, MinInt(limit, len(items)))
	for _, item := range items {
		out = append(out, item.label)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func collectResearchRelationshipSignals(state *ResearchMemoryState, findings []ResearchMemoryRecord, episodes []ResearchMemoryEpisode, limit int) []map[string]any {
	if state == nil || len(state.Edges) == 0 {
		return nil
	}
	recordIDs := make(map[string]struct{}, len(findings))
	for _, record := range findings {
		recordIDs[record.ID] = struct{}{}
	}
	episodeIDs := make(map[string]struct{}, len(episodes))
	for _, episode := range episodes {
		episodeIDs[episode.ID] = struct{}{}
	}
	out := make([]map[string]any, 0, limit)
	for _, edge := range state.Edges {
		if _, ok := recordIDs[edge.FromID]; !ok {
			if _, ok := episodeIDs[edge.FromID]; !ok {
				continue
			}
		}
		out = append(out, map[string]any{
			"id":     edge.ID,
			"type":   string(edge.Type),
			"fromId": edge.FromID,
			"toId":   edge.ToID,
			"weight": edge.Weight,
			"meta":   cloneAnyMap(edge.Metadata),
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func rankResearchEpisodes(episodes []ResearchMemoryEpisode, query string, limit int) []ResearchMemoryEpisode {
	type ranked struct {
		episode ResearchMemoryEpisode
		score   float64
	}
	queryNorm := normalizeResearchMemoryText(query)
	rankedEpisodes := make([]ranked, 0, len(episodes))
	for _, episode := range episodes {
		blob := normalizeResearchMemoryText(strings.Join([]string{
			episode.Query,
			episode.Summary,
			strings.Join(episode.AcceptedFindings, " "),
			strings.Join(episode.UnresolvedGaps, " "),
		}, " "))
		score := 0.2
		for _, term := range strings.Fields(queryNorm) {
			if len(term) < 3 {
				continue
			}
			if strings.Contains(blob, term) {
				score += 0.6
			}
		}
		if episode.Scope == ResearchMemoryScopeProject {
			score += 0.8
		}
		rankedEpisodes = append(rankedEpisodes, ranked{episode: episode, score: score})
	}
	sort.SliceStable(rankedEpisodes, func(i, j int) bool { return rankedEpisodes[i].score > rankedEpisodes[j].score })
	if len(rankedEpisodes) > limit {
		rankedEpisodes = rankedEpisodes[:limit]
	}
	out := make([]ResearchMemoryEpisode, 0, len(rankedEpisodes))
	for _, item := range rankedEpisodes {
		out = append(out, item.episode)
	}
	return out
}

func collectTopResearchTopics(findings []ResearchMemoryRecord, contradicted []ResearchMemoryRecord, limit int) []string {
	counts := map[string]int{}
	for _, record := range append(append([]ResearchMemoryRecord(nil), findings...), contradicted...) {
		for _, topic := range record.Topics {
			counts[topic]++
		}
	}
	type item struct {
		topic string
		count int
	}
	items := make([]item, 0, len(counts))
	for topic, count := range counts {
		items = append(items, item{topic: topic, count: count})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].count > items[j].count })
	out := make([]string, 0, MinInt(limit, len(items)))
	for _, item := range items {
		out = append(out, item.topic)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func collectResearchMemoryQueries(state *ResearchMemoryState, findings []ResearchMemoryRecord, limit int) []string {
	values := make([]string, 0, limit)
	if state != nil {
		for _, episode := range state.Episodes {
			values = append(values, episode.RecommendedNextQueries...)
			values = append(values, episode.UnresolvedGaps...)
		}
		for _, edge := range state.Edges {
			if edge.Type != ResearchMemoryEdgeFollowUpOf || len(edge.Metadata) == 0 {
				continue
			}
			if query := strings.TrimSpace(AsOptionalString(edge.Metadata["query"])); query != "" {
				values = append(values, query)
			}
		}
	}
	for _, record := range findings {
		values = append(values, record.Query)
	}
	values = uniqueStrings(values)
	if len(values) > limit {
		values = values[:limit]
	}
	return values
}

func buildResearchMemoryWorkspaceContext(projectID string, findings []ResearchMemoryRecord, contradicted []ResearchMemoryRecord, topics []string, methods []string, recommended []string, episodes []ResearchMemoryEpisode, relationshipSignals []map[string]any) map[string]any {
	return map[string]any{
		"projectId":             strings.TrimSpace(projectID),
		"trustedSourceClusters": stringSliceToAny(recommended),
		"unresolvedGaps":        stringSliceToAny(flattenEpisodeGaps(episodes, 10)),
		"followUpQueries":       stringSliceToAny(recommended),
		"relatedTopics":         stringSliceToAny(topics),
		"relatedMethods":        stringSliceToAny(methods),
		"relationshipSignals":   relationshipSignals,
		"topFindings":           researchMemoryRecordsToMaps(findings, 5),
		"contradictedFindings":  researchMemoryRecordsToMaps(contradicted, 3),
		"crystallizedEpisodes":  researchMemoryEpisodesToMaps(episodes, 3),
		"semanticKeywords":      stringSliceToAny(topics),
	}
}

func buildResearchMemorySummary(findings []ResearchMemoryRecord, contradicted []ResearchMemoryRecord, recommended []string, methods []string) string {
	if len(findings) == 0 && len(contradicted) == 0 {
		return "No prior research memory matched this query."
	}
	parts := make([]string, 0, 3)
	if len(findings) > 0 {
		parts = append(parts, fmt.Sprintf("%d active prior finding(s)", len(findings)))
	}
	if len(contradicted) > 0 {
		parts = append(parts, fmt.Sprintf("%d contradiction warning(s)", len(contradicted)))
	}
	if len(recommended) > 0 {
		parts = append(parts, fmt.Sprintf("%d follow-up query hint(s)", len(recommended)))
	}
	if len(methods) > 0 {
		parts = append(parts, fmt.Sprintf("%d reusable method cue(s)", len(methods)))
	}
	return strings.Join(parts, " · ")
}

type researchMemoryNormalizeStats struct {
	archivedRecords  int
	prunedRecords    int
	prunedEpisodes   int
	prunedProcedures int
	prunedEdges      int
}

func normalizeResearchMemoryState(state *ResearchMemoryState, retentionDays int, now int64) (researchMemoryNormalizeStats, bool) {
	if state == nil {
		return researchMemoryNormalizeStats{}, false
	}
	stats := researchMemoryNormalizeStats{}
	changed := false
	keptRecords := make([]ResearchMemoryRecord, 0, len(state.Records))
	recordIDs := make(map[string]struct{}, len(state.Records))
	for _, record := range state.Records {
		updated, recordChanged, archiveChange, prune := normalizeResearchMemoryRecord(record, retentionDays, now)
		if prune {
			stats.prunedRecords++
			changed = true
			continue
		}
		if archiveChange {
			stats.archivedRecords++
		}
		if recordChanged {
			changed = true
		}
		keptRecords = append(keptRecords, updated)
		recordIDs[updated.ID] = struct{}{}
	}
	if len(keptRecords) != len(state.Records) {
		changed = true
	}
	state.Records = keptRecords

	episodeCutoff := int64(0)
	if retentionDays > 0 {
		episodeCutoff = now - int64(retentionDays*2)*24*60*60*1000
	}
	keptEpisodes := make([]ResearchMemoryEpisode, 0, len(state.Episodes))
	episodeIDs := make(map[string]struct{}, len(state.Episodes))
	for _, episode := range state.Episodes {
		if episodeCutoff > 0 && episode.CreatedAt > 0 && episode.CreatedAt < episodeCutoff {
			stats.prunedEpisodes++
			changed = true
			continue
		}
		keptEpisodes = append(keptEpisodes, episode)
		episodeIDs[episode.ID] = struct{}{}
	}
	state.Episodes = keptEpisodes

	procedureCutoff := int64(0)
	if retentionDays > 0 {
		procedureCutoff = now - int64(retentionDays)*24*60*60*1000
	}
	keptProcedures := make([]ResearchProcedureMemory, 0, len(state.Procedures))
	for _, procedure := range state.Procedures {
		if procedureCutoff > 0 && procedure.UpdatedAt > 0 && procedure.UpdatedAt < procedureCutoff {
			stats.prunedProcedures++
			changed = true
			continue
		}
		keptProcedures = append(keptProcedures, procedure)
	}
	state.Procedures = keptProcedures

	keptEdges := make([]ResearchMemoryEdge, 0, len(state.Edges))
	for _, edge := range state.Edges {
		if _, ok := recordIDs[edge.FromID]; ok {
			keptEdges = append(keptEdges, edge)
			continue
		}
		if _, ok := episodeIDs[edge.FromID]; ok {
			keptEdges = append(keptEdges, edge)
			continue
		}
		stats.prunedEdges++
		changed = true
	}
	state.Edges = keptEdges
	if changed {
		state.UpdatedAt = now
	}
	return stats, changed
}

func normalizeResearchMemoryRecord(record ResearchMemoryRecord, retentionDays int, now int64) (ResearchMemoryRecord, bool, bool, bool) {
	changed := false
	archived := false
	prune := false
	anchor := firstResearchMemoryTimestamp(record.LastConfirmedAt, record.UpdatedAt, record.CreatedAt)
	ageDays := int((now - anchor) / (24 * 60 * 60 * 1000))
	if ageDays < 0 {
		ageDays = 0
	}
	if record.Metadata == nil {
		record.Metadata = map[string]any{}
	}
	record.Metadata["ageDays"] = ageDays
	record.Metadata["freshnessScore"] = researchMemoryFreshnessWeight(record, now)
	record.Metadata["stale"] = ageDays > MaxInt(record.FreshnessHalfLifeDays, 45)

	archiveThreshold := MaxInt(record.FreshnessHalfLifeDays*2, 120)
	if retentionDays > 0 {
		archiveThreshold = MinInt(archiveThreshold, retentionDays)
	}
	shouldArchive := false
	switch record.LifecycleStatus {
	case ResearchMemoryStatusContradicted:
		shouldArchive = false
	case ResearchMemoryStatusArchived:
		shouldArchive = true
	default:
		shouldArchive = ageDays >= archiveThreshold && (record.SupportCount <= 1 || record.LifecycleStatus == ResearchMemoryStatusSuperseded || record.LifecycleStatus == ResearchMemoryStatusTentative)
	}
	if shouldArchive && record.LifecycleStatus != ResearchMemoryStatusArchived {
		record.LifecycleStatus = ResearchMemoryStatusArchived
		record.UpdatedAt = now
		changed = true
		archived = true
	}
	if retentionDays > 0 {
		pruneThreshold := MaxInt(retentionDays*3, archiveThreshold*2)
		if record.LifecycleStatus == ResearchMemoryStatusArchived && ageDays >= pruneThreshold {
			prune = true
		}
	}
	return record, changed, archived, prune
}

func firstResearchMemoryTimestamp(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return NowMillis()
}

func inferResearchMemoryEvaluationTarget(query string, claim string, topics []string) string {
	blob := normalizeResearchMemoryText(strings.Join([]string{query, claim}, " "))
	if strings.Contains(blob, "benchmark") || strings.Contains(blob, "evaluate") || strings.Contains(blob, "trial") || strings.Contains(blob, "compare") {
		if len(topics) > 0 {
			return topics[0]
		}
	}
	return ""
}

func (r ResearchMemoryRecord) ProvenanceKey() string {
	if len(r.Provenance) == 0 {
		return ""
	}
	return strings.TrimSpace(firstNonEmpty(r.Provenance[0].CanonicalSourceID, r.Provenance[0].SourceID))
}

func findingsFromDossierPayload(dossier map[string]any) []EvidenceFinding {
	items := firstArtifactMaps(dossier["verifiedClaims"])
	canonicalByID := make(map[string]map[string]any)
	for _, source := range firstArtifactMaps(dossier["canonicalSources"]) {
		canonicalByID[AsOptionalString(source["canonicalId"])] = source
	}
	findings := make([]EvidenceFinding, 0, len(items))
	for _, item := range items {
		claim := strings.TrimSpace(AsOptionalString(firstNonEmptyValue(item["claimText"], item["claim"])))
		if claim == "" {
			continue
		}
		spans := firstArtifactMaps(item["evidenceSpans"])
		sourceID, paperTitle, snippet := "", "", ""
		if len(spans) > 0 {
			sourceID = strings.TrimSpace(AsOptionalString(firstNonEmptyValue(spans[0]["sourceCanonicalId"], spans[0]["sourceId"])))
			snippet = strings.TrimSpace(AsOptionalString(spans[0]["snippet"]))
			if source := canonicalByID[sourceID]; len(source) > 0 {
				paperTitle = strings.TrimSpace(AsOptionalString(source["title"]))
			}
		}
		findings = append(findings, EvidenceFinding{
			ID:         strings.TrimSpace(AsOptionalString(firstNonEmptyValue(item["packetId"], item["id"]))),
			Claim:      claim,
			Keywords:   deriveResearchMemoryTopics(strings.Join([]string{claim, paperTitle}, " ")),
			SourceID:   sourceID,
			PaperTitle: paperTitle,
			Snippet:    snippet,
			Confidence: ClampFloat(AsFloat(item["confidence"]), 0.2, 1.0),
			Status:     firstNonEmpty(AsOptionalString(item["verifierStatus"]), "verified"),
		})
	}
	return findings
}

func contradictionsFromDossierPayload(dossier map[string]any) []ContradictionPair {
	items := firstArtifactMaps(dossier["contradictions"])
	out := make([]ContradictionPair, 0, len(items))
	for _, item := range items {
		left := mapToEvidenceFinding(asMap(item["left"]))
		right := mapToEvidenceFinding(asMap(item["right"]))
		if strings.TrimSpace(left.Claim) == "" || strings.TrimSpace(right.Claim) == "" {
			continue
		}
		out = append(out, ContradictionPair{
			FindingA:    left,
			FindingB:    right,
			Severity:    ContradictionSeverity(firstNonEmpty(AsOptionalString(item["severity"]), string(ContradictionMedium))),
			Explanation: AsOptionalString(firstNonEmptyValue(item["explanation"], item["summary"])),
		})
	}
	return out
}

func mapToEvidenceFinding(raw map[string]any) EvidenceFinding {
	return EvidenceFinding{
		ID:         AsOptionalString(firstNonEmptyValue(raw["packetId"], raw["id"])),
		Claim:      AsOptionalString(firstNonEmptyValue(raw["claimText"], raw["claim"])),
		SourceID:   AsOptionalString(firstNonEmptyValue(raw["sourceCanonicalId"], raw["sourceId"])),
		PaperTitle: AsOptionalString(raw["paperTitle"]),
		Snippet:    AsOptionalString(raw["snippet"]),
		Confidence: ClampFloat(AsFloat(raw["confidence"]), 0.2, 1.0),
		Status:     AsOptionalString(firstNonEmptyValue(raw["verifierStatus"], raw["status"])),
		Keywords:   deriveResearchMemoryTopics(AsOptionalString(firstNonEmptyValue(raw["claimText"], raw["claim"]))),
	}
}

func crystallizeDossierSummary(query string, findings []EvidenceFinding, gaps []string) string {
	if len(findings) > 0 {
		return fmt.Sprintf("Compiled %d verified finding(s) for %s and retained %d unresolved gap(s).", len(findings), query, len(gaps))
	}
	return fmt.Sprintf("Compiled unresolved research memory for %s.", query)
}

func findingClaims(findings []EvidenceFinding) []string {
	out := make([]string, 0, len(findings))
	for _, finding := range findings {
		if claim := strings.TrimSpace(firstNonEmpty(finding.Claim, finding.Snippet)); claim != "" {
			out = append(out, claim)
		}
	}
	return uniqueStrings(out)
}

func contradictionSummaries(contradictions []ContradictionPair) []string {
	out := make([]string, 0, len(contradictions))
	for _, contradiction := range contradictions {
		if summary := strings.TrimSpace(firstNonEmpty(contradiction.Explanation, contradiction.FindingA.Claim)); summary != "" {
			out = append(out, summary)
		}
	}
	return uniqueStrings(out)
}

func upsertResearchMemoryEdge(edges *[]ResearchMemoryEdge, edge ResearchMemoryEdge) {
	for i := range *edges {
		if (*edges)[i].ID == edge.ID {
			(*edges)[i] = edge
			return
		}
	}
	*edges = append(*edges, edge)
}

func collectEpisodeGaps(episodes []ResearchMemoryEpisode, limit int) []string {
	return flattenEpisodeGaps(episodes, limit)
}

func flattenEpisodeGaps(episodes []ResearchMemoryEpisode, limit int) []string {
	values := make([]string, 0, limit)
	for _, episode := range episodes {
		values = append(values, episode.UnresolvedGaps...)
	}
	values = uniqueStrings(values)
	if len(values) > limit {
		values = values[:limit]
	}
	return values
}

func buildWorkspaceArtifactsFromEpisodes(episodes []ResearchMemoryEpisode, artifactType string) []map[string]any {
	out := make([]map[string]any, 0, MinInt(5, len(episodes)))
	for _, episode := range episodes {
		out = append(out, map[string]any{
			"artifactId":   episode.ID,
			"projectId":    episode.ProjectID,
			"artifactType": artifactType,
			"title":        firstNonEmpty(episode.Query, "Research memory digest"),
			"summary":      episode.Summary,
			"createdAt":    episode.CreatedAt,
		})
		if len(out) >= 5 {
			break
		}
	}
	return out
}

func buildWorkspaceAcceptedSeedPapers(records []ResearchMemoryRecord) []map[string]any {
	out := make([]map[string]any, 0, MinInt(8, len(records)))
	for _, record := range records {
		if len(record.Provenance) == 0 {
			continue
		}
		source := record.Provenance[0]
		if title := strings.TrimSpace(firstNonEmpty(source.PaperTitle, record.Claim)); title != "" {
			out = append(out, map[string]any{"title": title, "provider": source.SourceID})
		}
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func researchMemoryRecordsToMaps(records []ResearchMemoryRecord, limit int) []map[string]any {
	if len(records) > limit {
		records = records[:limit]
	}
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		out = append(out, map[string]any{
			"id":              record.ID,
			"claim":           record.Claim,
			"topics":          stringSliceToAny(record.Topics),
			"confidence":      record.Confidence,
			"status":          string(record.LifecycleStatus),
			"supportCount":    record.SupportCount,
			"lastConfirmedAt": record.LastConfirmedAt,
			"projectId":       record.ProjectID,
		})
	}
	return out
}

func researchMemoryEpisodesToMaps(episodes []ResearchMemoryEpisode, limit int) []map[string]any {
	if len(episodes) > limit {
		episodes = episodes[:limit]
	}
	out := make([]map[string]any, 0, len(episodes))
	for _, episode := range episodes {
		out = append(out, map[string]any{
			"id":                     episode.ID,
			"query":                  episode.Query,
			"summary":                episode.Summary,
			"recommendedNextQueries": stringSliceToAny(episode.RecommendedNextQueries),
			"createdAt":              episode.CreatedAt,
		})
	}
	return out
}
