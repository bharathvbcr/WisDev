package wisdev

import (
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

// Precision guard for the relevance-gate relaxations (60% anchor threshold, LLM
// synonyms, morphological stemming, retrieval-or-root admission, relaxed score
// floor). These broaden recall, so this test pins the precision boundary: papers
// that share only a minority of the query's concepts — even via strong cross-domain
// keyword collisions — must stay rejected. If a future relaxation admits any of
// these, that is an over-admission regression to investigate, not a flake.
func TestQueryRelevancePrecisionBoundary(t *testing.T) {
	ResetQueryPreparationStateForTest()
	root := "organ on a chip for immune rejection"

	rejected := []struct {
		name  string
		paper search.Paper
	}{
		{
			"cross-domain chip+immune collision (semiconductors, not biology)",
			search.Paper{
				Title:    "Computer chip immune to electromagnetic interference",
				Abstract: "A semiconductor chip design resilient to EMI in automotive control systems.",
			},
		},
		{
			"organ+transplant policy paper (no chip, no immune mechanism)",
			search.Paper{
				Title:    "Organ donation policy and transplant waiting lists",
				Abstract: "National registry analysis of organ allocation and transplant outcomes across regions.",
			},
		},
		{
			"single-anchor keyword collision (snack 'chip')",
			search.Paper{
				Title:    "Potato chip texture and consumer preference",
				Abstract: "Sensory analysis of fried snack crispness and salt content.",
			},
		},
		{
			"pure off-topic",
			search.Paper{
				Title:    "Soil microbiome diversity in tropical forests",
				Abstract: "We catalog bacterial taxa across forest plots over several seasons.",
			},
		},
	}
	for _, c := range rejected {
		if paperMatchesQueryRelevance(root, c.paper) {
			t.Errorf("over-admission: %s should be rejected (score=%.2f)", c.name, paperQueryRelevanceScore(root, c.paper))
		}
	}

	// Recall boundary the relaxations intend to keep: a paper covering a majority
	// of the concepts (organ + immune + rejection, 3 of 4) is admitted even though
	// it omits the "chip" platform concept. Documents the deliberate recall/precision
	// trade-off so a later tightening that drops it is a conscious choice.
	clinicalReview := search.Paper{
		Title:    "Immune rejection of organ transplants: a clinical review",
		Abstract: "Acute and chronic rejection of transplanted organs and immunosuppression strategies.",
	}
	if !paperMatchesQueryRelevance(root, clinicalReview) {
		t.Errorf("recall regression: majority-concept paper should be admitted (score=%.2f)", paperQueryRelevanceScore(root, clinicalReview))
	}
}
