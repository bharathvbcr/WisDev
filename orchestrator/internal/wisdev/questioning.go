package wisdev

import (
	"sort"
	"strings"
)

func questionByID() map[string]Question {
	questions := DefaultQuestionFlow()
	index := make(map[string]Question, len(questions))
	for _, question := range questions {
		index[question.ID] = question
	}
	return index
}

func DefaultQuestionFlow() []Question {
	return []Question{
		{
			ID:            "q1_domain",
			Type:          TypeDomain,
			Question:      "Which academic domain best matches your research intent?",
			IsMultiSelect: true,
			IsRequired:    true,
			Options: []QuestionOption{
				{Value: "medicine", Label: "Medicine & Healthcare"},
				{Value: "cs", Label: "Computer Science & AI"},
				{Value: "social", Label: "Social Sciences"},
				{Value: "climate", Label: "Climate & Environment"},
				{Value: "neuro", Label: "Neuroscience"},
				{Value: "physics", Label: "Physics & Engineering"},
				{Value: "biology", Label: "Biology & Life Sciences"},
				{Value: "humanities", Label: "Humanities"},
			},
		},
		{
			ID:            "q2_scope",
			Type:          TypeScope,
			Question:      "How broad should the search be?",
			IsMultiSelect: false,
			IsRequired:    true,
			Options: []QuestionOption{
				{Value: "focused", Label: "Focused", Description: "Top 10-15 high-signal papers"},
				{Value: "comprehensive", Label: "Comprehensive", Description: "Broad sweep of 30+ sources"},
				{Value: "exhaustive", Label: "Exhaustive", Description: "Systematic review depth"},
			},
		},
		{
			ID:            "q3_timeframe",
			Type:          TypeTimeframe,
			Question:      "What timeframe should be prioritized?",
			IsMultiSelect: false,
			IsRequired:    true,
			Options: []QuestionOption{
				{Value: "1year", Label: "Last Year", Description: "Latest breakthroughs only"},
				{Value: "5years", Label: "Last 5 Years", Description: "Modern consensus"},
				{Value: "alltime", Label: "All Time", Description: "Include foundational works"},
			},
		},
		{
			ID:            "q4_subtopics",
			Type:          TypeSubtopics,
			Question:      "Which subtopics matter most?",
			IsMultiSelect: true,
			IsRequired:    true,
		},
		{
			ID:            "q5_study_types",
			Type:          TypeStudyTypes,
			Question:      "What study types should be included?",
			IsMultiSelect: true,
			IsRequired:    false,
		},
		{
			ID:            "q6_exclusions",
			Type:          TypeExclusions,
			Question:      "Are there any specific exclusions?",
			IsMultiSelect: true,
			IsRequired:    false,
		},
	}
}

func EstimateComplexityScore(query string) float64 {
	text := strings.TrimSpace(strings.ToLower(query))
	if text == "" {
		return 0.4
	}
	score := 0.35
	tokenCount := len(strings.Fields(text))
	switch {
	case tokenCount > 18:
		score += 0.25
	case tokenCount > 10:
		score += 0.15
	default:
		score += 0.05
	}
	if strings.Contains(text, " and ") || strings.Contains(text, " versus ") || strings.Contains(text, " vs ") {
		score += 0.1
	}
	if strings.Contains(text, "systematic review") || strings.Contains(text, "meta-analysis") || strings.Contains(text, "prisma") {
		score += 0.15
	}
	if strings.Contains(text, "reproducibility") || strings.Contains(text, "causal") || strings.Contains(text, "longitudinal") {
		score += 0.1
	}
	if score > 1.0 {
		return 1.0
	}
	return score
}

func BuildAdaptiveQuestionSequence(complexity float64, domainHint string) ([]string, int, int) {
	base := []string{"q1_domain", "q2_scope", "q3_timeframe"}
	sequence := append([]string{}, base...)

	if complexity >= 0.45 {
		sequence = append(sequence, "q4_subtopics")
	}
	if complexity >= 0.65 || strings.EqualFold(domainHint, "medicine") {
		sequence = append(sequence, "q5_study_types")
	}
	if complexity >= 0.8 {
		sequence = append(sequence, "q6_exclusions")
	}

	minQuestions := 3
	if complexity >= 0.45 {
		minQuestions = 4
	}
	maxQuestions := len(sequence)
	return sequence, minQuestions, maxQuestions
}

func EnsureAdaptiveQuestionState(session *AgentSession) {
	if session == nil {
		return
	}
	if session.ComplexityScore <= 0 {
		query := session.CorrectedQuery
		if strings.TrimSpace(query) == "" {
			query = session.OriginalQuery
		}
		session.ComplexityScore = EstimateComplexityScore(query)
	}
	if len(session.QuestionSequence) == 0 || session.MinQuestions <= 0 || session.MaxQuestions <= 0 {
		sequence, minQuestions, maxQuestions := BuildAdaptiveQuestionSequence(session.ComplexityScore, session.DetectedDomain)
		session.QuestionSequence = sequence
		session.MinQuestions = minQuestions
		session.MaxQuestions = maxQuestions
	}
	if session.ClarificationBudget <= 0 {
		session.ClarificationBudget = 2
	}
}

func AnsweredQuestionCount(session *AgentSession) int {
	if session == nil {
		return 0
	}
	return len(session.Answers)
}

func FindNextQuestionID(session *AgentSession) string {
	if session == nil {
		return ""
	}
	EnsureAdaptiveQuestionState(session)
	answered := make(map[string]bool, len(session.Answers))
	for questionID := range session.Answers {
		answered[questionID] = true
	}
	for _, questionID := range session.QuestionSequence {
		if !answered[questionID] {
			return questionID
		}
	}
	return ""
}

func ShouldStopQuestioning(session *AgentSession) (bool, QuestionStopReason) {
	if session == nil {
		return true, QuestionStopReasonClarificationBudgetReached
	}
	EnsureAdaptiveQuestionState(session)
	count := AnsweredQuestionCount(session)
	if count >= session.MinQuestions {
		hasDomain := len(session.Answers["q1_domain"].Values) > 0
		hasScope := len(session.Answers["q2_scope"].Values) > 0
		hasTimeframe := len(session.Answers["q3_timeframe"].Values) > 0
		if hasDomain && hasScope && hasTimeframe {
			return true, QuestionStopReasonEvidenceSufficient
		}
	}
	if count >= session.MaxQuestions {
		return true, QuestionStopReasonClarificationBudgetReached
	}
	if FindNextQuestionID(session) == "" {
		return true, QuestionStopReasonClarificationBudgetReached
	}
	return false, QuestionStopReasonNone
}

func BuildQuestionStateSummary(session *AgentSession) map[string]any {
	if session == nil {
		return map[string]any{}
	}
	remaining := []string{}
	answered := make(map[string]bool, len(session.Answers))
	for questionID := range session.Answers {
		answered[questionID] = true
	}
	for _, questionID := range session.QuestionSequence {
		if !answered[questionID] {
			remaining = append(remaining, questionID)
		}
	}
	sort.Strings(remaining)
	return map[string]any{
		"answeredCount":        AnsweredQuestionCount(session),
		"minQuestions":         session.MinQuestions,
		"maxQuestions":         session.MaxQuestions,
		"complexityScore":      session.ComplexityScore,
		"remainingQuestionIds": remaining,
		"stopReason":           session.QuestionStopReason,
	}
}

// JaccardSimilarity computes token overlap between two strings.
func JaccardSimilarity(a, b string) float64 {
	aTokens := strings.Fields(strings.ToLower(a))
	bTokens := strings.Fields(strings.ToLower(b))
	if len(aTokens) == 0 || len(bTokens) == 0 {
		return 0.0
	}

	aMap := make(map[string]bool)
	for _, t := range aTokens {
		aMap[t] = true
	}

	intersection := 0
	bMap := make(map[string]bool)
	for _, t := range bTokens {
		if !bMap[t] {
			if aMap[t] {
				intersection++
			}
			bMap[t] = true
		}
	}

	union := len(aMap) + len(bMap) - intersection
	return float64(intersection) / float64(union)
}

// PruneRedundantBranches returns variants that are sufficiently different from siblings.
func PruneRedundantBranches(variants []map[string]any, siblings []string, threshold float64) []map[string]any {
	pruned := make([]map[string]any, 0, len(variants))
	for _, v := range variants {
		label, _ := v["label"].(string)
		if label == "" {
			continue
		}

		isRedundant := false
		for _, sib := range siblings {
			if JaccardSimilarity(label, sib) > threshold {
				isRedundant = true
				break
			}
		}

		if !isRedundant {
			pruned = append(pruned, v)
			siblings = append(siblings, label)
		}
	}
	return pruned
}
