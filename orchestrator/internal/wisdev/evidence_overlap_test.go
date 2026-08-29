package wisdev

import (
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

func TestClaimOverlapSeparatesOnTopicFromAdjacent(t *testing.T) {
	claim := "removing native function calling degrades agent tool use success"
	onTopic := search.Paper{
		Title:    "Small LLMs Are Weak Tool Learners",
		Abstract: "Tool use demands accurate tool invocation; smaller models degrade at function calling.",
	}
	adjacent := search.Paper{
		Title:    "Exploring the Limits of Transfer Learning with a Unified Text-to-Text Transformer",
		Abstract: "Transfer learning where a model is pre-trained then fine-tuned on downstream tasks.",
	}
	on, off := ClaimOverlap(claim, onTopic), ClaimOverlap(claim, adjacent)
	if !(on > off) {
		t.Fatalf("on-topic overlap %.3f must exceed adjacent %.3f", on, off)
	}
	if off > 0.2 {
		t.Fatalf("a paper sharing no technical vocabulary scored %.3f", off)
	}
}

func TestClaimOverlapIgnoresStopwords(t *testing.T) {
	// A candidate matching only on filler words must not score as related.
	claim := "the harness and the model are evaluated with a checklist"
	filler := search.Paper{Title: "The and are with a the", Abstract: "and the with are"}
	if got := ClaimOverlap(claim, filler); got != 0 {
		t.Fatalf("stopword-only overlap = %.3f, want 0", got)
	}
}

func TestClaimOverlapEmptyInputs(t *testing.T) {
	if got := ClaimOverlap("", search.Paper{Title: "anything"}); got != 0 {
		t.Fatalf("empty claim = %.3f, want 0", got)
	}
	if got := ClaimOverlap("verify gate blocks premature finish", search.Paper{}); got != 0 {
		t.Fatalf("empty paper = %.3f, want 0", got)
	}
}

func TestRankByClaimOverlapOrdersAndIsStable(t *testing.T) {
	claim := "bootstrap confidence interval coverage at small sample sizes"
	papers := []search.Paper{
		{Title: "Attention Is All You Need", Abstract: "transformer architecture"},
		{Title: "Bootstrap confidence intervals", Abstract: "coverage of percentile intervals at small sample sizes"},
		{Title: "Another unrelated paper", Abstract: "unrelated"},
	}
	ranked := rankByClaimOverlap(claim, papers)
	if len(ranked) != 3 {
		t.Fatalf("len = %d", len(ranked))
	}
	if ranked[0].paper.Title != "Bootstrap confidence intervals" {
		t.Fatalf("top candidate = %q", ranked[0].paper.Title)
	}
	// Ties keep retrieval order.
	if ranked[1].overlap == ranked[2].overlap && ranked[1].paper.Title != "Attention Is All You Need" {
		t.Fatalf("tie was not stable: %q before %q", ranked[1].paper.Title, ranked[2].paper.Title)
	}
}
