package cli

import (
	"encoding/json"
	"strings"
	"testing"

	internalwisdev "github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
)

func TestApplyManuscriptControls(t *testing.T) {
	// All controls set -> applied.
	p := &internalwisdev.ManuscriptPipeline{}
	applyManuscriptControls(p, manuscriptControls{targetWords: 1500, minCitations: 12, sectionFlow: []string{"introduction", "results"}})
	if p.TargetWords != 1500 || p.MinCitations != 12 || len(p.SectionFlow) != 2 {
		t.Fatalf("controls not applied: %+v", p)
	}

	// Zero/empty controls leave pipeline defaults untouched.
	p2 := &internalwisdev.ManuscriptPipeline{TargetWords: 900, MinCitations: 5, SectionFlow: []string{"abstract"}}
	applyManuscriptControls(p2, manuscriptControls{})
	if p2.TargetWords != 900 || p2.MinCitations != 5 || len(p2.SectionFlow) != 1 {
		t.Fatalf("empty controls should not overwrite existing values: %+v", p2)
	}
}

func TestManuscriptRawJSON(t *testing.T) {
	// json format: embed verbatim so it nests as a real object, not a quoted string.
	got := manuscriptRawJSON(`{"a":1}`, "json")
	if string(got) != `{"a":1}` {
		t.Fatalf("json format should embed verbatim, got %s", got)
	}
	var obj map[string]any
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("json-format manuscript should be valid JSON: %v", err)
	}

	// markdown format: quote it so the combined payload stays valid JSON.
	md := "# Title\n\nbody with \"quotes\""
	quoted := manuscriptRawJSON(md, "markdown")
	var back string
	if err := json.Unmarshal(quoted, &back); err != nil {
		t.Fatalf("markdown manuscript should encode to a JSON string: %v", err)
	}
	if back != md {
		t.Fatalf("round-trip mismatch: got %q want %q", back, md)
	}
}

func TestManuscriptOutputPath(t *testing.T) {
	// With an export path set, the manuscript lands beside it with a -manuscript.md suffix.
	s := &tuiState{outputPath: "results/run.md"}
	if got := s.manuscriptOutputPath(); got != "results/run-manuscript.md" {
		t.Fatalf("got %q, want results/run-manuscript.md", got)
	}

	// With no export path, it falls back to a timestamped manuscript file.
	s = &tuiState{}
	got := s.manuscriptOutputPath()
	if !strings.HasPrefix(got, "wisdev-manuscript-") || !strings.HasSuffix(got, ".md") {
		t.Fatalf("fallback path %q should be wisdev-manuscript-*.md", got)
	}
}
