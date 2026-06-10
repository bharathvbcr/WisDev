package wisdev

import (
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

func TestResearchBranchPlansWithExecutionStatus(t *testing.T) {
	root := "meniscus reconstruction strategies"
	planned := []string{
		root,
		"reconstruction strategies recent research",
		"reconstruction strategies systematic review",
	}
	executed := []string{root, "reconstruction strategies recent research"}
	coverage := map[string][]search.Paper{
		root: {
			{ID: "p1", Title: "Meniscus reconstruction strategies", Abstract: "meniscus repair outcomes"},
		},
		"reconstruction strategies recent research": {},
	}

	plans := researchBranchPlansWithExecutionStatus(root, planned, executed, coverage)
	if len(plans) != 3 {
		t.Fatalf("expected 3 branch plans, got %d", len(plans))
	}
	if plans[0].Status != "retrieved" || plans[0].StopReason != "sources_found" {
		t.Fatalf("expected first branch retrieved, got status=%q stop=%q", plans[0].Status, plans[0].StopReason)
	}
	if plans[1].Status != "executed" || plans[1].StopReason != "no_sources" {
		t.Fatalf("expected second branch executed without sources, got status=%q stop=%q", plans[1].Status, plans[1].StopReason)
	}
	if plans[2].Status != "planned" {
		t.Fatalf("expected third branch to remain planned, got %q", plans[2].Status)
	}
}
