package wisdev

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/rag"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

func TestFormatSynthesisPaperBulletsPrefersRecentPapers(t *testing.T) {
	lines := formatSynthesisPaperBullets("meniscus scaffolds recent research", []search.Paper{
		{Title: "Older landmark review", Abstract: "meniscus repair overview", Year: 2013, CitationCount: 147},
		{Title: "Recent meniscus scaffold trial", Abstract: "meniscus scaffold hydrogel outcomes", Year: 2024, CitationCount: 18},
	}, nil, 2, nil)
	if len(lines) != 2 {
		t.Fatalf("expected 2 bullets, got %#v", lines)
	}
	if !strings.Contains(lines[0], "Recent meniscus scaffold trial") {
		t.Fatalf("expected recent paper first, got %#v", lines)
	}
}

func TestFormatSynthesisPaperBulletsIncludeAuthorYearCitations(t *testing.T) {
	lines := formatSynthesisPaperBullets("acl reconstruction", []search.Paper{{
		Title:         "ACL graft outcomes",
		Abstract:      "Hamstring grafts showed lower re-tear rates in adolescent athletes.",
		Authors:       []string{"Smith, J.", "Lee, K."},
		Year:          2023,
		CitationCount: 41,
		Venue:         "American Journal of Sports Medicine",
	}}, nil, 1, nil)
	if len(lines) != 1 {
		t.Fatalf("expected 1 bullet, got %#v", lines)
	}
	line := lines[0]
	for _, want := range []string{"Smith", "Lee", "(2023", "41 citations", "report that", "ACL graft outcomes"} {
		if !strings.Contains(line, want) {
			t.Fatalf("expected %q in bullet, got %q", want, line)
		}
	}
	if strings.HasSuffix(line, "...") {
		t.Fatalf("bullet should not end with ellipsis: %q", line)
	}
}

func TestHeuristicSynthesisGroupsCompoundTopics(t *testing.T) {
	query := "meniscus scaffolds and acl reconstruction strategies"
	papers := []search.Paper{
		{Title: "Meniscus scaffold hydrogel", Abstract: "meniscus scaffold repair", Year: 2024, CitationCount: 12, Authors: []string{"Chen, L."}},
		{Title: "ACL reconstruction outcomes", Abstract: "acl reconstruction strategies in sport", Year: 2023, CitationCount: 40, Authors: []string{"Patel, R."}},
	}
	text := heuristicSynthesisWithoutLLM(query, papers, nil)
	if !strings.Contains(text, "##") {
		t.Fatalf("expected section headings in compound synthesis: %s", text)
	}
	keyStart := strings.Index(text, "## Key literature")
	keyEnd := strings.Index(text, "## References cited in this synthesis")
	if keyEnd < 0 {
		keyEnd = len(text)
	}
	keyLit := text[keyStart:keyEnd]
	if strings.Count(keyLit, "Meniscus scaffold hydrogel") > 1 {
		t.Fatalf("expected deduped paper mentions in key literature: %s", keyLit)
	}
	if !strings.Contains(text, "Provisional Research Synthesis") {
		t.Fatalf("expected provisional heading: %s", text)
	}
	if !strings.Contains(text, "Questions worth investigating") {
		t.Fatalf("expected investigator prompts: %s", text)
	}
	if !strings.Contains(text, "Evidence mix") {
		t.Fatalf("expected evidence mix section: %s", text)
	}
	if !strings.Contains(text, "[1]") {
		t.Fatalf("expected numbered inline citations: %s", text)
	}
	if !strings.Contains(text, "Executive takeaway") {
		t.Fatalf("expected executive takeaway section: %s", text)
	}
	if !strings.Contains(text, "Retrieval gaps") {
		t.Fatalf("expected retrieval gaps section: %s", text)
	}
}

func TestBuildSynthesisExecutiveTakeaway(t *testing.T) {
	papers := []search.Paper{{
		ID: "p1", Title: "Meniscus scaffold hydrogel", Abstract: "Hydrogel scaffolds improve meniscus repair outcomes.",
		Authors: []string{"Chen, L."}, Year: 2024, CitationCount: 12,
	}}
	lines := buildSynthesisExecutiveTakeaway("meniscus scaffolds", papers, nil, buildCitationRegistry(papers))
	if len(lines) == 0 || !strings.Contains(lines[0], "Chen") {
		t.Fatalf("expected lead author in takeaway: %#v", lines)
	}
}

func TestBuildSynthesisRetrievalGapsSparseCorpus(t *testing.T) {
	gaps := buildSynthesisRetrievalGaps("acl repair", []search.Paper{{Title: "One paper", Year: 2018}}, []search.Paper{{Title: "One paper"}}, nil)
	if len(gaps) == 0 {
		t.Fatal("expected retrieval gap guidance")
	}
	joined := strings.Join(gaps, " ")
	if !strings.Contains(joined, "2022") && !strings.Contains(joined, "Only 1") {
		t.Fatalf("expected sparse/recent gap hints: %s", joined)
	}
}

func TestPolishSynthesisText(t *testing.T) {
	got := polishSynthesisText("Sentence one.. Sentence two.\n\n\nParagraph two.")
	if strings.Contains(got, "..") || strings.Contains(got, "\n\n\n") {
		t.Fatalf("expected polished text, got %q", got)
	}
}

func TestSynthesisOrderedTopicSectionsPreservesClauseOrder(t *testing.T) {
	query := "meniscus scaffolds and acl reconstruction strategies"
	papers := []search.Paper{
		{Title: "ACL reconstruction outcomes", Abstract: "acl reconstruction strategies", Year: 2023},
		{Title: "Meniscus scaffold hydrogel", Abstract: "meniscus scaffold repair", Year: 2024},
	}
	sections := synthesisOrderedTopicSections(query, papers)
	if len(sections) < 2 {
		t.Fatalf("expected ordered sections, got %#v", sections)
	}
	if !strings.Contains(strings.ToLower(sections[0].Label), "meniscus") {
		t.Fatalf("expected first clause section first, got %#v", sections)
	}
}

func TestNumberedCitationRegistry(t *testing.T) {
	papers := []search.Paper{
		{ID: "p1", Title: "Paper A", Authors: []string{"Ada"}, Year: 2024, CitationCount: 5},
		{ID: "p2", Title: "Paper B", Authors: []string{"Bob"}, Year: 2023, CitationCount: 9},
	}
	registry := buildCitationRegistry(papers)
	if registry.marker(papers[0]) != "[1]" || registry.marker(papers[1]) != "[2]" {
		t.Fatalf("unexpected registry markers: %#v", registry)
	}
	line := formatSynthesisPaperBullets("query", papers[:1], nil, 1, registry)[0]
	if !strings.HasPrefix(line, "- [1]") {
		t.Fatalf("expected numbered bullet prefix, got %q", line)
	}
}

func TestFormatSynthesisEvidenceBulletsSkipsTitleAsClaim(t *testing.T) {
	papers := []search.Paper{{
		Title:         "ACL reconstruction outcomes",
		Authors:       []string{"Jones, A."},
		Year:          2022,
		CitationCount: 15,
	}}
	lines := formatSynthesisEvidenceBullets([]EvidenceItem{{
		PaperTitle: "ACL reconstruction outcomes",
		Claim:      "ACL reconstruction outcomes",
		Snippet:    "Hamstring grafts showed lower re-tear rates.",
	}}, papers, 3, nil)
	if len(lines) != 1 || !strings.Contains(lines[0], "Hamstring grafts") {
		t.Fatalf("unexpected evidence bullets: %#v", lines)
	}
	if !strings.Contains(lines[0], "Jones") || !strings.Contains(lines[0], "(2022") {
		t.Fatalf("expected inline author-year metadata in evidence bullet: %#v", lines)
	}
}

func TestEnrichProseAnswerWithInlineCitationsACLStyle(t *testing.T) {
	prose := "## Clinical Context of Concomitant ACL and Meniscal Injuries\n\n" +
		"The anterior cruciate ligament (ACL) is a critical stabilizer of the knee joint. " +
		"Patients who sustain an ACL injury face a high risk of developing post-traumatic osteoarthritis.\n\n" +
		"## Surgical Management and Concomitant Repairs\n\n" +
		"Concomitant meniscus repairs are frequently performed during ACL-R. " +
		"Meniscus Allograft Transplantation (MAT) can be performed alongside ACL-R in revision settings."
	papers := []search.Paper{
		{ID: "p1", Title: "ACL reconstruction and osteoarthritis risk", Abstract: "ACL injury and post-traumatic osteoarthritis risk after reconstruction", Authors: []string{"Smith, J."}, Year: 2021, CitationCount: 88},
		{ID: "p2", Title: "Meniscus repair during ACL reconstruction", Abstract: "Concomitant meniscus repairs during ACL-R improve stability", Authors: []string{"Patel, R."}, Year: 2023, CitationCount: 34},
		{ID: "p3", Title: "Meniscus allograft transplantation with ACL-R", Abstract: "MAT alongside ACL-R in revision settings restores contact mechanics", Authors: []string{"Nguyen, T."}, Year: 2024, CitationCount: 19},
	}
	enriched := enrichProseAnswerWithInlineCitations(prose, papers)
	if !strings.Contains(enriched, "[1]") || !strings.Contains(enriched, "Smith") {
		t.Fatalf("expected numbered author citation in ACL prose: %s", enriched)
	}
	if !strings.Contains(enriched, "References cited in this synthesis") {
		t.Fatalf("expected bibliography section: %s", enriched)
	}
	if strings.Contains(enriched, "   .") {
		t.Fatalf("expected whitespace-before-period cleanup: %s", enriched)
	}
}

func TestRenderStructuredAnswerWithInlineCitationsPlainLLMText(t *testing.T) {
	answer := &rag.StructuredAnswer{
		Text: "## Clinical Context\n\nACL injuries are common and often occur with meniscus tears.",
	}
	papers := []search.Paper{{
		ID: "p1", Title: "ACL and meniscus injuries", Abstract: "ACL injuries common with meniscus tears",
		Authors: []string{"Lee, K."}, Year: 2022, CitationCount: 25,
	}}
	query := "ACL injuries and meniscus tears"
	rendered := renderStructuredAnswerWithInlineCitations(query, answer, papers, nil, nil)
	if !strings.Contains(rendered, "[1]") || !strings.Contains(rendered, "Lee") {
		t.Fatalf("expected prose enrichment for plain LLM answer: %q", rendered)
	}
	if !strings.Contains(rendered, "Executive takeaway") {
		t.Fatalf("expected researcher front matter for LLM prose: %q", rendered)
	}
	if !strings.Contains(rendered, "Questions worth investigating") {
		t.Fatalf("expected researcher back matter for LLM prose: %q", rendered)
	}
}

func TestRenderStructuredAnswerWithInlineCitations(t *testing.T) {
	answer := &rag.StructuredAnswer{
		Sections: []rag.AnswerSection{{
			Heading: "Findings",
			Sentences: []rag.AnswerClaim{{
				Text:        "Hydrogel scaffolds improve meniscus repair",
				EvidenceIDs: []string{"p1"},
			}},
		}},
	}
	papers := []search.Paper{{
		ID:            "p1",
		Title:         "Meniscus scaffold hydrogel",
		Authors:       []string{"Chen, L."},
		Year:          2024,
		CitationCount: 12,
	}}
	query := "meniscus scaffold hydrogel"
	rendered := renderStructuredAnswerWithInlineCitations(query, answer, papers, nil, nil)
	if !strings.Contains(rendered, "Hydrogel scaffolds improve meniscus repair") {
		t.Fatalf("expected claim text: %q", rendered)
	}
	if !strings.Contains(rendered, "Chen") || !strings.Contains(rendered, "2024") {
		t.Fatalf("expected inline citation in rendered answer: %q", rendered)
	}
}

func TestFinalizeResearchAnswerPrependsAndAppendsResearcherSections(t *testing.T) {
	query := "ACL reconstruction meniscus repair"
	prose := "## Clinical Context\n\nACL injuries are common and often occur with meniscus tears."
	papers := []search.Paper{{
		ID: "p1", Title: "ACL and meniscus injuries", Abstract: "ACL injuries common with meniscus tears",
		Authors: []string{"Lee, K."}, Year: 2022, CitationCount: 25,
	}}
	final := finalizeResearchAnswer(query, prose, papers, nil, nil)
	for _, want := range []string{"Executive takeaway", "Research landscape", "Evidence mix", "[1]", "Lee", "References cited in this synthesis", "Questions worth investigating"} {
		if !strings.Contains(final, want) {
			t.Fatalf("expected %q in finalized answer, got:\n%s", want, final)
		}
	}
}

func TestSplitMarkdownBlockForEnrichmentSeparatesHeadingAndBody(t *testing.T) {
	parts := splitMarkdownBlockForEnrichment("## Clinical Context\nACL injuries are common.")
	if len(parts) != 2 {
		t.Fatalf("expected heading and body split, got %#v", parts)
	}
	if parts[0] != "## Clinical Context" {
		t.Fatalf("unexpected heading: %q", parts[0])
	}
	if !strings.Contains(parts[1], "ACL injuries") {
		t.Fatalf("unexpected body: %q", parts[1])
	}
}

func TestEnrichParagraphWithCitationsUsesGroundingWarningOnWeakOverlap(t *testing.T) {
	paragraph := "Quantum chromodynamics in hadron colliders shows novel phase transitions."
	papers := []search.Paper{{
		ID: "p1", Title: "ACL reconstruction outcomes", Abstract: "ACL graft healing in athletes",
		Authors: []string{"Lee, K."}, Year: 2022, CitationCount: 25,
	}}
	registry := buildCitationRegistry(papers)
	used := map[string]struct{}{}
	got := enrichParagraphWithCitations(paragraph, papers, nil, registry, used, "ACL reconstruction")
	if strings.Contains(got, "[1]") {
		t.Fatalf("expected no forced citation on weak overlap, got %q", got)
	}
	if !strings.Contains(got, "requires verification") {
		t.Fatalf("expected grounding warning, got %q", got)
	}
}

func TestEvidenceAwareCitationPrefersSnippetBackedPaper(t *testing.T) {
	paragraph := "ACL reconstruction is not superior to conservative management for preventing post-traumatic osteoarthritis."
	papers := []search.Paper{
		{ID: "p1", Title: "Meniscus repair techniques", Abstract: "meniscus scaffold outcomes", Authors: []string{"Chen, L."}, Year: 2024, CitationCount: 12},
		{ID: "p2", Title: "ACL reconstruction versus conservative care", Abstract: "PTOA outcomes after ACL injury", Authors: []string{"Smith, J."}, Year: 2021, CitationCount: 88},
	}
	evidence := []EvidenceItem{{
		PaperID: "p2",
		Claim:   "ACL reconstruction is not superior to conservative management for preventing post-traumatic osteoarthritis",
		Snippet: "current clinical evidence is insufficient to prove ACL-R superiority over conservative care for PTOA prevention",
	}}
	registry := buildCitationRegistry(papers)
	used := map[string]struct{}{}
	got := enrichParagraphWithCitations(paragraph, papers, evidence, registry, used, "ACL reconstruction meniscus")
	if !strings.Contains(got, "[2]") && !strings.Contains(got, "Smith") {
		t.Fatalf("expected evidence-backed Smith citation, got %q", got)
	}
	if strings.Contains(got, "requires verification") {
		t.Fatalf("expected grounded citation, not verification warning: %q", got)
	}
}

func TestFinalizeResearchAnswerIncludesGroundingAudit(t *testing.T) {
	query := "ACL reconstruction outcomes"
	prose := "## Clinical Context\n\nACL injuries are common and often occur with meniscus tears."
	papers := []search.Paper{{
		ID: "p1", Title: "ACL and meniscus injuries", Abstract: "ACL injuries common with meniscus tears",
		Authors: []string{"Lee, K."}, Year: 2022, CitationCount: 25,
	}}
	evidence := []EvidenceItem{{PaperID: "p1", Claim: "ACL injuries common with meniscus tears", Snippet: "ACL injuries are common"}}
	final := finalizeResearchAnswer(query, prose, papers, evidence, nil)
	if !strings.Contains(final, "## Grounding audit") {
		t.Fatalf("expected grounding audit section, got:\n%s", final)
	}
}

func TestFinalizeResearchAnswerIncludesLoopGapAspects(t *testing.T) {
	query := "ACL reconstruction outcomes"
	prose := "## Clinical Context\n\nACL injuries are common."
	papers := []search.Paper{{
		ID: "p1", Title: "ACL outcomes", Abstract: "ACL reconstruction outcomes",
		Authors: []string{"Lee, K."}, Year: 2022, CitationCount: 25,
	}}
	gap := &LoopGapState{
		MissingAspects:     []string{"longitudinal follow-up beyond 2 years"},
		MissingSourceTypes: []string{"randomized trial"},
		Contradictions:     []string{"graft choice effects conflict across cohorts"},
		NextQueries:        []string{"ACL reconstruction randomized trial 5 year"},
	}
	final := finalizeResearchAnswer(query, prose, papers, nil, gap)
	for _, want := range []string{"Coverage gap: longitudinal follow-up", "Missing source type: randomized trial", "Unresolved contradiction: graft choice", "Suggested follow-up retrieval"} {
		if !strings.Contains(final, want) {
			t.Fatalf("expected %q in gap-aware answer, got:\n%s", want, final)
		}
	}
}

func TestFinalizeResearchAnswerSkipsFrontMatterForHeuristic(t *testing.T) {
	query := "meniscus scaffolds"
	text := "## Provisional Research Synthesis\n\nFindings about scaffolds."
	papers := []search.Paper{{Title: "Scaffold review", Abstract: "meniscus scaffold", Year: 2021}}
	final := finalizeResearchAnswer(query, text, papers, nil, nil)
	if strings.Contains(final, "## Executive takeaway") {
		t.Fatalf("heuristic answer should not get duplicate executive takeaway: %s", final)
	}
}

func TestPaperSourceLabelFallsBackToVenue(t *testing.T) {
	label := paperSourceLabel(search.Paper{Title: "ACL outcomes after reconstruction", Venue: "American Journal of Sports Medicine", Year: 2022})
	if strings.Contains(label, "Unknown") {
		t.Fatalf("expected venue-based label, got %q", label)
	}
	if !strings.Contains(label, "American Journal") {
		t.Fatalf("expected venue in label, got %q", label)
	}
}

func TestDetectSynthesisMode(t *testing.T) {
	if detectSynthesisMode("## Provisional Research Synthesis\n\nBody") != "heuristic" {
		t.Fatal("expected heuristic mode")
	}
	if detectSynthesisMode("## Clinical context\n\nBody") != "llm" {
		t.Fatal("expected llm mode")
	}
}

func TestFinalizeHeuristicAddsLoopCritiqueGaps(t *testing.T) {
	query := "meniscus scaffolds"
	text := heuristicSynthesisWithoutLLM(query, []search.Paper{{
		Title: "Scaffold review", Abstract: "meniscus scaffold repair", Year: 2024, Authors: []string{"Chen, L."},
	}}, nil)
	gap := &LoopGapState{MissingAspects: []string{"randomized trial evidence"}}
	final := finalizeResearchAnswer(query, text, []search.Paper{{Title: "Scaffold review", Abstract: "meniscus scaffold", Year: 2024}}, nil, gap)
	if !strings.Contains(final, "## Grounding audit") {
		t.Fatalf("expected grounding audit in heuristic finalize: %s", final)
	}
	if !strings.Contains(final, "Loop critique gaps") || !strings.Contains(final, "randomized trial") {
		t.Fatalf("expected loop critique gaps, got:\n%s", final)
	}
}

func TestCleanAbstractLeadInStripsBackgroundPrefix(t *testing.T) {
	got := firstEvidenceSentence("BACKGROUND: Recent studies have shown that BMSC-Exos can be used for tissue repair.")
	if strings.Contains(strings.ToUpper(got), "BACKGROUND:") {
		t.Fatalf("expected BACKGROUND prefix stripped, got %q", got)
	}
	if !strings.Contains(got, "Recent studies") {
		t.Fatalf("expected sentence body preserved, got %q", got)
	}
}

func TestTrimEvidenceTextAvoidsMidWordEllipsis(t *testing.T) {
	text := "Healing of meniscus tears remains challenging because vascular supply is limited throughout most of the tissue."
	got := trimEvidenceText(text, 60)
	if strings.HasSuffix(got, "...") {
		t.Fatalf("trimEvidenceText should not append ellipsis, got %q", got)
	}
	if strings.Contains(got, "througho") || strings.Contains(got, "tissu") {
		t.Fatalf("trimEvidenceText should respect word boundaries, got %q", got)
	}
}

func TestFormatSynthesisBibliography(t *testing.T) {
	lines := formatSynthesisBibliography([]search.Paper{{
		Title:         "Scaffold review",
		Authors:       []string{"Nguyen, T.", "Park, S.", "Wu, H.", "Li, M."},
		Year:          2021,
		Venue:         "Biomaterials",
		CitationCount: 88,
	}}, 3, nil)
	if len(lines) != 1 {
		t.Fatalf("expected 1 bibliography line, got %#v", lines)
	}
	if !strings.Contains(lines[0], "Nguyen") || !strings.Contains(lines[0], "et al.") {
		t.Fatalf("expected et al. author formatting: %q", lines[0])
	}
	if !strings.Contains(lines[0], "(2021)") || !strings.Contains(lines[0], "Citations: 88") {
		t.Fatalf("expected year and citation count: %q", lines[0])
	}
}
