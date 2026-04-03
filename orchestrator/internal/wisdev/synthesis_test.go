package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSynthesis_ScorePlanCandidate(t *testing.T) {
	steps := []PlanStep{{Action: "research.verifyCitations"}, {Action: "research.buildClaimEvidenceTable"}}
	
	t.Run("General", func(t *testing.T) {
		s := scorePlanCandidate("query", "general", steps)
		assert.Greater(t, s, 0.5)
	})

	t.Run("Systematic Review", func(t *testing.T) {
		s := scorePlanCandidate("systematic review", "general", steps)
		assert.Greater(t, s, 0.6)
	})

	t.Run("Medicine", func(t *testing.T) {
		s := scorePlanCandidate("test", "medicine", steps)
		assert.Greater(t, s, 0.6)
	})

	t.Run("Novelty with snowball", func(t *testing.T) {
		stepsSnowball := []PlanStep{{Action: "research.snowballCitations"}}
		s := scorePlanCandidate("novel hypothesis", "general", stepsSnowball)
		assert.Greater(t, s, 0.6)
	})
}

func TestSynthesis_SynthesizePlanCandidates(t *testing.T) {
	session := &AgentSession{DetectedDomain: "cs"}

	t.Run("Default quick", func(t *testing.T) {
		res := SynthesizePlanCandidates(session, "quick overview")
		assert.Equal(t, 2, res.BranchWidth)
		assert.Equal(t, "fast_balanced", res.Selected.Hypothesis)
	})

	t.Run("Deep systematic", func(t *testing.T) {
		res := SynthesizePlanCandidates(session, "thorough systematic review")
		assert.Equal(t, 4, res.BranchWidth)
		assert.Equal(t, "verification_first", res.Selected.Hypothesis)
	})

	t.Run("Exploration heavy", func(t *testing.T) {
		res := SynthesizePlanCandidates(session, "frontier research")
		assert.Equal(t, "exploration_then_grounding", res.Selected.Hypothesis)
	})

	t.Run("With Priors", func(t *testing.T) {
		RecordPlanOutcome(wisdevOutcomeSummary{
			Query:      "ai",
			Success:    true,
			Hypothesis: "balanced_evidence_first",
			FinalReward: 1.0,
		})
		res := SynthesizePlanCandidates(session, "ai research")
		assert.Equal(t, "balanced_evidence_first", res.Selected.Hypothesis)
	})
}

func TestSynthesis_Helpers(t *testing.T) {
	assert.True(t, containsAny("hello world", "world"))
	assert.False(t, containsAny("hello", "foo"))

	step := CreatePlanStep("id", "act", "res", RiskLevelHigh, ExecutionTargetGoNative, true)
	assert.Equal(t, ModelTierHeavy, step.ModelTier)
	
	ps := newPlanState("pid", nil)
	assert.Equal(t, "pid", ps.PlanID)
}
