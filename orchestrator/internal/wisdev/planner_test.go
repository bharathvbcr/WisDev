package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPlanner_BuildDefaultPlan(t *testing.T) {
	session := &AgentSession{
		SessionID:     "s1",
		OriginalQuery: "test",
	}
	plan := BuildDefaultPlan(session)
	assert.Equal(t, "plan_s1", plan.PlanID)
	assert.Len(t, plan.Steps, 3)
	assert.NotEmpty(t, plan.Reasoning)

	session.CorrectedQuery = "corrected"
	plan2 := BuildDefaultPlan(session)
	assert.Contains(t, plan2.Steps[0].Reason, "corrected")
}

func TestPlanner_GenerateSearchQueries(t *testing.T) {
	t.Run("Default Scope", func(t *testing.T) {
		session := &Session{
			OriginalQuery: "AI",
			Answers: map[string]Answer{
				"q4_subtopics": {Values: []string{"ML", "Deep Learning"}},
			},
		}
		gq := GenerateSearchQueries(session)
		assert.Len(t, gq.Queries, 3) // Base + 2 subtopics
		assert.Equal(t, 3*12, gq.EstimatedResults)
	})

	t.Run("Exhaustive Scope with Exclusions", func(t *testing.T) {
		session := &Session{
			CorrectedQuery: "Machine Learning",
			Answers: map[string]Answer{
				"q2_scope":     {Values: []string{"exhaustive"}},
				"q4_subtopics": {Values: []string{"SVM", "CNN", "RNN"}},
				"q5_study_types": {Values: []string{"Survey"}},
				"q6_exclusions": {Values: []string{"old"}},
			},
		}
		gq := GenerateSearchQueries(session)
		assert.Contains(t, gq.Queries, "Machine Learning SVM Survey -old")
		assert.Equal(t, len(gq.Queries)*18, gq.EstimatedResults)
	})

	t.Run("Focused Scope", func(t *testing.T) {
		session := &Session{
			OriginalQuery: "Test",
			Answers: map[string]Answer{
				"q2_scope": {Values: []string{"focused"}},
				"q4_subtopics": {Values: []string{"S1", "S2", "S3", "S4", "S5"}},
			},
		}
		gq := GenerateSearchQueries(session)
		assert.True(t, len(gq.Queries) <= 4)
		assert.Equal(t, len(gq.Queries)*8, gq.EstimatedResults)
	})
}
