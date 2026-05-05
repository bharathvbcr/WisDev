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

	session.Query = "planner"
	plan3 := BuildDefaultPlan(session)
	assert.Contains(t, plan3.Steps[0].Reason, "planner")
}

func TestBuildDefaultPlan_StartsRetrievalWithoutModelPrepDependency(t *testing.T) {
	session := &AgentSession{
		SessionID:     "s1",
		OriginalQuery: "RLHF reinforcement learning",
	}

	plan := BuildDefaultPlan(session)
	assert.Len(t, plan.Steps, 3)
	assert.Equal(t, "research.retrievePapers", plan.Steps[2].Action)
	assert.Equal(t, ExecutionTargetGoNative, plan.Steps[2].ExecutionTarget)
	assert.Empty(t, plan.Steps[2].DependsOnStepIDs)

	session.Mode = WisDevModeYOLO
	yoloPlan := BuildDefaultPlan(session)
	assert.Equal(t, "research.retrievePapers", yoloPlan.Steps[2].Action)
	assert.Equal(t, ExecutionTargetGoNative, yoloPlan.Steps[2].ExecutionTarget)
	assert.Empty(t, yoloPlan.Steps[2].DependsOnStepIDs)
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
		assert.Equal(t, "AI", gq.QueryUsed)
	})

	t.Run("Exhaustive Scope with Exclusions", func(t *testing.T) {
		session := &Session{
			CorrectedQuery: "Machine Learning",
			Answers: map[string]Answer{
				"q2_scope":       {Values: []string{"exhaustive"}},
				"q4_subtopics":   {Values: []string{"SVM", "CNN", "RNN"}},
				"q5_study_types": {Values: []string{"Survey"}},
				"q6_exclusions":  {Values: []string{"old"}},
			},
		}
		gq := GenerateSearchQueries(session)
		assert.Contains(t, gq.Queries, "Machine Learning SVM Survey -old")
		assert.Equal(t, len(gq.Queries)*18, gq.EstimatedResults)
		assert.Equal(t, "Machine Learning", gq.QueryUsed)
	})

	t.Run("Evidence quality and output focus shape generated queries", func(t *testing.T) {
		session := &Session{
			CorrectedQuery: "LLM evaluation",
			Answers: map[string]Answer{
				"q4_subtopics":        {Values: []string{"benchmarks"}},
				"q5_study_types":      {Values: []string{"ablation_study"}},
				"q6_exclusions":       {Values: []string{"none"}},
				"q7_evidence_quality": {Values: []string{"peer_reviewed", "methods_transparency"}},
				"q8_output_focus":     {Values: []string{"method_comparison"}},
			},
		}
		gq := GenerateSearchQueries(session)
		expected := "LLM evaluation benchmarks ablation study peer reviewed methods transparency method comparison"
		assert.Contains(t, gq.Queries, expected)
		assert.NotContains(t, gq.Queries, expected+" -none")
		assert.Equal(t, []string{"LLM evaluation ablation study peer reviewed methods transparency method comparison"}, gq.CoverageMap["evidence_quality"])
		assert.Equal(t, []string{"LLM evaluation ablation study peer reviewed methods transparency method comparison"}, gq.CoverageMap["output_focus"])
	})

	t.Run("Focused Scope", func(t *testing.T) {
		session := &Session{
			OriginalQuery: "Test",
			Answers: map[string]Answer{
				"q2_scope":     {Values: []string{"focused"}},
				"q4_subtopics": {Values: []string{"S1", "S2", "S3", "S4", "S5"}},
			},
		}
		gq := GenerateSearchQueries(session)
		assert.True(t, len(gq.Queries) <= 4)
		assert.Equal(t, len(gq.Queries)*8, gq.EstimatedResults)
		assert.Equal(t, "Test", gq.QueryUsed)
	})

	t.Run("Planner Query Takes Precedence", func(t *testing.T) {
		session := &Session{
			Query:          "planner query",
			OriginalQuery:  "original query",
			CorrectedQuery: "corrected query",
			Answers:        map[string]Answer{},
		}
		gq := GenerateSearchQueries(session)
		assert.Contains(t, gq.Queries, "planner query")
		assert.NotContains(t, gq.Queries, "corrected query")
		assert.Equal(t, "planner query", gq.QueryUsed)
	})

	t.Run("Empty query returns zero queries", func(t *testing.T) {
		session := &Session{
			Query:          "",
			OriginalQuery:  "",
			CorrectedQuery: "",
			Answers:        map[string]Answer{},
		}
		gq := GenerateSearchQueries(session)
		assert.Empty(t, gq.Queries)
		assert.Equal(t, 0, gq.QueryCount)
		assert.Equal(t, 0, gq.EstimatedResults)
		assert.Empty(t, gq.CoverageMap)
		assert.Empty(t, gq.QueryUsed)
	})

	t.Run("Whitespace-only query returns zero queries", func(t *testing.T) {
		session := &Session{
			Query:          "   ",
			OriginalQuery:  "  \t  ",
			CorrectedQuery: " \n ",
			Answers: map[string]Answer{
				"q4_subtopics": {Values: []string{"ML", "NLP"}},
			},
		}
		gq := GenerateSearchQueries(session)
		assert.Empty(t, gq.Queries)
		assert.Equal(t, 0, gq.QueryCount)
		assert.Equal(t, 0, gq.EstimatedResults)
		assert.Empty(t, gq.QueryUsed)
	})

	t.Run("No subtopics returns base query only", func(t *testing.T) {
		session := &Session{
			OriginalQuery: "transformer architectures",
			Answers:       map[string]Answer{},
		}
		gq := GenerateSearchQueries(session)
		assert.Len(t, gq.Queries, 1)
		assert.Contains(t, gq.Queries, "transformer architectures")
		assert.Equal(t, 1, gq.QueryCount)
		assert.Equal(t, "transformer architectures", gq.QueryUsed)
	})

	t.Run("Corrected query used when original empty", func(t *testing.T) {
		session := &Session{
			CorrectedQuery: "corrected BERT fairness",
			OriginalQuery:  "",
			Answers:        map[string]Answer{},
		}
		gq := GenerateSearchQueries(session)
		assert.Len(t, gq.Queries, 1)
		assert.Contains(t, gq.Queries, "corrected BERT fairness")
		assert.Equal(t, "corrected BERT fairness", gq.QueryUsed)
	})

	t.Run("Empty exclusion strings must not produce broken query suffix", func(t *testing.T) {
		// Regression for P3-8: an empty string in the exclusions answer
		// previously produced " -" appended to every subtopic query, which
		// is a syntactically broken query that search backends reject with
		// zero results. After the fix, empty exclusions are skipped.
		session := &Session{
			OriginalQuery: "CRISPR gene editing",
			Answers: map[string]Answer{
				"q4_subtopics":  {Values: []string{"cancer therapy"}},
				"q6_exclusions": {Values: []string{"", "  "}}, // all empty/whitespace
			},
		}
		gq := GenerateSearchQueries(session)
		for _, q := range gq.Queries {
			assert.NotContains(t, q, " -",
				"query must not contain a dangling exclusion suffix when all exclusions are empty: %q", q)
		}
		assert.Contains(t, gq.Queries, "CRISPR gene editing cancer therapy",
			"subtopic query should be clean without an exclusion suffix")
	})

	t.Run("Valid exclusions are still applied correctly", func(t *testing.T) {
		session := &Session{
			OriginalQuery: "machine learning",
			Answers: map[string]Answer{
				"q4_subtopics":  {Values: []string{"reinforcement"}},
				"q6_exclusions": {Values: []string{"", "survey", "  "}}, // mix of empty and valid
			},
		}
		gq := GenerateSearchQueries(session)
		// The non-empty exclusion "survey" should appear; the empty ones should not
		// produce a bare " -" prefix.
		for _, q := range gq.Queries {
			if q == "machine learning" {
				continue // base query has no exclusions
			}
			assert.Contains(t, q, "-survey",
				"valid exclusion 'survey' must appear in subtopic query: %q", q)
			assert.NotContains(t, q, "- ",
				"empty exclusion must not leave a standalone dash in query: %q", q)
		}
	})
}

func TestBuildDefaultPlan_NilSessionSafety(t *testing.T) {
	// Regression for P5-6: BuildDefaultPlan dereferenced session fields before
	// the nil guard, causing a nil-pointer panic when session == nil.
	// After the fix, a nil session returns nil gracefully.
	assert.NotPanics(t, func() {
		result := BuildDefaultPlan(nil)
		assert.Nil(t, result, "BuildDefaultPlan(nil) must return nil without panicking")
	})
}

func TestBuildDefaultPlan_ReturnsNonNilForValidSession(t *testing.T) {
	session := &AgentSession{
		SessionID:     "plan-test",
		OriginalQuery: "quantum computing fault tolerance",
	}
	result := BuildDefaultPlan(session)
	assert.NotNil(t, result)
	assert.NotEmpty(t, result.PlanID)
	assert.NotEmpty(t, result.Steps)
}
