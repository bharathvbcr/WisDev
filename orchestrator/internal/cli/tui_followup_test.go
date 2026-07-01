package cli

import (
	"context"
	"strings"
	"testing"
)

func TestComposeFollowUpQuery(t *testing.T) {
	cases := []struct {
		name     string
		prev     string
		followUp string
		want     string
	}{
		{
			name:     "carries prior context",
			prev:     "sleep and memory consolidation",
			followUp: "what about REM specifically?",
			want:     "what about REM specifically? (follow-up to: sleep and memory consolidation)",
		},
		{
			name:     "empty follow-up keeps previous query",
			prev:     "sleep and memory consolidation",
			followUp: "   ",
			want:     "sleep and memory consolidation",
		},
		{
			name:     "no previous query uses follow-up alone",
			prev:     "",
			followUp: "what about REM?",
			want:     "what about REM?",
		},
		{
			name:     "identical question is not duplicated",
			prev:     "Sleep and memory",
			followUp: "sleep and memory",
			want:     "sleep and memory",
		},
		{
			name:     "trims whitespace",
			prev:     "  base query  ",
			followUp: "  deeper question  ",
			want:     "deeper question (follow-up to: base query)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := composeFollowUpQuery(tc.prev, tc.followUp); got != tc.want {
				t.Fatalf("composeFollowUpQuery(%q, %q) = %q, want %q", tc.prev, tc.followUp, got, tc.want)
			}
		})
	}
}

func TestMoveSettingGridIncludesLongFormRow(t *testing.T) {
	// Third row holds two settings: Long-form (6) and DocGen (7).
	if got := moveSettingDown(3); got != 6 {
		t.Fatalf("down from enhance should reach long-form, got %d", got)
	}
	if got := moveSettingDown(4); got != 7 {
		t.Fatalf("down from hypotheses should reach docgen, got %d", got)
	}
	if got := moveSettingDown(5); got != 7 {
		t.Fatalf("down from exhaustive should clamp to docgen, got %d", got)
	}
	if got := moveSettingDown(6); got != 6 {
		t.Fatalf("down from long-form should stay (caller exits settings), got %d", got)
	}
	if got := moveSettingDown(7); got != 7 {
		t.Fatalf("down from docgen should stay (caller exits settings), got %d", got)
	}
	if got := moveSettingUp(6); got != 3 {
		t.Fatalf("up from long-form should reach enhance, got %d", got)
	}
	if got := moveSettingUp(7); got != 4 {
		t.Fatalf("up from docgen should reach hypotheses, got %d", got)
	}
	if got := moveSettingRight(6); got != 7 {
		t.Fatalf("right on long-form should reach docgen, got %d", got)
	}
	if got := moveSettingRight(7); got != 6 {
		t.Fatalf("right on docgen should wrap to long-form, got %d", got)
	}
	if got := moveSettingLeft(6); got != 7 {
		t.Fatalf("left on long-form should reach docgen, got %d", got)
	}
	if got := moveSettingLeft(7); got != 6 {
		t.Fatalf("left on docgen should wrap to long-form, got %d", got)
	}
}

func TestToggleActiveSettingLongForm(t *testing.T) {
	s := &tuiState{activeSetting: 6}
	s.toggleActiveSetting()
	if !s.longFormReport {
		t.Fatal("expected long-form report to toggle on")
	}
	s.toggleActiveSetting()
	if s.longFormReport {
		t.Fatal("expected long-form report to toggle off")
	}
}

func TestStartFollowUpResearchEmptyInputIsNoop(t *testing.T) {
	s := &tuiState{
		originalQuery: "base query",
		query:         "base query",
	}
	s.startFollowUpResearch(context.Background(), "   ")
	if s.mode != modeInput {
		t.Fatalf("empty follow-up should not start a run, mode=%d", s.mode)
	}
	if s.keepPrevResultOnce {
		t.Fatal("empty follow-up should not set keepPrevResultOnce")
	}
	if s.query != "base query" {
		t.Fatalf("empty follow-up should not rewrite the query, got %q", s.query)
	}
}

func TestResultsFooterMentionsFollowUp(t *testing.T) {
	s := &tuiState{}
	if !strings.Contains(s.resultsFooterShortcut(), "f=follow-up") {
		t.Fatalf("results footer should advertise follow-up shortcut: %q", s.resultsFooterShortcut())
	}
}
