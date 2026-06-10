package cli

import (
	"strings"
	"testing"

	agent "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"
)

const citationsTestAnswer = "## Synthesis\n" +
	"Scaffold repair shows promise [1]. Long-term outcomes remain unclear [2].\n\n" +
	"## References cited in this synthesis\n" +
	"- [1] Ada Lovelace & Alan Turing (2024). Meniscus scaffold repair. Journal of Knee Surgery. Citations: 42\n" +
	"- [2] Grace Hopper (2021). Collagen implants for meniscal tears. Citations: 9\n" +
	"garbled line without marker\n" +
	"- [not-a-number] Broken entry. Citations: 1\n" +
	"- [3] Unmatchable entry about quantum gravity loops.\n\n" +
	"## Retrieval gaps to address\n" +
	"- [9] This bullet is in a different section and must be ignored.\n"

func citationsTestPapers() []agent.Paper {
	return []agent.Paper{
		{Title: "Collagen Implants for Meniscal Tears", Authors: []string{"Grace Hopper"}, Year: 2021},
		{Title: "Meniscus scaffold repair", Authors: []string{"Ada Lovelace", "Alan Turing"}, Year: 2024},
		{Title: "Unrelated cardiology paper", Year: 2019},
	}
}

func TestParseAnswerBibliographyWellFormed(t *testing.T) {
	entries := parseAnswerBibliography(citationsTestAnswer)
	if len(entries) != 3 {
		t.Fatalf("expected 3 parsed entries, got %d: %+v", len(entries), entries)
	}
	if entries[0].Number != 1 || !strings.Contains(entries[0].Text, "Meniscus scaffold repair") {
		t.Fatalf("unexpected first entry: %+v", entries[0])
	}
	if entries[1].Number != 2 || !strings.Contains(entries[1].Text, "Collagen implants") {
		t.Fatalf("unexpected second entry: %+v", entries[1])
	}
	if entries[2].Number != 3 {
		t.Fatalf("unexpected third entry: %+v", entries[2])
	}
}

func TestParseAnswerBibliographyToleratesGarbledAndMissing(t *testing.T) {
	if entries := parseAnswerBibliography(""); entries != nil {
		t.Fatalf("expected nil for empty answer, got %+v", entries)
	}
	if entries := parseAnswerBibliography("## Synthesis\nNo bibliography here."); entries != nil {
		t.Fatalf("expected nil when section missing, got %+v", entries)
	}
	garbled := "## References cited in this synthesis\n" +
		"completely garbled\n" +
		"- missing marker\n" +
		"- [0] zero is invalid\n" +
		"- [2]   \n"
	if entries := parseAnswerBibliography(garbled); len(entries) != 0 {
		t.Fatalf("expected garbled lines skipped, got %+v", entries)
	}
}

func TestMatchCitationEntryToPaperFuzzy(t *testing.T) {
	papers := citationsTestPapers()
	// Exact normalized containment despite case/punctuation differences.
	if idx := matchCitationEntryToPaper("Grace Hopper (2021). Collagen implants, for meniscal tears! Citations: 9", papers); idx != 0 {
		t.Fatalf("expected fuzzy match to paper 0, got %d", idx)
	}
	// Containment with markdown emphasis around the title.
	if idx := matchCitationEntryToPaper("Ada Lovelace & Alan Turing (2024). *Meniscus scaffold repair*. Journal", papers); idx != 1 {
		t.Fatalf("expected match to paper 1, got %d", idx)
	}
	// Token-overlap path: partial title with most tokens present.
	if idx := matchCitationEntryToPaper("Hopper. Collagen implants meniscal tears study", papers); idx != 0 {
		t.Fatalf("expected token-overlap match to paper 0, got %d", idx)
	}
	// No confident match.
	if idx := matchCitationEntryToPaper("Entirely different topic about quantum gravity", papers); idx != -1 {
		t.Fatalf("expected no match, got %d", idx)
	}
	if idx := matchCitationEntryToPaper("", papers); idx != -1 {
		t.Fatalf("expected no match for empty entry, got %d", idx)
	}
}

func TestBuildCitationPaperMap(t *testing.T) {
	result := &agent.YOLOResult{
		FinalAnswer: citationsTestAnswer,
		Papers:      citationsTestPapers(),
	}
	mapping := buildCitationPaperMap(result)
	if len(mapping) != 2 {
		t.Fatalf("expected 2 mapped citations, got %d: %+v", len(mapping), mapping)
	}
	if mapping[1] != 1 {
		t.Fatalf("expected [1] -> paper index 1, got %d", mapping[1])
	}
	if mapping[2] != 0 {
		t.Fatalf("expected [2] -> paper index 0, got %d", mapping[2])
	}
	if _, ok := mapping[3]; ok {
		t.Fatal("unmatchable entry [3] must not be mapped")
	}
	if buildCitationPaperMap(nil) != nil {
		t.Fatal("expected nil map for nil result")
	}
	if buildCitationPaperMap(&agent.YOLOResult{FinalAnswer: citationsTestAnswer}) != nil {
		t.Fatal("expected nil map when no papers")
	}
}

func TestJumpToCitationOpensPaperDetail(t *testing.T) {
	state := &tuiState{
		result: &agent.YOLOResult{
			FinalAnswer: citationsTestAnswer,
			Papers:      citationsTestPapers(),
		},
		terminalSize: func() (int, int, error) { return 100, 30, nil },
	}
	state.citationJumpInput = "2"
	state.jumpToCitation()
	if !state.showPaperDetail {
		t.Fatal("expected paper detail popup to open")
	}
	if state.resultPane != resultPaneSources {
		t.Fatalf("expected Sources pane, got %v", state.resultPane)
	}
	if state.paperDetailIdx != 0 {
		t.Fatalf("expected paper index 0 for citation [2], got %d", state.paperDetailIdx)
	}
	if !strings.Contains(state.saveMsg, "Citation [2]") {
		t.Fatalf("expected confirmation message, got %q", state.saveMsg)
	}
}

func TestJumpToCitationUnknownNumber(t *testing.T) {
	state := &tuiState{
		result: &agent.YOLOResult{
			FinalAnswer: citationsTestAnswer,
			Papers:      citationsTestPapers(),
		},
		terminalSize: func() (int, int, error) { return 100, 30, nil },
	}
	state.citationJumpInput = "42"
	state.jumpToCitation()
	if state.showPaperDetail {
		t.Fatal("popup must not open for unknown citation")
	}
	if !strings.Contains(state.saveMsg, "No source matched citation [42]") {
		t.Fatalf("expected miss message, got %q", state.saveMsg)
	}
}

func TestJumpToCitationPositionalFallback(t *testing.T) {
	// Answer without a bibliography section: fall back to Papers[n-1].
	state := &tuiState{
		result: &agent.YOLOResult{
			FinalAnswer: "Plain answer with [1] but no bibliography.",
			Papers:      citationsTestPapers(),
		},
		terminalSize: func() (int, int, error) { return 100, 30, nil },
	}
	state.citationJumpInput = "1"
	state.jumpToCitation()
	if !state.showPaperDetail || state.paperDetailIdx != 0 {
		t.Fatalf("expected positional fallback to paper 0, got detail=%v idx=%d", state.showPaperDetail, state.paperDetailIdx)
	}
}

func TestFormatGroundingScorecard(t *testing.T) {
	if card := formatGroundingScorecard(nil); card != "" {
		t.Fatalf("expected empty card for nil stats, got %q", card)
	}
	grounding := &agent.GroundingStats{GroundedClaims: 14, TotalClaims: 16, UnsupportedClaims: 2, CitedSources: 5}
	card := formatGroundingScorecard(grounding)
	for _, want := range []string{"Grounding:", "14/16 claims cited", "5 source(s)", "2 unsupported"} {
		if !strings.Contains(card, want) {
			t.Fatalf("expected scorecard to contain %q: %s", want, card)
		}
	}
	clean := &agent.GroundingStats{GroundedClaims: 4, TotalClaims: 4, CitedSources: 3}
	if got := formatGroundingScorecard(clean); strings.Contains(got, "unsupported") {
		t.Fatalf("expected no unsupported segment, got %q", got)
	}
}

func TestGroundingNeedsAttention(t *testing.T) {
	if groundingNeedsAttention(nil) {
		t.Fatal("nil stats must not warn")
	}
	if groundingNeedsAttention(&agent.GroundingStats{GroundedClaims: 4, TotalClaims: 4}) {
		t.Fatal("fully grounded stats must not warn")
	}
	if !groundingNeedsAttention(&agent.GroundingStats{GroundedClaims: 4, TotalClaims: 4, UnsupportedClaims: 1}) {
		t.Fatal("unsupported claims must warn")
	}
	if !groundingNeedsAttention(&agent.GroundingStats{GroundedClaims: 1, TotalClaims: 3}) {
		t.Fatal("low cited ratio must warn")
	}
}

func TestSectionGroundingSuffix(t *testing.T) {
	grounding := &agent.GroundingStats{
		GroundedClaims: 5,
		TotalClaims:    6,
		Sections: []agent.SectionGrounding{
			{Heading: "Synthesis", GroundedClaims: 3, TotalClaims: 4},
			{Heading: "Key literature", GroundedClaims: 2, TotalClaims: 2},
			{Heading: "Empty section"},
		},
	}
	if got := sectionGroundingSuffix(grounding, "Synthesis"); got != "(3/4 cited)" {
		t.Fatalf("unexpected suffix: %q", got)
	}
	// Case and punctuation insensitive heading match.
	if got := sectionGroundingSuffix(grounding, "key literature:"); got != "(2/2 cited)" {
		t.Fatalf("unexpected suffix: %q", got)
	}
	if got := sectionGroundingSuffix(grounding, "Empty section"); got != "" {
		t.Fatalf("zero-claim sections must not annotate, got %q", got)
	}
	if got := sectionGroundingSuffix(grounding, "Unknown heading"); got != "" {
		t.Fatalf("unknown heading must not annotate, got %q", got)
	}
	if got := sectionGroundingSuffix(nil, "Synthesis"); got != "" {
		t.Fatalf("nil stats must not annotate, got %q", got)
	}
}

func TestFormatAnswerLinesForTUIGroundedAddsSectionSuffix(t *testing.T) {
	theme := scholarlmTheme
	grounding := &agent.GroundingStats{
		GroundedClaims: 3,
		TotalClaims:    4,
		Sections: []agent.SectionGrounding{
			{Heading: "Synthesis", GroundedClaims: 3, TotalClaims: 4},
		},
	}
	answer := "## Synthesis\nSome claim [1].\n\n## Grounding audit\nMeta text."
	joined := strings.Join(formatAnswerLinesForTUIGrounded(answer, 100, theme, grounding), "\n")
	if !strings.Contains(joined, "(3/4 cited)") {
		t.Fatalf("expected per-section grounding suffix: %s", joined)
	}
	// Meta sections never receive a suffix.
	if strings.Count(joined, "cited)") != 1 {
		t.Fatalf("expected exactly one suffix: %s", joined)
	}
	// Without grounding the rendering is unchanged.
	plainJoined := strings.Join(formatAnswerLinesForTUI(answer, 100, theme), "\n")
	if strings.Contains(plainJoined, "cited)") {
		t.Fatalf("expected no suffix without grounding stats: %s", plainJoined)
	}
}

func TestBuildTUIResultLinesIncludesGroundingScorecard(t *testing.T) {
	s := &tuiState{
		result: &agent.YOLOResult{
			FinalAnswer: "Answer body.",
			Iterations:  1,
			PapersFound: 2,
			Grounding:   &agent.GroundingStats{GroundedClaims: 14, TotalClaims: 16, UnsupportedClaims: 2, CitedSources: 5},
		},
	}
	lines := buildTUIResultLines(s, 100, resultPaneAnswer)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "14/16 claims cited") {
		t.Fatalf("expected grounding scorecard in result header: %s", joined)
	}
}

func TestFormatYOLOResultMarkdownIncludesGrounding(t *testing.T) {
	result := &agent.YOLOResult{
		FinalAnswer: "Answer.",
		Iterations:  1,
		Grounding:   &agent.GroundingStats{GroundedClaims: 14, TotalClaims: 16, UnsupportedClaims: 2, CitedSources: 5},
	}
	md := formatYOLOResultMarkdown("query", result, 0, nil, nil)
	if !strings.Contains(md, "- Grounding: 14/16 claims cited · 5 source(s) · 2 unsupported") {
		t.Fatalf("expected grounding line in markdown run summary: %s", md)
	}
}
