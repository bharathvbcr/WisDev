package wisdev

import (
	"strings"
	"testing"
)

// The inline-citation enricher used to split prose on every period, breaking
// inside "(Author, et al., 1999; 451 citations)", author initials ("Philip F.")
// and "et al.", which produced dangling fragments like
// "Halloran, Anette Melk, et al. [requires verification...] 451 citations)".
func TestCitationSafeProseSentences_KeepsCitationsIntact(t *testing.T) {
	para := "Acute injury targets the epithelium (Philip F. Halloran, Anette Melk, et al., 1999; 451 citations). " +
		"On-chip models show barrier disruption in a dose-dependent manner. " +
		"Halloran et al. report that the senescent state prevents normal remodeling."

	got := citationSafeProseSentences(para, 0)
	if len(got) != 3 {
		t.Fatalf("expected 3 sentences, got %d: %#v", len(got), got)
	}
	if !strings.Contains(got[0], "451 citations)") {
		t.Errorf("first sentence must keep the full parenthetical citation intact, got %q", got[0])
	}
	for _, s := range got {
		trimmed := strings.TrimSpace(s)
		// No sentence should be a bare citation tail / author fragment.
		if strings.HasPrefix(trimmed, "1999;") || strings.HasPrefix(trimmed, "Anette Melk") ||
			trimmed == "451 citations)." || strings.HasPrefix(trimmed, "et al") {
			t.Errorf("sentence splitter produced a citation fragment: %q", trimmed)
		}
	}
}

func TestCitationSafeProseSentences_NoDecimalOrAbbrevSplit(t *testing.T) {
	got := citationSafeProseSentences("The rate rose 5.2 percent versus controls. Results held across cohorts.", 0)
	if len(got) != 2 {
		t.Fatalf("decimal 5.2 must not split; expected 2 sentences, got %d: %#v", len(got), got)
	}
}

func TestDedupeCitationArtifacts_CollapsesRepeatedWarnings(t *testing.T) {
	tag := groundingWarningTag
	in := "A claim. " + tag + " " + tag + " " + tag + " Next claim."
	out := dedupeCitationArtifacts(in)
	if n := strings.Count(out, tag); n != 1 {
		t.Fatalf("expected a single grounding warning tag after dedup, got %d: %q", n, out)
	}
}

// A paragraph the model already cited (numbered marker + author-year) must pass
// through enrichment untouched rather than being re-split and re-cited.
func TestEnrichParagraph_AlreadyCitedStaysIntact(t *testing.T) {
	para := "T lymphocytes drive acute rejection [9] (Elizabeth Ingulli, 2008; 308 citations)."
	registry := buildCitationRegistry(nil)
	used := map[string]struct{}{}
	got := enrichParagraphWithCitations(para, nil, nil, registry, used, "immune rejection")
	if got != para {
		t.Fatalf("already-cited paragraph must be unchanged.\n got: %q\nwant: %q", got, para)
	}
	if strings.Contains(got, groundingWarningTag) {
		t.Errorf("already-cited paragraph must not gain a verification warning: %q", got)
	}
}
