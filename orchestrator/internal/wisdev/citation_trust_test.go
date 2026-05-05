package wisdev

import (
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/evidence/citations"
)

func TestNormalizeCitationVerificationStatus(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		raw      string
		verified bool
		resolved bool
		expected CitationVerificationStatus
	}{
		{
			name:     "verified alias",
			raw:      " VERIFIED ",
			expected: CitationStatusVerified,
		},
		{
			name:     "ambiguous alias",
			raw:      "Duplicate",
			expected: CitationStatusAmbiguous,
		},
		{
			name:     "rejected alias",
			raw:      "UNRESOLVED",
			expected: CitationStatusRejected,
		},
		{
			name:     "verified fallback",
			raw:      "unknown",
			verified: true,
			expected: CitationStatusVerified,
		},
		{
			name:     "ambiguous fallback",
			raw:      "",
			resolved: true,
			expected: CitationStatusAmbiguous,
		},
		{
			name:     "rejected fallback",
			raw:      "",
			expected: CitationStatusRejected,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeCitationVerificationStatus(tc.raw, tc.verified, tc.resolved)
			if got != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, got)
			}
		})
	}
}

func TestCanonicalCitationWithTrustDefaults(t *testing.T) {
	t.Parallel()

	record := CanonicalCitation{
		ID:                 "c1",
		Title:              "Deep Learning in Robotics",
		DOI:                "10.1000/robotics",
		VerificationStatus: "",
	}
	canonical := canonicalCitationWithTrustDefaults(record)
	if canonical.VerificationStatus != CitationStatusRejected {
		t.Fatalf("expected fallback status rejected, got %q", canonical.VerificationStatus)
	}
	if canonical.ResolutionEngine != "wisdev" {
		t.Fatalf("expected default resolution engine, got %q", canonical.ResolutionEngine)
	}
	if canonical.SourceAuthority != "doi" {
		t.Fatalf("expected DOI authority, got %q", canonical.SourceAuthority)
	}
	if canonical.CanonicalID != "10.1000/robotics" {
		t.Fatalf("expected canonical id from DOI, got %q", canonical.CanonicalID)
	}
	if canonical.LandingURL != "https://doi.org/10.1000/robotics" {
		t.Fatalf("unexpected landing URL %q", canonical.LandingURL)
	}
	if canonical.ProvenanceHash == "" {
		t.Fatal("expected provenance hash to be computed")
	}

	verified := CanonicalCitation{
		ID:                 "c2",
		Title:              "Arxiv Origin Paper",
		ArxivID:            "2401.00123",
		VerificationStatus: "invalid",
		Verified:           true,
	}
	verifiedCanonical := canonicalCitationWithTrustDefaults(verified)
	if verifiedCanonical.VerificationStatus != CitationStatusRejected {
		t.Fatalf("explicit invalid should remain rejected, got %q", verifiedCanonical.VerificationStatus)
	}
	if verifiedCanonical.SourceAuthority != "arxiv" {
		t.Fatalf("expected arxiv authority, got %q", verifiedCanonical.SourceAuthority)
	}
	if verifiedCanonical.LandingURL != "https://arxiv.org/abs/2401.00123" {
		t.Fatalf("unexpected arxiv landing URL %q", verifiedCanonical.LandingURL)
	}

	fallbackVerified := CanonicalCitation{
		ID:       "c3",
		Title:    "Statistical Physics",
		Verified: true,
		ArxivID:  "abc123",
		Resolved: true,
	}
	agreement := canonicalCitationWithTrustDefaults(fallbackVerified)
	if agreement.VerificationStatus != CitationStatusVerified {
		t.Fatalf("expected verified fallback status, got %q", agreement.VerificationStatus)
	}
	if agreement.ResolverAgreementCount != 1 {
		t.Fatalf("expected agreement count default to 1 for verified, got %d", agreement.ResolverAgreementCount)
	}
}

func TestCanonicalCitationWithTrustDefaultsHeuristicDefaults(t *testing.T) {
	t.Parallel()

	record := CanonicalCitation{
		ID:                 "heuristic-1",
		Title:              "A heuristic title",
		VerificationStatus: "",
	}
	canonical := canonicalCitationWithTrustDefaults(record)
	if canonical.VerificationStatus != CitationStatusRejected {
		t.Fatalf("expected fallback status rejected, got %q", canonical.VerificationStatus)
	}
	if canonical.ResolutionEngine != "wisdev" {
		t.Fatalf("expected default resolution engine, got %q", canonical.ResolutionEngine)
	}
	if canonical.SourceAuthority != "heuristic" {
		t.Fatalf("expected heuristic authority when DOI/Arxiv unavailable, got %q", canonical.SourceAuthority)
	}
	if canonical.CanonicalID != "A heuristic title" {
		t.Fatalf("expected canonical id from title, got %q", canonical.CanonicalID)
	}
	if canonical.LandingURL != "" {
		t.Fatalf("expected no landing URL without DOI/Arxiv, got %q", canonical.LandingURL)
	}
	if canonical.ProvenanceHash == "" {
		t.Fatal("expected provenance hash to be computed for heuristic record")
	}
}

func TestCitationTrustBlockingIssues(t *testing.T) {
	t.Parallel()

	records := []CanonicalCitation{
		{
			ID:                 "1",
			DOI:                "10.0/a",
			Title:              "Alpha paper",
			CanonicalID:        "canonical-a",
			ConflictNote:       "first conflict",
			VerificationStatus: CitationStatusAmbiguous,
		},
		{
			ID:                 "2",
			Title:              "Beta paper",
			ConflictNote:       "second conflict",
			VerificationStatus: CitationStatusRejected,
		},
		{
			ID:                 "3",
			Title:              "Gamma paper",
			VerificationStatus: "",
		},
	}

	got := citationTrustBlockingIssues(records, []string{"  issue-a  ", "issue-a", "", "resolved-later"})
	expected := []string{
		"issue-a",
		"resolved-later",
		"ambiguous:canonical-a:first conflict",
		"rejected:Beta paper:second conflict",
		"rejected:Gamma paper:rejected citation record",
	}

	if len(got) != len(expected) {
		t.Fatalf("expected %d blocking issues, got %d", len(expected), len(got))
	}

	for idx, issue := range expected {
		if got[idx] != issue {
			t.Fatalf("expected blocking[%d] == %q, got %q", idx, issue, got[idx])
		}
	}
}

func TestBuildCitationTrustBundle(t *testing.T) {
	t.Parallel()

	bundle := BuildCitationTrustBundle(nil, nil, nil)
	if bundle != nil {
		t.Fatal("expected nil bundle when no input is provided")
	}

	records := []CanonicalCitation{
		{
			ID:                 "r1",
			Title:              "Verified source",
			DOI:                "10.1000/xyz",
			ResolutionEngine:   "engine-a",
			VerificationStatus: CitationStatusVerified,
			Resolved:           true,
			Verified:           true,
		},
		{
			ID:                 "r2",
			Title:              "Ambiguous source",
			ResolutionEngine:   "engine-b",
			VerificationStatus: CitationStatusAmbiguous,
			DOI:                "10.1000/xyz",
			CanonicalID:        "canonical-shared",
		},
	}
	withTrace := BuildCitationTrustBundle(records, []map[string]any{{"id": "r1", "engine": "engine-a"}}, []string{"seeded"})
	if withTrace == nil {
		t.Fatal("expected bundle when citations exist")
	}
	if withTrace.VerifiedCount != 1 || withTrace.AmbiguousCount != 1 || withTrace.RejectedCount != 0 {
		t.Fatalf("unexpected counts: v=%d a=%d r=%d", withTrace.VerifiedCount, withTrace.AmbiguousCount, withTrace.RejectedCount)
	}
	if len(withTrace.ResolverTrace) != 1 {
		t.Fatalf("expected custom trace to be preserved, got %d", len(withTrace.ResolverTrace))
	}
	if withTrace.PromotionEligible {
		t.Fatal("expected promotion to be false when blocking issues are present")
	}
	if got := withTrace.PromotionGate["promoted"]; got != false {
		t.Fatalf("expected promotion gate promoted=false, got %v", got)
	}
	if got := withTrace.BlockingIssues[0]; got != "seeded" {
		t.Fatalf("expected preserved issue first, got %q", got)
	}

	noTrace := BuildCitationTrustBundle(records[:1], nil, nil)
	if noTrace == nil {
		t.Fatal("expected bundle for non-empty records")
	}
	if len(noTrace.ResolverTrace) != 1 {
		t.Fatalf("expected generated resolver trace when input trace is empty")
	}
	if len(noTrace.PromotionGate) == 0 {
		t.Fatal("expected promotion gate details")
	}
}

func TestCitationTrustBundleToMapAndFromMap(t *testing.T) {
	t.Parallel()

	bundle := BuildCitationTrustBundle(
		[]CanonicalCitation{
			{
				ID:                 "r1",
				Title:              "Mapped paper",
				DOI:                "10.999/foo",
				VerificationStatus: CitationStatusVerified,
				Verified:           true,
				Resolved:           true,
			},
		},
		[]map[string]any{{"id": "r1"}},
		[]string{"blocked"},
	)
	if bundle == nil {
		t.Fatal("expected non-nil bundle")
	}
	raw := citationTrustBundleToMap(bundle)

	if raw == nil {
		t.Fatal("expected non-nil raw map")
	}
	if toInt(raw["verifiedCount"]) != bundle.VerifiedCount {
		t.Fatalf("expected verifiedCount to round-trip")
	}
	if len(raw["blockingIssues"].([]any)) != 1 {
		t.Fatalf("unexpected blocking issue count %d", len(raw["blockingIssues"].([]any)))
	}
	typed := raw["citations"].([]any)
	if len(typed) != 1 {
		t.Fatalf("expected one citation in map conversion, got %d", len(typed))
	}
	recordMap, ok := typed[0].(map[string]any)
	if !ok {
		t.Fatal("expected citation map entry")
	}
	if recordMap["sourceAuthority"] != "doi" {
		t.Fatalf("expected canonical citation source authority to be filled, got %q", recordMap["sourceAuthority"])
	}

	roundTripped := citationTrustBundleFromMap(raw, nil)
	if roundTripped == nil {
		t.Fatal("expected round-tripped bundle")
	}
	if roundTripped.VerifiedCount != bundle.VerifiedCount {
		t.Fatalf("expected verified count to remain %d, got %d", bundle.VerifiedCount, roundTripped.VerifiedCount)
	}

	raw["verifiedCount"] = 12
	raw["ambiguousCount"] = 13
	raw["rejectedCount"] = 14
	raw["promotionEligible"] = false
	raw["promotionGate"] = map[string]any{"promoted": false, "consensusMode": "single_source_verified"}
	raw["resolverTrace"] = []map[string]any{{"id": "seed"}}
	raw["blockingIssues"] = []string{"override"}
	raw["citations"] = []map[string]any{{
		"id":                 "override-id",
		"title":              "Override paper",
		"verificationStatus": "verified",
		"resolved":           true,
		"verified":           true,
	}}

	overridden := citationTrustBundleFromMap(raw, nil)
	if overridden == nil {
		t.Fatal("expected overridden bundle")
	}
	if overridden.VerifiedCount != 12 || overridden.AmbiguousCount != 13 || overridden.RejectedCount != 14 {
		t.Fatalf("expected count overrides to be applied")
	}
	if overridden.PromotionEligible {
		t.Fatalf("expected promotionEligible override to be false")
	}
	if overridden.PromotionGate["consensusMode"] != "single_source_verified" {
		t.Fatalf("expected promotion gate override to persist, got %v", overridden.PromotionGate["consensusMode"])
	}
	if overridden.ResolverTrace[0]["id"] != "seed" {
		t.Fatalf("expected resolver trace override to persist")
	}
}

func TestCitationTrustBundleFromMapNilRaw(t *testing.T) {
	t.Parallel()

	fallback := []CanonicalCitation{
		{
			ID:                 "fallback-1",
			Title:              "Fallback citation",
			VerificationStatus: CitationStatusVerified,
			Verified:           true,
			Resolved:           true,
		},
	}

	roundTrip := citationTrustBundleFromMap(nil, fallback)
	if roundTrip == nil {
		t.Fatal("expected bundle from nil raw map")
	}
	if len(roundTrip.Citations) != 1 {
		t.Fatalf("expected fallback citation to be used, got %d", len(roundTrip.Citations))
	}
	if roundTrip.Citations[0].CanonicalID == "" {
		t.Fatal("expected fallback citation normalization to run")
	}
}

func TestBuildCitationTrustBundleFromResultAndEligibility(t *testing.T) {
	t.Parallel()

	resultWithBundle := map[string]any{
		"citationTrustBundle": map[string]any{
			"citations":         []map[string]any{{"id": "embedded", "title": "Embedded source", "verificationStatus": "verified", "resolved": true, "verified": true}},
			"promotionEligible": true,
		},
	}
	bundle := buildCitationTrustBundleFromResult(resultWithBundle, []CanonicalCitation{
		{ID: "fallback"},
	})
	if bundle == nil {
		t.Fatal("expected bundle from embedded citationTrustBundle")
	}
	if !citationTrustBundlePromotionEligible(bundle) {
		t.Fatal("expected embedded bundle to be promotion-eligible")
	}

	resultFallback := map[string]any{
		"resolverTrace": []map[string]any{{"id": "fallback"}},
		"issues":        []string{"missing", "missing"},
	}
	fallback := []CanonicalCitation{
		{
			ID:                 "fallback",
			Title:              "Fallback source",
			VerificationStatus: CitationStatusVerified,
			Verified:           true,
			Resolved:           true,
		},
	}
	fallbackBundle := buildCitationTrustBundleFromResult(resultFallback, fallback)
	if fallbackBundle == nil {
		t.Fatal("expected bundle from fallback result path")
	}
	if len(fallbackBundle.ResolverTrace) != 1 {
		t.Fatalf("expected resolver trace to come from result")
	}
	if citationTrustBundlePromotionEligible(fallbackBundle) {
		t.Fatalf("expected fallback bundle to remain ineligible because blocking issues are present")
	}
	if len(fallbackBundle.BlockingIssues) != 1 {
		t.Fatalf("expected deduped blocking issues, got %d", len(fallbackBundle.BlockingIssues))
	}
}

func TestResolverTraceFromCitations(t *testing.T) {
	t.Parallel()

	trace := resolverTraceFromCitations(nil)
	if trace != nil {
		t.Fatalf("expected nil trace for no records")
	}

	trace = resolverTraceFromCitations([]CanonicalCitation{
		{
			ID:                 "x",
			DOI:                "10.0/a",
			VerificationStatus: CitationStatusVerified,
			Resolved:           true,
			Verified:           true,
		},
		{
			ID:                 "y",
			Title:              "Title",
			ArxivID:            "2401.0001",
			ResolutionEngine:   "custom",
			VerificationStatus: CitationStatusAmbiguous,
		},
	})
	if len(trace) != 2 {
		t.Fatalf("expected 2 trace records, got %d", len(trace))
	}
	if trace[0]["id"] != "x" || trace[0]["authority"] != "doi" || trace[0]["engine"] != "wisdev" {
		t.Fatalf("unexpected normalized first trace record: %#v", trace[0])
	}
	if trace[1]["status"] != CitationStatusAmbiguous {
		t.Fatalf("expected second trace status ambiguous, got %v", trace[1]["status"])
	}
}

func TestBuildCitationPromotionGate(t *testing.T) {
	t.Parallel()

	records := []CanonicalCitation{
		{
			ID:                 "m1",
			Title:              "Shared paper",
			DOI:                "10.1000/shared",
			VerificationStatus: CitationStatusVerified,
			ResolutionEngine:   "openalex",
			Verified:           true,
			Resolved:           true,
		},
		{
			ID:                 "m2",
			Title:              "Shared paper",
			DOI:                "10.1000/shared",
			VerificationStatus: CitationStatusVerified,
			ResolutionEngine:   "crossref",
			Verified:           true,
			Resolved:           true,
		},
	}
	gate := buildCitationPromotionGate(records, nil)
	if gate == nil {
		t.Fatal("expected non-nil promotion gate")
	}
	if gate["promoted"] != true {
		t.Fatalf("expected promoted=true for consensus match")
	}
	if gate["consensusMode"] != "multi_source" {
		t.Fatalf("expected multi_source consensus, got %v", gate["consensusMode"])
	}

	verdict, ok := gate["agreementSources"].([]any)
	if !ok || len(verdict) != 2 {
		t.Fatalf("expected 2 agreement sources, got %#v", gate["agreementSources"])
	}

	ambiguousGate := buildCitationPromotionGate([]CanonicalCitation{
		{
			ID:                 "amb",
			Title:              "Ambiguous",
			DOI:                "10.1000/amb",
			VerificationStatus: CitationStatusAmbiguous,
		},
	}, []string{"manual-check"})
	if ambiguousGate["promoted"] != false {
		t.Fatalf("expected promotion gate to be false with ambiguity + blocking issue")
	}
	if ambiguousGate["consensusMode"] != "blocked" {
		t.Fatalf("expected blocked mode with remaining blocking issues, got %v", ambiguousGate["consensusMode"])
	}

	conflictNote, ok := ambiguousGate["conflictNote"].(string)
	if !ok || len(conflictNote) == 0 {
		t.Fatalf("expected non-empty conflict note for blocked case")
	}

	single := buildCitationPromotionGate([]CanonicalCitation{
		{
			ID:                 "single",
			Title:              "Single",
			DOI:                "10.1000/single",
			VerificationStatus: CitationStatusVerified,
			Verified:           true,
			Resolved:           true,
		},
	}, nil)
	if single["promoted"] != false {
		t.Fatalf("expected single verified source to remain blocked until multi-source agreement")
	}
	if single["consensusMode"] != "single_source_insufficient" {
		t.Fatalf("expected single_source_insufficient mode, got %v", single["consensusMode"])
	}
	if got, ok := single["blockingIssues"].([]any); !ok || len(got) != 0 {
		t.Fatalf("expected empty blocking issues slice, got %#v", single["blockingIssues"])
	}
	if got := single["conflictNote"]; got == "" {
		t.Fatalf("expected conflict note for single-source citation promotion block")
	}

	rejected := buildCitationPromotionGate([]CanonicalCitation{
		{
			ID:                 "rej",
			Title:              "Rejected source",
			VerificationStatus: CitationStatusRejected,
		},
	}, nil)
	if rejected["promoted"] != false {
		t.Fatalf("expected rejection-only set to not promote")
	}
	if rejected["consensusMode"] != "blocked" {
		t.Fatalf("expected blocked mode without consensus, got %v", rejected["consensusMode"])
	}
	if got := rejected["blockingIssues"].([]any); len(got) != 0 {
		t.Fatalf("expected no explicit blocking issues when none provided, got %#v", got)
	}
}

func TestDedupeAndSliceConverters(t *testing.T) {
	t.Parallel()

	got := dedupeTrimmedStrings([]string{"  a  ", "b", "a", "", "b ", "b", "c"})
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("unexpected dedupe output %#v", got)
	}

	if dedupeTrimmedStrings(nil) != nil {
		t.Fatal("expected nil output for empty dedupe input")
	}

	anys := stringSliceToAny([]string{"x", "y"})
	if len(anys) != 2 || anys[0] != "x" || anys[1] != "y" {
		t.Fatalf("unexpected stringSliceToAny output %#v", anys)
	}

	emptyAny := stringSliceToAny(nil)
	if emptyAny == nil || len(emptyAny) != 0 {
		t.Fatal("expected empty non-nil slice for empty input")
	}

	if citationTrustBundlePromotionEligible(nil) {
		t.Fatal("nil bundle should be ineligible")
	}
}

func TestCitationBundlePromotionEligibilityFunction(t *testing.T) {
	t.Parallel()

	if citationTrustBundlePromotionEligible(nil) {
		t.Fatal("nil bundle should not be eligible")
	}

	if citationTrustBundlePromotionEligible(&CitationTrustBundle{PromotionEligible: true}) != true {
		t.Fatal("expected eligible bundle to return true")
	}

	if citationTrustBundlePromotionEligible(&CitationTrustBundle{PromotionEligible: false}) {
		t.Fatal("expected non-eligible bundle to return false")
	}
}

func TestCompatibilityWithResolvedCitationsForPromotion(t *testing.T) {
	t.Parallel()

	records := citations.EvaluatePromotion([]citations.ResolvedCitation{
		{
			CanonicalID:          "doi:10.1000/case",
			Title:                "Same source",
			DOI:                  "10.1000/case",
			ResolutionEngine:     "engine-a",
			ResolutionConfidence: 1,
			Resolved:             true,
		},
		{
			CanonicalID:          "doi:10.1000/case",
			Title:                "Same source",
			DOI:                  "10.1000/case",
			ResolutionEngine:     "engine-b",
			ResolutionConfidence: 1,
			Resolved:             true,
		},
		{
			CanonicalID:          "doi:10.1000/case",
			Title:                "Different year",
			DOI:                  "10.1000/case",
			ResolutionEngine:     "engine-c",
			Year:                 2000,
			ResolutionConfidence: 1,
			Resolved:             true,
		},
	}, 2)
	if !records.Promoted {
		t.Fatalf("expected evaluator to promote matching dominant consensus")
	}
}
