package wisdev

import (
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

// Regression: the multi-concept query "organ on a chip for immune rejection"
// once collapsed to zero admitted papers because the relevance gate required
// every literal anchor (organ AND chip AND immune AND rejection) to appear, with
// no synonym awareness. Real organ-on-chip immunology papers phrase these as
// microphysiological / immunogenicity / allograft, so all were rejected even
// though the search legitimately retrieved them. The gate now admits papers that
// cover a majority of the concepts via synonyms.
func TestOrganOnChipImmuneRejectionAdmission(t *testing.T) {
	root := "organ on a chip for immune rejection"

	onTopic := []struct {
		name  string
		paper search.Paper
	}{
		{
			"microphysiological-system synonym hit",
			search.Paper{
				Title:    "A vascularized microphysiological system for modeling transplant rejection",
				Abstract: "We present a microfluidic chip recreating allograft rejection with perfused human endothelium and circulating T cells to study graft injury.",
			},
		},
		{
			"organ-on-chip immunogenicity paper (says immunogenicity, not 'immune')",
			search.Paper{
				Title:    "Organ-on-a-chip platform for immunogenicity assessment of biologics",
				Abstract: "An organ-on-chip device evaluates host immunological responses and graft tolerance under physiological flow.",
			},
		},
		{
			"classic organ-on-chip immune paper",
			search.Paper{
				Title:    "Organ-on-chip models of the immune response to allograft rejection",
				Abstract: "This review covers organ-on-chip systems reproducing immune rejection of transplanted tissue.",
			},
		},
		{
			"in-vitro immune model synonym hit",
			search.Paper{
				Title:    "In vitro immune response modeling of graft-versus-host disease",
				Abstract: "A microfluidic platform models transplant rejection using primary immune cells.",
			},
		},
	}

	for _, c := range onTopic {
		if !paperMatchesQueryRelevance(root, c.paper) {
			t.Errorf("expected on-topic paper admitted: %s (score=%.2f)", c.name, paperQueryRelevanceScore(root, c.paper))
		}
	}

	// Precision backstop: an unrelated paper sharing no concept must still be rejected.
	offTopic := search.Paper{
		Title:    "Quarterly earnings prediction with transformer models",
		Abstract: "A finance model forecasts company revenue from market signals.",
	}
	if paperMatchesQueryRelevance(root, offTopic) {
		t.Errorf("expected off-topic finance paper to be rejected")
	}

	// The whole batch must survive the production filter (was 0 before the fix).
	batch := make([]search.Paper, 0, len(onTopic))
	for _, c := range onTopic {
		batch = append(batch, c.paper)
	}
	if got := filterPapersByQueryRelevance(root, batch); len(got) != len(onTopic) {
		t.Fatalf("expected all %d on-topic papers admitted, got %d", len(onTopic), len(got))
	}
}
