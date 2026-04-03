package wisdev

import (
	"fmt"
	"strings"
)

type GeneratedQueries struct {
	Queries          []string            `json:"queries"`
	QueryCount       int                 `json:"queryCount"`
	EstimatedResults int                 `json:"estimatedResults"`
	CoverageMap      map[string][]string `json:"coverageMap"`
}

func BuildDefaultPlan(session *AgentSession) *PlanState {
	query := session.CorrectedQuery
	if query == "" {
		query = session.OriginalQuery
	}
	baseID := fmt.Sprintf("plan_%s", session.SessionID)
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
			ID:               "step-03",
			Action:           "research.retrievePapers",
			Reason:           "Parallel evidence gathering from multiple agents",
			Risk:             RiskLevelLow,
			ModelTier:        ModelTierLight,
			ExecutionTarget:  ExecutionTargetGoNative,
			Parallelizable:   true,
			DependsOnStepIDs: []string{"step-02"},
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

	scopeCap := 6
	switch scope {
	case "exhaustive":
		scopeCap = 10
	case "focused":
		scopeCap = 4
	}

	coverageMap := make(map[string][]string)
	querySet := make(map[string]struct{})
	base := strings.TrimSpace(session.CorrectedQuery)
	if base == "" {
		base = strings.TrimSpace(session.OriginalQuery)
	}

	if base != "" {
		querySet[base] = struct{}{}
	}

	studyFragment := ""
	if len(studyTypes) > 0 {
		studyFragment = " " + strings.Join(studyTypes, " ")
	}

	exclusionFragment := ""
	for _, ex := range exclusions {
		exclusionFragment += " -" + strings.TrimSpace(ex)
	}

	for _, subtopic := range subtopics {
		subtopic = strings.TrimSpace(subtopic)
		if subtopic == "" {
			continue
		}
		q := fmt.Sprintf("%s %s%s%s", base, subtopic, studyFragment, exclusionFragment)
		q = strings.TrimSpace(q)
		querySet[q] = struct{}{}
		coverageMap[subtopic] = []string{q}
	}

	queries := make([]string, 0, len(querySet))
	for q := range querySet {
		queries = append(queries, q)
	}

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
	}
}
