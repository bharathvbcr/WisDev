package wisdev

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
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

func TestMergePostRetrievalDraftCritiqueCarriesFinalReviewState(t *testing.T) {
	original := &LoopDraftCritique{
		NeedsRevision:      true,
		Reasoning:          "The draft needed intervention evidence.",
		NextQueries:        []string{"oncology biomarker intervention trial"},
		MissingAspects:     []string{"interventional validation"},
		MissingSourceTypes: []string{"randomized trials"},
	}
	review := &LoopDraftCritique{
		NeedsRevision: false,
		Reasoning:     "The revised draft is now evidence-grounded.",
		Confidence:    0.88,
	}

	merged := mergePostRetrievalDraftCritique(original, review, true, true)

	if merged == nil {
		t.Fatalf("expected merged critique")
	}
	if merged.NeedsRevision {
		t.Fatalf("expected final review to clear revision requirement")
	}
	if !merged.RetrievalReopened || !merged.AdditionalEvidenceFound {
		t.Fatalf("expected retrieval reopening flags to survive, got %#v", merged)
	}
	if !strings.Contains(merged.Reasoning, "needed intervention evidence") || !strings.Contains(merged.Reasoning, "revised draft") {
		t.Fatalf("expected reasoning to preserve original and final review, got %q", merged.Reasoning)
	}

	gap := mergeDraftCritiqueIntoGapState(&LoopGapState{Sufficient: true}, merged, "oncology biomarker reproducibility")
	if gap == nil || len(gap.Ledger) != 1 {
		t.Fatalf("expected final critique ledger entry, got %#v", gap)
	}
	if got := gap.Ledger[0].Title; got != "Draft critique reopened retrieval and final review passed" {
		t.Fatalf("unexpected final review ledger title %q", got)
	}
}

func TestBuildLoopAutonomousReviewMetadataSummarizesTraceAndCritique(t *testing.T) {
	loopResult := &LoopResult{
		StopReason: "coverage_satisfied",
		DraftCritique: &LoopDraftCritique{
			NeedsRevision:           false,
			RetrievalReopened:       true,
			AdditionalEvidenceFound: true,
			Reasoning:               "Post retrieval review passed.",
			Confidence:              0.91,
			NextQueries:             []string{"sleep memory intervention study"},
		},
		ReasoningTrace: []ReasoningTraceEntry{
			{Phase: "synthesis", Decision: "critique", Reasoning: "opened follow-up retrieval"},
			{Phase: "synthesis", Decision: "post_critique_review", Reasoning: "final review passed"},
		},
		GapAnalysis: &LoopGapState{
			Sufficient: true,
			Ledger: []CoverageLedgerEntry{{
				Status: coverageLedgerStatusResolved,
				Title:  "Draft critique resolved",
			}},
		},
	}

	review := buildLoopAutonomousReviewMetadata(loopResult)

	if review == nil {
		t.Fatalf("expected autonomous review metadata")
	}
	if review["status"] != "resolved_after_follow_up" {
		t.Fatalf("expected resolved follow-up status, got %#v", review["status"])
	}
	if review["postCritiqueReview"] != true {
		t.Fatalf("expected post critique review flag, got %#v", review["postCritiqueReview"])
	}
	if review["traceStepCount"] != 2 {
		t.Fatalf("expected trace step count, got %#v", review["traceStepCount"])
	}
	if review["openLedgerCount"] != 0 || review["stopReason"] != "coverage_satisfied" {
		t.Fatalf("expected closed ledger and stop reason, got %#v", review)
	}
	if got := toStringSlice(review["nextQueries"]); len(got) != 1 || got[0] != "sleep memory intervention study" {
		t.Fatalf("expected critique next query, got %#v", review["nextQueries"])
	}
}

func TestAutonomousLoopRunReCritiquesRevisedDraftAfterCritiqueRetrieval(t *testing.T) {
	var searched []string
	reg := search.NewProviderRegistry()
	reg.Register(&mockSearchProvider{
		name: "post_critique_review",
		SearchFunc: func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
			searched = append(searched, query)
			if strings.Contains(strings.ToLower(query), "intervention") {
				return []search.Paper{{
					ID:       "p-intervention",
					Title:    "Sleep memory intervention follow up",
					Abstract: "A randomized sleep and memory intervention study resolves the draft critique.",
					Source:   "pubmed",
					Score:    0.92,
				}}, nil
			}
			return []search.Paper{{
				ID:       "p-base",
				Title:    "Sleep and memory evidence",
				Abstract: "Initial observational evidence links sleep quality to memory performance.",
				Source:   "openalex",
				Score:    0.82,
			}}, nil
		},
	})
	reg.SetDefaultOrder([]string{"post_critique_review"})

	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	loop := NewAutonomousLoop(reg, lc)

	allowAutonomousSufficiency(msc, `{"sufficient": true, "reasoning": "base evidence is adequate for drafting", "confidence": 0.82}`)
	allowAutonomousDossier(msc, `[]`)
	msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
	})).Return(&llmv1.GenerateResponse{Text: "Initial draft"}, nil).Once()
	msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
	})).Return(&llmv1.GenerateResponse{Text: "Revised draft"}, nil).Once()
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Critique the following research draft")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"needsRevision": true, "reasoning": "Initial critique requires intervention evidence.", "nextQueries": ["sleep memory intervention study"], "missingAspects": ["intervention evidence"], "confidence": 0.41}`}, nil).Once()
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Critique the following research draft")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"needsRevision": false, "reasoning": "Post retrieval review passes after intervention evidence.", "confidence": 0.89}`}, nil).Once()

	result, err := loop.Run(context.Background(), LoopRequest{
		Query:                       "sleep memory",
		MaxIterations:               1,
		MaxSearchTerms:              2,
		HitsPerSearch:               2,
		MaxUniquePapers:             4,
		DisableHypothesisGeneration: true,
	})

	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result == nil || result.DraftCritique == nil {
		t.Fatalf("expected final draft critique, got %#v", result)
	}
	if !strings.Contains(result.FinalAnswer, "Revised draft") {
		t.Fatalf("expected revised draft answer, got %q", result.FinalAnswer)
	}
	if result.DraftCritique.NeedsRevision {
		t.Fatalf("expected post-retrieval critique to clear revision requirement, got %#v", result.DraftCritique)
	}
	if !result.DraftCritique.RetrievalReopened || !result.DraftCritique.AdditionalEvidenceFound {
		t.Fatalf("expected critique retrieval evidence flags, got %#v", result.DraftCritique)
	}
	if !strings.Contains(result.DraftCritique.Reasoning, "Initial critique") || !strings.Contains(result.DraftCritique.Reasoning, "Post retrieval review") {
		t.Fatalf("expected final critique reasoning to include both review stages, got %q", result.DraftCritique.Reasoning)
	}
	if !containsQueryFragment(searched, "intervention") {
		t.Fatalf("expected critique follow-up retrieval, searched %#v", searched)
	}
	msc.AssertExpectations(t)
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

func TestAutonomousLoopRunDefaultsToSearchCacheUnlessBypassed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bypass bool
		want   bool
	}{
		{name: "default cache enabled", bypass: false, want: false},
		{name: "explicit bypass", bypass: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := search.NewProviderRegistry()
			var seen []bool
			reg.Register(&mockSearchProvider{
				name: "cache-policy",
				SearchFunc: func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
					seen = append(seen, opts.SkipCache)
					return []search.Paper{{
						ID:       "p-cache",
						Title:    "Cache policy evidence",
						Abstract: "Evidence for the cache policy path.",
						Source:   "mock",
						Score:    0.91,
					}}, nil
				},
			})
			reg.SetDefaultOrder([]string{"cache-policy"})

			msc := &mockLLMServiceClient{}
			lc := llm.NewClient()
			lc.SetClient(msc)
			loop := NewAutonomousLoop(reg, lc)

			msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
				return strings.Contains(req.Prompt, "Evaluate if the following papers")
			})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": true, "reasoning": "covered", "nextQuery": ""}`}, nil).Once()
			msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
				return strings.Contains(req.Prompt, "Extract the top 2-3")
			})).Return(&llmv1.StructuredResponse{JsonResult: `[]`}, nil).Maybe()
			msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
				return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
			})).Return(&llmv1.GenerateResponse{Text: "cache policy answer"}, nil).Once()
			allowAutonomousCritique(msc, "")

			_, err := loop.Run(context.Background(), LoopRequest{
				Query:                       "cache policy",
				MaxIterations:               1,
				MaxSearchTerms:              1,
				HitsPerSearch:               1,
				MaxUniquePapers:             1,
				BypassSearchCache:           tc.bypass,
				DisableHypothesisGeneration: true,
			})
			if err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			if len(seen) == 0 {
				t.Fatalf("expected search provider to be called")
			}
			if seen[0] != tc.want {
				t.Fatalf("expected SkipCache=%v, got %v", tc.want, seen[0])
			}
			msc.AssertExpectations(t)
		})
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
