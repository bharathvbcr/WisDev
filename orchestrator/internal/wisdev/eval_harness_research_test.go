package wisdev

import "testing"

func TestRunEvalHarnessScenariosIncludesResearchQuality(t *testing.T) {
	report, err := RunEvalHarnessScenarios(NewToolRegistry())
	if err != nil {
		t.Fatalf("RunEvalHarnessScenarios returned error: %v", err)
	}

	results, ok := report.Groups["researchQuality"]
	if !ok {
		t.Fatalf("expected researchQuality group in eval harness report")
	}
	if len(results) == 0 {
		t.Fatalf("expected researchQuality scenarios to run")
	}
	seenBudgetExhausted := false
	seenVerifierPromotion := false
	seenContradictionPressure := false
	for _, result := range results {
		if !result.Passed {
			t.Fatalf("expected researchQuality scenario %q to pass, got error: %s", result.Name, result.Error)
		}
		switch result.Name {
		case "deep_budget_exhaustion_stale_sources_and_citation_conflict":
			seenBudgetExhausted = true
		case "verifier_promotes_multi_source_claims":
			seenVerifierPromotion = true
		case "open_ledger_reopens_followups_and_penalizes_weak_citations":
			seenContradictionPressure = true
		}
	}
	if !seenBudgetExhausted || !seenVerifierPromotion || !seenContradictionPressure {
		t.Fatalf("expected high-depth research quality scenarios to cover budget, verifier, and contradiction pressure, got %#v", results)
	}
}
