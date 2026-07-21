package wisdev

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGuidedFlow(t *testing.T) {
	flow := NewGuidedFlow()
	session := &Session{
		OriginalQuery: "AI in medicine",
		Answers:       make(map[string]Answer),
	}

	// 1. Get First Question
	q, ok := flow.GetNextQuestion(session)
	assert.True(t, ok)
	assert.NotNil(t, q)
	firstID := q.ID

	// 2. Process Answer
	answer := Answer{
		QuestionID: firstID,
		Values:     []string{"value1"},
	}
	err := flow.ProcessAnswer(context.Background(), session, answer)
	assert.NoError(t, err)
	assert.Equal(t, 1, session.CurrentQuestionIndex)

	// 3. Process Answer with wrong ID
	err = flow.ProcessAnswer(context.Background(), session, Answer{QuestionID: "wrong"})
	assert.Error(t, err)

	// 4. Test domain adaptation
	session.CurrentQuestionIndex = 0
	session.QuestionSequence = []string{"q1_domain", "other"}
	err = flow.ProcessAnswer(context.Background(), session, Answer{
		QuestionID: "q1_domain",
		Values:     []string{"cs", "bio"},
	})
	assert.NoError(t, err)
	assert.Equal(t, "cs", session.DetectedDomain)
	assert.Equal(t, []string{"bio"}, session.SecondaryDomains)
}

func TestGuidedFlow_NormalizesSingleSelectAnswers(t *testing.T) {
	flow := NewGuidedFlow()
	session := &Session{
		OriginalQuery:        "AI in medicine",
		Answers:              make(map[string]Answer),
		CurrentQuestionIndex: 2,
		QuestionSequence:     []string{"q1_domain", "q2_scope", "q3_timeframe"},
		DetectedDomain:       "cs",
	}

	err := flow.ProcessAnswer(context.Background(), session, Answer{
		QuestionID:    "q3_timeframe",
		Values:        []string{"1year", "5years"},
		DisplayValues: []string{"Last Year", "Last 5 Years"},
	})

	assert.NoError(t, err)
	assert.Equal(t, []string{"1year"}, session.Answers["q3_timeframe"].Values)
	assert.Equal(t, []string{"Last Year"}, session.Answers["q3_timeframe"].DisplayValues)
}

func TestGuidedFlow_EdgeCases(t *testing.T) {
	flow := NewGuidedFlow()

	t.Run("NilSession", func(t *testing.T) {
		assert.Nil(t, flow.ensureAdaptiveSequence(nil))
	})

	t.Run("NoMoreQuestions", func(t *testing.T) {
		session := &Session{CurrentQuestionIndex: 100, QuestionSequence: []string{"q1"}}
		q, ok := flow.GetNextQuestion(session)
		assert.False(t, ok)
		assert.Nil(t, q)

		err := flow.ProcessAnswer(context.Background(), session, Answer{})
		assert.Error(t, err)
	})

	t.Run("PlanningQueryDrivesAdaptiveSequence", func(t *testing.T) {
		planningQuery := "systematic review meta-analysis reproducibility causal longitudinal treatment outcomes and biomarker stratification across multicenter randomized cohorts with translational validation"
		session := &Session{
			Query:          planningQuery,
			OriginalQuery:  "short query",
			CorrectedQuery: "short query",
		}

		sequence := flow.ensureAdaptiveSequence(session)
		assert.Equal(t, []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics", "q5_study_types", "q6_exclusions", "q7_evidence_quality", "q8_output_focus"}, sequence)
	})
}
