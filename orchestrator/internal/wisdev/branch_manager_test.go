package wisdev

import (
	"context"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

func TestBranchManagerScoreUsesBranchEvidenceAndBeliefs(t *testing.T) {
	manager := NewBranchManager(3, 0.3)
	weakHypothesis := &Hypothesis{ID: "weak", Claim: "weak branch", ConfidenceScore: 0.4}
	strongHypothesis := &Hypothesis{ID: "strong", Claim: "strong branch", ConfidenceScore: 0.4}

	weak := manager.Fork("", weakHypothesis)
	strong := manager.Fork("", strongHypothesis)
	strong.Evidence = []EvidenceFinding{
		{ID: "s1", SourceID: "source-a", Confidence: 0.9},
		{ID: "s2", SourceID: "source-b", Confidence: 0.85},
		{ID: "s3", SourceID: "source-c", Confidence: 0.8},
	}
	attachBranchEvidence(strong)

	if manager.Score(strong) <= manager.Score(weak) {
		t.Fatalf("expected evidence-rich branch %.3f to score above weak branch %.3f", manager.Score(strong), manager.Score(weak))
	}
}

func TestBranchManagerForkClonesParentBeliefState(t *testing.T) {
	manager := NewBranchManager(3, 0.3)
	parent := manager.Fork("", &Hypothesis{ID: "parent", Claim: "parent claim", ConfidenceScore: 0.7})
	parent.BeliefState.AddBelief(&Belief{
		ID:         "parent-extra",
		Claim:      "parent extra belief",
		Confidence: 0.8,
		Status:     BeliefStatusActive,
		CreatedAt:  NowMillis(),
		UpdatedAt:  NowMillis(),
	})

	child := manager.Fork(parent.ID, &Hypothesis{ID: "child", Claim: "child claim", ConfidenceScore: 0.6})
	if child.BeliefState == parent.BeliefState {
		t.Fatal("child branch must not share parent belief state pointer")
	}
	if _, ok := child.BeliefState.Beliefs["parent-extra"]; !ok {
		t.Fatal("child branch should inherit a cloned parent belief")
	}
	child.BeliefState.Beliefs["parent-extra"].Confidence = 0.1
	if parent.BeliefState.Beliefs["parent-extra"].Confidence == 0.1 {
		t.Fatal("mutating child belief state should not mutate parent branch")
	}
}

func TestAutonomousLoopAdvanceBranchSessionKeepsEvidenceBranchLocal(t *testing.T) {
	reg := search.NewProviderRegistry()
	reg.Register(&mockSearchProvider{
		name: "branch-provider",
		papers: []search.Paper{{
			ID:       "branch-paper",
			Title:    "Branch-only evidence",
			Abstract: "Evidence retrieved for a branch-local follow-up.",
			Year:     2025,
		}},
	})
	loop := NewAutonomousLoop(reg, nil)
	manager := NewBranchManager(3, 0.3)
	branch := manager.Fork("", &Hypothesis{ID: "branch-hypothesis", Claim: "branch claim", ConfidenceScore: 0.5})
	branch.PendingQueries = []string{"branch follow-up"}
	coverage := map[string][]search.Paper{}

	findings := loop.advanceBranchSession(context.Background(), branch, search.SearchOpts{Limit: 1}, 10, coverage)
	if len(findings) != 1 {
		t.Fatalf("expected one branch-local finding, got %+v", findings)
	}
	if len(branch.Papers) != 1 || branch.Papers[0].ID != "branch-paper" {
		t.Fatalf("expected branch-local paper pool to be updated, got %+v", branch.Papers)
	}
	if len(branch.ExecutedQueries) != 1 || branch.ExecutedQueries[0] != "branch follow-up" {
		t.Fatalf("expected branch-local executed query, got %+v", branch.ExecutedQueries)
	}
	if _, ok := coverage["branch:"+branch.ID+":branch follow-up"]; !ok {
		t.Fatalf("expected branch-local coverage entry, got %+v", coverage)
	}
	if len(branch.BeliefState.GetActiveBeliefs()[0].SupportingEvidence) == 0 {
		t.Fatal("expected branch belief to attach branch-local evidence")
	}
}
