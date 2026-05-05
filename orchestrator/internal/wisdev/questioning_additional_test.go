package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestQuestioningHelpers(t *testing.T) {
	t.Run("question index and complexity", func(t *testing.T) {
		index := questionByID()
		assert.Contains(t, index, "q1_domain")
		assert.Equal(t, TypeScope, index["q2_scope"].Type)

		assert.Equal(t, 0.4, EstimateComplexityScore(""))
		assert.Equal(t, 0.4, EstimateComplexityScore("sleep interventions adults"))
		assert.Greater(t, EstimateComplexityScore("systematic review meta-analysis reproducibility causal longitudinal and versus evidence synthesis comparison of treatment outcomes"), 0.8)
		assert.Greater(t, EstimateComplexityScore("a very long query with more than ten distinct tokens and comparisons"), 0.45)
	})

	t.Run("adaptive sequence", func(t *testing.T) {
		baseSequence, baseMinQuestions, baseMaxQuestions := BuildAdaptiveQuestionSequence(0.4, "social")
		assert.Equal(t, []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics", "q5_study_types", "q6_exclusions"}, baseSequence)
		assert.Equal(t, 6, baseMinQuestions)
		assert.Equal(t, 6, baseMaxQuestions)

		sequence, minQuestions, maxQuestions := BuildAdaptiveQuestionSequence(0.9, "medicine")
		assert.Equal(t, []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics", "q5_study_types", "q6_exclusions", "q7_evidence_quality", "q8_output_focus"}, sequence)
		assert.Equal(t, 8, minQuestions)
		assert.Equal(t, 8, maxQuestions)
	})

	t.Run("ensure state and flow", func(t *testing.T) {
		session := &AgentSession{
			OriginalQuery:    "systematic review of treatment outcomes",
			DetectedDomain:   "medicine",
			Answers:          map[string]Answer{},
			QuestionSequence: nil,
		}

		EnsureAdaptiveQuestionState(session)
		assert.Greater(t, session.ComplexityScore, 0.4)
		assert.NotEmpty(t, session.QuestionSequence)
		assert.Equal(t, 2, session.ClarificationBudget)
		assert.Equal(t, 0, AnsweredQuestionCount(nil))

		session.Answers["q1_domain"] = Answer{QuestionID: "q1_domain", Values: []string{"medicine"}}
		session.Answers["q2_scope"] = Answer{QuestionID: "q2_scope", Values: []string{"focused"}}
		assert.Equal(t, "q3_timeframe", FindNextQuestionID(session))

		stop, reason := ShouldStopQuestioning(session)
		assert.False(t, stop)
		assert.Equal(t, QuestionStopReasonNone, reason)
	})

	t.Run("planning query drives complexity when legacy fields are stale", func(t *testing.T) {
		planningQuery := "systematic review meta-analysis reproducibility causal longitudinal treatment outcomes and biomarker stratification across multicenter randomized cohorts with translational validation"
		session := &AgentSession{
			Query:          planningQuery,
			OriginalQuery:  "short query",
			CorrectedQuery: "short query",
			DetectedDomain: "medicine",
			Answers:        map[string]Answer{},
		}

		EnsureAdaptiveQuestionState(session)
		assert.Greater(t, session.ComplexityScore, 0.8)
		assert.Equal(t, []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics", "q5_study_types", "q6_exclusions", "q7_evidence_quality", "q8_output_focus"}, session.QuestionSequence)
	})

	t.Run("evidence sufficient and budget reached", func(t *testing.T) {
		session := &AgentSession{
			Answers: map[string]Answer{
				"q1_domain":    {QuestionID: "q1_domain", Values: []string{"medicine"}},
				"q2_scope":     {QuestionID: "q2_scope", Values: []string{"focused"}},
				"q3_timeframe": {QuestionID: "q3_timeframe", Values: []string{"5years"}},
			},
			QuestionSequence:    []string{"q1_domain", "q2_scope", "q3_timeframe"},
			MinQuestions:        3,
			MaxQuestions:        5,
			ClarificationBudget: 2,
		}

		stop, reason := ShouldStopQuestioning(session)
		assert.True(t, stop)
		assert.Equal(t, QuestionStopReasonEvidenceSufficient, reason)
		session.QuestionStopReason = reason

		summary := BuildQuestionStateSummary(session)
		assert.Equal(t, 3, summary["answeredCount"])
		assert.Equal(t, QuestionStopReasonEvidenceSufficient, summary["stopReason"])
	})

	t.Run("budget reached and nil session", func(t *testing.T) {
		stop, reason := ShouldStopQuestioning(nil)
		assert.True(t, stop)
		assert.Equal(t, QuestionStopReasonClarificationBudgetReached, reason)

		session := &AgentSession{
			Answers:             map[string]Answer{},
			QuestionSequence:    []string{"q1_domain"},
			MinQuestions:        1,
			MaxQuestions:        1,
			ClarificationBudget: 1,
		}
		session.Answers["q1_domain"] = Answer{QuestionID: "q1_domain", Values: []string{"medicine"}}
		stop, reason = ShouldStopQuestioning(session)
		assert.True(t, stop)
		assert.Equal(t, QuestionStopReasonClarificationBudgetReached, reason)
	})

	t.Run("supplemental follow-up answers do not satisfy planned question budget", func(t *testing.T) {
		session := &AgentSession{
			Answers: map[string]Answer{
				"q1_domain":            {QuestionID: "q1_domain", Values: []string{"medicine"}},
				"q2_scope":             {QuestionID: "q2_scope", Values: []string{"focused"}},
				"q3_timeframe":         {QuestionID: "q3_timeframe", Values: []string{"5years"}},
				"follow_up_refinement": {QuestionID: "follow_up_refinement", Values: []string{"sleep"}},
			},
			QuestionSequence:    []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics"},
			MinQuestions:        4,
			MaxQuestions:        5,
			ClarificationBudget: 2,
		}

		assert.Equal(t, 3, AnsweredQuestionCount(session))
		stop, reason := ShouldStopQuestioning(session)
		assert.False(t, stop)
		assert.Equal(t, QuestionStopReasonNone, reason)
		assert.Equal(t, "q4_subtopics", FindNextQuestionID(session))
	})

	t.Run("planned study types must be surfaced before stopping", func(t *testing.T) {
		session := &AgentSession{
			Answers: map[string]Answer{
				"q1_domain":    {QuestionID: "q1_domain", Values: []string{"medicine"}},
				"q2_scope":     {QuestionID: "q2_scope", Values: []string{"focused"}},
				"q3_timeframe": {QuestionID: "q3_timeframe", Values: []string{"5years"}},
				"q4_subtopics": {QuestionID: "q4_subtopics", Values: []string{"biomarkers"}},
			},
			QuestionSequence:    []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics", "q5_study_types"},
			MinQuestions:        5,
			MaxQuestions:        5,
			ClarificationBudget: 2,
		}

		stop, reason := ShouldStopQuestioning(session)
		assert.False(t, stop)
		assert.Equal(t, QuestionStopReasonNone, reason)
		assert.Equal(t, "q5_study_types", FindNextQuestionID(session))

		session.Answers["q5_study_types"] = Answer{QuestionID: "q5_study_types", Values: nil}
		stop, reason = ShouldStopQuestioning(session)
		assert.False(t, stop)
		assert.Equal(t, QuestionStopReasonNone, reason)
		assert.Equal(t, "q5_study_types", FindNextQuestionID(session))

		session.Answers["q5_study_types"] = Answer{QuestionID: "q5_study_types", Values: []string{"randomized_trials"}}
		stop, reason = ShouldStopQuestioning(session)
		assert.True(t, stop)
		assert.Equal(t, QuestionStopReasonEvidenceSufficient, reason)
	})

	t.Run("blank planned answers do not advance question progress", func(t *testing.T) {
		session := &AgentSession{
			Answers: map[string]Answer{
				"q1_domain":    {QuestionID: "q1_domain", Values: []string{"medicine"}},
				"q2_scope":     {QuestionID: "q2_scope", Values: []string{"focused"}},
				"q3_timeframe": {QuestionID: "q3_timeframe", Values: []string{"5years"}},
				"q4_subtopics": {QuestionID: "q4_subtopics", Values: []string{}},
			},
			QuestionSequence:    []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics"},
			MinQuestions:        4,
			MaxQuestions:        4,
			ClarificationBudget: 2,
		}

		assert.Equal(t, 3, AnsweredQuestionCount(session))
		assert.Equal(t, "q4_subtopics", FindNextQuestionID(session))
		summary := BuildQuestionStateSummary(session)
		assert.Equal(t, 3, summary["answeredCount"])
		assert.Equal(t, []string{"q4_subtopics"}, summary["remainingQuestionIds"])
	})
}

func TestQuestioningSimilarityAndPruning(t *testing.T) {
	assert.Equal(t, 0.0, JaccardSimilarity("", ""))
	assert.Greater(t, JaccardSimilarity("deep learning", "deep neural learning"), 0.0)

	variants := []map[string]any{
		{"label": "deep learning methods"},
		{"label": "completely unrelated topic"},
		{"label": ""},
	}
	pruned := PruneRedundantBranches(variants, []string{"deep learning approaches"}, 0.4)
	assert.Len(t, pruned, 1)
	assert.Equal(t, "completely unrelated topic", pruned[0]["label"])
}
