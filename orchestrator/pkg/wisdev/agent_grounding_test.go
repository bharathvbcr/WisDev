package wisdev

import (
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/rag"
)

func TestGroundingStatsFromStructuredAnswer(t *testing.T) {
	answer := &rag.StructuredAnswer{
		Sections: []rag.AnswerSection{
			{
				Heading: "Synthesis",
				Sentences: []rag.AnswerClaim{
					{Text: "Grounded claim.", EvidenceIDs: []string{"p1", "p2"}},
					{Text: "Ungrounded claim.", Unsupported: true},
					{Text: "   "}, // empty text must not count
				},
			},
			{
				Heading: "Key literature",
				Sentences: []rag.AnswerClaim{
					{Text: "Another grounded claim.", EvidenceIDs: []string{"P1", " "}},
				},
			},
			{Heading: "Empty section"},
		},
	}
	stats := groundingStatsFromStructuredAnswer(answer)
	if stats == nil {
		t.Fatal("expected stats")
	}
	if stats.TotalClaims != 3 {
		t.Fatalf("expected 3 claims, got %d", stats.TotalClaims)
	}
	if stats.GroundedClaims != 2 {
		t.Fatalf("expected 2 grounded claims, got %d", stats.GroundedClaims)
	}
	if stats.UnsupportedClaims != 1 {
		t.Fatalf("expected 1 unsupported claim, got %d", stats.UnsupportedClaims)
	}
	// "p1"/"P1" dedupe case-insensitively; blank IDs are ignored.
	if stats.CitedSources != 2 {
		t.Fatalf("expected 2 distinct cited sources, got %d", stats.CitedSources)
	}
	if len(stats.Sections) != 2 {
		t.Fatalf("expected 2 non-empty sections, got %d", len(stats.Sections))
	}
	if stats.Sections[0].Heading != "Synthesis" || stats.Sections[0].GroundedClaims != 1 || stats.Sections[0].TotalClaims != 2 {
		t.Fatalf("unexpected section stats: %+v", stats.Sections[0])
	}
}

func TestGroundingStatsFromStructuredAnswerNilAndEmpty(t *testing.T) {
	if stats := groundingStatsFromStructuredAnswer(nil); stats != nil {
		t.Fatalf("expected nil for nil answer, got %+v", stats)
	}
	if stats := groundingStatsFromStructuredAnswer(&rag.StructuredAnswer{}); stats != nil {
		t.Fatalf("expected nil for empty answer, got %+v", stats)
	}
	empty := &rag.StructuredAnswer{Sections: []rag.AnswerSection{{Heading: "Only empty", Sentences: []rag.AnswerClaim{{Text: "  "}}}}}
	if stats := groundingStatsFromStructuredAnswer(empty); stats != nil {
		t.Fatalf("expected nil when no countable claims, got %+v", stats)
	}
}
