package wisdev

import (
	"fmt"
	"sort"
	"strings"
)

type GeneratedQueries struct {
	Queries          []string            `json:"queries"`
	QueryCount       int                 `json:"queryCount"`
	EstimatedResults int                 `json:"estimatedResults"`
	CoverageMap      map[string][]string `json:"coverageMap"`
	QueryUsed        string              `json:"queryUsed,omitempty"`
}

func BuildDefaultPlan(session *AgentSession) *PlanState {
	if session == nil {
		return nil
	}
	return BuildGuidedOrchestrationPlan(session, nil, false)
}

func BuildGuidedOrchestrationPlan(session *AgentSession, queries []string, generatedFromTree bool) *PlanState {
	if session == nil {
		return nil
	}
	query := ResolveSessionSearchQuery(session.Query, session.CorrectedQuery, session.OriginalQuery)
	baseID := fmt.Sprintf("plan_%s", session.SessionID)
	planQueries := normalizeGuidedPlanQueries(query, queries)
	steps := []PlanStep{
		{
			ID:              "step-01",
			Action:          ActionResearchQueryDecompose,
			Reason:          "High-level task decomposition for " + query,
			Risk:            RiskLevelLow,
			ModelTier:       ModelTierStandard,
			ExecutionTarget: ExecutionTargetPythonCapability,
			Parallelizable:  false,
			Params: map[string]any{
				"query":             query,
				"queries":           append([]string(nil), planQueries...),
				"generatedFromTree": generatedFromTree,
			},
		},
		{
			ID:               "step-02",
			Action:           ActionResearchProposeHypotheses,
			Reason:           "Proactive research hypothesis generation",
			Risk:             RiskLevelLow,
			ModelTier:        ModelTierHeavy,
			ExecutionTarget:  ExecutionTargetPythonCapability,
			Parallelizable:   false,
			DependsOnStepIDs: []string{"step-01"},
			Params: map[string]any{
				"query":             query,
				"queries":           append([]string(nil), planQueries...),
				"generatedFromTree": generatedFromTree,
			},
		},
	}

	retrieveStepIDs := make([]string, 0, len(planQueries))
	for index, planQuery := range planQueries {
		stepID := nextPlanStepID(len(steps) + 1)
		params := guidedStableRetrievePapersPlanParams(session)
		params["query"] = planQuery
		params["queryIndex"] = index
		params["queryCount"] = len(planQueries)
		params["generatedFromTree"] = generatedFromTree
		params["executionMode"] = NormalizeWisDevMode(string(session.Mode))
		params["planMode"] = "guided_stable"
		steps = append(steps, PlanStep{
			ID:                 stepID,
			Action:             ActionResearchRetrievePapers,
			Reason:             "Parallel evidence gathering for " + planQuery,
			Risk:               RiskLevelLow,
			ModelTier:          ModelTierLight,
			ExecutionTarget:    ExecutionTargetGoNative,
			Parallelizable:     true,
			ParallelGroup:      "guided_retrieval",
			EstimatedCostCents: 1,
			MaxAttempts:        2,
			TimeoutMs:          30000,
			Params:             params,
		})
		retrieveStepIDs = append(retrieveStepIDs, stepID)
	}

	citationStepID := nextPlanStepID(len(steps) + 1)
	steps = append(steps, PlanStep{
		ID:                 citationStepID,
		Action:             ActionResearchResolveCanonicalCitations,
		Reason:             "Resolve retrieved papers against canonical citation authorities",
		Risk:               RiskLevelMedium,
		ModelTier:          ModelTierStandard,
		ExecutionTarget:    ExecutionTargetGoNative,
		DependsOnStepIDs:   retrieveStepIDs,
		EstimatedCostCents: 1,
		MaxAttempts:        2,
		TimeoutMs:          30000,
	})

	claimStepID := nextPlanStepID(len(steps) + 1)
	steps = append(steps, PlanStep{
		ID:                 claimStepID,
		Action:             ActionResearchBuildClaimEvidenceTable,
		Reason:             "Assemble the claim-evidence matrix for drafting",
		Risk:               RiskLevelLow,
		ModelTier:          ModelTierStandard,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		DependsOnStepIDs:   []string{citationStepID},
		EstimatedCostCents: 1,
		MaxAttempts:        2,
		TimeoutMs:          30000,
	})

	contradictionStepID := nextPlanStepID(len(steps) + 1)
	steps = append(steps, PlanStep{
		ID:                 contradictionStepID,
		Action:             ActionResearchDetectContradictions,
		Reason:             "Surface conflicting evidence before synthesis",
		Risk:               RiskLevelMedium,
		ModelTier:          ModelTierStandard,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		DependsOnStepIDs:   []string{claimStepID},
		EstimatedCostCents: 1,
		MaxAttempts:        2,
		TimeoutMs:          30000,
	})

	thoughtStepID := nextPlanStepID(len(steps) + 1)
	steps = append(steps, PlanStep{
		ID:                 thoughtStepID,
		Action:             ActionResearchGenerateThoughts,
		Reason:             "Generate structured internal reasoning for the synthesis layer",
		Risk:               RiskLevelMedium,
		ModelTier:          ModelTierStandard,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		DependsOnStepIDs:   []string{contradictionStepID},
		EstimatedCostCents: 1,
		MaxAttempts:        2,
		TimeoutMs:          30000,
	})

	verifyStepID := nextPlanStepID(len(steps) + 1)
	steps = append(steps, PlanStep{
		ID:                      verifyStepID,
		Action:                  ActionResearchVerifyReasoningPaths,
		Reason:                  "Gate the guided reasoning paths before synthesis",
		Risk:                    RiskLevelHigh,
		ModelTier:               ModelTierHeavy,
		ExecutionTarget:         ExecutionTargetGoNative,
		RequiresHumanCheckpoint: true,
		DependsOnStepIDs:        []string{thoughtStepID},
		EstimatedCostCents:      1,
		MaxAttempts:             2,
		TimeoutMs:               30000,
	})

	synthesisStepID := nextPlanStepID(len(steps) + 1)
	steps = append(steps, PlanStep{
		ID:                 synthesisStepID,
		Action:             ActionResearchSynthesizeAnswer,
		Reason:             "Produce the final guided synthesis from grounded evidence",
		Risk:               RiskLevelMedium,
		ModelTier:          ModelTierHeavy,
		ExecutionTarget:    ExecutionTargetGoNative,
		DependsOnStepIDs:   []string{verifyStepID},
		EstimatedCostCents: 1,
		MaxAttempts:        2,
		TimeoutMs:          30000,
	})

	steps = append(steps, PlanStep{
		ID:                 nextPlanStepID(len(steps) + 1),
		Action:             "research.coordinateReplan",
		Reason:             "Prepare a recovery path if synthesis confidence is insufficient",
		Risk:               RiskLevelLow,
		ModelTier:          ModelTierStandard,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		DependsOnStepIDs:   []string{synthesisStepID},
		EstimatedCostCents: 1,
		MaxAttempts:        2,
		TimeoutMs:          30000,
	})

	plan := newPlanState(baseID, steps)
	planMode := "guided"
	if NormalizeWisDevMode(string(session.Mode)) == WisDevModeYOLO {
		planMode = "yolo"
	}
	plan.Reasoning = fmt.Sprintf(
		"%s full-research plan using the guided-stable action graph with %d retrieval queries, canonical citation grounding, contradiction checks, reasoning-path verification, and synthesis checkpoints.",
		planMode,
		len(planQueries),
	)
	return plan
}

func nextPlanStepID(stepNumber int) string {
	return fmt.Sprintf("step-%02d", stepNumber)
}

func normalizeGuidedPlanQueries(baseQuery string, queries []string) []string {
	normalized := make([]string, 0, len(queries)+1)
	seen := make(map[string]struct{}, len(queries)+1)
	for _, raw := range queries {
		query := strings.TrimSpace(raw)
		if query == "" {
			continue
		}
		key := strings.ToLower(query)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, query)
	}
	if len(normalized) == 0 {
		if fallback := strings.TrimSpace(baseQuery); fallback != "" {
			normalized = append(normalized, fallback)
		}
	}
	return normalized
}

func guidedStableRetrievePapersPlanParams(session *AgentSession) map[string]any {
	if session == nil {
		return defaultRetrievePapersPlanParams(nil)
	}
	stableSession := *session
	stableSession.Mode = WisDevModeGuided
	return defaultRetrievePapersPlanParams(&stableSession)
}

func GenerateSearchQueries(session *Session) GeneratedQueries {
	scope := "comprehensive"
	if ans, ok := session.Answers["q2_scope"]; ok && len(ans.Values) > 0 {
		scope = ans.Values[0]
	}

	subtopics := []string{}
	if ans, ok := session.Answers["q4_subtopics"]; ok {
		subtopics = ans.Values
	}

	studyTypes := []string{}
	if ans, ok := session.Answers["q5_study_types"]; ok {
		studyTypes = ans.Values
	}

	exclusions := []string{}
	if ans, ok := session.Answers["q6_exclusions"]; ok {
		exclusions = ans.Values
	}

	evidenceQuality := []string{}
	if ans, ok := session.Answers["q7_evidence_quality"]; ok {
		evidenceQuality = ans.Values
	}

	outputFocus := []string{}
	if ans, ok := session.Answers["q8_output_focus"]; ok {
		outputFocus = ans.Values
	}

	scopeCap := 6
	switch scope {
	case "exhaustive":
		scopeCap = 10
	case "focused":
		scopeCap = 4
	}

	coverageMap := make(map[string][]string)
	querySet := make(map[string]struct{})
	base := strings.TrimSpace(ResolveSessionSearchQuery(session.Query, session.CorrectedQuery, session.OriginalQuery))

	if base == "" {
		// All query fields are empty. Return an empty result so the caller
		// can detect this and fall back to the session's seed query.
		return GeneratedQueries{
			Queries:          []string{},
			QueryCount:       0,
			EstimatedResults: 0,
			CoverageMap:      coverageMap,
			QueryUsed:        "",
		}
	}

	querySet[base] = struct{}{}

	studyFragment := ""
	if terms := queryAnswerTerms(studyTypes); len(terms) > 0 {
		studyFragment = " " + strings.Join(terms, " ")
	}

	qualityFragment := ""
	if terms := queryAnswerTerms(evidenceQuality); len(terms) > 0 {
		qualityFragment = " " + strings.Join(terms, " ")
	}

	focusFragment := ""
	if terms := queryAnswerTerms(outputFocus); len(terms) > 0 {
		focusFragment = " " + strings.Join(terms, " ")
	}

	exclusionFragment := ""
	for _, ex := range exclusions {
		trimmed := strings.TrimSpace(ex)
		if trimmed == "" || strings.EqualFold(trimmed, "none") || strings.EqualFold(trimmed, "no exclusions") {
			continue
		}
		exclusionFragment += " -" + normalizeQueryAnswerTerm(trimmed)
	}

	planningFragment := studyFragment + qualityFragment + focusFragment
	if planningFragment != "" {
		enrichedBase := strings.TrimSpace(base + planningFragment + exclusionFragment)
		if enrichedBase != base {
			querySet[enrichedBase] = struct{}{}
		}
		if qualityFragment != "" {
			coverageMap["evidence_quality"] = []string{enrichedBase}
		}
		if focusFragment != "" {
			coverageMap["output_focus"] = []string{enrichedBase}
		}
	}

	for _, subtopic := range subtopics {
		subtopic = strings.TrimSpace(subtopic)
		if subtopic == "" {
			continue
		}
		q := fmt.Sprintf("%s %s%s%s", base, subtopic, planningFragment, exclusionFragment)
		q = strings.TrimSpace(q)
		querySet[q] = struct{}{}
		coverageMap[subtopic] = []string{q}
	}

	// Collect subtopic queries (everything except base) and sort them so the
	// output order is deterministic across runs. Then prepend base so it is
	// always present regardless of scopeCap truncation.
	subtopicQueries := make([]string, 0, len(querySet)-1)
	for q := range querySet {
		if q != base {
			subtopicQueries = append(subtopicQueries, q)
		}
	}
	sort.Strings(subtopicQueries)

	queries := make([]string, 0, len(querySet))
	queries = append(queries, base)
	queries = append(queries, subtopicQueries...)

	if len(queries) > scopeCap {
		queries = queries[:scopeCap]
	}

	resultsPerQuery := 12
	switch scope {
	case "focused":
		resultsPerQuery = 8
	case "exhaustive":
		resultsPerQuery = 18
	}

	return GeneratedQueries{
		Queries:          queries,
		QueryCount:       len(queries),
		EstimatedResults: len(queries) * resultsPerQuery,
		CoverageMap:      coverageMap,
		QueryUsed:        base,
	}
}

func queryAnswerTerms(values []string) []string {
	terms := make([]string, 0, len(values))
	for _, value := range values {
		term := normalizeQueryAnswerTerm(value)
		if term == "" || strings.EqualFold(term, "none") {
			continue
		}
		terms = append(terms, term)
	}
	return terms
}

func normalizeQueryAnswerTerm(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "_", " ")
	value = strings.Join(strings.Fields(value), " ")
	return value
}
