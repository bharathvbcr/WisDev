package wisdev

import (
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/evidence"
)

func TestSelectRelevantPacketsSourceDiversity(t *testing.T) {
	// Corpus: source A has 5 packets, sources B..E have 1 each — all section-relevant.
	mk := func(id, src string) evidence.EvidencePacket {
		return evidence.EvidencePacket{
			PacketID:         id,
			SectionRelevance: []string{"introduction"},
			EvidenceSpans:    []evidence.EvidenceSpan{{SourceCanonicalID: src}},
		}
	}
	packets := []evidence.EvidencePacket{
		mk("a1", "srcA"), mk("a2", "srcA"), mk("a3", "srcA"), mk("a4", "srcA"), mk("a5", "srcA"),
		mk("b1", "srcB"), mk("c1", "srcC"), mk("d1", "srcD"), mk("e1", "srcE"),
	}
	distinctSources := func(sel []evidence.EvidencePacket) int {
		s := map[string]struct{}{}
		for _, p := range sel {
			s[packetPrimarySource(p)] = struct{}{}
		}
		return len(s)
	}

	// Default selector is relevance-ordered: the first 5 packets are all srcA -> 1 source.
	t.Setenv("MANUSCRIPT_DIVERSIFY_SOURCES", "")
	def := selectRelevantPackets(packets, "introduction", 5, nil, false)
	if got := distinctSources(def); got != 1 {
		t.Errorf("default selector distinct sources = %d, want 1 (relevance-ordered clusters on srcA)", got)
	}

	// Diversified selector spreads across all 5 distinct sources before repeating srcA.
	t.Setenv("MANUSCRIPT_DIVERSIFY_SOURCES", "1")
	div := selectRelevantPackets(packets, "introduction", 5, nil, true)
	if len(div) != 5 {
		t.Fatalf("diversified selection size = %d, want 5", len(div))
	}
	if got := distinctSources(div); got != 5 {
		t.Errorf("diversified selector distinct sources = %d, want 5", got)
	}

	// Cross-section spread: a shared `assigned` tally (as planSections maintains) makes a
	// later section prefer sources the earlier one did not cite, so the UNION grows.
	corpus := []evidence.EvidencePacket{}
	for _, s := range []string{"s1", "s2", "s3", "s4"} {
		for i := 0; i < 2; i++ { // 2 packets per source
			corpus = append(corpus, mk(s+"_"+string(rune('a'+i)), s))
		}
	}
	assigned := map[string]int{}
	sec1 := selectRelevantPackets(corpus, "introduction", 2, assigned, true)
	for _, p := range sec1 { // mimic planSections' shared tally
		assigned[p.PacketID]++
		assigned["src:"+packetPrimarySource(p)]++
	}
	sec2 := selectRelevantPackets(corpus, "introduction", 2, assigned, true)
	u := map[string]struct{}{}
	for _, p := range append(append([]evidence.EvidencePacket{}, sec1...), sec2...) {
		u[packetPrimarySource(p)] = struct{}{}
	}
	if len(u) != 4 {
		t.Errorf("cross-section union of sources = %d, want 4 (two sections spread across all four sources)", len(u))
	}
}

func TestBroadSynthesisSectionEnvExtension(t *testing.T) {
	// Built-in review sections are always broad.
	for _, id := range []string{"abstract", "introduction", "literature_review", "discussion", "conclusion"} {
		if !isBroadSynthesisSection(id) {
			t.Errorf("%q should be broad by default", id)
		}
	}
	// Research-synopsis sections are NOT broad unless opted in.
	t.Setenv("MANUSCRIPT_BROAD_SECTIONS", "")
	for _, id := range []string{"objectives", "methodology", "expected_outcomes"} {
		if isBroadSynthesisSection(id) {
			t.Errorf("%q should not be broad with empty env", id)
		}
	}
	// The env opt-in grounds them (normalized: spaces/hyphens -> underscores).
	t.Setenv("MANUSCRIPT_BROAD_SECTIONS", "objectives, Analysis-Plan ,expected_outcomes")
	for _, id := range []string{"objectives", "analysis_plan", "expected_outcomes"} {
		if !isBroadSynthesisSection(id) {
			t.Errorf("%q should be broad after env opt-in", id)
		}
	}
	// Built-ins still broad; an unlisted id stays specific.
	if !isBroadSynthesisSection("introduction") {
		t.Error("introduction must remain broad")
	}
	if isBroadSynthesisSection("methodology") {
		t.Error("methodology was not opted in and must stay specific")
	}
}

func TestReviewRoundsClamping(t *testing.T) {
	// Default cap (env unset) preserves the historical ceiling of 5.
	cases := map[int]int{0: defaultReviewRounds, -2: defaultReviewRounds, 1: 1, 4: 4, 50: 5}
	for set, want := range cases {
		p := &ManuscriptPipeline{ReviewRounds: set}
		if got := p.reviewRounds(); got != want {
			t.Errorf("ReviewRounds=%d -> %d, want %d", set, got, want)
		}
	}
}

func TestReviewRoundsCapEnvOverride(t *testing.T) {
	// MANUSCRIPT_MAX_REVIEW_ROUNDS raises the ceiling for exhaustive "max mode" runs,
	// and is clamped to [1,20] so a malformed value can neither disable review nor
	// spin unbounded.
	cases := []struct {
		env      string
		set      int
		wantCap  int
		wantSeen int
	}{
		{env: "10", set: 10, wantCap: 10, wantSeen: 10},   // explicit 10 honored when cap allows it
		{env: "10", set: 50, wantCap: 10, wantSeen: 10},   // still clamped to the raised cap
		{env: "3", set: 10, wantCap: 3, wantSeen: 3},      // a lower cap tightens the loop
		{env: "0", set: 10, wantCap: 1, wantSeen: 1},      // floor
		{env: "999", set: 100, wantCap: 20, wantSeen: 20}, // ceiling
		{env: "abc", set: 50, wantCap: 5, wantSeen: 5},    // malformed -> default 5
	}
	for _, c := range cases {
		t.Setenv("MANUSCRIPT_MAX_REVIEW_ROUNDS", c.env)
		if got := maxReviewRoundsCap(); got != c.wantCap {
			t.Errorf("maxReviewRoundsCap(env=%q) = %d, want %d", c.env, got, c.wantCap)
		}
		p := &ManuscriptPipeline{ReviewRounds: c.set}
		if got := p.reviewRounds(); got != c.wantSeen {
			t.Errorf("reviewRounds(env=%q, set=%d) = %d, want %d", c.env, c.set, got, c.wantSeen)
		}
	}
}

func TestAnySectionNeedsRevision(t *testing.T) {
	clean := []SectionDraftArtifact{{ReviewStatus: "needs_review"}, {ReviewStatus: "verified"}}
	if anySectionNeedsRevision(clean) {
		t.Fatal("no section flagged needs_revision -> false")
	}
	flagged := []SectionDraftArtifact{{ReviewStatus: "needs_revision", UnresolvedIssues: []string{"x"}}}
	if !anySectionNeedsRevision(flagged) {
		t.Fatal("a needs_revision section with issues -> true")
	}
	// needs_revision but no unresolved issues should not loop forever.
	noIssues := []SectionDraftArtifact{{ReviewStatus: "needs_revision"}}
	if anySectionNeedsRevision(noIssues) {
		t.Fatal("needs_revision without issues -> false (avoids non-converging loop)")
	}
}

func TestSectionsContentFingerprintDetectsChange(t *testing.T) {
	a := []SectionDraftArtifact{{SectionID: "intro", Content: "alpha"}}
	b := []SectionDraftArtifact{{SectionID: "intro", Content: "alpha"}}
	if sectionsContentFingerprint(a) != sectionsContentFingerprint(b) {
		t.Fatal("identical content should share a fingerprint")
	}
	b[0].Content = "alpha revised"
	if sectionsContentFingerprint(a) == sectionsContentFingerprint(b) {
		t.Fatal("changed content must change the fingerprint")
	}
}

func TestResolveSectionTemplatesBackend(t *testing.T) {
	// Default plan when no flow is set.
	p := &ManuscriptPipeline{}
	if got := p.resolveSectionTemplates(); len(got) != len(defaultSectionTemplates()) || got[0].id != "abstract" {
		t.Fatalf("default flow should be the full plan starting with abstract, got %d sections", len(got))
	}
	// Custom flow: order honored, known ids reuse tuned briefs, unknown -> generic, deduped.
	p = &ManuscriptPipeline{SectionFlow: []string{"Introduction", "results", "background", "background"}}
	got := p.resolveSectionTemplates()
	want := []string{"introduction", "results", "background"}
	if len(got) != len(want) {
		t.Fatalf("expected %d sections, got %d", len(want), len(got))
	}
	for i, id := range want {
		if got[i].id != id {
			t.Fatalf("section %d: got %q want %q", i, got[i].id, id)
		}
	}
	if got[0].writerRole != "framing_writer" {
		t.Fatalf("introduction should reuse the tuned brief, got role %q", got[0].writerRole)
	}
	if got[2].title != "Background" {
		t.Fatalf("unknown id should humanize the title, got %q", got[2].title)
	}
}
