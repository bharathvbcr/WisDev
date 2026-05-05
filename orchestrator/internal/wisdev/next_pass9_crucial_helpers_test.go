package wisdev

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextPass9CrucialWisdevHelpers(t *testing.T) {
	t.Run("gap accessors return nil safely and expose populated fields", func(t *testing.T) {
		assert.Nil(t, gapLedger(nil))
		assert.Nil(t, gapObservedSourceFamilies(nil))
		assert.Nil(t, gapMissingAspects(nil))
		assert.Nil(t, gapMissingSourceTypes(nil))
		assert.Nil(t, gapContradictions(nil))

		gap := &LoopGapState{
			Ledger:                 []CoverageLedgerEntry{{ID: "ledger-1"}},
			ObservedSourceFamilies: []string{"pubmed"},
			MissingAspects:         []string{"replication"},
			MissingSourceTypes:     []string{"full_text"},
			Contradictions:         []string{"conflict"},
		}
		assert.Equal(t, gap.Ledger, gapLedger(gap))
		assert.Equal(t, gap.ObservedSourceFamilies, gapObservedSourceFamilies(gap))
		assert.Equal(t, gap.MissingAspects, gapMissingAspects(gap))
		assert.Equal(t, gap.MissingSourceTypes, gapMissingSourceTypes(gap))
		assert.Equal(t, gap.Contradictions, gapContradictions(gap))
	})

	t.Run("float coercion covers supported runtime payload shapes", func(t *testing.T) {
		for _, tc := range []struct {
			value any
			want  float64
		}{
			{float64(1.25), 1.25},
			{float32(2.5), 2.5},
			{3, 3},
			{int32(4), 4},
			{int64(5), 5},
			{json.Number("6.75"), 6.75},
			{" 7.5 ", 7.5},
			{json.Number("bad"), 0},
			{"bad", 0},
			{nil, 0},
		} {
			assert.Equal(t, tc.want, floatFromAny(tc.value), "value=%v", tc.value)
		}
	})

	t.Run("worker query helpers map focus, domains, and completion state", func(t *testing.T) {
		assert.Equal(t, "focus only", buildResearchWorkerQuery("", " focus only "))
		assert.Equal(t, "root only", buildResearchWorkerQuery(" root only ", ""))
		assert.Equal(t, "root focus", buildResearchWorkerQuery(" root ", " focus "))

		assert.Equal(t, "clinical trial cohort guideline evidence", domainSpecificEvidenceQuery("clinical"))
		assert.Equal(t, "clinical trial cohort guideline evidence", domainSpecificEvidenceQuery("medicine"))
		assert.Equal(t, "benchmark ablation reproducibility dataset", domainSpecificEvidenceQuery("machine learning"))
		assert.Equal(t, "benchmark ablation reproducibility dataset", domainSpecificEvidenceQuery("cs"))
		assert.Equal(t, "primary source precedent review", domainSpecificEvidenceQuery("legal"))
		assert.Equal(t, "independent source triangulation evidence", domainSpecificEvidenceQuery("history"))

		assert.False(t, researchWorkersExecuted(nil))
		assert.False(t, researchWorkersExecuted([]ResearchWorkerState{{Status: "running"}}))
		assert.True(t, researchWorkersExecuted([]ResearchWorkerState{{Status: " completed "}}))
	})

	t.Run("branch revision pressure counts only open or unexecuted branches", func(t *testing.T) {
		pressure := branchRevisionPressure([]ResearchBranchEvaluation{
			{Query: "open", OpenGaps: []string{"missing"}},
			{Query: "unexecuted", StopReason: "branch_unexecuted"},
			{Query: "needs evidence", StopReason: "branch_needs_evidence"},
			{Query: "complete", StopReason: "complete"},
		})
		assert.Equal(t, 3, pressure)
		assert.Equal(t, 0, branchRevisionPressure(nil))
	})

	t.Run("worker branch plan extraction supports typed maps and loose artifacts", func(t *testing.T) {
		typed := defaultResearchBranchPlan("root", "typed query", "typed-id")
		plans := researchBranchPlansFromWorkerReports("root", []ResearchWorkerState{
			{Role: ResearchWorkerScout, Artifacts: map[string]any{"branchPlans": []ResearchBranchPlan{typed}}},
			{Role: ResearchWorkerCitationGraph, Artifacts: map[string]any{"branchPlans": []map[string]any{
				{"query": "mapped query", "retrieval_plan": []any{"mapped query", "mapped verification"}},
			}}},
			{Role: ResearchWorkerSourceDiversifier, Artifacts: map[string]any{"branchPlans": []any{
				map[string]any{"title": "loose query", "queries": "loose verification"},
				"bad-shape",
			}}},
			{Role: ResearchWorkerIndependentVerifier, Artifacts: map[string]any{}},
		})

		require.Len(t, plans, 3)
		assert.Equal(t, "typed query", plans[0].Query)
		assert.Equal(t, "mapped query", plans[1].Query)
		assert.Contains(t, plans[1].RetrievalPlan, "root")
		assert.Contains(t, plans[1].RetrievalPlan, "mapped verification")
		assert.Equal(t, "loose query", plans[2].Query)
		assert.Contains(t, plans[2].RetrievalPlan, "loose verification")
	})
}
