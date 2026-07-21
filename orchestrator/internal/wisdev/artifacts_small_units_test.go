package wisdev

import (
	"testing"
)

func TestArtifactValueConverters(t *testing.T) {
	nilVal := firstArtifactValue(nil, "fallback")
	if nilVal != "fallback" {
		t.Fatalf("expected fallback value when all entries are nil/empty, got %#v", nilVal)
	}

	seq := []any{"first", "second"}
	first := firstArtifactValue(seq, "fallback")
	if got, ok := first.([]any); !ok || len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("expected full []any slice passthrough, got %#v", first)
	}

	emptySlice := []any{}
	fallback := firstArtifactValue(emptySlice, 99)
	if fallback != 99 {
		t.Fatalf("expected fallback value for empty slice, got %#v", fallback)
	}

	typedMaps := firstArtifactMaps([]map[string]any{{"a": 1}, {"b": 2}})
	if len(typedMaps) != 2 {
		t.Fatalf("expected two map entries, got %d", len(typedMaps))
	}

	mixed := firstArtifactMaps([]any{1, map[string]any{"ok": true}, "x"})
	if len(mixed) != 1 || mixed[0]["ok"] != true {
		t.Fatalf("expected only map entry to pass through, got %#v", mixed)
	}

	if got := toArtifactAnySlice(nil); got != nil {
		t.Fatalf("expected nil for nil input, got %#v", got)
	}
	copied := toArtifactAnySlice([]any{"a", 1})
	if len(copied) != 2 || copied[0] != "a" || copied[1] != 1 {
		t.Fatalf("unexpected artifact slice conversion %#v", copied)
	}

	if got := toArtifactAnySlice("x"); len(got) != 1 || got[0] != "x" {
		t.Fatalf("expected singleton conversion, got %#v", got)
	}
}

func TestToStringAndNumericConverters(t *testing.T) {
	texts := toStringSlice([]string{"a", " b ", ""})
	if len(texts) != 3 {
		t.Fatalf("expected passthrough for []string, got %v", texts)
	}

	coerced := toStringSlice([]any{"a", "", 2, nil, " b "})
	if len(coerced) != 4 || coerced[0] != "a" || coerced[1] != "2" || coerced[2] != "<nil>" || coerced[3] != "b" {
		t.Fatalf("unexpected coerced string slice %#v", coerced)
	}

	if got := toStringSlice(123); got != nil {
		t.Fatalf("expected nil for non-slice conversion, got %#v", got)
	}

	if got := toInt(12); got != 12 {
		t.Fatalf("expected int passthrough, got %d", got)
	}
	if got := toInt(int32(4)); got != 4 {
		t.Fatalf("expected int32 conversion, got %d", got)
	}
	if got := toInt(int64(7)); got != 7 {
		t.Fatalf("expected int64 conversion, got %d", got)
	}
	if got := toInt(float64(3)); got != 3 {
		t.Fatalf("expected float64 conversion, got %d", got)
	}
	if got := toInt("nope"); got != 0 {
		t.Fatalf("expected default 0, got %d", got)
	}

	if got := toFloat(float64(1.5)); got != 1.5 {
		t.Fatalf("expected float64 passthrough, got %f", got)
	}
	if got := toFloat(float32(2.25)); got != 2.25 {
		t.Fatalf("expected float32 conversion, got %f", got)
	}
	if got := toFloat(3); got != 3 {
		t.Fatalf("expected int conversion, got %f", got)
	}
	if got := toFloat("nope"); got != 0 {
		t.Fatalf("expected default 0, got %f", got)
	}

	if got := toBool(true); !got {
		t.Fatalf("expected true for bool true")
	}
	if got := toBool(false); got {
		t.Fatalf("expected false for bool false")
	}
	if got := toBool(1); got {
		t.Fatalf("expected false for non-bool input")
	}
}

func TestDedupeStringsAndArtifactKeys(t *testing.T) {
	deduped := dedupeStrings([]string{"b", "a", "a", "c", "b", "b", "d"})
	expected := []string{"b", "a", "c", "b", "d"}
	if len(deduped) != len(expected) {
		t.Fatalf("expected %d deduped items, got %d (%v)", len(expected), len(deduped), deduped)
	}
	for i, want := range expected {
		if deduped[i] != want {
			t.Fatalf("expected deduped[%d]=%q got %q", i, want, deduped[i])
		}
	}

	set := StepArtifactSet{
		Action:    "research.retrievePapers",
		Artifacts: map[string]any{"extra": true},
		PaperBundle: &PaperArtifactBundle{
			Papers:              []Source{{ID: "s1"}},
			RetrievalStrategies: []string{"search"},
			RetrievalTrace:      []map[string]any{{"k": "v"}},
			QueryUsed:           "q",
			TraceID:             "t",
		},
		CitationBundle: &CitationArtifactBundle{
			Citations:        []CanonicalCitation{{ID: "c1"}},
			CanonicalSources: []CanonicalCitation{{ID: "c1"}},
			VerifiedRecords:  []CanonicalCitation{{ID: "c2"}},
			DuplicateCount:   1,
		},
		CitationTrustBundle: &CitationTrustBundle{},
		ReasoningBundle: &ReasoningArtifactBundle{
			Branches:     []ReasoningBranch{{Claim: "x"}},
			Verification: &ReasoningVerification{ReadyForSynthesis: true},
		},
		ClaimEvidenceArtifact: &ClaimEvidenceArtifact{Table: "t1", RowCount: 1},
	}
	keys := artifactKeys(set)
	expectedSet := map[string]struct{}{
		"branches":              {},
		"canonicalSources":      {},
		"claimEvidenceArtifact": {},
		"claimEvidenceTable":    {},
		"citationBundle":        {},
		"citationTrustBundle":   {},
		"citations":             {},
		"extra":                 {},
		"paperBundle":           {},
		"papers":                {},
		"queryUsed":             {},
		"reasoningBundle":       {},
		"reasoningVerification": {},
		"retrievalStrategies":   {},
		"retrievalTrace":        {},
		"traceId":               {},
		"verifiedRecords":       {},
	}
	if len(keys) != len(expectedSet) {
		t.Fatalf("expected %d keys, got %d: %v", len(expectedSet), len(keys), keys)
	}
	for _, key := range keys {
		if _, ok := expectedSet[key]; !ok {
			t.Fatalf("unexpected key %q in artifact key output", key)
		}
	}
}

func TestValidateEmitterIngressKeys(t *testing.T) {
	if got := validateEmitterIngressKeys("research.retrievePapers", map[string]any{}); got != nil {
		t.Fatalf("expected retrievePapers with empty map to pass, got %v", got)
	}
	if got := validateEmitterIngressKeys("research.retrievePapers", map[string]any{"papers": []any{}}); got != nil {
		t.Fatalf("expected retrievePapers papers requirement to pass, got %v", got)
	}

	if got := validateEmitterIngressKeys("research.resolveCanonicalCitations", map[string]any{}); got != nil {
		t.Fatalf("expected resolveCanonicalCitations with empty map to pass, got %v", got)
	}
	if got := validateEmitterIngressKeys("research.resolveCanonicalCitations", map[string]any{"foo": "bar"}); got == nil {
		t.Fatal("expected resolveCanonicalCitations to require canonicalSources or citations")
	}
	if got := validateEmitterIngressKeys("research.resolveCanonicalCitations", map[string]any{"citations": []any{}}); got != nil {
		t.Fatalf("expected resolveCanonicalCitations with citations to pass, got %v", got)
	}

	if got := validateEmitterIngressKeys("research.verifyCitations", map[string]any{}); got != nil {
		t.Fatalf("expected verifyCitations with empty map to pass, got %v", got)
	}
	if got := validateEmitterIngressKeys("research.verifyCitations", map[string]any{"foo": "bar"}); got == nil {
		t.Fatal("expected verifyCitations to require verifiedRecords or citations")
	}
	if got := validateEmitterIngressKeys("research.verifyCitations", map[string]any{"verifiedRecords": []any{}}); got != nil {
		t.Fatalf("expected verifyCitations with verifiedRecords to pass, got %v", got)
	}

	if got := validateEmitterIngressKeys("research.proposeHypotheses", map[string]any{}); got != nil {
		t.Fatalf("expected proposeHypotheses with empty map to pass, got %v", got)
	}
	if got := validateEmitterIngressKeys("research.proposeHypotheses", map[string]any{"foo": "bar"}); got == nil {
		t.Fatal("expected proposeHypotheses to require branches/hypotheses")
	}
	if got := validateEmitterIngressKeys("research.proposeHypotheses", map[string]any{"hypotheses": []any{}}); got != nil {
		t.Fatalf("expected proposeHypotheses with hypotheses to pass, got %v", got)
	}

	if got := validateEmitterIngressKeys("research.verifyReasoningPaths", map[string]any{}); got != nil {
		t.Fatalf("expected verifyReasoningPaths with empty map to pass, got %v", got)
	}
	if got := validateEmitterIngressKeys("research.verifyReasoningPaths", map[string]any{"foo": "bar"}); got == nil {
		t.Fatalf("expected verifyReasoningPaths to require verification summary and branches")
	}
	if got := validateEmitterIngressKeys("research.verifyReasoningPaths", map[string]any{"branches": []any{}, "readyForSynthesis": true}); got != nil {
		t.Fatalf("expected verifyReasoningPaths with readyForSynthesis to pass, got %v", got)
	}

	if got := validateEmitterIngressKeys("research.buildClaimEvidenceTable", map[string]any{}); got != nil {
		t.Fatalf("expected buildClaimEvidenceTable with empty map to pass, got %v", got)
	}
	if got := validateEmitterIngressKeys("research.buildClaimEvidenceTable", map[string]any{"foo": "bar"}); got == nil {
		t.Fatal("expected buildClaimEvidenceTable to require claimEvidenceTable")
	}
	if got := validateEmitterIngressKeys("research.buildClaimEvidenceTable", map[string]any{"claimEvidenceTable": map[string]any{"table": "t"}}); got != nil {
		t.Fatalf("expected buildClaimEvidenceTable with table key to pass, got %v", got)
	}

	if got := validateEmitterIngressKeys("unknown", map[string]any{}); got != nil {
		t.Fatalf("expected unknown action to skip validation, got %v", got)
	}
	if got := validateEmitterIngressKeys("research.search", map[string]any{}); got != nil {
		t.Fatalf("expected search action to skip validation, got %v", got)
	}
	if got := validateEmitterIngressKeys("", nil); got != nil {
		t.Fatalf("expected nil or empty result/action to pass, got %v", got)
	}
}

func TestNormalizeStepArtifactsDegradesOnIngressMismatch(t *testing.T) {
	step := PlanStep{ID: "canon", Action: "research.resolveCanonicalCitations"}
	artifactSet, err := normalizeStepArtifacts(step, map[string]any{"foo": "bar"}, []Source{{ID: "p1", Title: "Paper 1"}})
	if err == nil {
		t.Fatal("expected ingress mismatch error")
	}
	if artifactSet.Artifacts["artifactNormalizationStage"] != "ingress_validation" {
		t.Fatalf("expected ingress_validation stage, got %#v", artifactSet.Artifacts["artifactNormalizationStage"])
	}
	if artifactSet.Artifacts["artifactNormalizationErrorCode"] != "artifact_ingress_contract_mismatch" {
		t.Fatalf("expected ingress contract mismatch code, got %#v", artifactSet.Artifacts["artifactNormalizationErrorCode"])
	}
	if artifactSet.PaperBundle == nil || len(artifactSet.PaperBundle.Papers) != 1 {
		t.Fatalf("expected degraded artifact set to preserve source bundle, got %#v", artifactSet.PaperBundle)
	}
	if artifactSet.Artifacts["foo"] != "bar" {
		t.Fatalf("expected raw artifact payload to be preserved, got %#v", artifactSet.Artifacts)
	}
}

func TestAnnotateArtifactNormalizationFailureMarksSchemaValidation(t *testing.T) {
	err := validateStepArtifactSetAgainstCanonicalSchema(StepArtifactSet{
		StepID:    "canon",
		Action:    "research.resolveCanonicalCitations",
		Artifacts: map[string]any{"canonicalSources": []any{map[string]any{"title": "Canonical Source"}}},
		CitationBundle: &CitationArtifactBundle{
			CanonicalSources: []CanonicalCitation{{Title: "Canonical Source"}},
		},
	})
	if err == nil {
		t.Fatal("expected schema validation error")
	}
	artifactSet := annotateArtifactNormalizationFailure(StepArtifactSet{
		StepID:    "canon",
		Action:    "research.resolveCanonicalCitations",
		Artifacts: map[string]any{"canonicalSources": []any{map[string]any{"title": "Canonical Source"}}},
	}, "schema_validation", err)
	if artifactSet.Artifacts["artifactNormalizationStage"] != "schema_validation" {
		t.Fatalf("expected schema_validation stage, got %#v", artifactSet.Artifacts["artifactNormalizationStage"])
	}
	if artifactSet.Artifacts["artifactNormalizationErrorCode"] != "artifact_schema_violation" {
		t.Fatalf("expected schema violation code, got %#v", artifactSet.Artifacts["artifactNormalizationErrorCode"])
	}
	if canonical := firstArtifactMaps(artifactSet.Artifacts["canonicalSources"]); len(canonical) != 1 {
		t.Fatalf("expected raw canonicalSources artifact to be preserved, got %#v", artifactSet.Artifacts["canonicalSources"])
	}
}
