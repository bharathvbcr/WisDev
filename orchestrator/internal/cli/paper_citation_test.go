package cli

import (
	"strings"
	"testing"

	agent "github.com/wisdev/wisdev-agent-os/orchestrator/pkg/wisdev"
)

func TestFormatPaperBibliographyIncludesCitations(t *testing.T) {
	bib := formatPaperBibliography(agent.Paper{
		Title:         "Meniscus scaffold repair",
		Authors:       []string{"Ada Lovelace", "Alan Turing"},
		Year:          2024,
		Venue:         "Journal of Knee Surgery",
		CitationCount: 42,
		DOI:           "10.1000/meniscus.2024",
	})
	for _, want := range []string{
		"Ada Lovelace & Alan Turing (2024)",
		"Meniscus scaffold repair",
		"Journal of Knee Surgery",
		"Citations: 42",
		"doi.org/10.1000/meniscus.2024",
	} {
		if !strings.Contains(bib, want) {
			t.Fatalf("expected bibliography to contain %q: %s", want, bib)
		}
	}
}

func TestFormatPaperBibTeX(t *testing.T) {
	bib := formatPaperBibTeX(agent.Paper{
		Title:         "Meniscus scaffold repair",
		Authors:       []string{"Ada Lovelace"},
		Year:          2024,
		Venue:         "Journal of Knee Surgery",
		CitationCount: 42,
		DOI:           "10.1000/meniscus.2024",
	}, 0)
	for _, want := range []string{
		"@article{",
		"title = {{Meniscus} scaffold repair}",
		"author = {Ada Lovelace}",
		"year = {2024}",
		"doi = {10.1000/meniscus.2024}",
	} {
		if !strings.Contains(bib, want) {
			t.Fatalf("expected bibtex to contain %q: %s", want, bib)
		}
	}
}

func TestBibTeXKeyStableAndDeduped(t *testing.T) {
	paper := agent.Paper{
		Title:   "Meniscus scaffold repair",
		Authors: []string{"Ada Lovelace"},
		Year:    2024,
	}
	if first, second := bibTeXKey(paper, 0), bibTeXKey(paper, 0); first != second {
		t.Fatalf("expected stable cite key, got %q vs %q", first, second)
	}
	if got := bibTeXKey(paper, 0); got != "lovelace2024meniscus1" {
		t.Fatalf("unexpected cite key: %q", got)
	}
	// Identical papers at different positions never collide.
	if bibTeXKey(paper, 0) == bibTeXKey(paper, 1) {
		t.Fatal("expected per-index dedup of cite keys")
	}
	// Degenerate metadata still produces a usable key.
	if got := bibTeXKey(agent.Paper{}, 2); got != "unknown0paper3" {
		t.Fatalf("unexpected fallback key: %q", got)
	}
}

func TestFormatPaperBibTeXEscapesAndProtectsTitle(t *testing.T) {
	bib := formatPaperBibTeX(agent.Paper{
		Title:   "DNA repair in {special} Mice",
		Authors: []string{"Rosalind Franklin"},
		Year:    1953,
	}, 0)
	if !strings.Contains(bib, "title = {{DNA} repair in \\{special\\} {Mice}}") {
		t.Fatalf("expected protected/escaped title, got: %s", bib)
	}
}

func TestFormatPaperBibTeXIncludesSourceNote(t *testing.T) {
	bib := formatPaperBibTeX(agent.Paper{
		Title:         "Meniscus scaffold repair",
		Authors:       []string{"Ada Lovelace"},
		Year:          2024,
		Source:        "openalex",
		CitationCount: 42,
	}, 0)
	if !strings.Contains(bib, "note = {Source: openalex · Citations: 42}") {
		t.Fatalf("expected source api note, got: %s", bib)
	}
}

func TestFormatYOLOResultBibTeXDedupedKeys(t *testing.T) {
	result := &agent.YOLOResult{Papers: []agent.Paper{
		{Title: "Meniscus scaffold repair", Authors: []string{"Ada Lovelace"}, Year: 2024},
		{Title: "Meniscus scaffold repair", Authors: []string{"Ada Lovelace"}, Year: 2024},
	}}
	out := formatYOLOResultBibTeX(result)
	if !strings.Contains(out, "@article{lovelace2024meniscus1,") || !strings.Contains(out, "@article{lovelace2024meniscus2,") {
		t.Fatalf("expected deduped cite keys in export: %s", out)
	}
}

func TestFormatPaperCitationLine(t *testing.T) {
	line := formatPaperCitationLine(agent.Paper{
		Authors:       []string{"Ada Lovelace"},
		Year:          2023,
		Venue:         "Nature",
		CitationCount: 15,
	})
	if !strings.Contains(line, "Citations: 15") || !strings.Contains(line, "Nature") {
		t.Fatalf("unexpected citation line: %s", line)
	}
}
