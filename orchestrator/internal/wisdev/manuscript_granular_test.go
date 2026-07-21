package wisdev

import "testing"

func TestResolveSectionTemplatesDefault(t *testing.T) {
	p := &ManuscriptPipeline{}
	got := p.resolveSectionTemplates()
	if len(got) != len(defaultSectionTemplates()) {
		t.Fatalf("default flow should return the full template set, got %d", len(got))
	}
	if got[0].id != "abstract" {
		t.Fatalf("default plan should start with abstract, got %q", got[0].id)
	}
}

func TestResolveSectionTemplatesHonorsFlow(t *testing.T) {
	p := &ManuscriptPipeline{SectionFlow: []string{"Introduction", "results", "discussion"}}
	got := p.resolveSectionTemplates()
	want := []string{"introduction", "results", "discussion"}
	if len(got) != len(want) {
		t.Fatalf("expected %d sections, got %d", len(want), len(got))
	}
	for i, id := range want {
		if got[i].id != id {
			t.Fatalf("section %d: got %q want %q", i, got[i].id, id)
		}
	}
	// Known ids reuse the tuned brief (non-generic writer role).
	if got[0].writerRole != "framing_writer" {
		t.Fatalf("introduction should reuse tuned brief, got role %q", got[0].writerRole)
	}
}

func TestResolveSectionTemplatesUnknownAndDedup(t *testing.T) {
	p := &ManuscriptPipeline{SectionFlow: []string{"background", "background", "Future Work"}}
	got := p.resolveSectionTemplates()
	if len(got) != 2 {
		t.Fatalf("expected 2 deduped sections, got %d", len(got))
	}
	if got[0].id != "background" || got[1].id != "future_work" {
		t.Fatalf("unexpected ids: %q, %q", got[0].id, got[1].id)
	}
	if got[1].title != "Future Work" {
		t.Fatalf("unknown id should humanize title, got %q", got[1].title)
	}
}

func TestMinimizeEmDashes(t *testing.T) {
	cases := map[string]string{
		"no dashes here":           "no dashes here",
		"a — b":                    "a, b",
		"word—word":                "word, word",
		"end of clause — and more": "end of clause, and more",
	}
	for in, want := range cases {
		if got := minimizeEmDashes(in); got != want {
			t.Errorf("minimizeEmDashes(%q) = %q, want %q", in, got, want)
		}
	}
	if minimizeEmDashes("plain") != "plain" {
		t.Error("no-op path failed")
	}
}

// NOTE: TestReviewRoundsClamping and TestSectionsContentFingerprintDetectsChange
// live in manuscript_review_loop_test.go in this (backend) tree, so they are not
// duplicated here. The WisDev ARC tree keeps them in this file.

func TestGenerateManuscriptToolExposesGranularControls(t *testing.T) {
	props, _ := mcpGenerateManuscriptTool().InputSchema["properties"].(map[string]any)
	for _, key := range []string{"words", "minCitations", "flow", "reviewRounds", "genre"} {
		if _, ok := props[key]; !ok {
			t.Errorf("wisdevGenerateManuscript should expose %q", key)
		}
	}
	searchProps, _ := mcpSearchPapersTool().InputSchema["properties"].(map[string]any)
	if _, ok := searchProps["minCitations"]; !ok {
		t.Error("wisdevSearchPapers should expose minCitations")
	}
}
