package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/evidence"
	internalwisdev "github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
	agent "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"
)

func TestDocGenCommandWiring(t *testing.T) {
	if !isKnownCommand("docgen") {
		t.Fatalf("docgen should be a known command")
	}
	if got := normalizeInvocation([]string{"docugen"}); len(got) != 1 || got[0] != "docgen" {
		t.Fatalf("alias docugen: expected docgen, got %#v", got)
	}
	// `wisdev docgen "question"` must dispatch to docgen, not be rewritten into a
	// bare search for the words "docgen question".
	args := normalizeInvocation([]string{"docgen", "some topic"})
	if args[0] != "docgen" {
		t.Fatalf("docgen invocation rewritten: %v", args)
	}
	// "paper" must NOT be an alias: a bare question beginning with "paper" stays a
	// search, never a docgen mode-switch with a truncated topic.
	if got := normalizeInvocation([]string{"paper", "on", "transformers"}); got[0] != "search" {
		t.Fatalf("bare question starting with 'paper' should route to search, got %#v", got)
	}
	for _, topic := range []string{"docgen", "docugen"} {
		if _, ok := commandHelp[topic]; !ok {
			t.Errorf("missing help topic for %q", topic)
		}
	}
	// docgen/docugen must be typo-suggestible like every other command.
	for _, name := range []string{"docgen", "docugen"} {
		if !slices.Contains(suggestCommands, name) {
			t.Errorf("%q missing from suggestCommands", name)
		}
	}
}

func TestRunDocGenRequiresQuery(t *testing.T) {
	var out, errOut strings.Builder
	if err := runDocGen([]string{"--offline"}, &out, &errOut); err == nil {
		t.Fatalf("expected error when no topic is provided")
	}
}

func TestResolveDocGenFormat(t *testing.T) {
	cases := []struct {
		format, output string
		jsonOut        bool
		want           string
	}{
		{"", "paper.tex", false, "latex"},
		{"", "paper.LaTeX", false, "latex"},
		{"", "paper.md", false, "markdown"},
		{"", "out.json", false, "json"},
		{"", "", true, "json"},
		{"latex", "x.md", false, "latex"}, // explicit flag wins over extension
		{"tex", "", false, "latex"},
		{"md", "", false, "markdown"},
		{"json", "", false, "json"},
		{"", "", false, "markdown"},
	}
	for _, c := range cases {
		got, err := resolveDocGenFormat(c.format, c.output, c.jsonOut)
		if err != nil {
			t.Fatalf("resolveDocGenFormat(%q,%q,%v) error: %v", c.format, c.output, c.jsonOut, err)
		}
		if got != c.want {
			t.Fatalf("resolveDocGenFormat(%q,%q,%v)=%q want %q", c.format, c.output, c.jsonOut, got, c.want)
		}
	}
	if _, err := resolveDocGenFormat("pdf", "", false); err == nil {
		t.Fatalf("expected error for unknown format pdf")
	}
}

func TestLatexEscapeDecodesAndEscapes(t *testing.T) {
	out := latexEscape("Cost 50% & up $5 in Q&amp;A_test #1 — fin")
	// The HTML entity &amp; is decoded to & and then escaped to \& (so "Q&amp;A" -> "Q\&A").
	for _, want := range []string{`50\%`, `\$5`, `Q\&A`, `\_test`, `\#1`, "---"} {
		if !strings.Contains(out, want) {
			t.Fatalf("latexEscape missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "&amp;") {
		t.Fatalf("HTML entity not decoded: %q", out)
	}
}

// TestLatexEscapeEmDashNotDropped guards the "whichdue" regression: an unspaced
// em/en dash must become LaTeX ---/-- (which compile to a visible dash), never be
// silently dropped (joining the surrounding words).
func TestLatexEscapeEmDashNotDropped(t *testing.T) {
	out := latexEscape("which—due") // em dash, no surrounding spaces
	if strings.Contains(out, "whichdue") {
		t.Fatalf("em dash was dropped, joining words: %q", out)
	}
	if out != "which---due" {
		t.Fatalf("em dash should become ---, got %q", out)
	}
	if en := latexEscape("2020–2024"); en != "2020--2024" {
		t.Fatalf("en dash should become --, got %q", en)
	}
}

func TestRenderManuscriptLatexStructure(t *testing.T) {
	research := &agent.YOLOResult{Papers: []agent.Paper{
		{Title: "A Grounded Study", Authors: []string{"Doe, J."}, Year: 2021, Venue: "Nature", Link: "https://doi.org/10.1/x", CitationCount: 5},
	}}
	tex := renderManuscriptLatex("RAG & clinical_support", research, sampleManuscriptResult(), false)
	for _, want := range []string{
		`\documentclass`, `\begin{document}`, `\end{document}`,
		`\title{RAG \& clinical\_support}`, `\begin{abstract}`, `\section{Results}`,
		`\section*{Peer Review}`, `\begin{thebibliography}{99}`, `\bibitem{ref1}`,
		`\url{https://doi.org/10.1/x}`, `\emph{Nature}`,
	} {
		if !strings.Contains(tex, want) {
			t.Fatalf("latex output missing %q", want)
		}
	}
	if strings.Count(tex, `\begin{document}`) != 1 || strings.Count(tex, `\end{document}`) != 1 {
		t.Fatalf("unbalanced document environment")
	}
	// The abstract section must NOT also appear as a numbered \section.
	if strings.Contains(tex, `\section{Abstract}`) {
		t.Fatalf("abstract should render as the abstract environment, not a \\section")
	}
}

func sampleManuscriptResult() internalwisdev.ManuscriptPipelineResult {
	return internalwisdev.ManuscriptPipelineResult{
		Blueprint: internalwisdev.ManuscriptBlueprint{
			SectionOrder: []string{"abstract", "introduction", "results"},
		},
		SectionDrafts: []internalwisdev.SectionDraftArtifact{
			{SectionID: "results", Title: "Results", Content: "Treatment A improves outcome B."},
			{SectionID: "abstract", Title: "Abstract", Content: "We synthesize grounded findings."},
			{SectionID: "introduction", Title: "Introduction", Content: ""},
		},
		VisualArtifacts: []internalwisdev.VisualArtifact{
			{Title: "Concept Diagram", Kind: "concept_diagram", SpecType: "mermaid", Spec: "flowchart TD\n  a-->b", Caption: "A grounded diagram."},
		},
		CritiqueReport: map[string]any{
			"overallScore":    0.82,
			"strengths":       []string{"Strong packet lineage."},
			"weaknesses":      []string{"Introduction lacks grounding."},
			"recommendations": []string{"Rewrite introduction."},
		},
		RevisionTasks: []map[string]any{
			{"status": "pending", "title": "Rewrite Introduction"},
			{"status": "completed", "title": "Finalize"},
		},
	}
}

func TestRenderManuscriptMarkdownOrdersSectionsAndSurfacesArtifacts(t *testing.T) {
	research := &agent.YOLOResult{
		Papers: []agent.Paper{
			{Title: "A Grounded Study", Authors: []string{"Doe, J.", "Roe, R."}, Year: 2021, Venue: "Nature", Link: "https://example.org/a", CitationCount: 12},
		},
	}
	md := renderManuscriptMarkdown("CRISPR off-target detection", research, sampleManuscriptResult(), false)

	if !strings.HasPrefix(md, "# CRISPR off-target detection\n") {
		t.Fatalf("expected title heading, got:\n%s", md)
	}
	// Section order from the blueprint must be honored (abstract before results).
	abstractIdx := strings.Index(md, "## Abstract")
	resultsIdx := strings.Index(md, "## Results")
	if abstractIdx < 0 || resultsIdx < 0 || abstractIdx > resultsIdx {
		t.Fatalf("sections not rendered in blueprint order: abstract=%d results=%d", abstractIdx, resultsIdx)
	}
	// Empty section content gets a placeholder rather than being dropped.
	if !strings.Contains(md, "## Introduction") || !strings.Contains(md, "no grounded content available") {
		t.Fatalf("expected introduction placeholder, got:\n%s", md)
	}
	for _, want := range []string{
		"## Figures & Visuals",
		"```mermaid",
		"## Peer Review",
		"**Overall score:** 0.82",
		"- Strong packet lineage.",
		"## References",
		"Doe, J. & Roe, R. (2021). A Grounded Study. *Nature*. 12 citations. https://example.org/a",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("manuscript markdown missing %q in:\n%s", want, md)
		}
	}
}

func TestRenderCritiqueMarkdownHandlesAnySliceValues(t *testing.T) {
	// Values arriving through a JSON round-trip are []any, not []string.
	critique := map[string]any{
		"overallScore": 0.5,
		"risks":        []any{"Unresolved contradiction.", 42, "Coverage gap."},
	}
	out := renderCritiqueMarkdown(critique)
	if !strings.Contains(out, "**Risks**") || !strings.Contains(out, "- Unresolved contradiction.") || !strings.Contains(out, "- Coverage gap.") {
		t.Fatalf("expected risk bullets from []any, got:\n%s", out)
	}
	if strings.Contains(out, "**Strengths**") {
		t.Fatalf("did not expect a strengths section when none provided:\n%s", out)
	}
}

func TestRenderCritiqueMarkdownEmptyIsBlank(t *testing.T) {
	if got := renderCritiqueMarkdown(nil); got != "" {
		t.Fatalf("expected empty critique to render nothing, got %q", got)
	}
}

func TestDocGenSourcesFallBackToCanonicalSources(t *testing.T) {
	result := internalwisdev.ManuscriptPipelineResult{
		RawMaterials: evidence.ManuscriptRawMaterialSet{
			CanonicalSources: []evidence.CanonicalCitationRecord{
				{CanonicalID: "doi:1", Title: "Fallback Source", Authors: []string{"Doe, J.", "Roe, R."}, Year: 2019, Venue: "Journal of Tests", LandingURL: "https://example.org/f"},
				{CanonicalID: "doi:2", Title: ""}, // skipped: no title
			},
		},
	}
	sources := docGenSources(nil, result)
	if len(sources) != 1 {
		t.Fatalf("expected 1 fallback source, got %d: %v", len(sources), sources)
	}
	// Author and venue must survive from the canonical record (corpus-file mode has no
	// research.Papers, so this fallback branch is the only metadata source).
	if !strings.Contains(sources[0], "Fallback Source") || !strings.Contains(sources[0], "(2019)") {
		t.Fatalf("unexpected fallback formatting: %q", sources[0])
	}
	if !strings.Contains(sources[0], "Doe") {
		t.Fatalf("fallback reference dropped authors: %q", sources[0])
	}
	if !strings.Contains(sources[0], "Journal of Tests") {
		t.Fatalf("fallback reference dropped venue: %q", sources[0])
	}
}

func TestDocGenPapersFromResultConvertsAndFilters(t *testing.T) {
	research := &agent.YOLOResult{
		Papers: []agent.Paper{
			{Title: "Keep Me", Authors: []string{"A"}, Year: 2020, CitationCount: 3, OpenAccessURL: "u"},
			{Title: "   "}, // blank title is filtered out
		},
	}
	papers := docGenPapersFromResult(research)
	if len(papers) != 1 {
		t.Fatalf("expected 1 converted paper, got %d", len(papers))
	}
	if papers[0].Title != "Keep Me" || papers[0].OpenAccessUrl != "u" || papers[0].CitationCount != 3 {
		t.Fatalf("conversion lost fields: %+v", papers[0])
	}
	if got := docGenPapersFromResult(nil); got != nil {
		t.Fatalf("expected nil for nil result, got %v", got)
	}
}

func TestFormatDocGenReferenceAuthorVariants(t *testing.T) {
	cases := map[string][]string{
		"Solo, A.":          {"Solo, A."},
		"One, A. & Two, B.": {"One, A.", "Two, B."},
		"First, A. et al.":  {"First, A.", "Second, B.", "Third, C."},
	}
	for wantPrefix, authors := range cases {
		ref := formatDocGenReference(authors, 2020, "Title", "", "", 0)
		if !strings.HasPrefix(ref, wantPrefix) {
			t.Fatalf("authors %v: expected prefix %q, got %q", authors, wantPrefix, ref)
		}
	}
	// No authors, no year, no venue: just the title with a trailing period.
	if ref := formatDocGenReference(nil, 0, "Bare Title", "", "", 0); ref != "Bare Title." {
		t.Fatalf("unexpected bare reference: %q", ref)
	}
}
