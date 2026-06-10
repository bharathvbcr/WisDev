package wisdev

import (
	"testing"

	internal "github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func TestFromInternalBeliefStateMinimalView(t *testing.T) {
	bs := &internal.BeliefState{Beliefs: map[string]*internal.Belief{
		"b1": {
			ID:                    "b1",
			Claim:                 "Scaffolds improve outcomes",
			Confidence:            0.42,
			Status:                internal.BeliefStatusActive,
			SupportingEvidence:    []string{"e1", "e2"},
			ContradictingEvidence: []string{"e3"},
		},
		"b2": {
			ID:         "b2",
			Claim:      "Allografts outperform autografts",
			Confidence: 0.81,
			Status:     internal.BeliefStatusRefuted,
		},
		"b3": {ID: "b3", Claim: "   ", Confidence: 0.9},
	}}

	beliefs := fromInternalBeliefState(bs)
	if len(beliefs) != 2 {
		t.Fatalf("expected 2 beliefs (blank claim skipped), got %d", len(beliefs))
	}
	if beliefs[0].Confidence < beliefs[1].Confidence {
		t.Fatalf("expected confidence-descending order, got %+v", beliefs)
	}
	if beliefs[0].Claim != "Allografts outperform autografts" || beliefs[0].Status != "refuted" {
		t.Fatalf("unexpected first belief: %+v", beliefs[0])
	}
	second := beliefs[1]
	if second.SupportCount != 2 || second.ContradictionCount != 1 {
		t.Fatalf("expected evidence tallies 2/1, got %+v", second)
	}
}

func TestFromInternalBeliefStateEmpty(t *testing.T) {
	if got := fromInternalBeliefState(nil); got != nil {
		t.Fatalf("expected nil for nil state, got %v", got)
	}
	if got := fromInternalBeliefState(&internal.BeliefState{}); got != nil {
		t.Fatalf("expected nil for empty state, got %v", got)
	}
}

func TestFromInternalGapState(t *testing.T) {
	gap := &internal.LoopGapState{
		Sufficient:     false,
		Reasoning:      " coverage incomplete ",
		MissingAspects: []string{"long-term outcomes", " ", "pediatric cohorts"},
		Coverage: internal.LoopCoverageState{
			PlannedQueryCount:        6,
			ExecutedQueryCount:       4,
			UnexecutedPlannedQueries: []string{"meniscus allograft registry data", ""},
			QueriesWithoutCoverage:   []string{"scaffold immunogenicity"},
		},
	}
	gaps := fromInternalGapState(gap)
	if gaps == nil {
		t.Fatal("expected gap summary")
	}
	if gaps.Sufficient {
		t.Fatal("expected sufficient=false")
	}
	if gaps.Reasoning != "coverage incomplete" {
		t.Fatalf("reasoning = %q", gaps.Reasoning)
	}
	if len(gaps.MissingAspects) != 2 {
		t.Fatalf("expected blank aspects dropped, got %v", gaps.MissingAspects)
	}
	if gaps.PlannedQueryCount != 6 || gaps.ExecutedQueryCount != 4 {
		t.Fatalf("coverage counts = %d/%d", gaps.ExecutedQueryCount, gaps.PlannedQueryCount)
	}
	if len(gaps.UnexecutedPlannedQueries) != 1 || len(gaps.QueriesWithoutCoverage) != 1 {
		t.Fatalf("unexpected query lists: %+v", gaps)
	}
	if fromInternalGapState(nil) != nil {
		t.Fatal("expected nil passthrough for nil gap state")
	}
}
