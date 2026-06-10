package wisdev

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

// TestAssembleDossierFanOutPreservesPaperOrderWithBoundedConcurrency verifies
// that the bounded per-paper extraction fan-out (1) returns evidence in the
// exact order of the input papers and (2) actually overlaps LLM extractions
// while staying within the semaphore bound.
func TestAssembleDossierFanOutPreservesPaperOrderWithBoundedConcurrency(t *testing.T) {
	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	loop := &AutonomousLoop{llmClient: lc}

	papers := make([]search.Paper, 5)
	for i := range papers {
		papers[i] = search.Paper{
			ID:       fmt.Sprintf("paper-%d", i),
			Title:    fmt.Sprintf("T%d", i),
			Abstract: fmt.Sprintf("A%d", i),
		}
	}

	var trackMu sync.Mutex
	inFlight := 0
	maxInFlight := 0
	track := func(mock.Arguments) {
		trackMu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		trackMu.Unlock()
		time.Sleep(40 * time.Millisecond)
		trackMu.Lock()
		inFlight--
		trackMu.Unlock()
	}

	for i := range papers {
		marker := fmt.Sprintf("Paper Title: %s\n", papers[i].Title)
		payload := fmt.Sprintf(`[{"claim":"claim-%d","snippet":"snippet-%d","confidence":0.9}]`, i, i)
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return req != nil &&
				strings.Contains(req.Prompt, "Extract the top 2-3") &&
				strings.Contains(req.Prompt, marker)
		})).Run(track).Return(&llmv1.StructuredResponse{JsonResult: payload}, nil).Once()
	}

	// An empty query makes the raw-material packet builder yield nothing, which
	// forces assembleDossier down the per-paper LLM extraction fan-out.
	evidence, err := loop.assembleDossier(context.Background(), "", papers)
	assert.NoError(t, err)
	if assert.Len(t, evidence, len(papers)) {
		for i := range papers {
			assert.Equal(t, fmt.Sprintf("claim-%d", i), evidence[i].Claim, "evidence %d must keep paper order", i)
			assert.Equal(t, fmt.Sprintf("snippet-%d", i), evidence[i].Snippet)
			assert.Equal(t, papers[i].ID, evidence[i].PaperID)
			assert.Equal(t, papers[i].Title, evidence[i].PaperTitle)
		}
	}
	assert.Greater(t, maxInFlight, 1, "expected overlapping in-flight extractions")
	assert.LessOrEqual(t, maxInFlight, assembleDossierMaxConcurrentExtractions, "fan-out must respect the semaphore bound")
	msc.AssertExpectations(t)
}

// TestAutonomousRunConcurrentPostCritiqueSufficiencyKeepsLoopResult mirrors the
// mock script of TestAutonomousLoopRunReCritiquesRevisedDraftAfterCritiqueRetrieval
// and asserts that running the post-critique sufficiency evaluation
// concurrently with the re-synthesis leg yields the same LoopResult
// (converged flag + final answer + critique flags) as the sequential code.
func TestAutonomousRunConcurrentPostCritiqueSufficiencyKeepsLoopResult(t *testing.T) {
	reg := search.NewProviderRegistry()
	reg.Register(&mockSearchProvider{
		name: "concurrent_sufficiency",
		SearchFunc: func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
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
	reg.SetDefaultOrder([]string{"concurrent_sufficiency"})

	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	loop := NewAutonomousLoop(reg, lc)

	allowAutonomousSufficiency(msc, `{"sufficient": true, "reasoning": "evidence is adequate for drafting", "confidence": 0.83}`)
	allowAutonomousDossier(msc, `[]`)
	msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
	})).Return(&llmv1.GenerateResponse{Text: "Initial draft"}, nil).Once()
	msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
	})).Return(&llmv1.GenerateResponse{Text: "Revised draft"}, nil).Once()
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil && strings.Contains(req.Prompt, "Critique the following research draft")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"needsRevision": true, "reasoning": "Initial critique requires intervention evidence.", "nextQueries": ["sleep memory intervention study"], "missingAspects": ["intervention evidence"], "confidence": 0.41}`}, nil).Once()
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil && strings.Contains(req.Prompt, "Critique the following research draft")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"needsRevision": false, "reasoning": "Post retrieval review passes after intervention evidence.", "confidence": 0.89}`}, nil).Once()

	result, err := loop.Run(context.Background(), LoopRequest{
		Query:                       "sleep memory",
		MaxIterations:               1,
		MaxSearchTerms:              2,
		HitsPerSearch:               2,
		MaxUniquePapers:             4,
		DisableHypothesisGeneration: true,
	})

	assert.NoError(t, err)
	if assert.NotNil(t, result) {
		assert.True(t, result.Converged, "post-critique sufficiency=true must still drive the converged flag")
		assert.Contains(t, result.FinalAnswer, "Revised draft")
		if assert.NotNil(t, result.DraftCritique) {
			assert.False(t, result.DraftCritique.NeedsRevision)
			assert.True(t, result.DraftCritique.RetrievalReopened)
			assert.True(t, result.DraftCritique.AdditionalEvidenceFound)
		}
	}
	msc.AssertExpectations(t)
}
