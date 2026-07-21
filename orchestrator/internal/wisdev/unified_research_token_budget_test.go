package wisdev

import (
	"context"
	"strings"
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/resilience"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

func TestResolveResearchTokenBudgetTokensEnforcesDefault(t *testing.T) {
	t.Setenv(researchTokenBudgetEnvVar, "")
	if got := resolveResearchTokenBudgetTokens(0); got != defaultResearchTokenBudget {
		t.Fatalf("expected default budget when env is unset, got %d", got)
	}
	t.Setenv(researchTokenBudgetEnvVar, "0")
	if got := resolveResearchTokenBudgetTokens(0); got != defaultResearchTokenBudget {
		t.Fatalf("expected default budget when env is zero, got %d", got)
	}
	t.Setenv(researchTokenBudgetEnvVar, "-25")
	if got := resolveResearchTokenBudgetTokens(0); got != defaultResearchTokenBudget {
		t.Fatalf("expected default budget when env is negative, got %d", got)
	}
	t.Setenv(researchTokenBudgetEnvVar, "not-a-number")
	if got := resolveResearchTokenBudgetTokens(0); got != defaultResearchTokenBudget {
		t.Fatalf("expected default budget when env is invalid, got %d", got)
	}
	t.Setenv(researchTokenBudgetEnvVar, "123456")
	if got := resolveResearchTokenBudgetTokens(0); got != 123456 {
		t.Fatalf("expected env override, got %d", got)
	}
	if got := resolveResearchTokenBudgetTokens(999999); got != 999999 {
		t.Fatalf("expected larger request allocation to raise the ceiling, got %d", got)
	}
	if got := resolveResearchTokenBudgetTokens(10); got != 123456 {
		t.Fatalf("expected small request allocation not to shrink the floor, got %d", got)
	}
}

func TestResearchTokenBudgetContextRoundTrip(t *testing.T) {
	if researchTokenBudgetFromContext(context.Background()) != nil {
		t.Fatalf("expected no budget on a fresh context")
	}
	budget := resilience.NewTokenBudget("session-ctx", "", 100)
	ctx := contextWithResearchTokenBudget(context.Background(), budget)
	if researchTokenBudgetFromContext(ctx) != budget {
		t.Fatalf("expected shared budget to round-trip through the context")
	}
	if got := contextWithResearchTokenBudget(context.Background(), nil); researchTokenBudgetFromContext(got) != nil {
		t.Fatalf("expected nil budget to leave the context unchanged")
	}
}

func TestEstimateLoopResultTokensUsesCharHeuristic(t *testing.T) {
	if estimateLoopResultTokens(nil) != 0 {
		t.Fatalf("expected zero estimate for nil result")
	}
	loopResult := &LoopResult{
		FinalAnswer: strings.Repeat("a", 400),
		Evidence: []EvidenceFinding{{
			Claim:   strings.Repeat("b", 40),
			Snippet: strings.Repeat("c", 60),
		}},
		ReasoningTrace: []ReasoningTraceEntry{{
			Decision:  strings.Repeat("d", 20),
			Reasoning: strings.Repeat("e", 80),
		}},
		ExecutedQueries: []string{strings.Repeat("f", 100)},
	}
	want := (400 + 40 + 60 + 20 + 80 + 100) / 4
	if got := estimateLoopResultTokens(loopResult); got != want {
		t.Fatalf("expected %d estimated tokens, got %d", want, got)
	}
}

func TestRunLoopReturnsPartialResultsWhenInheritedTokenBudgetIsExhausted(t *testing.T) {
	rt := &UnifiedResearchRuntime{loop: &AutonomousLoop{}}
	budget := resilience.NewTokenBudget("budget-session", "", 1000)
	if !budget.ConsumeUsage("research_pass", 5000) {
		t.Fatalf("expected budget to report exhaustion after overshoot")
	}
	ctx := contextWithResearchTokenBudget(context.Background(), budget)

	var events []PlanExecutionEvent
	result, err := rt.RunLoop(ctx, LoopRequest{
		Query:               "sleep memory",
		DisableQueryEnhance: true,
	}, ResearchExecutionPlaneDeep, func(event PlanExecutionEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("expected exhausted budget to return partial results, got error: %v", err)
	}
	if result == nil || result.LoopResult == nil || result.State == nil {
		t.Fatalf("expected populated partial result, got %#v", result)
	}
	if result.LoopResult.StopReason != researchStopReasonTokenBudgetExhausted {
		t.Fatalf("expected token budget stop reason, got %q", result.LoopResult.StopReason)
	}
	if result.State.StopReason != researchStopReasonTokenBudgetExhausted {
		t.Fatalf("expected state stop reason, got %q", result.State.StopReason)
	}
	if !containsRuntimeStage(events, "token_budget_exhausted") {
		t.Fatalf("expected token_budget_exhausted event, got %#v", events)
	}
	if result.State.DurableJob == nil || result.State.DurableJob.Status != researchDurableJobStatusCompleted {
		t.Fatalf("expected durable job to complete cleanly, got %#v", result.State.DurableJob)
	}
}

func TestFinalizeLoopResultSkipsVerifierRevisionWhenTokenBudgetExhausted(t *testing.T) {
	rt := &UnifiedResearchRuntime{loop: &AutonomousLoop{}}
	state := &ResearchSessionState{
		SessionID: "revision-budget",
		Query:     "sleep memory",
		Plane:     ResearchExecutionPlaneDeep,
		Budget:    &ResearchBudgetDecision{FollowUpSearchBudget: 1},
	}
	loopResult := &LoopResult{
		FinalAnswer:     "Sleep may improve recall in the cited study.",
		ExecutedQueries: []string{"sleep memory"},
		Papers: []search.Paper{{
			ID:     "p1",
			Title:  "Sleep and memory consolidation",
			Source: "pubmed",
		}},
		Evidence: []EvidenceFinding{{
			ID:         "ev1",
			Claim:      "Sleep may improve recall.",
			SourceID:   "p1",
			PaperTitle: "Sleep and memory consolidation",
			Confidence: 0.82,
			Status:     "supported",
		}},
		GapAnalysis: &LoopGapState{
			NextQueries: []string{"sleep memory independent replication"},
			Ledger: []CoverageLedgerEntry{{
				ID:                "source-gap",
				Category:          "source_diversity",
				Status:            coverageLedgerStatusOpen,
				Title:             "Need independent replication",
				Description:       "The synthesis still needs an independent source family.",
				SupportingQueries: []string{"sleep memory independent replication"},
				Confidence:        0.63,
				Required:          true,
				Priority:          92,
				ObligationType:    "source_diversity",
				OwnerWorker:       string(ResearchWorkerIndependentVerifier),
				Severity:          "high",
			}},
		},
	}

	budget := resilience.NewTokenBudget("revision-budget", "", 1000)
	budget.ConsumeUsage("research_pass", 1000)
	budgetLoop := resilience.NewBudgetAwareAgentLoop(budget, researchRuntimeMaxBudgetPasses, researchTokenBudgetPassCost)
	if budgetLoop.CanIterate() {
		t.Fatalf("expected exhausted controller before finalize")
	}

	var events []PlanExecutionEvent
	err := rt.finalizeLoopResultWithVerifier(context.Background(), LoopRequest{Query: "sleep memory", MaxIterations: 1, MaxSearchTerms: 1}, state, loopResult, nil, budgetLoop, func(event PlanExecutionEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("expected budget-skipped revision to preserve base result, got error: %v", err)
	}
	if containsRuntimeStage(events, "verifier_revision_started") {
		t.Fatalf("expected verifier revision pass to be skipped, got %#v", events)
	}
	if !containsRuntimeStage(events, "token_budget_exhausted") {
		t.Fatalf("expected token_budget_exhausted event, got %#v", events)
	}
	if loopResult.FinalizationGate == nil || !loopResult.FinalizationGate.Provisional {
		t.Fatalf("expected provisional finalization gate, got %#v", loopResult.FinalizationGate)
	}
	if !strings.HasPrefix(loopResult.FinalAnswer, "Provisional") {
		t.Fatalf("expected provisional final answer, got %q", loopResult.FinalAnswer)
	}
	if state.VerifierDecision == nil || !containsQueryFragment(state.VerifierDecision.RevisionReasons, "token budget exhausted") {
		t.Fatalf("expected skipped-revision reason on verifier decision, got %#v", state.VerifierDecision)
	}
}
