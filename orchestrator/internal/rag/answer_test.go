package rag

import (
	"strings"
	"testing"
)

func TestRenderAnswerSectionsWithCitationsAppendsParenthetical(t *testing.T) {
	sections := []AnswerSection{{
		Heading: "Findings",
		Sentences: []AnswerClaim{{
			Text:        "Sleep consolidation improves recall",
			EvidenceIDs: []string{"paper-1"},
		}},
	}}
	rendered := RenderAnswerSectionsWithCitations(sections, func(ids []string) string {
		if len(ids) == 1 && ids[0] == "paper-1" {
			return "(Smith et al., 2021; 42 citations)"
		}
		return ""
	})
	if rendered == "" {
		t.Fatal("expected rendered answer")
	}
	for _, want := range []string{
		"Sleep consolidation improves recall.",
		"(Smith et al., 2021; 42 citations)",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected %q in rendered answer: %q", want, rendered)
		}
	}
}
