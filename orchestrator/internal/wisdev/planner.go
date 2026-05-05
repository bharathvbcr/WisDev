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
	query := ResolveSessionSearchQuery(session.Query, session.CorrectedQuery, session.OriginalQuery)
	baseID := fmt.Sprintf("plan_%s", session.SessionID)
	if session.Mode == WisDevModeYOLO {
		steps := []PlanStep{
			{
				ID:              "step-01",
				Action:          "research.queryDecompose",
				Reason:          "Decompose the autonomous research goal for " + query,
				Risk:            RiskLevelLow,
				ModelTier:       ModelTierHeavy,
				ExecutionTarget: ExecutionTargetPythonCapability,
			},
			{
				ID:               "step-02",
				Action:           "research.proposeHypotheses",
				Reason:           "Generate candidate research paths before retrieval",
				Risk:             RiskLevelLow,
				ModelTier:        ModelTierHeavy,
				ExecutionTarget:  ExecutionTargetPythonCapability,
				DependsOnStepIDs: []string{"step-01"},
			},
			{
				ID:              "step-03",
				Action:          "research.retrievePapers",
				Reason:          "Fan out retrieval across multiple sources",
				Risk:            RiskLevelLow,
				ModelTier:       ModelTierLight,
				ExecutionTarget: ExecutionTargetGoNative,
				Parallelizable:  true,
				ParallelGroup:   "retrieval",
			},
			{
				ID:               "step-04",
				Action:           "research.resolveCanonicalCitations",
				Reason:           "Resolve retrieved papers against canonical citation authorities",
				Risk:             RiskLevelMedium,
				ModelTier:        ModelTierStandard,
				ExecutionTarget:  ExecutionTargetGoNative,
				DependsOnStepIDs: []string{"step-03"},
			},
			{
				ID:               "step-05",
				Action:           "research.buildClaimEvidenceTable",
				Reason:           "Assemble the claim-evidence matrix for drafting",
				Risk:             RiskLevelLow,
				ModelTier:        ModelTierStandard,
				ExecutionTarget:  ExecutionTargetPythonCapability,
				DependsOnStepIDs: []string{"step-04"},
			},
			{
				ID:               "step-06",
				Action:           "research.detectContradictions",
				Reason:           "Surface conflicting evidence before synthesis",
				Risk:             RiskLevelMedium,
				ModelTier:        ModelTierStandard,
				ExecutionTarget:  ExecutionTargetPythonCapability,
				DependsOnStepIDs: []string{"step-05"},
			},
			{
				ID:               "step-07",
				Action:           "research.generateThoughts",
				Reason:           "Generate structured internal reasoning for the synthesis layer",
				Risk:             RiskLevelMedium,
				ModelTier:        ModelTierStandard,
				ExecutionTarget:  ExecutionTargetPythonCapability,
				DependsOnStepIDs: []string{"step-06"},
			},
			{
				ID:                      "step-08",
				Action:                  "research.verifyReasoningPaths",
				Reason:                  "Gate the autonomous reasoning paths before synthesis",
				Risk:                    RiskLevelHigh,
				ModelTier:               ModelTierHeavy,
				ExecutionTarget:         ExecutionTargetGoNative,
				RequiresHumanCheckpoint: true,
				DependsOnStepIDs:        []string{"step-07"},
			},
			{
				ID:               "step-09",
				Action:           ActionResearchSynthesizeAnswer,
				Reason:           "Produce the final autonomous synthesis",
				Risk:             RiskLevelMedium,
				ModelTier:        ModelTierHeavy,
				ExecutionTarget:  ExecutionTargetPythonCapability,
				DependsOnStepIDs: []string{"step-08"},
			},
			{
				ID:               "step-10",
				Action:           "research.coordinateReplan",
				Reason:           "Prepare a recovery path if synthesis confidence is insufficient",
				Risk:             RiskLevelLow,
				ModelTier:        ModelTierStandard,
				ExecutionTarget:  ExecutionTargetPythonCapability,
				DependsOnStepIDs: []string{"step-09"},
			},
		}
		return &PlanState{
			PlanID:    baseID,
			Steps:     steps,
			Reasoning: "yolo autonomous research plan with canonical citation grounding and reasoning-path verification.",
		}
	}
	steps := []PlanStep{
		{
			ID:              "step-01",
			Action:          "research.queryDecompose",
			Reason:          "High-level task decomposition for " + query,
			Risk:            RiskLevelLow,
			ModelTier:       ModelTierHeavy,
			ExecutionTarget: ExecutionTargetPythonCapability,
			Parallelizable:  false,
		},
		{
			ID:               "step-02",
			Action:           "research.proposeHypotheses",
			Reason:           "Proactive research hypothesis generation",
			Risk:             RiskLevelLow,
			ModelTier:        ModelTierHeavy,
			ExecutionTarget:  ExecutionTargetPythonCapability,
			Parallelizable:   false,
			DependsOnStepIDs: []string{"step-01"},
		},
		{
			ID:              "step-03",
			Action:          "research.retrievePapers",
			Reason:          "Parallel evidence gathering from multiple agents",
			Risk:            RiskLevelLow,
			ModelTier:       ModelTierLight,
			ExecutionTarget: ExecutionTargetGoNative,
			Parallelizable:  true,
		},
	}
	return &PlanState{
		PlanID:    baseID,
		Steps:     steps,
		Reasoning: "True Agent research plan with task decomposition and hypothesis generation.",
	}
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
