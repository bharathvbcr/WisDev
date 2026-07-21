package wisdev

import (
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

// Regression coverage for the "organ on a chip for immune rejection" failure:
// the search retrieved on-topic papers under synonym branches, but the relevance
// gate discarded every one because it demanded all literal root anchors. The
// four fixes below keep those papers admitted while still rejecting off-topic
// noise: (1) judging papers against the branch query that retrieved them,
// (2) a fractional anchor-coverage threshold, (3) LLM-prepared synonyms feeding
// the gate, and (4) morphological stem matching.

func microphysiologicalPaper() search.Paper {
	return search.Paper{
		ID:       "mps1",
		Title:    "A vascularized microphysiological system for modeling transplant rejection",
		Abstract: "A microfluidic chip recreates allograft rejection with perfused human endothelium and circulating immune cells to study graft injury.",
	}
}

func offTopicSoilPaper() search.Paper {
	return search.Paper{
		ID:       "off1",
		Title:    "Soil microbiome diversity in tropical forests",
		Abstract: "We catalog bacterial taxa across forest plots over several seasons.",
	}
}

// Fix 2 + 3 (curated) + 4: the microphysiological-system paper is admitted
// against the literal root query even though it never says "organ" or "immune".
func TestOrganOnChip_SynonymPaperAdmittedAgainstRoot(t *testing.T) {
	ResetQueryPreparationStateForTest()
	root := "organ on a chip for immune rejection"
	if !paperMatchesQueryRelevance(root, microphysiologicalPaper()) {
		t.Fatal("microphysiological-system paper must be admitted against the root query")
	}
	if paperMatchesQueryRelevance(root, offTopicSoilPaper()) {
		t.Fatal("off-topic soil paper must still be rejected")
	}
}

// Fix 1: a paper retrieved by a synonym branch query is judged against that
// branch query, not only the narrower root terms.
func TestOrganOnChip_RetrievalQueryAdmission(t *testing.T) {
	ResetQueryPreparationStateForTest()
	root := "organ on a chip for immune rejection"
	branch := "microphysiological systems transplant rejection"

	_, accepted := admitSearchPapersForRetrievalQuery(nil, root, branch, []search.Paper{microphysiologicalPaper()}, 10)
	if len(accepted) != 1 {
		t.Fatalf("branch-retrieved paper must be admitted via its retrieval query, got %d", len(accepted))
	}

	_, rejected := admitSearchPapersForRetrievalQuery(nil, root, branch, []search.Paper{offTopicSoilPaper()}, 10)
	if len(rejected) != 0 {
		t.Fatalf("off-topic paper must be rejected by both branch and root query, got %d", len(rejected))
	}
}

// Fix 3 (dynamic): for a query the curated map does not cover, the LLM-prepared
// synonyms cached for that query let the gate recognise a synonym-phrased paper.
func TestRelevance_DynamicLLMSynonymsAdmitPaper(t *testing.T) {
	ResetQueryPreparationStateForTest()
	root := "graphene supercapacitor electrolyte"
	paper := search.Paper{
		ID:       "sc1",
		Title:    "Ultracapacitor electrodes operating in ionic liquid media",
		Abstract: "Reduced graphene oxide electrodes deliver high-rate energy storage in ionic liquid systems.",
	}

	// Without prepared synonyms the paper falls short of the anchor threshold.
	if paperMatchesQueryRelevance(root, paper) {
		t.Fatal("paper should not match before LLM synonyms are available")
	}

	storePreparedQuery(PreparedResearchQuery{
		Original:    root,
		Corrected:   root,
		SearchQuery: root,
		Intent:      "academic",
		Keywords:    []string{"graphene", "supercapacitor", "electrolyte"},
		Synonyms:    []string{"ultracapacitor", "ionic liquid"},
	})

	if !paperMatchesQueryRelevance(root, paper) {
		t.Fatal("paper should be admitted once LLM synonyms (ultracapacitor, ionic liquid) feed the gate")
	}

	off := search.Paper{ID: "off2", Title: "Quarterly earnings prediction with transformers", Abstract: "A finance model forecasts revenue."}
	if paperMatchesQueryRelevance(root, off) {
		t.Fatal("off-topic finance paper must stay rejected even with synonyms cached")
	}
}

// Fix 4: morphological stems collapse inflected/derived forms.
func TestRelevanceStem_Morphology(t *testing.T) {
	cases := [][2]string{
		{"immune", "immun"},
		{"immunity", "immun"},
		{"immunological", "immun"},
		{"immunogenicity", "immun"},
		{"rejection", "reject"},
		{"rejecting", "reject"},
		{"perfusion", "perfus"},
		{"perfused", "perfus"},
	}
	for _, c := range cases {
		if got := relevanceStem(c[0]); got != c[1] {
			t.Errorf("relevanceStem(%q) = %q, want %q", c[0], got, c[1])
		}
	}
	// "organ" must NOT collapse into organic/organize/organization stems.
	if relevanceStem("organ") != "organ" {
		t.Errorf("relevanceStem(organ) must stay organ, got %q", relevanceStem("organ"))
	}
	// Stem matching admits a non-curated inflected form.
	if !bodyContainsAnchor("a perfused bioreactor maintained viability", "perfusion") {
		t.Error("bodyContainsAnchor should match 'perfused' for anchor 'perfusion' via stemming")
	}
}
