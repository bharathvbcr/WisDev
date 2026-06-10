package cli

import (
	"strings"
	"testing"

	agent "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"
)

func plainLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, removeEscapeSequences(line))
	}
	return out
}

func joinedPlain(lines []string) string {
	return strings.Join(plainLines(lines), "\n")
}

func TestRenderConfidenceBarFillAndValue(t *testing.T) {
	theme := activeTheme()
	cases := []struct {
		score  float64
		filled int
		label  string
	}{
		{0.52, 5, "0.52"},
		{0.72, 7, "0.72"},
		{0.0, 0, "0.00"},
		{1.0, 10, "1.00"},
		{1.7, 10, "1.70"}, // clamped fill, raw score label
	}
	for _, tc := range cases {
		plain := removeEscapeSequences(renderConfidenceBar(tc.score, theme))
		if got := strings.Count(plain, "▓"); got != tc.filled {
			t.Fatalf("score %.2f: filled glyphs = %d, want %d (%q)", tc.score, got, tc.filled, plain)
		}
		if got := strings.Count(plain, "░"); got != confidenceBarWidth-tc.filled {
			t.Fatalf("score %.2f: empty glyphs = %d, want %d", tc.score, got, confidenceBarWidth-tc.filled)
		}
		if !strings.HasSuffix(plain, tc.label) {
			t.Fatalf("score %.2f: expected %q suffix in %q", tc.score, tc.label, plain)
		}
	}
}

func TestConfidenceBandStyle(t *testing.T) {
	theme := activeTheme()
	if got := confidenceBandStyle(0.85, theme); got != theme.HealthOK {
		t.Fatalf("high band style = %q, want HealthOK", got)
	}
	if got := confidenceBandStyle(0.55, theme); got != "" {
		t.Fatalf("neutral band style = %q, want empty", got)
	}
	if got := confidenceBandStyle(0.2, theme); got != theme.StatusWarn {
		t.Fatalf("low band style = %q, want StatusWarn", got)
	}
}

func TestBuildHypothesesPaneLines(t *testing.T) {
	theme := activeTheme()
	result := &agent.YOLOResult{
		Hypotheses: []agent.Hypothesis{
			{
				Claim:                   "Scaffold-based meniscus repair restores load distribution",
				ConfidenceScore:         0.72,
				Status:                  "supported",
				FalsifiabilityCondition: "registry data show no difference vs meniscectomy",
			},
			{Claim: "Allografts fail earlier in young athletes", ConfidenceScore: 0.31, Status: "active"},
		},
		Beliefs: []agent.Belief{
			{Claim: "Early mobilization is safe post-repair", Confidence: 0.8, Status: "active", SupportCount: 3},
			{Claim: "Suture repair outperforms scaffolds", Confidence: 0.45, Status: "active", SupportCount: 2, ContradictionCount: 1},
		},
	}
	text := joinedPlain(buildHypothesesPaneLines(result, 100, theme, false))

	for _, want := range []string{
		"Hypotheses (2):",
		"Scaffold-based meniscus repair restores load distribution",
		"[supported]",
		"falsifiable if: registry data show no difference vs meniscectomy",
		"Beliefs (2):",
		"(2 for / 1 against)",
		"Tensions (beliefs with contradicting evidence):",
		"Suture repair outperforms scaffolds (2 supporting vs 1 contradicting)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("hypotheses pane missing %q in:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "▓▓▓▓▓▓▓░░░ 0.72") {
		t.Fatalf("expected 0.72 confidence bar in:\n%s", text)
	}
}

func TestBuildHypothesesPaneLinesEmptyDedicatedPane(t *testing.T) {
	theme := activeTheme()
	text := joinedPlain(buildHypothesesPaneLines(&agent.YOLOResult{}, 100, theme, false))
	if !strings.Contains(text, "No hypotheses or beliefs recorded") {
		t.Fatalf("expected empty-state message, got:\n%s", text)
	}
	if lines := buildHypothesesPaneLines(&agent.YOLOResult{}, 100, theme, true); len(lines) != 0 {
		t.Fatalf("expected no lines in All pane when empty, got %v", lines)
	}
}

func TestBuildHypothesesPaneLinesCapsAllPane(t *testing.T) {
	theme := activeTheme()
	result := &agent.YOLOResult{}
	for i := 0; i < maxHypothesesInAllPane+3; i++ {
		result.Hypotheses = append(result.Hypotheses, agent.Hypothesis{Claim: "claim", ConfidenceScore: 0.5})
	}
	text := joinedPlain(buildHypothesesPaneLines(result, 100, theme, true))
	if !strings.Contains(text, "+3 more in Hypotheses pane (y)") {
		t.Fatalf("expected overflow hint, got:\n%s", text)
	}
}

func TestBuildCoverageGapLines(t *testing.T) {
	theme := activeTheme()
	result := &agent.YOLOResult{
		Gaps: &agent.CoverageGaps{
			Sufficient:               false,
			PlannedQueryCount:        4,
			ExecutedQueryCount:       2,
			UnexecutedPlannedQueries: []string{"meniscus registry outcomes", "pediatric scaffold trials"},
			MissingAspects:           []string{"long-term failure rates"},
			QueriesWithoutCoverage:   []string{"scaffold immunogenicity"},
		},
	}
	text := joinedPlain(buildCoverageGapLines(result, 100, theme))
	for _, want := range []string{
		"Coverage: 2/4 planned queries executed",
		"○ meniscus registry outcomes",
		"○ pediatric scaffold trials",
		"Open gaps:",
		"• long-term failure rates",
		"• no sources found for: scaffold immunogenicity",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("coverage block missing %q in:\n%s", want, text)
		}
	}
}

func TestBuildCoverageGapLinesNoGaps(t *testing.T) {
	theme := activeTheme()
	if lines := buildCoverageGapLines(&agent.YOLOResult{}, 100, theme); lines != nil {
		t.Fatalf("expected nil for run without coverage data, got %v", lines)
	}
	text := joinedPlain(buildCoverageGapLines(&agent.YOLOResult{
		Gaps: &agent.CoverageGaps{Sufficient: true, PlannedQueryCount: 3, ExecutedQueryCount: 3},
	}, 100, theme))
	if !strings.Contains(text, "Coverage: 3/3 planned queries executed") || !strings.Contains(text, "No open coverage gaps.") {
		t.Fatalf("unexpected sufficient coverage block:\n%s", text)
	}
}

func TestResolveCoverageGapsDerivedFromQueries(t *testing.T) {
	result := &agent.YOLOResult{
		PlannedQueries:  []string{"alpha", "beta", "gamma"},
		ExecutedQueries: []string{"Alpha", "gamma"},
	}
	gaps := resolveCoverageGaps(result)
	if gaps == nil {
		t.Fatal("expected derived coverage gaps")
	}
	if gaps.PlannedQueryCount != 3 || gaps.ExecutedQueryCount != 2 {
		t.Fatalf("derived counts = %d/%d, want 2/3", gaps.ExecutedQueryCount, gaps.PlannedQueryCount)
	}
	if len(gaps.UnexecutedPlannedQueries) != 1 || gaps.UnexecutedPlannedQueries[0] != "beta" {
		t.Fatalf("unexecuted = %v, want [beta]", gaps.UnexecutedPlannedQueries)
	}
}

func TestQueriesPaneShowsCoverageBlock(t *testing.T) {
	s := &tuiState{
		result: &agent.YOLOResult{
			FinalAnswer:     "answer",
			PlannedQueries:  []string{"alpha", "beta"},
			ExecutedQueries: []string{"alpha"},
		},
	}
	text := joinedPlain(buildTUIResultLines(s, 100, resultPaneQueries))
	if !strings.Contains(text, "Coverage: 1/2 planned queries executed") {
		t.Fatalf("queries pane missing coverage block:\n%s", text)
	}
	if !strings.Contains(text, "○ beta") {
		t.Fatalf("queries pane missing unexecuted query marker:\n%s", text)
	}
	if strings.Contains(text, "Planned Research Agenda:") {
		t.Fatalf("agenda list should be consolidated into the coverage block:\n%s", text)
	}
}

func TestFormatHypothesesAndBeliefsMarkdown(t *testing.T) {
	result := &agent.YOLOResult{
		Hypotheses: []agent.Hypothesis{
			{Claim: "Scaffolds restore function", ConfidenceScore: 0.6, Status: "active", FalsifiabilityCondition: "no functional gain at 24 months"},
		},
		Beliefs: []agent.Belief{
			{Claim: "Repair beats resection", Confidence: 0.7, Status: "active", SupportCount: 2, ContradictionCount: 1},
		},
	}
	md := formatHypothesesAndBeliefsMarkdown(result)
	for _, want := range []string{
		"## Hypotheses & beliefs",
		"- [0.60] (active) Scaffolds restore function",
		"Falsifiable if: no functional gain at 24 months",
		"### Beliefs",
		"- [0.70] (active) Repair beats resection — 2 supporting / 1 contradicting",
		"### Tensions",
		"- Repair beats resection (2 supporting vs 1 contradicting)",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q in:\n%s", want, md)
		}
	}
	if formatHypothesesAndBeliefsMarkdown(&agent.YOLOResult{}) != "" {
		t.Fatal("expected empty markdown section for empty result")
	}
}
