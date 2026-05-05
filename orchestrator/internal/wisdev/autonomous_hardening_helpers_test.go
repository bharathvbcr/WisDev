package wisdev

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

func TestBuildCritiqueFollowUpQueriesPrefersLedgerQueries(t *testing.T) {
	gap := &LoopGapState{
		Ledger: []CoverageLedgerEntry{
			{
				Category:          "coverage",
				Status:            coverageLedgerStatusOpen,
				Title:             "Need longitudinal validation",
				SupportingQueries: []string{"oncology biomarker longitudinal cohort validation"},
			},
		},
	}
	critique := &LoopDraftCritique{
		NeedsRevision:  true,
		MissingAspects: []string{"longitudinal validation"},
	}

	queries := buildCritiqueFollowUpQueries("oncology biomarker reproducibility", critique, gap, nil)
	if len(queries) == 0 {
		t.Fatalf("expected critique follow-up queries, got none")
	}
	if queries[0] != "oncology biomarker longitudinal cohort validation" {
		t.Fatalf("expected ledger query to be preferred, got %q", queries[0])
	}
}

func TestBuildFollowUpQueriesFromLedgerPrioritizesExplicitQueriesAcrossEntries(t *testing.T) {
	ledger := []CoverageLedgerEntry{
		{
			Category:          "coverage",
			Status:            coverageLedgerStatusOpen,
			Title:             "Need longitudinal validation",
			Description:       "longitudinal cohort biomarker validation",
			SupportingQueries: []string{"oncology biomarker longitudinal cohort validation"},
		},
		{
			Category:          "contradiction",
			Status:            coverageLedgerStatusOpen,
			Title:             "Resolve contradiction",
			Description:       "studies disagree on reproducibility across cohorts",
			SupportingQueries: []string{"oncology biomarker reproducibility conflicting cohort results"},
		},
		{
			Category:          "source_diversity",
			Status:            coverageLedgerStatusOpen,
			Title:             "Missing systematic review evidence",
			Description:       "The loop still needs stronger systematic review evidence.",
			SupportingQueries: []string{"oncology biomarker systematic review reproducibility"},
		},
	}

	queries := buildFollowUpQueriesFromLedger("oncology biomarker reproducibility", ledger, 3)
	if len(queries) != 3 {
		t.Fatalf("expected three explicit ledger queries, got %#v", queries)
	}
	expected := []string{
		"oncology biomarker longitudinal cohort validation",
		"oncology biomarker reproducibility conflicting cohort results",
		"oncology biomarker systematic review reproducibility",
	}
	for idx, value := range expected {
		if queries[idx] != value {
			t.Fatalf("query %d: expected %q, got %q", idx, value, queries[idx])
		}
	}
}

func TestMergeDraftCritiqueIntoGapStateDoesNotReopenResolvedGaps(t *testing.T) {
	gap := &LoopGapState{
		Sufficient: true,
		Reasoning:  "Coverage is now sufficient after the follow-up retrieval.",
		Ledger: []CoverageLedgerEntry{
			{ID: "resolved-1", Status: coverageLedgerStatusResolved, Title: "Intervention evidence added"},
		},
	}
	critique := &LoopDraftCritique{
		NeedsRevision:           true,
		RetrievalReopened:       true,
		AdditionalEvidenceFound: true,
		Reasoning:               "The draft needed intervention evidence before finalization.",
		NextQueries:             []string{"oncology biomarker intervention trial"},
		MissingAspects:          []string{"interventional validation"},
		MissingSourceTypes:      []string{"randomized trials"},
	}

	merged := mergeDraftCritiqueIntoGapState(gap, critique, "oncology biomarker reproducibility")
	if merged == nil {
		t.Fatalf("expected merged gap state")
	}
	if !merged.Sufficient {
		t.Fatalf("expected resolved gap state to remain sufficient")
	}
	if len(merged.NextQueries) != 0 {
		t.Fatalf("expected resolved critique queries to stay out of top-level nextQueries, got %#v", merged.NextQueries)
	}
	if len(merged.MissingAspects) != 0 {
		t.Fatalf("expected resolved critique gaps to stay out of top-level missingAspects, got %#v", merged.MissingAspects)
	}
	if len(merged.Ledger) != 2 {
		t.Fatalf("expected critique ledger entry to be appended, got %#v", merged.Ledger)
	}
	last := merged.Ledger[len(merged.Ledger)-1]
	if last.Status != coverageLedgerStatusResolved {
		t.Fatalf("expected resolved critique ledger status, got %q", last.Status)
	}
	if last.Title != "Draft critique reopened retrieval and resolved" {
		t.Fatalf("unexpected critique ledger title %q", last.Title)
	}
}

func TestHasMaterialOpenCoverageGapsTreatsUnavailableSufficiencyAsNonBlockingWhenEvidenceIsBroad(t *testing.T) {
	gap := &LoopGapState{
		Sufficient: true,
		Ledger: []CoverageLedgerEntry{{
			Category: "coverage",
			Status:   coverageLedgerStatusOpen,
			Title:    "Structured sufficiency checkpoint unavailable",
		}},
	}
	evidence := []EvidenceItem{{Claim: "a"}, {Claim: "b"}, {Claim: "c"}}
	if hasMaterialOpenCoverageGaps(gap, evidence) {
		t.Fatalf("expected unavailable sufficiency checkpoint to remain visible but non-blocking with broad evidence")
	}
	if !hasMaterialOpenCoverageGaps(gap, evidence[:1]) {
		t.Fatalf("expected same checkpoint to remain blocking when evidence is shallow")
	}
}

func TestCritiqueDraftCooldownErrorFallsBackHeuristically(t *testing.T) {
	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	loop := &AutonomousLoop{llmClient: lc}

	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.GetPrompt(), "Critique the following research draft")
	})).Return(nil, errors.New("vertex structured output provider cooldown active; retry after 45s")).Once()

	critique := loop.critiqueDraft(
		context.Background(),
		"oncology biomarker reproducibility",
		"draft answer",
		[]search.Paper{{ID: "p1", Title: "Cohort Study", Source: "openalex"}},
		[]EvidenceItem{{Claim: "claim", Snippet: "snippet", PaperTitle: "Cohort Study"}},
		&LoopGapState{MissingAspects: []string{"longitudinal validation"}},
	)

	if critique == nil {
		t.Fatalf("expected heuristic critique")
	}
	if !critique.NeedsRevision {
		t.Fatalf("expected heuristic critique to keep revision need for shallow evidence")
	}
	if len(critique.NextQueries) == 0 {
		t.Fatalf("expected heuristic critique to preserve follow-up queries")
	}
	msc.AssertNumberOfCalls(t, "StructuredOutput", 1)
}

func TestBuildEvidenceFindingsFromRawMaterialProducesGroundedPackets(t *testing.T) {
	papers := []search.Paper{
		{
			ID:       "openalex:W1",
			DOI:      "10.1000/example",
			Title:    "Cohort Biomarker Validation",
			Abstract: "Biomarker X remained reproducible across two independent cohorts. The study confirmed stable longitudinal performance.",
			FullText: "Results showed biomarker X remained reproducible across two independent cohorts with stable longitudinal performance.",
			Source:   "openalex",
			Year:     2024,
		},
	}

	findings := buildEvidenceFindingsFromRawMaterial("oncology biomarker reproducibility", papers, 3)
	if len(findings) == 0 {
		t.Fatalf("expected grounded evidence findings, got none")
	}
	if findings[0].SourceID == "" {
		t.Fatalf("expected finding source id to be populated")
	}
	if findings[0].Snippet == "" {
		t.Fatalf("expected finding snippet to be populated")
	}
	if findings[0].PaperTitle == "" {
		t.Fatalf("expected paper title to be populated")
	}
}
