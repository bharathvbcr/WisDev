package wisdev

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
)

func TestAutonomousLoopHelpers_QueryManagement(t *testing.T) {
	t.Run("appendUniqueLoopQuery deduplicates and trims", func(t *testing.T) {
		queries := appendUniqueLoopQuery([]string{"Sleep and memory", "memory systems"}, "  sleep and memory  ")
		assert.Equal(t, []string{"Sleep and memory", "memory systems"}, queries)

		queries = appendUniqueLoopQuery(queries, "  contrast  ")
		assert.Equal(t, []string{"Sleep and memory", "memory systems", "contrast"}, queries)

		queries = appendUniqueLoopQuery(queries, " ")
		assert.Equal(t, []string{"Sleep and memory", "memory systems", "contrast"}, queries)
	})

	t.Run("filterUnexecutedLoopQueries trims and deduplicates", func(t *testing.T) {
		pending := filterUnexecutedLoopQueries(
			[]string{"Sleep", "  contrast", "RECALL", "recall ", " ", "SLEEP"},
			[]string{" recall ", "Noise"},
		)
		assert.Equal(t, []string{"Sleep", "contrast"}, pending)
	})

	t.Run("record and clone loop coverage", func(t *testing.T) {
		coverage := map[string][]search.Paper{
			"  primary ": {
				{ID: "p1", Title: "Primary"},
			},
			"invalid":    {{ID: "skip"}},
			"secondary ": nil,
		}
		recordLoopQueryCoverage(coverage, "", []search.Paper{{ID: "p2"}})
		recordLoopQueryCoverage(coverage, "primary", []search.Paper{{ID: "p2"}})
		recordLoopQueryCoverage(nil, "none", []search.Paper{{ID: "p3"}})

		clone := cloneLoopQueryCoverage(coverage)
		assert.Len(t, clone["primary"], 1)
		assert.Contains(t, []string{"p1", "p2"}, clone["primary"][0].ID)
		assert.Equal(t, 0, len(clone["secondary"]))
		assert.Equal(t, "skip", clone["invalid"][0].ID)
	})
}

func TestAutonomousLoopHelpers_DedupAndBounds(t *testing.T) {
	t.Run("appendUniqueSearchPapersWithinBudget deduplicates keys and budget", func(t *testing.T) {
		existing := []search.Paper{
			{ID: "p1", Title: "Primary"},
		}
		incoming := []search.Paper{
			{ID: "p1", Title: "Primary duplicate by ID"},
			{ID: "p2", DOI: "doi-1", Title: "Secondary"},
			{ID: "", DOI: "doi-1", Title: "Duplicate DOI"},
			{ID: "p3", Title: "Third"},
		}

		merged, accepted := appendUniqueSearchPapersWithinBudget(existing, incoming, 2)
		assert.Len(t, merged, 2)
		assert.Len(t, accepted, 2)
		assert.Equal(t, "p1", merged[0].ID)
		assert.Equal(t, "p2", merged[1].ID)
		assert.Equal(t, "p1", accepted[0].ID)
		assert.Equal(t, "p2", accepted[1].ID)

		merged, accepted = appendUniqueSearchPapersWithinBudget(existing, incoming, 0)
		assert.Len(t, merged, 4)
		assert.Len(t, accepted, 4)
		assert.Equal(t, "p1", accepted[0].ID)
		assert.Equal(t, "p2", accepted[1].ID)
		assert.Equal(t, "", accepted[2].ID)
		assert.Equal(t, "p3", accepted[3].ID)
	})

	t.Run("searchPaperDedupKey respects field precedence", func(t *testing.T) {
		assert.Equal(t, "p1", searchPaperDedupKey(search.Paper{
			ID:    "P1",
			DOI:   "10.1/abc",
			Link:  "https://alt",
			Title: "Fallback",
		}))
		assert.Equal(t, "p1", searchPaperDedupKey(search.Paper{
			ID:    "p1",
			DOI:   "10.1/abc",
			Link:  "https://alt",
			Title: "Fallback",
		}))
		assert.Equal(t, "https://alt", searchPaperDedupKey(search.Paper{
			ID:    "   ",
			DOI:   "  ",
			Link:  "https://alt",
			Title: "Fallback",
		}))
		assert.Equal(t, "fallback", searchPaperDedupKey(search.Paper{
			ID:    "   ",
			DOI:   "  ",
			Link:  "  ",
			Title: "Fallback",
		}))
		assert.Equal(t, "", searchPaperDedupKey(search.Paper{}))
	})
}

func TestAutonomousLoopHelpers_Resolution(t *testing.T) {
	t.Run("resolvers cover defaults", func(t *testing.T) {
		assert.Equal(t, 10, resolveLoopHitsPerSearch(0))
		assert.Equal(t, 10, resolveLoopHitsPerSearch(-5))
		assert.Equal(t, 7, resolveLoopHitsPerSearch(7))

		assert.Equal(t, 0, resolveLoopSearchTermBudget(0, 0))
		assert.Equal(t, 5, resolveLoopSearchTermBudget(5, 0))
		assert.Equal(t, 5, resolveLoopSearchTermBudget(0, 5))
		assert.Equal(t, 3, resolveLoopSearchTermBudget(8, 3))
		assert.Equal(t, 6, resolveLoopQueryParallelism(string(WisDevModeYOLO), ResearchExecutionPlaneAutonomous))
		assert.Equal(t, 4, resolveLoopQueryParallelism(string(WisDevModeYOLO), ""))
		assert.Equal(t, 3, resolveLoopQueryParallelism(string(WisDevModeGuided), ResearchExecutionPlaneAutonomous))
		assert.Equal(t, 3, resolveLoopQueryParallelism("weird-mode", ResearchExecutionPlaneAutonomous))
		assert.Equal(t, "deep", string(firstResearchExecutionPlane(ResearchExecutionPlaneDeep, "")))
		assert.Equal(t, "", string(firstResearchExecutionPlane("", " ", "")))
		assert.True(t, isHighDepthResearchPlane(ResearchExecutionPlaneMultiAgent))
		assert.True(t, isHighDepthResearchPlane(ResearchExecutionPlaneAutonomous))
		assert.False(t, isHighDepthResearchPlane(ResearchExecutionPlaneSimple))
	})

	t.Run("adaptive parallelism scales with confidence", func(t *testing.T) {
		loop := &AutonomousLoop{}
		assert.Equal(t, 4, loop.resolveAdaptiveParallelism(string(WisDevModeGuided), 0.2, ResearchExecutionPlaneSimple))
		assert.Equal(t, 4, loop.resolveAdaptiveParallelism(string(WisDevModeGuided), 0.1, ResearchExecutionPlaneSimple))
		assert.Equal(t, 6, loop.resolveAdaptiveParallelism(string(WisDevModeYOLO), 0.2, ResearchExecutionPlaneSimple))
		assert.Equal(t, 6, loop.resolveAdaptiveParallelism(string(WisDevModeYOLO), 0.5, ResearchExecutionPlaneAutonomous))
	})

	t.Run("adaptive loop recursion and critique limits", func(t *testing.T) {
		assert.Equal(t, 5, resolveLoopGapRecursionDepth(string(WisDevModeYOLO), ResearchExecutionPlaneAutonomous))
		assert.Equal(t, 4, resolveLoopGapRecursionDepth(string(WisDevModeYOLO), ResearchExecutionPlaneSimple))
		assert.Equal(t, 3, resolveLoopGapRecursionDepth("guided", ResearchExecutionPlaneAutonomous))
		assert.Equal(t, 2, resolveLoopGapRecursionDepth("legacy", ResearchExecutionPlaneSimple))

		assert.Equal(t, 6, resolveCritiqueFollowUpLimit(string(WisDevModeYOLO), ResearchExecutionPlaneAutonomous))
		assert.Equal(t, 4, resolveCritiqueFollowUpLimit(string(WisDevModeYOLO), ResearchExecutionPlaneSimple))
		assert.Equal(t, 4, resolveCritiqueFollowUpLimit("guided", ResearchExecutionPlaneAutonomous))
		assert.Equal(t, 3, resolveCritiqueFollowUpLimit("guided", ResearchExecutionPlaneSimple))
	})

	t.Run("resolveLoopMaxIterations clamps by search terms and budget constraints", func(t *testing.T) {
		assert.Equal(t, 0, resolveLoopMaxIterations(0, 3))
		assert.Equal(t, -3, resolveLoopMaxIterations(-3, 3))
		assert.Equal(t, 8, resolveLoopMaxIterations(8, 16))
		assert.Equal(t, 8, resolveLoopMaxIterations(8, 0))
		assert.Equal(t, 7, resolveLoopMaxIterations(8, 7))
		assert.Equal(t, 8, resolveLoopMaxIterations(8, 12))
	})

	t.Run("dynamic provider selection honors cooldown and plane", func(t *testing.T) {
		assert.False(t, shouldUseDynamicProviderSelection(string(WisDevModeGuided), ResearchExecutionPlaneAutonomous, false, nil))
		assert.False(t, shouldUseDynamicProviderSelection(string(WisDevModeGuided), ResearchExecutionPlaneAutonomous, true, nil))
		assert.True(t, shouldUseDynamicProviderSelection(string(WisDevModeGuided), ResearchExecutionPlaneDeep, true, &llm.Client{VertexDirect: &llm.VertexClient{}}))
		assert.False(t, shouldUseDynamicProviderSelection(string(WisDevModeGuided), ResearchExecutionPlaneSimple, true, &llm.Client{VertexDirect: &llm.VertexClient{}}))
		assert.True(t, shouldUseDynamicProviderSelection(string(WisDevModeYOLO), ResearchExecutionPlaneSimple, true, &llm.Client{VertexDirect: &llm.VertexClient{}}))
	})

	t.Run("nextLoopQueryBatch slices pending queries", func(t *testing.T) {
		pending := []string{"q1", "", "Q2", "Q3"}
		batch := nextLoopQueryBatch(&pending, 2)
		assert.Equal(t, []string{"q1", ""}, batch)
		assert.Equal(t, []string{"Q2", "Q3"}, pending)

		assert.Nil(t, nextLoopQueryBatch(nil, 2))
		assert.Nil(t, nextLoopQueryBatch(&pending, 0))
		pending = []string{}
		assert.Nil(t, nextLoopQueryBatch(&pending, 1))
	})

	t.Run("emitLoopProgress handles nil emitters and normalizes payload", func(t *testing.T) {
		emitLoopProgress(nil, LoopRequest{}, " skipped ", " skipped ", nil)

		var seen []PlanExecutionEvent
		emitLoopProgress(
			func(event PlanExecutionEvent) { seen = append(seen, event) },
			LoopRequest{
				ProjectID:     "  project-42 ",
				ResearchPlane: ResearchExecutionPlaneDeep,
			},
			" stage ",
			"  message in progress  ",
			nil,
		)

		assert.Len(t, seen, 1)
		assert.Equal(t, EventProgress, seen[0].Type)
		assert.Equal(t, "project-42", seen[0].SessionID)
		assert.Equal(t, "wisdev-agent-os/orchestrator/internal/wisdev", seen[0].OwningComponent)
		assert.Equal(t, "wisdev.autonomous", seen[0].Payload["component"])
		assert.Equal(t, "research_loop", seen[0].Payload["operation"])
		assert.Equal(t, "stage", seen[0].Payload["stage"])
		assert.Equal(t, string(ResearchExecutionPlaneDeep), seen[0].Payload["researchPlane"])
		assert.Equal(t, "message in progress", seen[0].Message)
	})
}

func TestAutonomousLoopHelpers_GapAndCoverageSignals(t *testing.T) {
	plane := ResearchExecutionPlaneDeep

	t.Run("closeRecursiveCoverageGaps returns early when converged with no open gaps", func(t *testing.T) {
		loop := NewAutonomousLoop(search.NewProviderRegistry(), nil)
		result, err := loop.closeRecursiveCoverageGaps(
			context.Background(),
			LoopRequest{Query: "test", ResearchPlane: plane},
			[]string{"q1"},
			[]string{"q1"},
			map[string][]search.Paper{
				"q1": {{ID: "p1"}},
			},
			[]search.Paper{{ID: "p1"}},
			nil,
			&sufficiencyAnalysis{Sufficient: true},
			true,
			0,
			0,
			0,
			2,
			func(event PlanExecutionEvent) {},
		)
		t.Logf("before assertions: gap NextQueries=%v, MissingAspects=%v, MissingSourceTypes=%v, LedgerOpen=%d, Planned=%v, Executed=%v, Papers=%d",
			result.GapAnalysis.NextQueries,
			result.GapAnalysis.MissingAspects,
			result.GapAnalysis.MissingSourceTypes,
			countOpenCoverageLedgerEntries(result.GapAnalysis.Ledger),
			result.PlannedQueries,
			result.ExecutedQueries,
			len(result.Papers),
		)
		if result.GapAnalysis == nil {
			fmt.Println("gap analysis is nil")
		} else {
			fmt.Printf("gap next=%v missingAspects=%v missingSource=%v openLedger=%d planned=%v executed=%v papers=%d converged=%v\n",
				result.GapAnalysis.NextQueries,
				result.GapAnalysis.MissingAspects,
				result.GapAnalysis.MissingSourceTypes,
				countOpenCoverageLedgerEntries(result.GapAnalysis.Ledger),
				result.PlannedQueries,
				result.ExecutedQueries,
				len(result.Papers),
				result.Converged,
			)
		}
		assert.NoError(t, err)
		assert.NotNil(t, result.Analysis)
		assert.True(t, result.Converged)
		assert.Equal(t, []string{"q1"}, result.PlannedQueries)
		assert.Equal(t, []string{"q1"}, result.ExecutedQueries)
		assert.Len(t, result.Papers, 1)
	})

	t.Run("gap status helpers classify actionable states", func(t *testing.T) {
		assert.False(t, isOpenCoverageLedgerEntryActionable(CoverageLedgerEntry{
			Status:         coverageLedgerStatusOpen,
			ObligationType: "budget_exhausted",
		}))
		assert.True(t, isOpenCoverageLedgerEntryActionable(CoverageLedgerEntry{
			Status:         coverageLedgerStatusOpen,
			ObligationType: "source_diversity",
		}))

		assert.False(t, hasOpenActionableCoverageGaps(&LoopGapState{
			Ledger: []CoverageLedgerEntry{
				{Status: coverageLedgerStatusResolved, ObligationType: "coverage_gap"},
			},
		}))
		assert.True(t, hasOpenActionableCoverageGaps(&LoopGapState{
			Ledger: []CoverageLedgerEntry{
				{Status: coverageLedgerStatusOpen, ObligationType: "source_diversity"},
			},
		}))
		assert.True(t, hasOpenActionableCoverageGaps(&LoopGapState{
			NextQueries: []string{"alternative evidence"},
		}))
		assert.True(t, hasOpenActionableCoverageGaps(&LoopGapState{
			MissingAspects: []string{"counter-evidence"},
		}))
	})

	t.Run("determineAutonomousStopReason falls back through default branches", func(t *testing.T) {
		assert.Equal(t, "runtime_no_result", determineAutonomousStopReason(nil))
		assert.Equal(t, "no_grounded_sources", determineAutonomousStopReason(&LoopResult{}))
		assert.Equal(t, "coverage_satisfied", determineAutonomousStopReason(&LoopResult{
			Converged: true,
			Papers:    []search.Paper{{ID: "p"}},
		}))
	})
}

func TestAutonomousLoopHelpers_AdaptiveBudgetsAndLLMJSON(t *testing.T) {
	t.Run("computeAdaptiveBudgets handles early exits", func(t *testing.T) {
		loop := &AutonomousLoop{}

		loop.computeAdaptiveBudgets([]*Hypothesis{}, 3)
		loop.computeAdaptiveBudgets([]*Hypothesis{
			{ConfidenceScore: 0.7, IsTerminated: true},
			{ConfidenceScore: 0.1},
		}, 0)
	})

	t.Run("computeAdaptiveBudgets allocates budgets with uncertainty and rounding guards", func(t *testing.T) {
		loop := &AutonomousLoop{}
		hypotheses := []*Hypothesis{
			{ConfidenceScore: 0.2},
			{ConfidenceScore: 1.0, AllocatedQueryBudget: 9, IsTerminated: true},
			{ConfidenceScore: 0.6},
		}

		loop.computeAdaptiveBudgets(hypotheses, 4)
		assert.Equal(t, 3, hypotheses[0].AllocatedQueryBudget)
		assert.Equal(t, 9, hypotheses[1].AllocatedQueryBudget)
		assert.Equal(t, 1, hypotheses[2].AllocatedQueryBudget)
	})

	t.Run("unmarshalLLMJSON parses exact json only and rejects wrapped payloads", func(t *testing.T) {
		type payload struct {
			Name string `json:"name"`
		}

		var p payload
		assert.NoError(t, unmarshalLLMJSON("{\"name\":\"alpha\"}", &p))
		assert.Equal(t, "alpha", p.Name)

		var p2 payload
		assert.Error(t, unmarshalLLMJSON("```json\n{\"name\":\"bravo\"}\n```", &p2))

		var p3 payload
		assert.Error(t, unmarshalLLMJSON("prefix {\"name\":\"charlie\"} suffix", &p3))

		var p4 payload
		assert.Error(t, unmarshalLLMJSON("nonsense", &p4))
	})

}

func TestAutonomousLoopHelpers_ExecutionAndLimits(t *testing.T) {
	t.Run("executeLoopSearchBatch normalizes queries, preserves order, and captures warnings", func(t *testing.T) {
		reg := search.NewProviderRegistry()
		reg.Register(&mockSearchProvider{
			name: "mock",
			SearchFunc: func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
				if query == "error" {
					return nil, errors.New("provider failed")
				}
				return []search.Paper{{ID: query, Title: "Paper " + query}}, nil
			},
		})

		loop := NewAutonomousLoop(reg, nil)
		results := loop.executeLoopSearchBatch(
			context.Background(),
			[]string{"  q1", "q2", "q1", "", "error"},
			search.SearchOpts{Limit: 3},
			2,
		)

		assert.Len(t, results, 3)
		assert.Equal(t, "q1", results[0].Query)
		assert.Equal(t, "q2", results[1].Query)
		assert.Equal(t, "error", results[2].Query)
		assert.Len(t, results[0].Result.Papers, 1)
		assert.Len(t, results[1].Result.Papers, 1)
		assert.Len(t, results[2].Result.Warnings, 1)
		assert.Equal(t, "provider failed", results[2].Result.Warnings[0].Message)
		assert.Equal(t, "mock", results[2].Result.Warnings[0].Provider)
	})

	t.Run("executeLoopSearchBatch returns nil without loop context", func(t *testing.T) {
		assert.Nil(t, ((*AutonomousLoop)(nil)).executeLoopSearchBatch(context.Background(), []string{"q"}, search.SearchOpts{}, 1))
	})

	t.Run("remainingLoopSearchLimit clamps by budget and maxUnique", func(t *testing.T) {
		assert.Equal(t, 10, remainingLoopSearchLimit(0, 0, 0))
		assert.Equal(t, 5, remainingLoopSearchLimit(3, 5, 8))
		assert.Equal(t, 4, remainingLoopSearchLimit(0, 6, 4))
		assert.Equal(t, 0, remainingLoopSearchLimit(7, 6, 7))
		assert.Equal(t, 3, remainingLoopSearchLimit(5, 3, 20))
	})

	t.Run("executeLoopSearchBatch short-circuits search calls on canceled context", func(t *testing.T) {
		callCount := 0
		reg := search.NewProviderRegistry()
		reg.Register(&mockSearchProvider{
			name: "mock",
			SearchFunc: func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
				callCount++
				return []search.Paper{{ID: query}}, nil
			},
		})
		loop := NewAutonomousLoop(reg, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		results := loop.executeLoopSearchBatch(
			ctx,
			[]string{"  alpha  ", "beta", "beta", ""},
			search.SearchOpts{Limit: 3},
			2,
		)

		assert.Equal(t, 0, callCount)
		assert.Len(t, results, 2)
		assert.Equal(t, "alpha", results[0].Query)
		assert.Equal(t, "beta", results[1].Query)
	})
}

func TestAutonomousLoopHelpers_QueryQueueAssembly(t *testing.T) {
	t.Run("enqueueLoopQuery dedupes normalized queries", func(t *testing.T) {
		seen := map[string]struct{}{}
		pending := []string{}

		assert.True(t, enqueueLoopQuery(&pending, seen, "  Alpha "))
		assert.False(t, enqueueLoopQuery(&pending, seen, "alpha"))
		assert.False(t, enqueueLoopQuery(&pending, seen, "  "))

		assert.Equal(t, []string{"Alpha"}, pending)
	})

	t.Run("buildRecursiveGapFollowUpQueries merges ledger, gap, and analysis queues", func(t *testing.T) {
		gap := &LoopGapState{
			Ledger:      []CoverageLedgerEntry{{SupportingQueries: []string{"gap follow-up"}}},
			NextQueries: []string{"next from gap"},
		}
		analysis := &sufficiencyAnalysis{
			NextQueries: []string{"analysis next", "gap follow-up"},
		}

		queries := buildRecursiveGapFollowUpQueries("base", gap, analysis, 3)
		assert.Len(t, queries, 3)
		assert.ElementsMatch(t, []string{"gap follow-up", "next from gap", "analysis next"}, queries)

		fallback := buildRecursiveGapFollowUpQueries("base", nil, &sufficiencyAnalysis{MissingAspects: []string{"replication"}}, 2)
		assert.Len(t, fallback, 1)
		assert.Equal(t, "base replication", fallback[0])
	})
}

func TestAutonomousLoopHelpers_FollowUpDerivation(t *testing.T) {
	t.Run("deriveLoopFollowUpQueries dedupes and caps candidates", func(t *testing.T) {
		analysis := &sufficiencyAnalysis{
			MissingAspects: []string{"metabolic", "temporal", "causal", "observational", "validation"},
		}

		queries := deriveLoopFollowUpQueries("memory", analysis, nil)
		assert.Len(t, queries, 4)
		assert.Equal(t, "memory metabolic", queries[0])
		assert.Equal(t, "memory temporal", queries[1])
		assert.Equal(t, "memory causal", queries[2])
		assert.Equal(t, "memory observational", queries[3])
	})

	t.Run("deriveLoopFollowUpQueries draws terms from source papers", func(t *testing.T) {
		queries := deriveLoopFollowUpQueries("cognitive architecture", nil, []search.Paper{
			{
				ID:       "p1",
				Title:    "Adaptive cognitive memory systems",
				Abstract: "Adaptive systems can improve memory retrieval and long-term consolidation.",
				Source:   "openalex",
			},
		})

		assert.Len(t, queries, 4)
		assert.ElementsMatch(t,
			[]string{
				"cognitive architecture adaptive",
				"cognitive architecture cognitive",
				"cognitive architecture memory",
				"cognitive architecture improve",
			},
			queries,
		)
	})
}

func TestAutonomousLoopHelpers_FollowUpCapacityLimits(t *testing.T) {
	t.Run("buildRecursiveGapFollowUpQueries respects limit before normalization", func(t *testing.T) {
		gap := &LoopGapState{
			Ledger: []CoverageLedgerEntry{
				{SupportingQueries: []string{"ledger a"}},
			},
			NextQueries: []string{"next b", "next c"},
		}
		analysis := &sufficiencyAnalysis{
			NextQueries: []string{"analysis d", "analysis e"},
		}

		queries := buildRecursiveGapFollowUpQueries("base", gap, analysis, 2)
		assert.Len(t, queries, 2)
		for _, query := range queries {
			assert.NotEmpty(t, query)
		}
	})
}

func TestAutonomousLoopHelpers_RecursiveGapClosureGuardrails(t *testing.T) {
	t.Run("closeRecursiveCoverageGaps returns early when term budget is exhausted", func(t *testing.T) {
		loop := NewAutonomousLoop(search.NewProviderRegistry(), nil)
		result, err := loop.closeRecursiveCoverageGaps(
			context.Background(),
			LoopRequest{Query: "base", Mode: string(WisDevModeGuided), ResearchPlane: ResearchExecutionPlaneDeep},
			[]string{"q1"},
			[]string{"q1", "q2"},
			map[string][]search.Paper{"q1": {{ID: "p1"}}},
			[]search.Paper{{ID: "p1"}},
			nil,
			&sufficiencyAnalysis{Sufficient: false},
			false,
			5,
			12,
			0,
			2,
			nil,
		)

		assert.NoError(t, err)
		assert.Equal(t, []string{"q1"}, result.PlannedQueries)
		assert.Equal(t, []string{"q1", "q2"}, result.ExecutedQueries)
		assert.False(t, result.Converged)
	})

	t.Run("closeRecursiveCoverageGaps returns early when queryParallelism is non-positive", func(t *testing.T) {
		loop := NewAutonomousLoop(search.NewProviderRegistry(), nil)
		result, err := loop.closeRecursiveCoverageGaps(
			context.Background(),
			LoopRequest{Query: "base", Mode: string(WisDevModeGuided), ResearchPlane: ResearchExecutionPlaneDeep},
			[]string{"q1"},
			[]string{"q1", "q2"},
			map[string][]search.Paper{"q1": {{ID: "p1"}}},
			[]search.Paper{{ID: "p1"}},
			nil,
			&sufficiencyAnalysis{Sufficient: false},
			false,
			5,
			12,
			12,
			0,
			nil,
		)

		assert.NoError(t, err)
		assert.Equal(t, []string{"q1"}, result.PlannedQueries)
		assert.Equal(t, []string{"q1", "q2"}, result.ExecutedQueries)
		assert.False(t, result.Converged)
	})

	t.Run("closeRecursiveCoverageGaps does not recurse when no actionable gaps remain", func(t *testing.T) {
		callCount := 0
		reg := search.NewProviderRegistry()
		reg.Register(&mockSearchProvider{
			name: "mock",
			SearchFunc: func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
				callCount++
				return []search.Paper{}, nil
			},
		})
		loop := NewAutonomousLoop(search.NewProviderRegistry(), nil)
		result, err := loop.closeRecursiveCoverageGaps(
			context.Background(),
			LoopRequest{Query: "base", Mode: string(WisDevModeGuided), ResearchPlane: ResearchExecutionPlaneSimple},
			[]string{"q1"},
			[]string{"q1"},
			map[string][]search.Paper{"q1": {{ID: "p1", FullText: "available"}}},
			[]search.Paper{{ID: "p1", FullText: "available"}},
			nil,
			&sufficiencyAnalysis{Sufficient: false},
			false,
			5,
			12,
			12,
			3,
			nil,
		)

		assert.NoError(t, err)
		assert.NotNil(t, result.GapAnalysis)
		assert.False(t, hasOpenActionableCoverageGaps(result.GapAnalysis))
		assert.Empty(t, result.GapAnalysis.NextQueries)
		assert.Empty(t, result.GapAnalysis.MissingAspects)
		assert.Empty(t, result.GapAnalysis.MissingSourceTypes)
		assert.Equal(t, 0, callCount)
		assert.Equal(t, []string{"q1"}, result.PlannedQueries)
		assert.Equal(t, []string{"q1"}, result.ExecutedQueries)
		assert.Len(t, result.Papers, 1)
		assert.False(t, result.Converged)
	})

	t.Run("closeRecursiveCoverageGaps dedupes candidates against seen queries", func(t *testing.T) {
		callCount := 0
		callQueries := make([]string, 0, 3)
		reg := search.NewProviderRegistry()
		reg.Register(&mockSearchProvider{
			name: "mock",
			SearchFunc: func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
				callCount++
				callQueries = append(callQueries, query)
				return []search.Paper{{ID: "unique-paper"}}, nil
			},
		})
		loop := NewAutonomousLoop(reg, nil)
		result, err := loop.closeRecursiveCoverageGaps(
			context.Background(),
			LoopRequest{
				Query:         "base",
				Mode:          string(WisDevModeGuided),
				ResearchPlane: ResearchExecutionPlaneSimple,
			},
			[]string{"seed"},
			[]string{"seed"},
			map[string][]search.Paper{"seed": {{ID: "p1", FullText: "available"}}},
			[]search.Paper{{ID: "p1", FullText: "available"}},
			map[string]struct{}{"seed": {}, "alpha": {}},
			&sufficiencyAnalysis{
				NextQueries: []string{"alpha", "beta", "beta"},
			},
			false,
			5,
			5,
			2,
			3,
			nil,
		)

		assert.NoError(t, err)
		assert.True(t, hasOpenActionableCoverageGaps(result.GapAnalysis))
		assert.Equal(t, 1, callCount)
		assert.Equal(t, []string{"beta"}, callQueries)
		assert.ElementsMatch(t, []string{"seed", "beta"}, result.PlannedQueries)
		assert.Contains(t, result.ExecutedQueries, "beta")
		assert.Len(t, result.Papers, 2)
	})
}

func TestAutonomousLoopHelpers_EvidenceSignals(t *testing.T) {
	t.Run("loopEvidenceTokens filters punctuation and stopwords", func(t *testing.T) {
		tokens := loopEvidenceTokens("The quick-brown, foxes and the moonlight")
		assert.Equal(t, []string{"quick", "brown", "foxes", "moonlight"}, tokens)
		assert.Empty(t, loopEvidenceTokens("   and the  "))
	})

	t.Run("loopEvidenceOverlap counts shared tokens", func(t *testing.T) {
		keywords := loopEvidenceTokenSet("gamma radiation and stars")
		assert.Equal(t, 1, loopEvidenceOverlap(keywords, "gamma rays observed", "stellar"))
		assert.Equal(t, 1, loopEvidenceOverlap(map[string]struct{}{"alpha": {}}, "alpha beta"))
	})

	t.Run("selectLoopHypothesisEvidence applies source and overlap gates", func(t *testing.T) {
		findings := []EvidenceFinding{
			{
				ID:         "e1",
				Claim:      "phase transition controls learning",
				Snippet:    "A study found phase transitions increase accuracy.",
				PaperTitle: "paper-a",
				SourceID:   "source-a",
				Confidence: 0.73,
			},
			{
				ID:         "e2",
				Claim:      "orthogonal evidence from unrelated context",
				Snippet:    "This appears unrelated to the claim.",
				PaperTitle: "paper-b",
				SourceID:   "source-b",
				Confidence: 0.65,
			},
		}
		index := map[string]map[string]struct{}{
			"climate warming": {"source-a": {}},
		}
		selected := selectLoopHypothesisEvidence("climate warming", "climate warming", "", findings, index, 2)
		assert.Len(t, selected, 1)
		assert.Equal(t, "e1", selected[0].ID)

		selected = selectLoopHypothesisEvidence("climate warming", "missing-source-query", "", findings, nil, 1)
		assert.Len(t, selected, 0)
	})

	t.Run("support text and confidence fallback", func(t *testing.T) {
		assert.Equal(t, "alpha beta", buildLoopHypothesisSupportText("alpha", " beta "))
		assert.Equal(t, 0.55, averageLoopEvidenceConfidence(nil, 0))
		assert.Equal(t, 0.45, averageLoopEvidenceConfidence([]*EvidenceFinding{
			{Confidence: 0.2},
		}, 0))
		assert.Equal(t, 0.7, averageLoopEvidenceConfidence([]*EvidenceFinding{
			{Confidence: 0.6},
			{Confidence: 0.8},
		}, 0.55))
	})
}
