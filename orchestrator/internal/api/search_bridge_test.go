package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/mock"
	"google.golang.org/adk/agent"
)

type mockSearchProvider struct {
	search.BaseProvider
	name       string
	SearchFunc func(context.Context, string, search.SearchOpts) ([]search.Paper, error)
}

func (m *mockSearchProvider) Name() string      { return m.name }
func (m *mockSearchProvider) Domains() []string { return []string{"general"} }
func (m *mockSearchProvider) Healthy() bool     { return true }
func (m *mockSearchProvider) Search(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
	if m.SearchFunc == nil {
		return nil, nil
	}
	return m.SearchFunc(ctx, query, opts)
}

type apiTestADKAgent struct {
	agent.Agent
	name      string
	subAgents []agent.Agent
}

func (a *apiTestADKAgent) Name() string {
	return a.name
}

func (a *apiTestADKAgent) SubAgents() []agent.Agent {
	return a.subAgents
}

func containsAnyString(raw any, expected string) bool {
	switch values := raw.(type) {
	case []string:
		for _, value := range values {
			if value == expected {
				return true
			}
		}
	case []any:
		for _, value := range values {
			if text, ok := value.(string); ok && text == expected {
				return true
			}
		}
	}
	return false
}

func registerWisDevRoutesWithTestRuntime(mux *http.ServeMux) *wisdev.AgentGateway {
	gw := &wisdev.AgentGateway{
		Loop: wisdev.NewAutonomousLoop(nil, nil),
	}
	RegisterWisDevRoutes(mux, gw, nil, nil)
	return gw
}

func testResearchLoopResult(query string, executedQueries []string, papers []search.Paper) *wisdev.LoopResult {
	query = strings.TrimSpace(query)
	if len(executedQueries) == 0 {
		executedQueries = []string{query}
	}
	if len(papers) == 0 {
		papers = []search.Paper{{
			ID:       "paper-" + strings.ReplaceAll(query, " ", "-"),
			Title:    firstNonEmptyString(query, "Research paper"),
			Abstract: "Loop-backed test paper.",
			Source:   "crossref",
		}}
	}
	coverage := make(map[string][]search.Paper, len(executedQueries))
	for idx, executed := range executedQueries {
		paper := papers[minInt(idx, len(papers)-1)]
		coverage[executed] = []search.Paper{paper}
	}
	return &wisdev.LoopResult{
		Papers:          papers,
		ExecutedQueries: executedQueries,
		QueryCoverage:   coverage,
		Iterations:      1,
		Converged:       true,
		GapAnalysis: &wisdev.LoopGapState{
			Sufficient:            true,
			ObservedEvidenceCount: len(papers),
			Coverage: wisdev.LoopCoverageState{
				PlannedQueryCount:  len(executedQueries),
				ExecutedQueryCount: len(executedQueries),
				CoveredQueryCount:  len(executedQueries),
				UniquePaperCount:   len(papers),
			},
		},
		FinalAnswer: "Loop-backed synthesis.",
		FinalizationGate: &wisdev.ResearchFinalizationGate{
			Status:      "promote",
			Ready:       true,
			Provisional: false,
			StopReason:  "verified_final",
		},
		StopReason:     "verified_final",
		ReasoningGraph: &wisdev.ReasoningGraph{Query: query},
		MemoryTiers:    &wisdev.MemoryTierState{},
		RuntimeState: &wisdev.ResearchSessionState{
			SessionID: "test-runtime-session",
			Query:     query,
			Plane:     wisdev.ResearchExecutionPlaneAutonomous,
			Blackboard: &wisdev.ResearchBlackboard{
				ReadyForSynthesis: true,
			},
			StopReason: "verified_final",
		},
	}
}

func assertResearchOrCitationGraphQuery(t *testing.T, query string, baseQuery string) {
	t.Helper()
	normalized := strings.ToLower(strings.TrimSpace(query))
	if strings.Contains(normalized, strings.ToLower(strings.TrimSpace(baseQuery))) {
		return
	}
	if strings.Contains(normalized, " references") ||
		strings.Contains(normalized, " citations") ||
		strings.Contains(normalized, "citing papers") ||
		strings.Contains(normalized, "citation graph") {
		return
	}
	t.Fatalf("expected research or citation-graph query related to %q, got %q", baseQuery, query)
}

func failAutonomousHypothesisProposals(msc *mockLLMServiceClient, err error) {
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return isAutonomousHypothesisProposalPrompt(req)
	})).Return((*llmv1.StructuredResponse)(nil), err).Maybe()
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Critique the following research draft")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"needsRevision": false, "reasoning": "grounded", "confidence": 0.82}`}, nil).Maybe()
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(isAutonomousCommitteePrompt)).Return(&llmv1.StructuredResponse{JsonResult: `{"verdict":"approve","reason":"covered by test evidence"}`}, nil).Maybe()
	msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return req != nil &&
			(strings.Contains(req.Prompt, "Generate 2 specific search queries") ||
				strings.Contains(req.Prompt, "Generate 3 specific search queries"))
	})).Return(&llmv1.GenerateResponse{Text: `["targeted evidence query","replication evidence query"]`}, nil).Maybe()
}

func allowAutonomousCritique(msc *mockLLMServiceClient) {
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Critique the following research draft")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"needsRevision": false, "reasoning": "grounded", "confidence": 0.82}`}, nil).Maybe()
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(isAutonomousCommitteePrompt)).Return(&llmv1.StructuredResponse{JsonResult: `{"verdict":"approve","reason":"covered by test evidence"}`}, nil).Maybe()
}

func isAutonomousHypothesisProposalPrompt(req *llmv1.StructuredRequest) bool {
	return req != nil &&
		(strings.Contains(req.Prompt, "Propose 3 research hypotheses") ||
			strings.Contains(req.Prompt, "Propose 3-5 hypotheses"))
}

func isAutonomousCommitteePrompt(req *llmv1.StructuredRequest) bool {
	if req == nil {
		return false
	}
	return strings.Contains(req.Prompt, "Role: FactChecker") ||
		strings.Contains(req.Prompt, "Role: Synthesizer") ||
		strings.Contains(req.Prompt, "Role: ContradictionAnalyst") ||
		strings.Contains(req.Prompt, "Role: Supervisor")
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func TestRunModularFastSearchUsesBridgeRegistry(t *testing.T) {
	originalBuildBridgeRegistry := buildBridgeRegistry
	originalRunBridgeParallelSearch := runBridgeParallelSearch
	t.Cleanup(func() {
		buildBridgeRegistry = originalBuildBridgeRegistry
		runBridgeParallelSearch = originalRunBridgeParallelSearch
	})

	buildBridgeRegistry = func(requestedProviders ...string) *search.ProviderRegistry {
		expected := []string{"semantic_scholar", "openalex", "pubmed", "core", "arxiv", "europe_pmc", "crossref", "dblp"}
		if len(requestedProviders) != len(expected) {
			t.Fatalf("unexpected provider count: %+v", requestedProviders)
		}
		for index, provider := range expected {
			if requestedProviders[index] != provider {
				t.Fatalf("unexpected providers: %+v", requestedProviders)
			}
		}
		return search.NewProviderRegistry()
	}

	runBridgeParallelSearch = func(_ context.Context, _ *search.ProviderRegistry, query string, opts search.SearchOpts) search.SearchResult {
		if query != "rlhf" {
			t.Fatalf("expected query rlhf, got %q", query)
		}
		if opts.Limit != 8 {
			t.Fatalf("expected limit 8, got %d", opts.Limit)
		}
		if !opts.QualitySort {
			t.Fatalf("expected quality sort to be enabled")
		}
		return search.SearchResult{
			Papers: []search.Paper{
				{
					ID:            "p1",
					Title:         "Reward Modeling for RLHF",
					Abstract:      "Abstract",
					Link:          "https://example.com/p1",
					DOI:           "10.1000/p1",
					Source:        "semantic_scholar",
					SourceApis:    []string{"semantic_scholar"},
					Authors:       []string{"Ada Lovelace"},
					Year:          2024,
					Venue:         "NeurIPS",
					Keywords:      []string{"rlhf", "reward modeling"},
					CitationCount: 11,
					Score:         0.88,
				},
			},
			Providers: map[string]int{
				"semantic_scholar": 1,
			},
			LatencyMs: 17,
			Cached:    true,
		}
	}

	papers, err := runModularFastSearch(context.Background(), nil, "rlhf", 8)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(papers) != 1 {
		t.Fatalf("expected 1 paper, got %d", len(papers))
	}
	if papers[0].Title != "Reward Modeling for RLHF" {
		t.Fatalf("unexpected paper title: %q", papers[0].Title)
	}
	if len(papers[0].SourceApis) != 1 || papers[0].SourceApis[0] != "semantic_scholar" {
		t.Fatalf("expected sourceApis metadata, got %+v", papers[0].SourceApis)
	}
	if papers[0].Publication != "NeurIPS" {
		t.Fatalf("expected publication metadata, got %q", papers[0].Publication)
	}
}

func TestRunModularFastSearchRecoversFromPanics(t *testing.T) {
	originalBuildBridgeRegistry := buildBridgeRegistry
	originalRunBridgeParallelSearch := runBridgeParallelSearch
	t.Cleanup(func() {
		buildBridgeRegistry = originalBuildBridgeRegistry
		runBridgeParallelSearch = originalRunBridgeParallelSearch
	})

	buildBridgeRegistry = func(_ ...string) *search.ProviderRegistry {
		return search.NewProviderRegistry()
	}
	runBridgeParallelSearch = func(_ context.Context, _ *search.ProviderRegistry, _ string, _ search.SearchOpts) search.SearchResult {
		panic("boom")
	}

	papers, err := runModularFastSearch(context.Background(), nil, "rlhf", 8)
	if err == nil {
		t.Fatalf("expected panic to be converted into an error")
	}
	if len(papers) != 0 {
		t.Fatalf("expected no papers on recovered panic, got %d", len(papers))
	}
	if !strings.Contains(err.Error(), "modular fast search panic") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunModularParallelSearchDelegatesToCanonicalWisdevSearch(t *testing.T) {
	originalCanonical := runCanonicalWisdevParallelSearch
	t.Cleanup(func() {
		runCanonicalWisdevParallelSearch = originalCanonical
	})

	registry := search.NewProviderRegistry()
	runCanonicalWisdevParallelSearch = func(_ context.Context, _ redis.UniversalClient, query string, opts wisdev.SearchOptions) (*wisdev.MultiSourceResult, error) {
		if query != "rlhf" {
			t.Fatalf("expected query rlhf, got %q", query)
		}
		if !opts.ExpandQuery {
			t.Fatalf("expected expand query to remain enabled")
		}
		if opts.Registry != registry {
			t.Fatalf("expected injected registry to be forwarded")
		}
		return &wisdev.MultiSourceResult{
			QueryUsed: "rlhf expanded",
			EnhancedQuery: wisdev.EnhancedQuery{
				Original: "rlhf",
				Expanded: "rlhf expanded",
				Intent:   "papers",
			},
		}, nil
	}

	result, err := runModularParallelSearch(context.Background(), nil, registry, "rlhf", wisdev.SearchOptions{
		Limit:       5,
		ExpandQuery: true,
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if result == nil {
		t.Fatalf("expected result payload")
	}
	if result.QueryUsed != "rlhf expanded" {
		t.Fatalf("expected queryUsed to survive canonical delegate, got %q", result.QueryUsed)
	}
}

func TestAutonomousResearchRouteReturnsEnvelope(t *testing.T) {
	originalRunUnifiedResearchLoop := runUnifiedResearchLoop
	t.Cleanup(func() {
		runUnifiedResearchLoop = originalRunUnifiedResearchLoop
	})

	runUnifiedResearchLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, plane wisdev.ResearchExecutionPlane, req wisdev.LoopRequest, _ func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error) {
		if plane != wisdev.ResearchExecutionPlaneAutonomous {
			t.Fatalf("expected autonomous execution plane, got %q", plane)
		}
		if req.Query != "RLHF reinforcement learning" {
			t.Fatalf("expected corrected query, got %q", req.Query)
		}
		return testResearchLoopResult(req.Query, []string{req.Query}, []search.Paper{
			{
				ID:       "p1",
				Title:    "Reward Modeling for RLHF",
				Abstract: "A useful paper",
				DOI:      "10.1000/p1",
				Source:   "semantic_scholar",
			},
		}), nil
	}

	mux := http.NewServeMux()
	registerWisDevRoutesWithTestRuntime(mux)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/research/autonomous", strings.NewReader(`{
		"session":{"sessionId":"s1","correctedQuery":"RLHF reinforcement learning"},
		"plan":{"query":"ignored"}
	}`))
	req = withTestUserID(req, "u1")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if body["traceId"] == "" {
		t.Fatalf("expected traceId in envelope")
	}
	payload, ok := body["autonomousResearch"].(map[string]any)
	if !ok {
		t.Fatalf("expected autonomousResearch payload in envelope")
	}
	prismaReport, ok := payload["prismaReport"].(map[string]any)
	if !ok {
		t.Fatalf("expected prismaReport payload")
	}
	if prismaReport["included"] != float64(1) {
		t.Fatalf("expected included count 1, got %v", prismaReport["included"])
	}
}

func TestAutonomousResearchRouteReturnsErrorWhenUnifiedLoopFails(t *testing.T) {
	originalRunUnifiedResearchLoop := runUnifiedResearchLoop
	t.Cleanup(func() {
		runUnifiedResearchLoop = originalRunUnifiedResearchLoop
	})

	runUnifiedResearchLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, _ wisdev.ResearchExecutionPlane, _ wisdev.LoopRequest, _ func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error) {
		return nil, errors.New("unified loop unavailable")
	}

	mux := http.NewServeMux()
	registerWisDevRoutesWithTestRuntime(mux)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/research/autonomous", strings.NewReader(`{
		"session":{"sessionId":"s1","correctedQuery":"RLHF reinforcement learning"}
	}`))
	req = withTestUserID(req, "u1")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected provider-unavailable response, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "wisdev autonomous research loop failed") {
		t.Fatalf("expected canonical loop error, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "provider_unavailable") {
		t.Fatalf("expected provider-unavailable classification, got %s", rec.Body.String())
	}
}

func TestAutonomousResearchRouteMarksLoopFailureAsDegradedFallback(t *testing.T) {
	originalRunUnifiedResearchLoop := runUnifiedResearchLoop
	t.Cleanup(func() {
		runUnifiedResearchLoop = originalRunUnifiedResearchLoop
	})

	registry := search.NewProviderRegistry()
	registry.Register(&mockSearchProvider{
		name: "autonomous_fallback_mock",
		SearchFunc: func(_ context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
			if query != "RLHF reinforcement learning" {
				t.Fatalf("expected fallback query, got %q", query)
			}
			if opts.Limit != 12 {
				t.Fatalf("expected limit 12 from execution profile, got %d", opts.Limit)
			}
			return []search.Paper{
				{
					ID:       "p1",
					Title:    "Reward Modeling for RLHF",
					Abstract: "A useful paper",
					Source:   "semantic_scholar",
				},
			}, nil
		},
	})
	registry.SetDefaultOrder([]string{"autonomous_fallback_mock"})

	runUnifiedResearchLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, _ wisdev.ResearchExecutionPlane, _ wisdev.LoopRequest, _ func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error) {
		return nil, errors.New("loop execution failed")
	}

	gw := &wisdev.AgentGateway{
		Loop:           wisdev.NewAutonomousLoop(registry, nil),
		SearchRegistry: registry,
	}

	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/research/autonomous", strings.NewReader(`{
		"session":{"sessionId":"s1","correctedQuery":"RLHF reinforcement learning"},
		"plan":{"query":"ignored"}
	}`))
	req = withTestUserID(req, "u1")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected unified-runtime failure response, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "wisdev autonomous research loop failed") {
		t.Fatalf("expected terminal unified-runtime failure, got %s", rec.Body.String())
	}
}

func TestDeepResearchRouteUsesAutonomousLoopGapLedger(t *testing.T) {
	originalRetrieveCanonicalPapers := wisdev.RetrieveCanonicalPapers
	originalRunUnifiedResearchLoop := runUnifiedResearchLoop
	t.Cleanup(func() {
		wisdev.RetrieveCanonicalPapers = originalRetrieveCanonicalPapers
		runUnifiedResearchLoop = originalRunUnifiedResearchLoop
	})

	wisdev.RetrieveCanonicalPapers = func(_ context.Context, _ redis.UniversalClient, _ string, _ int) ([]wisdev.Source, map[string]any, error) {
		t.Fatalf("canonical deep retrieval should not run when the autonomous loop succeeds")
		return nil, nil, nil
	}
	var capturedLoopReq wisdev.LoopRequest
	runUnifiedResearchLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, plane wisdev.ResearchExecutionPlane, req wisdev.LoopRequest, _ func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error) {
		capturedLoopReq = req
		if plane != wisdev.ResearchExecutionPlaneDeep {
			t.Fatalf("expected deep execution plane, got %q", plane)
		}
		if req.Query != "sleep spindles" {
			t.Fatalf("expected loop query sleep spindles, got %q", req.Query)
		}
		return &wisdev.LoopResult{
			Papers: []search.Paper{
				{ID: "p1", Title: "Sleep spindle evidence", Abstract: "Sleep spindles predict memory consolidation.", Source: "crossref"},
			},
			Iterations:      2,
			Converged:       false,
			ExecutedQueries: []string{"sleep spindles", "sleep spindle mechanism"},
			QueryCoverage: map[string][]search.Paper{
				"sleep spindles":          {{ID: "p1", Title: "Sleep spindle evidence", Abstract: "Sleep spindles predict memory consolidation.", Source: "crossref"}},
				"sleep spindle mechanism": nil,
			},
			GapAnalysis: &wisdev.LoopGapState{
				Sufficient:             false,
				Reasoning:              "Need intervention studies to close the mechanism claim.",
				NextQueries:            []string{"sleep spindle intervention trial"},
				MissingAspects:         []string{"causal intervention evidence"},
				MissingSourceTypes:     []string{"randomized trials"},
				ObservedSourceFamilies: []string{"crossref"},
				ObservedEvidenceCount:  1,
				Ledger: []wisdev.CoverageLedgerEntry{
					{
						ID:                "ledger-1",
						Category:          "source_diversity",
						Status:            "open",
						Title:             "Need intervention studies",
						Description:       "Randomized trials are still missing.",
						SupportingQueries: []string{"sleep spindle intervention trial"},
						SourceFamilies:    []string{"crossref"},
						Confidence:        0.44,
					},
				},
				Confidence: 0.44,
				Coverage: wisdev.LoopCoverageState{
					PlannedQueryCount:      2,
					ExecutedQueryCount:     2,
					CoveredQueryCount:      1,
					UniquePaperCount:       1,
					QueriesWithoutCoverage: []string{"sleep spindle mechanism"},
				},
			},
			DraftCritique: &wisdev.LoopDraftCritique{
				NeedsRevision:           true,
				RetrievalReopened:       true,
				AdditionalEvidenceFound: true,
				Reasoning:               "The initial draft needed randomized intervention evidence.",
				NextQueries:             []string{"sleep spindle intervention trial"},
			},
			ReasoningGraph: &wisdev.ReasoningGraph{},
			MemoryTiers:    &wisdev.MemoryTierState{},
			RuntimeState: &wisdev.ResearchSessionState{
				SessionID: "s1",
				Query:     "sleep spindles",
				Plane:     wisdev.ResearchExecutionPlaneDeep,
				Budget: &wisdev.ResearchBudgetDecision{
					SearchTermBudget:     8,
					FollowUpSearchBudget: 3,
				},
				DurableJob: &wisdev.ResearchDurableJobState{
					JobID:       "deep-job-1",
					SessionID:   "s1",
					Plane:       wisdev.ResearchExecutionPlaneDeep,
					Status:      "completed",
					Replayable:  true,
					ResumeToken: "s1",
				},
				SourceAcquisition: &wisdev.ResearchSourceAcquisitionPlan{
					Attempts: []wisdev.ResearchSourceAcquisitionAttempt{{
						SourceID:              "p1",
						CanonicalID:           "pmid:12345678",
						SourceType:            "pdf",
						Status:                "planned",
						WorkerPlane:           "python_docling",
						NeedsPythonExtraction: true,
					}},
					RequiredPythonExtractions: 1,
				},
				StopReason: "verifier_requires_revision",
			},
		}, nil
	}

	gw := &wisdev.AgentGateway{
		Loop: wisdev.NewAutonomousLoop(nil, nil),
	}

	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/research/deep", strings.NewReader(`{
		"query":"sleep spindles",
		"categories":["sleep"],
		"userId":"u1",
		"projectId":"p1",
		"sessionId":"s1",
		"qualityMode":"balanced"
	}`))
	req = withTestUserID(req, "u1")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected deep research 200, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	payload, ok := body["deepResearch"].(map[string]any)
	if !ok {
		t.Fatalf("expected deepResearch payload in envelope")
	}
	metadata, ok := payload["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("expected metadata payload, got %#v", payload["metadata"])
	}
	if metadata["executionPlane"] != "go_canonical_runtime" {
		t.Fatalf("expected go_canonical_runtime execution plane, got %#v", metadata["executionPlane"])
	}
	if capturedLoopReq.MaxIterations != 5 {
		t.Fatalf("expected balanced max iterations 5, got %d", capturedLoopReq.MaxIterations)
	}
	if capturedLoopReq.MaxSearchTerms != 6 {
		t.Fatalf("expected balanced max search terms 6, got %d", capturedLoopReq.MaxSearchTerms)
	}
	if capturedLoopReq.HitsPerSearch != 12 {
		t.Fatalf("expected balanced hits per search 12, got %d", capturedLoopReq.HitsPerSearch)
	}
	if metadata["qualityMode"] != "balanced" {
		t.Fatalf("expected balanced quality metadata, got %#v", metadata["qualityMode"])
	}
	if metadata["runtimeStateAvailable"] != true {
		t.Fatalf("expected runtimeStateAvailable metadata, got %#v", metadata)
	}
	if metadata["durableJobId"] != "deep-job-1" {
		t.Fatalf("expected deep durable job metadata, got %#v", metadata["durableJobId"])
	}
	if metadata["pythonPdfExtractions"] != float64(1) {
		t.Fatalf("expected Python PDF extraction metadata, got %#v", metadata["pythonPdfExtractions"])
	}
	if _, ok := payload["durableJob"].(map[string]any); !ok {
		t.Fatalf("expected deep durableJob payload, got %#v", payload["durableJob"])
	}
	if _, ok := payload["sourceAcquisition"].(map[string]any); !ok {
		t.Fatalf("expected deep sourceAcquisition payload, got %#v", payload["sourceAcquisition"])
	}
	gapAnalysis, ok := payload["gapAnalysis"].(map[string]any)
	if !ok {
		t.Fatalf("expected gapAnalysis payload, got %#v", payload["gapAnalysis"])
	}
	if gapAnalysis["reasoning"] != "Need intervention studies to close the mechanism claim." {
		t.Fatalf("expected loop reasoning in gapAnalysis, got %#v", gapAnalysis["reasoning"])
	}
	draftCritique, ok := payload["draftCritique"].(map[string]any)
	if !ok {
		t.Fatalf("expected draftCritique payload, got %#v", payload["draftCritique"])
	}
	if draftCritique["retrievalReopened"] != true {
		t.Fatalf("expected retrievalReopened=true, got %#v", draftCritique["retrievalReopened"])
	}
	workerMetadata, ok := payload["workerMetadata"].(map[string]any)
	if !ok {
		t.Fatalf("expected workerMetadata payload, got %#v", payload["workerMetadata"])
	}
	if workerMetadata["retrievalMode"] != "deep_canonical_runtime" {
		t.Fatalf("expected deep canonical runtime retrieval mode, got %#v", workerMetadata["retrievalMode"])
	}
	if workerMetadata["observedEvidenceCount"] != float64(1) {
		t.Fatalf("expected observedEvidenceCount 1, got %#v", workerMetadata["observedEvidenceCount"])
	}
	coverageLedger, ok := payload["coverageLedger"].([]any)
	if !ok || len(coverageLedger) != 1 {
		t.Fatalf("expected coverageLedger payload, got %#v", payload["coverageLedger"])
	}
	gaps, ok := payload["gaps"].([]any)
	if !ok || len(gaps) == 0 {
		t.Fatalf("expected structured gaps payload, got %#v", payload["gaps"])
	}
	firstGap, ok := gaps[0].(map[string]any)
	if !ok {
		t.Fatalf("expected first gap map, got %#v", gaps[0])
	}
	if firstGap["id"] != "ledger-1" {
		t.Fatalf("expected ledger-backed gap id, got %#v", firstGap["id"])
	}
	if firstGap["observedEvidenceCount"] != float64(1) {
		t.Fatalf("expected observedEvidenceCount on gap payload, got %#v", firstGap["observedEvidenceCount"])
	}
}

func TestDeepResearchRouteExposesCitationGraphAndDurableTaskMetadata(t *testing.T) {
	originalRetrieveCanonicalPapers := wisdev.RetrieveCanonicalPapers
	originalRunUnifiedResearchLoop := runUnifiedResearchLoop
	t.Cleanup(func() {
		wisdev.RetrieveCanonicalPapers = originalRetrieveCanonicalPapers
		runUnifiedResearchLoop = originalRunUnifiedResearchLoop
	})

	wisdev.RetrieveCanonicalPapers = func(_ context.Context, _ redis.UniversalClient, _ string, _ int) ([]wisdev.Source, map[string]any, error) {
		t.Fatalf("canonical deep retrieval should not run when the autonomous loop succeeds")
		return nil, nil, nil
	}
	runUnifiedResearchLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, plane wisdev.ResearchExecutionPlane, req wisdev.LoopRequest, _ func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error) {
		if plane != wisdev.ResearchExecutionPlaneDeep {
			t.Fatalf("expected deep execution plane, got %q", plane)
		}
		citationGraph := &wisdev.ResearchCitationGraph{
			Query: req.Query,
			Nodes: []wisdev.ResearchCitationGraphNode{
				{ID: "doi:10.1000/seed", Title: "Seed Trial", CanonicalID: "doi:10.1000/seed", SourceFamily: "pubmed"},
				{ID: "doi:10.1000/citing", Title: "Forward Citation", CanonicalID: "doi:10.1000/citing", SourceFamily: "semantic_scholar"},
			},
			Edges: []wisdev.ResearchCitationGraphEdge{
				{SourceID: "doi:10.1000/citing", TargetID: "doi:10.1000/seed", Kind: "forward_citation"},
			},
			IdentityConflicts: []string{"doi:10.1000/seed has conflicting title metadata"},
			CoverageLedger: []wisdev.CoverageLedgerEntry{{
				ID:                "citation-identity-open",
				Category:          "citation_identity_conflict",
				Status:            "open",
				Title:             "Citation identity conflicts require reconciliation",
				SupportingQueries: []string{"deep citation graph DOI arXiv PMID reconciliation"},
				Required:          true,
				Priority:          98,
			}},
		}
		state := &wisdev.ResearchSessionState{
			SessionID: "deep-citation-session",
			Query:     req.Query,
			Plane:     wisdev.ResearchExecutionPlaneDeep,
			CoverageLedger: []wisdev.CoverageLedgerEntry{{
				ID:                "citation-identity-open",
				Category:          "citation_identity_conflict",
				Status:            "open",
				Title:             "Citation identity conflicts require reconciliation",
				SupportingQueries: []string{"deep citation graph DOI arXiv PMID reconciliation"},
				Required:          true,
				Priority:          98,
			}},
			BranchEvaluations: []wisdev.ResearchBranchEvaluation{{
				ID:              "branch-citation",
				Query:           "deep citation graph DOI arXiv PMID reconciliation",
				Status:          "open",
				VerifierVerdict: "revise_required",
				BranchScore:     0.46,
				StopReason:      "branch_open_gap",
				OpenGaps:        []string{"citation identity conflicts require reconciliation"},
			}},
			VerifierDecision: &wisdev.ResearchVerifierDecision{
				Role:            wisdev.ResearchWorkerIndependentVerifier,
				Verdict:         "revise_required",
				StopReason:      "verifier_requires_revision",
				RevisionReasons: []string{"citation identity conflicts remain unresolved"},
				Confidence:      0.41,
				EvidenceOnly:    true,
			},
			DurableJob: &wisdev.ResearchDurableJobState{
				JobID:       "deep-citation-job",
				SessionID:   "deep-citation-session",
				Plane:       wisdev.ResearchExecutionPlaneDeep,
				Status:      "completed",
				Replayable:  true,
				ResumeToken: "deep-citation-session",
			},
			DurableTasks: []wisdev.ResearchDurableTaskState{{
				TaskKey:       "task-citation-graph",
				CheckpointKey: "checkpoint-citation-graph",
				TraceID:       "trace-citation-graph",
				Operation:     "citation_graph_step",
				Role:          wisdev.ResearchWorkerCitationGraph,
				Status:        "checkpointed_open",
				TimeoutMs:     8000,
				RetryPolicy: wisdev.ResearchRetryPolicy{
					MaxAttempts:         2,
					BackoffMs:           750,
					RetryableErrorCodes: []string{"timeout", "provider_unavailable"},
				},
				Attempt: 1,
			}},
			CitationGraph: citationGraph,
			StopReason:    "verifier_requires_revision",
		}
		state.DurableJob.Tasks = state.DurableTasks
		return &wisdev.LoopResult{
			Papers: []search.Paper{{ID: "doi:10.1000/seed", Title: "Seed Trial", Abstract: "Seed abstract.", Source: "pubmed", DOI: "10.1000/seed"}},
			ExecutedQueries: []string{
				req.Query,
				"deep citation graph DOI arXiv PMID reconciliation",
			},
			QueryCoverage: map[string][]search.Paper{
				req.Query: {{ID: "doi:10.1000/seed", Title: "Seed Trial", Abstract: "Seed abstract.", Source: "pubmed", DOI: "10.1000/seed"}},
			},
			GapAnalysis: &wisdev.LoopGapState{
				Sufficient: false,
				Reasoning:  "Citation identity remains unresolved.",
				Ledger:     state.CoverageLedger,
				NextQueries: []string{
					"deep citation graph DOI arXiv PMID reconciliation",
				},
				ObservedEvidenceCount: 1,
			},
			FinalizationGate: &wisdev.ResearchFinalizationGate{
				Status:          "provisional",
				Provisional:     true,
				StopReason:      "verifier_requires_revision",
				VerifierVerdict: "revise_required",
			},
			FinalAnswer:    "Provisional citation synthesis",
			StopReason:     "verifier_requires_revision",
			RuntimeState:   state,
			ReasoningGraph: &wisdev.ReasoningGraph{},
			MemoryTiers:    &wisdev.MemoryTierState{},
		}, nil
	}

	gw := &wisdev.AgentGateway{
		Loop: wisdev.NewAutonomousLoop(nil, nil),
		ADKRuntime: &wisdev.ADKRuntime{
			Config: wisdev.DefaultADKRuntimeConfig(),
			Agent: &apiTestADKAgent{
				name: "wisdev-root",
				subAgents: []agent.Agent{
					&apiTestADKAgent{name: "wisdev-reasoning"},
					&apiTestADKAgent{name: "python-researcher"},
				},
			},
		},
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/research/deep", strings.NewReader(`{
		"query":"deep citation graph",
		"categories":["citation"],
		"userId":"u1",
		"projectId":"p1",
		"sessionId":"deep-citation-session"
	}`))
	req = withTestUserID(req, "u1")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected deep research 200, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	payload, ok := body["deepResearch"].(map[string]any)
	if !ok {
		t.Fatalf("expected deepResearch payload in envelope")
	}
	metadata, ok := payload["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("expected metadata payload, got %#v", payload["metadata"])
	}
	if metadata["answerStatus"] != "provisional" {
		t.Fatalf("expected provisional answerStatus, got %#v", metadata["answerStatus"])
	}
	if metadata["verifierVerdict"] != "revise_required" {
		t.Fatalf("expected verifier verdict metadata, got %#v", metadata["verifierVerdict"])
	}
	if metadata["citationGraphNodeCount"] != float64(2) || metadata["citationGraphEdgeCount"] != float64(1) {
		t.Fatalf("expected citation graph counts in metadata, got %#v", metadata)
	}
	if metadata["durableTaskCount"] != float64(1) {
		t.Fatalf("expected durable task count metadata, got %#v", metadata["durableTaskCount"])
	}
	reasoningRuntime, ok := metadata["reasoningRuntime"].(map[string]any)
	if !ok {
		t.Fatalf("expected reasoningRuntime metadata, got %#v", metadata["reasoningRuntime"])
	}
	if reasoningRuntime["runtimeMode"] != "tree_search_with_programmatic_planner" || reasoningRuntime["treeSearchRuntime"] != true {
		t.Fatalf("expected tree-search reasoning runtime metadata, got %#v", reasoningRuntime)
	}
	if !containsAnyString(metadata["configuredSubAgents"], "wisdev-reasoning") || !containsAnyString(metadata["configuredSubAgents"], "python-researcher") {
		t.Fatalf("expected configured subagent metadata, got %#v", metadata["configuredSubAgents"])
	}
	payloadReasoningRuntime, ok := payload["reasoningRuntime"].(map[string]any)
	if !ok || payloadReasoningRuntime["runtimeMode"] != "tree_search_with_programmatic_planner" {
		t.Fatalf("expected payload reasoningRuntime metadata, got %#v", payload["reasoningRuntime"])
	}
	if _, ok := payload["citationGraph"].(map[string]any); !ok {
		t.Fatalf("expected citationGraph payload, got %#v", payload["citationGraph"])
	}
	durableTasks, ok := payload["durableTasks"].([]any)
	if !ok || len(durableTasks) != 1 {
		t.Fatalf("expected durableTasks payload, got %#v", payload["durableTasks"])
	}
	task, ok := durableTasks[0].(map[string]any)
	if !ok || task["operation"] != "citation_graph_step" || task["checkpointKey"] == "" || task["traceId"] == "" {
		t.Fatalf("expected citation graph durable task metadata, got %#v", durableTasks)
	}
	obligations, ok := payload["coverageObligations"].([]any)
	if !ok || len(obligations) == 0 {
		t.Fatalf("expected coverageObligations payload, got %#v", payload["coverageObligations"])
	}
	firstObligation, ok := obligations[0].(map[string]any)
	if !ok || firstObligation["obligationType"] != "missing_citation_identity" || firstObligation["ownerWorker"] != "citation_graph" {
		t.Fatalf("expected typed citation identity obligation, got %#v", obligations)
	}
	branches, ok := payload["researchBranches"].([]any)
	if !ok || len(branches) != 1 {
		t.Fatalf("expected researchBranches payload, got %#v", payload["researchBranches"])
	}
}

func TestDeepResearchRouteReturnsBudgetExhaustedAnswerStatusFromRuntime(t *testing.T) {
	originalRetrieveCanonicalPapers := wisdev.RetrieveCanonicalPapers
	originalRunUnifiedResearchLoop := runUnifiedResearchLoop
	t.Cleanup(func() {
		wisdev.RetrieveCanonicalPapers = originalRetrieveCanonicalPapers
		runUnifiedResearchLoop = originalRunUnifiedResearchLoop
	})

	wisdev.RetrieveCanonicalPapers = func(_ context.Context, _ redis.UniversalClient, _ string, _ int) ([]wisdev.Source, map[string]any, error) {
		t.Fatalf("canonical deep retrieval should not run when the autonomous loop succeeds")
		return nil, nil, nil
	}
	runUnifiedResearchLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, plane wisdev.ResearchExecutionPlane, req wisdev.LoopRequest, _ func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error) {
		state := &wisdev.ResearchSessionState{
			SessionID: "deep-budget-session",
			Query:     req.Query,
			Plane:     plane,
			CoverageLedger: []wisdev.CoverageLedgerEntry{{
				ID:                "budget-open",
				Category:          "budget",
				Status:            "open",
				Title:             "Budget exhausted before counter-evidence recovery",
				Description:       "Contradiction recovery remained open after budget exhaustion.",
				SupportingQueries: []string{"budget exhausted counter evidence"},
				Required:          true,
				Priority:          99,
			}},
			VerifierDecision: &wisdev.ResearchVerifierDecision{
				Role:            wisdev.ResearchWorkerIndependentVerifier,
				Verdict:         "revise_required",
				StopReason:      "verifier_requires_revision",
				RevisionReasons: []string{"counter evidence remains open"},
				Confidence:      0.37,
				EvidenceOnly:    true,
			},
			DurableJob: &wisdev.ResearchDurableJobState{
				JobID:       "deep-budget-job",
				SessionID:   "deep-budget-session",
				Plane:       plane,
				Status:      "completed",
				Replayable:  true,
				ResumeToken: "deep-budget-session",
				StopReason:  "budget_exhausted_with_open_gaps",
				BudgetUsed:  wisdev.ResearchBudgetUsage{ExecutedQueries: 4, OpenLedgerCount: 1, Exhausted: true},
			},
			DurableTasks: []wisdev.ResearchDurableTaskState{{
				TaskKey:       "task-verifier-budget",
				CheckpointKey: "checkpoint-verifier-budget",
				TraceID:       "trace-verifier-budget",
				Operation:     "verifier_pass",
				Role:          wisdev.ResearchWorkerIndependentVerifier,
				Status:        "checkpointed_open",
				TimeoutMs:     10000,
				RetryPolicy:   wisdev.ResearchRetryPolicy{MaxAttempts: 1},
				Attempt:       1,
			}},
			StopReason: "budget_exhausted_with_open_gaps",
		}
		state.DurableJob.Tasks = state.DurableTasks
		return &wisdev.LoopResult{
			Papers:          []search.Paper{{ID: "paper-budget", Title: "Budget Paper", Abstract: "Budget abstract.", Source: "crossref"}},
			ExecutedQueries: []string{req.Query, "budget exhausted counter evidence"},
			GapAnalysis: &wisdev.LoopGapState{
				Sufficient: false,
				Reasoning:  "Budget exhausted with open counter-evidence gaps.",
				Ledger:     state.CoverageLedger,
				NextQueries: []string{
					"budget exhausted counter evidence",
				},
				ObservedEvidenceCount: 1,
			},
			FinalizationGate: &wisdev.ResearchFinalizationGate{
				Status:          "provisional",
				Provisional:     true,
				StopReason:      "budget_exhausted_with_open_gaps",
				VerifierVerdict: "revise_required",
				OpenLedgerCount: 1,
			},
			FinalAnswer:    "Provisional budget-limited synthesis",
			StopReason:     "budget_exhausted_with_open_gaps",
			RuntimeState:   state,
			ReasoningGraph: &wisdev.ReasoningGraph{},
			MemoryTiers:    &wisdev.MemoryTierState{},
		}, nil
	}

	gw := &wisdev.AgentGateway{Loop: wisdev.NewAutonomousLoop(nil, nil)}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/research/deep", strings.NewReader(`{
		"query":"budget exhausted research",
		"categories":["methods"],
		"userId":"u1",
		"projectId":"p1",
		"sessionId":"deep-budget-session"
	}`))
	req = withTestUserID(req, "u1")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected deep research 200, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	payload, ok := body["deepResearch"].(map[string]any)
	if !ok {
		t.Fatalf("expected deepResearch payload in envelope")
	}
	metadata, ok := payload["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("expected metadata payload, got %#v", payload["metadata"])
	}
	if metadata["answerStatus"] != "budget_exhausted" {
		t.Fatalf("expected budget_exhausted answerStatus, got %#v", metadata["answerStatus"])
	}
	durableJob, ok := payload["durableJob"].(map[string]any)
	if !ok {
		t.Fatalf("expected durableJob payload, got %#v", payload["durableJob"])
	}
	budgetUsed, ok := durableJob["budgetUsed"].(map[string]any)
	if !ok || budgetUsed["exhausted"] != true {
		t.Fatalf("expected exhausted durable budget payload, got %#v", durableJob["budgetUsed"])
	}
	if metadata["durableJobId"] != "deep-budget-job" || metadata["durableTaskCount"] != float64(1) {
		t.Fatalf("expected durable job/task metadata, got %#v", metadata)
	}
	obligations, ok := payload["coverageObligations"].([]any)
	if !ok || len(obligations) == 0 {
		t.Fatalf("expected budget coverage obligation, got %#v", payload["coverageObligations"])
	}
	firstObligation, ok := obligations[0].(map[string]any)
	if !ok || firstObligation["obligationType"] != "missing_counter_evidence" || firstObligation["status"] != "open" {
		t.Fatalf("expected open budget/counter-evidence obligation, got %#v", obligations)
	}
}

func TestDeepResearchRouteFailsWhenUnifiedLoopFails(t *testing.T) {
	originalRetrieveCanonicalPapers := wisdev.RetrieveCanonicalPapers
	originalRunUnifiedResearchLoop := runUnifiedResearchLoop
	t.Cleanup(func() {
		wisdev.RetrieveCanonicalPapers = originalRetrieveCanonicalPapers
		runUnifiedResearchLoop = originalRunUnifiedResearchLoop
	})

	wisdev.RetrieveCanonicalPapers = func(_ context.Context, _ redis.UniversalClient, query string, limit int) ([]wisdev.Source, map[string]any, error) {
		if query != "sleep spindles" {
			t.Fatalf("expected canonical query sleep spindles, got %q", query)
		}
		if limit != 16 {
			t.Fatalf("expected canonical budget limit 16, got %d", limit)
		}
		return []wisdev.Source{
			{ID: "fallback-p1", Title: "Fallback paper", Source: "crossref"},
		}, nil, nil
	}
	runUnifiedResearchLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, plane wisdev.ResearchExecutionPlane, req wisdev.LoopRequest, _ func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error) {
		if plane != wisdev.ResearchExecutionPlaneDeep {
			t.Fatalf("expected deep execution plane, got %q", plane)
		}
		if req.Query != "sleep spindles" {
			t.Fatalf("expected loop query sleep spindles, got %q", req.Query)
		}
		return nil, errors.New("loop execution failed")
	}

	gw := &wisdev.AgentGateway{
		Loop: wisdev.NewAutonomousLoop(nil, nil),
	}

	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/research/deep", strings.NewReader(`{
		"query":"sleep spindles",
		"categories":["sleep"],
		"userId":"u1",
		"projectId":"p1",
		"sessionId":"s1"
	}`))
	req = withTestUserID(req, "u1")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected deep research runtime failure, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "wisdev deep research loop failed") {
		t.Fatalf("expected terminal deep runtime failure, got %s", rec.Body.String())
	}
}

func TestDeepResearchRouteClassifiesUnifiedLoopRateLimit(t *testing.T) {
	originalRetrieveCanonicalPapers := wisdev.RetrieveCanonicalPapers
	originalRunUnifiedResearchLoop := runUnifiedResearchLoop
	t.Cleanup(func() {
		wisdev.RetrieveCanonicalPapers = originalRetrieveCanonicalPapers
		runUnifiedResearchLoop = originalRunUnifiedResearchLoop
	})

	wisdev.RetrieveCanonicalPapers = func(_ context.Context, _ redis.UniversalClient, query string, limit int) ([]wisdev.Source, map[string]any, error) {
		return []wisdev.Source{{ID: "fallback-p1", Title: "Fallback paper", Source: "crossref"}}, nil, nil
	}
	runUnifiedResearchLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, _ wisdev.ResearchExecutionPlane, _ wisdev.LoopRequest, _ func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error) {
		return nil, errors.New("generate structured content failed: Error 429, Message: Resource exhausted")
	}

	gw := &wisdev.AgentGateway{
		Loop: wisdev.NewAutonomousLoop(nil, nil),
	}

	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/research/deep", strings.NewReader(`{
		"query":"sleep spindles",
		"categories":["sleep"],
		"userId":"u1",
		"projectId":"p1",
		"sessionId":"s1"
	}`))
	req = withTestUserID(req, "u1")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected provider rate limit response, got %d body=%s", rec.Code, rec.Body.String())
	}
	var apiErr APIError
	if err := json.NewDecoder(rec.Body).Decode(&apiErr); err != nil {
		t.Fatalf("failed to decode rate limit error: %v", err)
	}
	if apiErr.Error.Code != ErrRateLimit || apiErr.Error.Details["errorKind"] != "rate_limit" || apiErr.Error.Details["retryable"] != true {
		t.Fatalf("expected retryable rate-limit details, got %#v", apiErr.Error)
	}
}

func TestAutonomousResearchRoutePassesPlannedQueriesToUnifiedRuntime(t *testing.T) {
	originalRunUnifiedResearchLoop := runUnifiedResearchLoop
	t.Cleanup(func() {
		runUnifiedResearchLoop = originalRunUnifiedResearchLoop
	})

	seenSeedQueries := make([]string, 0, 4)
	runUnifiedResearchLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, plane wisdev.ResearchExecutionPlane, req wisdev.LoopRequest, _ func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error) {
		if plane != wisdev.ResearchExecutionPlaneAutonomous {
			t.Fatalf("expected autonomous execution plane, got %q", plane)
		}
		seenSeedQueries = append(seenSeedQueries, req.SeedQueries...)
		return testResearchLoopResult(req.Query, append([]string(nil), req.SeedQueries...), nil), nil
	}

	mux := http.NewServeMux()
	registerWisDevRoutesWithTestRuntime(mux)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/research/autonomous", strings.NewReader(`{
		"session":{"sessionId":"s1","correctedQuery":"RLHF reinforcement learning"},
		"plan":{"queries":["seed query"]}
	}`))
	req = withTestUserID(req, "u1")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	payload, ok := body["autonomousResearch"].(map[string]any)
	if !ok {
		t.Fatalf("expected autonomousResearch payload in envelope")
	}
	coverageMap, ok := payload["coverageMap"].(map[string]any)
	if !ok {
		t.Fatalf("expected coverageMap payload")
	}
	if _, ok := coverageMap["seed query"]; !ok {
		t.Fatalf("expected coverageMap to include planned query, got %#v", coverageMap)
	}
	if !containsString(seenSeedQueries, "seed query") {
		t.Fatalf("expected planned query to reach unified runtime, got %#v", seenSeedQueries)
	}
	metadata, ok := payload["metadata"].(map[string]any)
	if !ok || metadata["executionPlane"] != "go_canonical_runtime" {
		t.Fatalf("expected canonical runtime metadata, got %#v", payload["metadata"])
	}
}

func TestAutonomousResearchRouteHonorsExplicitAllowlistForAutonomousAugmentation(t *testing.T) {
	originalRetrieveCanonicalPapers := wisdev.RetrieveCanonicalPapers
	originalProposeAutonomousHypotheses := proposeAutonomousHypotheses
	t.Cleanup(func() {
		wisdev.RetrieveCanonicalPapers = originalRetrieveCanonicalPapers
		proposeAutonomousHypotheses = originalProposeAutonomousHypotheses
	})

	wisdev.RetrieveCanonicalPapers = func(_ context.Context, _ redis.UniversalClient, _ string, _ int) ([]wisdev.Source, map[string]any, error) {
		return []wisdev.Source{
			{ID: "p1", Title: "Sleep consolidation paper", Summary: "A useful paper", Source: "semantic_scholar"},
		}, nil, nil
	}

	programmaticCalls := 0
	hypothesisCalls := 0
	proposeAutonomousHypotheses = func(_ context.Context, _ *wisdev.AgentGateway, _ string) ([]wisdev.Hypothesis, error) {
		hypothesisCalls++
		return []wisdev.Hypothesis{{ID: "h1", Claim: "Should not run", ConfidenceScore: 0.9}}, nil
	}

	registry := search.NewProviderRegistry()
	registry.Register(&MockProvider{
		name: "mock",
		papers: []search.Paper{
			{ID: "p1", Title: "Sleep consolidation paper", Abstract: "A useful paper", Source: "mock"},
		},
	})
	search.ApplyDomainRoutes(registry)

	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Evaluate if the following papers")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": true, "reasoning": "enough", "nextQuery": ""}`}, nil).Maybe()
	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Extract the top 2-3")
	})).Return(&llmv1.StructuredResponse{JsonResult: `[]`}, nil).Maybe()
	mockLLM.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
	})).Return(&llmv1.GenerateResponse{Text: "Final"}, nil).Maybe()
	allowAutonomousCritique(mockLLM)
	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return isAutonomousHypothesisProposalPrompt(req)
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"hypotheses":[{"claim":"Blocked hypothesis","confidenceThreshold":0.82}]}`}, nil).Maybe()

	gw := &wisdev.AgentGateway{
		Store:          wisdev.NewInMemorySessionStore(),
		Registry:       wisdev.NewToolRegistry(),
		SearchRegistry: registry,
		Loop:           wisdev.NewAutonomousLoop(registry, client),
		PythonExecute: func(_ context.Context, action string, payload map[string]any, _ *wisdev.AgentSession) (map[string]any, error) {
			switch action {
			case "research.generateThoughts", "research.queryDecompose":
				programmaticCalls++
			}
			return map[string]any{"confidence": 0.82, "summary": action}, nil
		},
	}

	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/research/autonomous", strings.NewReader(`{
		"session":{"sessionId":"s1","correctedQuery":"sleep and memory"},
		"plan":{"queries":["hippocampal replay"]},
		"enableWisdevTools":true,
		"allowlistedTools":["research.retrievePapers"],
		"requireHumanConfirmation":false
	}`))
	req = withTestUserID(req, "u1")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if programmaticCalls != 0 {
		t.Fatalf("expected programmatic-loop actions to be skipped, got %d calls", programmaticCalls)
	}
	if hypothesisCalls != 0 {
		t.Fatalf("expected hypothesis enrichment to be skipped, got %d calls", hypothesisCalls)
	}
	mockLLM.AssertNotCalled(t, "StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return isAutonomousHypothesisProposalPrompt(req)
	}))
	mockLLM.AssertExpectations(t)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	payload, ok := body["autonomousResearch"].(map[string]any)
	if !ok {
		t.Fatalf("expected autonomousResearch payload in envelope")
	}
	loopMeta, ok := payload["programmaticLoop"].(map[string]any)
	if !ok {
		t.Fatalf("expected programmaticLoop metadata when augmentation is skipped")
	}
	if loopMeta["skipped"] != true {
		t.Fatalf("expected skipped programmaticLoop metadata, got %#v", loopMeta)
	}
	if got := wisdev.AsOptionalString(loopMeta["skipReason"]); got != autonomousActionBlockedNotAllowlisted {
		t.Fatalf("expected skipReason %q, got %#v", autonomousActionBlockedNotAllowlisted, loopMeta["skipReason"])
	}
	reasoningRuntime, ok := payload["reasoningRuntime"].(map[string]any)
	if !ok {
		t.Fatalf("expected reasoningRuntime payload metadata, got %#v", payload["reasoningRuntime"])
	}
	if reasoningRuntime["programmaticTreePlanner"] != false || reasoningRuntime["treeSearchRuntime"] != false {
		t.Fatalf("expected blocked planner and hypothesis policy in reasoningRuntime, got %#v", reasoningRuntime)
	}
	if got := wisdev.AsOptionalString(reasoningRuntime["runtimeMode"]); got != "classic_loop" {
		t.Fatalf("expected classic_loop when planner and hypothesis generation are blocked, got %#v", reasoningRuntime)
	}
	if _, exists := payload["hypotheses"]; exists {
		t.Fatalf("expected hypothesis enrichment to be omitted when policy disallows it, got %#v", payload["hypotheses"])
	}
	reasoningGraph, ok := payload["reasoningGraph"].(map[string]any)
	if !ok {
		t.Fatalf("expected reasoningGraph payload")
	}
	nodes, ok := reasoningGraph["nodes"].([]any)
	if !ok {
		t.Fatalf("expected reasoningGraph nodes, got %#v", reasoningGraph["nodes"])
	}
	for _, raw := range nodes {
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if got := wisdev.AsOptionalString(node["type"]); got == string(wisdev.ReasoningNodeHypothesis) {
			t.Fatalf("expected live loop reasoning graph to omit hypothesis nodes when blocked, got %#v", node)
		}
	}
}

func TestAutonomousResearchRouteGenerateOnlyAllowlistUsesSeedHypothesesWithoutProposeCalls(t *testing.T) {
	originalRetrieveCanonicalPapers := wisdev.RetrieveCanonicalPapers
	originalProposeAutonomousHypotheses := proposeAutonomousHypotheses
	t.Cleanup(func() {
		wisdev.RetrieveCanonicalPapers = originalRetrieveCanonicalPapers
		proposeAutonomousHypotheses = originalProposeAutonomousHypotheses
	})

	wisdev.RetrieveCanonicalPapers = func(_ context.Context, _ redis.UniversalClient, _ string, _ int) ([]wisdev.Source, map[string]any, error) {
		return []wisdev.Source{
			{ID: "p1", Title: "Sleep consolidation paper", Summary: "A useful paper", Source: "semantic_scholar"},
		}, nil, nil
	}

	programmaticCalls := 0
	hypothesisCalls := 0
	proposeAutonomousHypotheses = func(_ context.Context, _ *wisdev.AgentGateway, _ string) ([]wisdev.Hypothesis, error) {
		hypothesisCalls++
		return []wisdev.Hypothesis{{ID: "h1", Claim: "Should not run", ConfidenceScore: 0.9}}, nil
	}

	registry := search.NewProviderRegistry()
	registry.Register(&MockProvider{
		name: "mock",
		papers: []search.Paper{
			{ID: "p1", Title: "Sleep consolidation paper", Abstract: "A useful paper", Source: "mock"},
		},
	})
	search.ApplyDomainRoutes(registry)

	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Evaluate if the following papers")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": true, "reasoning": "enough", "nextQuery": ""}`}, nil).Maybe()
	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Extract the top 2-3")
	})).Return(&llmv1.StructuredResponse{JsonResult: `[]`}, nil).Maybe()
	mockLLM.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
	})).Return(&llmv1.GenerateResponse{Text: "Final"}, nil).Maybe()
	allowAutonomousCritique(mockLLM)
	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return isAutonomousHypothesisProposalPrompt(req)
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"hypotheses":[{"claim":"Blocked hypothesis","confidenceThreshold":0.82}]}`}, nil).Maybe()

	gw := &wisdev.AgentGateway{
		Store:          wisdev.NewInMemorySessionStore(),
		Registry:       wisdev.NewToolRegistry(),
		SearchRegistry: registry,
		Loop:           wisdev.NewAutonomousLoop(registry, client),
		PythonExecute: func(_ context.Context, action string, payload map[string]any, _ *wisdev.AgentSession) (map[string]any, error) {
			switch action {
			case "research.generateThoughts", "research.queryDecompose":
				programmaticCalls++
			}
			return map[string]any{"confidence": 0.82, "summary": action}, nil
		},
	}

	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/research/autonomous", strings.NewReader(`{
		"session":{"sessionId":"s1","correctedQuery":"sleep and memory"},
		"plan":{"queries":["hippocampal replay"]},
		"enableWisdevTools":true,
		"allowlistedTools":["research.retrievePapers","research.generateHypotheses"],
		"requireHumanConfirmation":false
	}`))
	req = withTestUserID(req, "u1")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if programmaticCalls != 0 {
		t.Fatalf("expected programmatic-loop actions to be skipped, got %d calls", programmaticCalls)
	}
	if hypothesisCalls != 0 {
		t.Fatalf("expected propose-based hypothesis enrichment to be skipped, got %d calls", hypothesisCalls)
	}
	mockLLM.AssertNotCalled(t, "StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return isAutonomousHypothesisProposalPrompt(req)
	}))
	mockLLM.AssertExpectations(t)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	payload, ok := body["autonomousResearch"].(map[string]any)
	if !ok {
		t.Fatalf("expected autonomousResearch payload in envelope")
	}
	loopMeta, ok := payload["programmaticLoop"].(map[string]any)
	if !ok {
		t.Fatalf("expected programmaticLoop metadata when augmentation is skipped")
	}
	if loopMeta["skipped"] != true {
		t.Fatalf("expected skipped programmaticLoop metadata, got %#v", loopMeta)
	}
	if got := wisdev.AsOptionalString(loopMeta["skipReason"]); got != autonomousActionBlockedNotAllowlisted {
		t.Fatalf("expected skipReason %q, got %#v", autonomousActionBlockedNotAllowlisted, loopMeta["skipReason"])
	}
	reasoningRuntime, ok := payload["reasoningRuntime"].(map[string]any)
	if !ok {
		t.Fatalf("expected reasoningRuntime payload metadata, got %#v", payload["reasoningRuntime"])
	}
	if reasoningRuntime["programmaticTreePlanner"] != false || reasoningRuntime["treeSearchRuntime"] != false {
		t.Fatalf("expected blocked planner and blocked loop hypothesis policy, got %#v", reasoningRuntime)
	}
	if got := wisdev.AsOptionalString(reasoningRuntime["runtimeMode"]); got != "classic_loop" {
		t.Fatalf("expected classic_loop when planner and loop hypothesis generation are blocked, got %#v", reasoningRuntime)
	}
	hypotheses, ok := payload["hypotheses"].([]any)
	if !ok || len(hypotheses) == 0 {
		t.Fatalf("expected deterministic seed hypothesis payloads, got %#v", payload["hypotheses"])
	}
	claims := make(map[string]struct{}, len(hypotheses))
	for _, raw := range hypotheses {
		hypothesis, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("expected hypothesis payload object, got %#v", raw)
		}
		claims[wisdev.AsOptionalString(hypothesis["claim"])] = struct{}{}
	}
	if _, ok := claims["sleep and memory"]; !ok {
		t.Fatalf("expected seed hypotheses to include original query, got %#v", claims)
	}
	if _, ok := claims["Should not run"]; ok {
		t.Fatalf("expected generate-only policy to avoid proposeAutonomousHypotheses output, got %#v", claims)
	}
	reasoningGraph, ok := payload["reasoningGraph"].(map[string]any)
	if !ok {
		t.Fatalf("expected reasoningGraph payload")
	}
	nodes, ok := reasoningGraph["nodes"].([]any)
	if !ok {
		t.Fatalf("expected reasoningGraph nodes, got %#v", reasoningGraph["nodes"])
	}
	for _, raw := range nodes {
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if got := wisdev.AsOptionalString(node["type"]); got == string(wisdev.ReasoningNodeHypothesis) {
			t.Fatalf("expected live loop reasoning graph to omit hypothesis nodes when propose is not allowlisted, got %#v", node)
		}
	}
}

func TestAutonomousResearchRouteFallsBackToOriginalQuery(t *testing.T) {
	originalRunUnifiedResearchLoop := runUnifiedResearchLoop
	t.Cleanup(func() {
		runUnifiedResearchLoop = originalRunUnifiedResearchLoop
	})

	capturedQuery := ""
	runUnifiedResearchLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, _ wisdev.ResearchExecutionPlane, req wisdev.LoopRequest, _ func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error) {
		capturedQuery = req.Query
		return testResearchLoopResult(req.Query, []string{req.Query}, []search.Paper{{ID: "p1", Title: "Original Query Paper", Source: "crossref"}}), nil
	}

	mux := http.NewServeMux()
	registerWisDevRoutesWithTestRuntime(mux)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/research/autonomous", strings.NewReader(`{
		"session":{"sessionId":"s1","originalQuery":"seed query survives","correctedQuery":"   "}
	}`))
	req = withTestUserID(req, "u1")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if capturedQuery != "seed query survives" {
		t.Fatalf("expected originalQuery fallback, got %q", capturedQuery)
	}
}

func TestAutonomousResearchRouteUsesPlanHintsForCoverageAndHypotheses(t *testing.T) {
	originalRunUnifiedResearchLoop := runUnifiedResearchLoop
	originalProposeAutonomousHypotheses := proposeAutonomousHypotheses
	t.Cleanup(func() {
		runUnifiedResearchLoop = originalRunUnifiedResearchLoop
		proposeAutonomousHypotheses = originalProposeAutonomousHypotheses
	})
	proposeAutonomousHypotheses = func(_ context.Context, _ *wisdev.AgentGateway, query string) ([]wisdev.Hypothesis, error) {
		return []wisdev.Hypothesis{
			{ID: "h1", Claim: "Hypothesis 1 for " + query},
			{ID: "h2", Claim: "Hypothesis 2 for " + query},
			{ID: "h3", Claim: "Hypothesis 3 for " + query},
		}, nil
	}

	runUnifiedResearchLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, _ wisdev.ResearchExecutionPlane, req wisdev.LoopRequest, _ func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error) {
		return testResearchLoopResult(req.Query, []string{"sleep and memory", "hippocampal replay", "systems consolidation"}, []search.Paper{
			{ID: "p1", Title: "Sleep consolidation paper", Source: "crossref"},
			{ID: "p2", Title: "Replay mechanism paper", Source: "crossref"},
			{ID: "p3", Title: "Systems consolidation paper", Source: "crossref"},
		}), nil
	}

	mux := http.NewServeMux()
	registerWisDevRoutesWithTestRuntime(mux)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/research/autonomous", strings.NewReader(`{
		"session":{"sessionId":"s1","correctedQuery":"sleep and memory"},
		"plan":{
			"queries":["hippocampal replay","systems consolidation"],
			"coverageMap":{"mechanism":["hippocampal replay"]}
		}
	}`))
	req = withTestUserID(req, "u1")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	payload, ok := body["autonomousResearch"].(map[string]any)
	if !ok {
		t.Fatalf("expected autonomousResearch payload in envelope")
	}
	coverageMap, ok := payload["coverageMap"].(map[string]any)
	if !ok {
		t.Fatalf("expected coverageMap payload, got %#v", payload["coverageMap"])
	}
	if _, exists := coverageMap["mechanism"]; !exists {
		t.Fatalf("expected plan coverage key to be preserved, got %#v", coverageMap)
	}
	mechanismCoverage, ok := coverageMap["mechanism"].([]any)
	if !ok || len(mechanismCoverage) != 1 {
		t.Fatalf("expected mechanism coverage to include only its mapped paper, got %#v", coverageMap["mechanism"])
	}
	mechanismPaper, ok := mechanismCoverage[0].(map[string]any)
	if !ok {
		t.Fatalf("expected mechanism coverage paper payload, got %#v", mechanismCoverage[0])
	}
	if got := wisdev.AsOptionalString(mechanismPaper["title"]); got != "Replay mechanism paper" {
		t.Fatalf("expected scoped coverage paper, got %q", got)
	}
	hypotheses, ok := payload["hypotheses"].([]any)
	if !ok || len(hypotheses) != 3 {
		t.Fatalf("expected three hypothesis payloads from canonical+planned queries, got %#v", payload["hypotheses"])
	}
}

func TestAutonomousResearchRoutePrefersUnifiedRuntimeWhenInitialized(t *testing.T) {
	originalRunUnifiedResearchLoop := runUnifiedResearchLoop
	t.Cleanup(func() {
		runUnifiedResearchLoop = originalRunUnifiedResearchLoop
	})
	t.Setenv("WISDEV_ALLOW_GO_CITATION_FALLBACK", "true")

	queriesCaptured := make([]string, 0, 3)
	runUnifiedResearchLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, plane wisdev.ResearchExecutionPlane, req wisdev.LoopRequest, _ func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error) {
		if plane != wisdev.ResearchExecutionPlaneAutonomous {
			t.Fatalf("expected autonomous execution plane, got %q", plane)
		}
		queriesCaptured = append(queriesCaptured, req.Query)
		queriesCaptured = append(queriesCaptured, req.SeedQueries...)
		return &wisdev.LoopResult{
			Papers: []search.Paper{
				{
					ID:       "paper-sleep-and-memory",
					Title:    "Replay mechanism paper",
					Abstract: "Evidence for canonical loop execution.",
					Source:   "crossref",
				},
			},
			Iterations:    2,
			Converged:     true,
			QueryCoverage: map[string][]search.Paper{"mechanism": {{ID: "paper-mechanism", Title: "Hippocampal mechanism", Abstract: "Canonical mechanism evidence."}}},
			ExecutedQueries: []string{
				"sleep and memory",
				"hippocampal replay",
				"systems consolidation",
			},
			GapAnalysis: &wisdev.LoopGapState{
				Sufficient:             true,
				Reasoning:              "Sufficient evidence from canonical loop.",
				ObservedSourceFamilies: []string{"crossref"},
				ObservedEvidenceCount:  1,
				Ledger:                 []wisdev.CoverageLedgerEntry{},
				Confidence:             0.89,
				Coverage:               wisdev.LoopCoverageState{PlannedQueryCount: 3, ExecutedQueryCount: 3, CoveredQueryCount: 3, UniquePaperCount: 1},
			},
			MemoryTiers: &wisdev.MemoryTierState{
				ShortTermWorking: []wisdev.MemoryEntry{{ID: "query-model"}},
				LongTermVector:   []wisdev.MemoryEntry{{ID: "base"}},
			},
			Mode:        wisdev.WisDevModeYOLO,
			ServiceTier: wisdev.ServiceTierStandard,
			DraftCritique: &wisdev.LoopDraftCritique{
				NeedsRevision: false,
				Reasoning:     "No revision needed.",
			},
			ReasoningGraph: &wisdev.ReasoningGraph{
				Query: "sleep and memory",
				Nodes: []wisdev.ReasoningNode{{ID: "q0"}},
				Edges: []wisdev.ReasoningEdge{{From: "q0", To: "q0", Label: "supports"}},
			},
			RuntimeState: &wisdev.ResearchSessionState{
				SessionID: "s1",
				Query:     "sleep and memory",
				Plane:     wisdev.ResearchExecutionPlaneAutonomous,
				Budget: &wisdev.ResearchBudgetDecision{
					SearchTermBudget:   5,
					WorkerSearchBudget: 2,
				},
				DurableJob: &wisdev.ResearchDurableJobState{
					JobID:      "auto-job-1",
					SessionID:  "s1",
					Plane:      wisdev.ResearchExecutionPlaneAutonomous,
					Status:     "completed",
					Replayable: true,
				},
				SourceAcquisition: &wisdev.ResearchSourceAcquisitionPlan{
					Attempts: []wisdev.ResearchSourceAcquisitionAttempt{{
						SourceID:    "paper-sleep-and-memory",
						CanonicalID: "doi:10.1000/sleep",
						SourceType:  "doi",
						Status:      "planned",
						WorkerPlane: "go_fetch",
					}},
				},
				StopReason: "promoted",
			},
		}, nil
	}

	gw := &wisdev.AgentGateway{
		Runtime: wisdev.NewUnifiedResearchRuntime(wisdev.NewAutonomousLoop(nil, nil), nil, nil, nil),
	}

	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/research/autonomous", strings.NewReader(`{
		"query":"sleep and memory",
		"plan":{
			"queries":["hippocampal replay","systems consolidation"],
			"coverageMap":{"mechanism":["hippocampal replay"]}
		}
	}`))
	req = withTestUserID(req, "u1")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	payload, ok := body["autonomousResearch"].(map[string]any)
	if !ok {
		t.Fatalf("expected autonomousResearch payload in envelope")
	}
	metadata, ok := payload["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("expected metadata payload, got %#v", payload["metadata"])
	}
	if metadata["executionPlane"] != "go_canonical_runtime" {
		t.Fatalf("expected go_canonical_runtime execution plane, got %#v", metadata["executionPlane"])
	}
	if metadata["runtimeStateAvailable"] != true {
		t.Fatalf("expected runtimeStateAvailable metadata, got %#v", metadata)
	}
	if metadata["durableJobStatus"] != "completed" {
		t.Fatalf("expected durable job status in metadata, got %#v", metadata["durableJobStatus"])
	}
	if metadata["sourceAcquisitionAttempts"] != float64(1) {
		t.Fatalf("expected source acquisition attempt count in metadata, got %#v", metadata["sourceAcquisitionAttempts"])
	}
	if len(queriesCaptured) == 0 {
		t.Fatalf("expected canonical unified loop to be invoked")
	}
	if _, ok := payload["durableJob"].(map[string]any); !ok {
		t.Fatalf("expected durableJob payload from unified runtime state, got %#v", payload["durableJob"])
	}
	sourceAcquisition, ok := payload["sourceAcquisition"].(map[string]any)
	if !ok {
		t.Fatalf("expected sourceAcquisition payload from unified runtime state, got %#v", payload["sourceAcquisition"])
	}
	attempts, ok := sourceAcquisition["attempts"].([]any)
	if !ok || len(attempts) != 1 {
		t.Fatalf("expected source acquisition attempts in payload, got %#v", sourceAcquisition["attempts"])
	}
	coverageMap, ok := payload["coverageMap"].(map[string]any)
	if !ok {
		t.Fatalf("expected coverageMap payload, got %#v", payload["coverageMap"])
	}
	mechanismCoverage, ok := coverageMap["mechanism"].([]any)
	if !ok || len(mechanismCoverage) != 1 {
		t.Fatalf("expected scoped mechanism coverage, got %#v", coverageMap["mechanism"])
	}
	gaps, ok := payload["gaps"].([]any)
	if ok && len(gaps) != 0 {
		t.Fatalf("expected no structured quest gaps in canonical loop path, got %#v", payload["gaps"])
	}
}

func TestAutonomousResearchRoutePersistsUnifiedLoopWorkerTrace(t *testing.T) {
	originalRunUnifiedResearchLoop := runUnifiedResearchLoop
	t.Cleanup(func() {
		runUnifiedResearchLoop = originalRunUnifiedResearchLoop
	})
	t.Setenv("WISDEV_JOURNAL_PATH", t.TempDir()+"\\wisdev_journal.jsonl")

	runUnifiedResearchLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, plane wisdev.ResearchExecutionPlane, req wisdev.LoopRequest, onEvent func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error) {
		if plane != wisdev.ResearchExecutionPlaneAutonomous {
			t.Fatalf("expected autonomous execution plane, got %q", plane)
		}
		if onEvent == nil {
			t.Fatalf("expected route to provide trace event callback")
		}
		onEvent(wisdev.PlanExecutionEvent{
			Type:      wisdev.EventProgress,
			TraceID:   "worker-event-trace",
			SessionID: "",
			Message:   "research worker citation_verifier completed",
			Payload: map[string]any{
				"role":               "citation_verifier",
				"stage":              "completed",
				"executedQueryCount": float64(1),
			},
			Owner:              "go",
			SubAgent:           "citation_verifier",
			OwningComponent:    "wisdev-agent-os/orchestrator/internal/wisdev",
			ResultOrigin:       "research_worker",
			ResultConfidence:   0.78,
			ResultFusionIntent: "blackboard_merge",
			CreatedAt:          wisdev.NowMillis(),
		})
		return &wisdev.LoopResult{
			Papers: []search.Paper{{
				ID:       "paper-sleep-trace",
				Title:    "Sleep trace evidence",
				Abstract: "Evidence for persisted worker traces.",
				Source:   "crossref",
			}},
			Iterations:      1,
			Converged:       true,
			ExecutedQueries: []string{req.Query},
			QueryCoverage: map[string][]search.Paper{
				req.Query: {{ID: "paper-sleep-trace", Title: "Sleep trace evidence", Abstract: "Evidence for persisted worker traces.", Source: "crossref"}},
			},
			GapAnalysis: &wisdev.LoopGapState{
				Sufficient:             true,
				Reasoning:              "Worker trace persisted.",
				ObservedSourceFamilies: []string{"crossref"},
				ObservedEvidenceCount:  1,
				Ledger:                 []wisdev.CoverageLedgerEntry{},
				Confidence:             0.88,
				Coverage:               wisdev.LoopCoverageState{PlannedQueryCount: 1, ExecutedQueryCount: 1, CoveredQueryCount: 1, UniquePaperCount: 1},
			},
			MemoryTiers: &wisdev.MemoryTierState{},
			Mode:        wisdev.WisDevModeGuided,
			ServiceTier: wisdev.ServiceTierStandard,
			ReasoningGraph: &wisdev.ReasoningGraph{
				Query: req.Query,
				Nodes: []wisdev.ReasoningNode{{ID: "root"}},
			},
		}, nil
	}

	gw := &wisdev.AgentGateway{
		Runtime: wisdev.NewUnifiedResearchRuntime(wisdev.NewAutonomousLoop(nil, nil), nil, nil, nil),
		Journal: wisdev.NewRuntimeJournal(nil),
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/research/autonomous", strings.NewReader(`{
		"session":{"sessionId":"trace-session","correctedQuery":"sleep and memory"},
		"plan":{"queries":["hippocampal replay"]}
	}`))
	req = withTestUserID(req, "u1")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	entries := gw.Journal.ReadSession("trace-session", 20)
	if len(entries) == 0 {
		t.Fatalf("expected persisted worker trace entries")
	}
	var workerEntry *wisdev.RuntimeJournalEntry
	for idx := range entries {
		if entries[idx].EventType == string(wisdev.EventProgress) && wisdev.AsOptionalString(entries[idx].Payload["role"]) == "citation_verifier" {
			workerEntry = &entries[idx]
			break
		}
	}
	if workerEntry == nil {
		t.Fatalf("expected citation verifier worker trace entry, got %#v", entries)
	}
	if workerEntry.TraceID != "worker-event-trace" {
		t.Fatalf("expected worker trace id to be preserved, got %q", workerEntry.TraceID)
	}
	if got := wisdev.AsOptionalString(workerEntry.Payload["resultOrigin"]); got != "research_worker" {
		t.Fatalf("expected research_worker result origin, got %q", got)
	}
	if got := wisdev.AsOptionalString(workerEntry.Payload["resultFusionIntent"]); got != "blackboard_merge" {
		t.Fatalf("expected blackboard merge intent, got %q", got)
	}
	if workerEntry.UserID != "u1" {
		t.Fatalf("expected authorized user id on trace entry, got %q", workerEntry.UserID)
	}
}

func TestBuildAutonomousCoveragePayloadScopesArtifactsToMappedQueries(t *testing.T) {
	coverage := buildAutonomousCoveragePayload(
		map[string][]string{"mechanism": {"hippocampal replay"}},
		[]string{"sleep and memory", "hippocampal replay", "systems consolidation"},
		map[string][]map[string]any{
			"sleep and memory":      {{"id": "p1", "title": "Sleep consolidation paper"}},
			"hippocampal replay":    {{"id": "p2", "title": "Replay mechanism paper"}},
			"systems consolidation": {{"id": "p3", "title": "Systems consolidation paper"}},
		},
	)

	mechanism, ok := coverage["mechanism"].([]map[string]any)
	if !ok || len(mechanism) != 1 {
		t.Fatalf("expected scoped mechanism coverage, got %#v", coverage["mechanism"])
	}
	if got := wisdev.AsOptionalString(mechanism[0]["title"]); got != "Replay mechanism paper" {
		t.Fatalf("expected mapped query paper, got %q", got)
	}
}

func TestAutonomousResearchRouteRequiresUnifiedRuntimeForPlannedQueries(t *testing.T) {
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/research/autonomous", strings.NewReader(`{
		"session":{"sessionId":"s1","correctedQuery":"sleep and memory"},
		"plan":{"queries":["hippocampal replay"],"coverageMap":{"mechanism":["hippocampal replay"]}}
	}`))
	req = withTestUserID(req, "u1")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected unified runtime requirement failure, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "wisdev unified runtime is required for autonomous research") {
		t.Fatalf("expected unified runtime requirement error, got %s", rec.Body.String())
	}
}

func TestAutonomousResearchRouteLoopUsesProfileSearchBudget(t *testing.T) {
	searchLimits := make([]int, 0, 1)
	budgetProvider := &mockSearchProvider{
		name: "route_budget",
		SearchFunc: func(_ context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
			searchLimits = append(searchLimits, opts.Limit)
			if !strings.Contains(query, "sleep and memory") {
				t.Fatalf("expected canonical query family, got %q", query)
			}
			papers := make([]search.Paper, 0, opts.Limit)
			for i := 0; i < opts.Limit; i++ {
				papers = append(papers, search.Paper{
					ID:       fmt.Sprintf("paper-%d", i+1),
					Title:    fmt.Sprintf("Paper %d", i+1),
					Abstract: "A",
				})
			}
			return papers, nil
		},
	}
	reg := search.NewProviderRegistry()
	reg.Register(budgetProvider)
	reg.SetDefaultOrder([]string{"route_budget"})

	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	failAutonomousHypothesisProposals(msc, errors.New("proposal unavailable"))
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Evaluate if the following papers")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": true, "reasoning": "enough", "nextQuery": ""}`}, nil).Maybe()
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Extract the top 2-3")
	})).Return(&llmv1.StructuredResponse{JsonResult: `[]`}, nil).Maybe()
	msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
	})).Return(&llmv1.GenerateResponse{Text: "Final"}, nil).Maybe()

	gw := &wisdev.AgentGateway{
		Loop: wisdev.NewAutonomousLoop(reg, lc),
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/research/autonomous", strings.NewReader(`{
		"session":{"sessionId":"s1","correctedQuery":"sleep and memory"},
		"maxIterations":1
	}`))
	req = withTestUserID(req, "u1")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if len(searchLimits) == 0 {
		t.Fatalf("expected loop path to search with profile hitsPerSearch=12")
	}
	for _, limit := range searchLimits {
		if limit != 12 {
			t.Fatalf("expected every canonical loop search to use profile hitsPerSearch=12, got %#v", searchLimits)
		}
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	payload, ok := body["autonomousResearch"].(map[string]any)
	if !ok {
		t.Fatalf("expected autonomousResearch payload in envelope")
	}
	papers, ok := payload["papers"].([]any)
	if !ok || len(papers) != 12 {
		t.Fatalf("expected loop response to include 12 papers from profile budget, got %#v", payload["papers"])
	}
}

func TestAutonomousResearchRouteLoopUsesProfileSearchTermCap(t *testing.T) {
	var searchQueriesMu sync.Mutex
	searchQueries := make([]string, 0, 4)
	cappedProvider := &mockSearchProvider{
		name: "route_search_term_cap",
		SearchFunc: func(_ context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
			searchQueriesMu.Lock()
			searchQueries = append(searchQueries, query)
			searchQueriesMu.Unlock()
			return []search.Paper{
				{
					ID:       fmt.Sprintf("paper-%s", strings.ReplaceAll(query, " ", "-")),
					Title:    query,
					Abstract: "A",
				},
			}, nil
		},
	}
	reg := search.NewProviderRegistry()
	reg.Register(cappedProvider)
	reg.SetDefaultOrder([]string{"route_search_term_cap"})

	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	failAutonomousHypothesisProposals(msc, errors.New("proposal unavailable"))
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Evaluate if the following papers")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": false, "reasoning": "need more", "nextQuery": "refine"}`}, nil).Maybe()
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Extract the top 2-3")
	})).Return(&llmv1.StructuredResponse{JsonResult: `[]`}, nil).Maybe()
	msc.On("StructuredOutput", mock.Anything, mock.Anything).Return(&llmv1.StructuredResponse{JsonResult: `{}`}, nil).Maybe()
	msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
	})).Return(&llmv1.GenerateResponse{Text: "Final"}, nil).Maybe()

	gw := &wisdev.AgentGateway{
		Loop: wisdev.NewAutonomousLoop(reg, lc),
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/research/autonomous", strings.NewReader(`{
		"session":{"sessionId":"s1","correctedQuery":"sleep and memory"},
		"plan":{"queries":["hippocampal replay","systems consolidation","slow-wave sleep","memory reactivation","REM sleep","circadian rhythm","memory consolidation mechanisms"]},
		"maxIterations":8
	}`))
	req = withTestUserID(req, "u1")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	searchQueriesMu.Lock()
	capturedSearchQueries := append([]string(nil), searchQueries...)
	searchQueriesMu.Unlock()
	if len(capturedSearchQueries) > 25 {
		t.Fatalf("expected loop path to stay within profile plus verifier follow-up budget, got %#v", capturedSearchQueries)
	}
	for _, expected := range []string{"sleep and memory", "hippocampal replay", "systems consolidation", "slow-wave sleep"} {
		if !containsString(capturedSearchQueries, expected) {
			t.Fatalf("expected loop path to include %q within expanded profile budget, got %#v", expected, capturedSearchQueries)
		}
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	payload, ok := body["autonomousResearch"].(map[string]any)
	if !ok {
		t.Fatalf("expected autonomousResearch payload in envelope")
	}
	coverageMap, ok := payload["coverageMap"].(map[string]any)
	if !ok {
		t.Fatalf("expected coverageMap payload, got %#v", payload["coverageMap"])
	}
	if _, exists := coverageMap["slow-wave sleep"]; !exists {
		t.Fatalf("expected expanded loop path to include slow-wave sleep coverage, got %#v", coverageMap)
	}
}

func TestAutonomousResearchRouteUsesUnifiedRuntimeProfileSearchTermCap(t *testing.T) {
	originalRunUnifiedResearchLoop := runUnifiedResearchLoop
	t.Cleanup(func() {
		runUnifiedResearchLoop = originalRunUnifiedResearchLoop
	})

	seenQueries := make([]string, 0, 5)
	runUnifiedResearchLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, _ wisdev.ResearchExecutionPlane, req wisdev.LoopRequest, _ func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error) {
		seenQueries = append(seenQueries, req.SeedQueries...)
		return testResearchLoopResult(req.Query, req.SeedQueries, []search.Paper{
			{ID: "sleep and memory", Title: "sleep and memory", Source: "crossref"},
			{ID: "hippocampal replay", Title: "hippocampal replay", Source: "crossref"},
			{ID: "systems consolidation", Title: "systems consolidation", Source: "crossref"},
			{ID: "slow-wave sleep", Title: "slow-wave sleep", Source: "crossref"},
			{ID: "memory reactivation", Title: "memory reactivation", Source: "crossref"},
		}), nil
	}

	mux := http.NewServeMux()
	registerWisDevRoutesWithTestRuntime(mux)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/research/autonomous", strings.NewReader(`{
		"enableWisdevTools":true,
		"allowlistedTools":["research.generateHypotheses"],
		"requireHumanConfirmation":false,
		"session":{"sessionId":"s1","correctedQuery":"sleep and memory"},
		"plan":{"queries":["hippocampal replay","systems consolidation","slow-wave sleep","memory reactivation"]},
		"maxIterations":5
	}`))
	req = withTestUserID(req, "u1")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	expectedQueries := []string{"sleep and memory", "hippocampal replay", "systems consolidation", "slow-wave sleep", "memory reactivation"}
	sort.Strings(seenQueries)
	sortedExpectedQueries := append([]string(nil), expectedQueries...)
	sort.Strings(sortedExpectedQueries)
	if !reflect.DeepEqual(seenQueries, sortedExpectedQueries) {
		t.Fatalf("expected unified route to pass balanced profile execution set, got %#v", seenQueries)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	payload, ok := body["autonomousResearch"].(map[string]any)
	if !ok {
		t.Fatalf("expected autonomousResearch payload in envelope")
	}
	coverageMap, ok := payload["coverageMap"].(map[string]any)
	if !ok {
		t.Fatalf("expected coverageMap payload, got %#v", payload["coverageMap"])
	}
	if _, exists := coverageMap["REM sleep"]; exists {
		t.Fatalf("expected capped unified route to omit unscheduled query coverage, got %#v", coverageMap)
	}
	hypotheses, ok := payload["hypotheses"].([]any)
	if !ok || len(hypotheses) != 5 {
		t.Fatalf("expected hypotheses only for executed unified queries, got %#v", payload["hypotheses"])
	}
}

func TestAutonomousResearchRouteRequiresSessionOwnerWhenPersistedSessionExists(t *testing.T) {
	gw := &wisdev.AgentGateway{
		Store:      wisdev.NewInMemorySessionStore(),
		SessionTTL: time.Hour,
	}
	session, err := gw.CreateSession(context.Background(), "u1", "sleep and memory")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/research/autonomous", strings.NewReader(`{
		"session":{"sessionId":"`+session.SessionID+`","correctedQuery":"sleep and memory"}
	}`))
	req = withTestUserID(req, "u2")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}

	var body APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body.Error.Code != ErrUnauthorized {
		t.Fatalf("expected %s, got %s", ErrUnauthorized, body.Error.Code)
	}
}

func TestBuildAutonomousHypothesisPayloadsFromLoop(t *testing.T) {
	result := &wisdev.LoopResult{
		ReasoningGraph: &wisdev.ReasoningGraph{
			Query: "rlhf alignment",
			Nodes: []wisdev.ReasoningNode{
				{
					ID:         "hyp-1",
					Type:       wisdev.ReasoningNodeHypothesis,
					Label:      "Reward calibration improves evaluator robustness",
					Confidence: 0.84,
					SourceIDs:  []string{"paper-1"},
					Metadata: map[string]any{
						"evidenceIds": []string{"ev-1"},
					},
				},
			},
		},
		Evidence: []wisdev.EvidenceFinding{
			{
				ID:         "ev-1",
				Claim:      "Calibration reduces false positives.",
				PaperTitle: "Paper One",
				SourceID:   "paper-1",
				Confidence: 0.81,
			},
		},
	}

	payloads := buildAutonomousHypothesisPayloadsFromLoop("rlhf alignment", result)
	if len(payloads) != 1 {
		t.Fatalf("expected one hypothesis payload, got %d", len(payloads))
	}
	if got := wisdev.AsOptionalString(payloads[0]["claim"]); got != "Reward calibration improves evaluator robustness" {
		t.Fatalf("unexpected hypothesis claim %q", got)
	}
	evidence, ok := payloads[0]["evidence"].([]map[string]any)
	if !ok || len(evidence) != 1 {
		t.Fatalf("expected serialized evidence payload, got %#v", payloads[0]["evidence"])
	}
	if got := wisdev.AsOptionalString(evidence[0]["paperTitle"]); got != "Paper One" {
		t.Fatalf("expected supporting paper title, got %q", got)
	}
}

func TestBuildAutonomousHypothesisPayloadsPrefersLoopClaimsOverSeedFallback(t *testing.T) {
	result := &wisdev.LoopResult{
		ReasoningGraph: &wisdev.ReasoningGraph{
			Query: "sleep and memory",
			Nodes: []wisdev.ReasoningNode{
				{
					ID:         "hyp-1",
					Type:       wisdev.ReasoningNodeHypothesis,
					Label:      "Replay stabilizes overnight consolidation",
					Confidence: 0.79,
				},
			},
		},
	}

	defaultPolicy := resolveAutonomousExecutionPolicy(nil, "guided", nil, nil, nil)
	payloads := buildAutonomousHypothesisPayloads(context.Background(), nil, "sleep and memory", []string{"hippocampal replay", "systems consolidation"}, result, defaultPolicy)
	if len(payloads) != 1 {
		t.Fatalf("expected loop hypotheses to suppress seed fallback, got %d payloads", len(payloads))
	}
	if got := wisdev.AsOptionalString(payloads[0]["claim"]); got != "Replay stabilizes overnight consolidation" {
		t.Fatalf("unexpected hypothesis claim %q", got)
	}
}

func TestBuildAutonomousHypothesisPayloadsSuppressesLoopClaimsWhenPolicyDisallowsHypotheses(t *testing.T) {
	result := &wisdev.LoopResult{
		ReasoningGraph: &wisdev.ReasoningGraph{
			Query: "sleep and memory",
			Nodes: []wisdev.ReasoningNode{
				{
					ID:         "hyp-1",
					Type:       wisdev.ReasoningNodeHypothesis,
					Label:      "Replay stabilizes overnight consolidation",
					Confidence: 0.79,
				},
			},
		},
	}

	enabled := true
	requireConfirmation := false
	blockedPolicy := resolveAutonomousExecutionPolicy(
		nil,
		"guided",
		&enabled,
		[]string{wisdev.ActionResearchRetrievePapers},
		&requireConfirmation,
	)
	payloads := buildAutonomousHypothesisPayloads(context.Background(), nil, "sleep and memory", []string{"hippocampal replay"}, result, blockedPolicy)
	if len(payloads) != 0 {
		t.Fatalf("expected blocked policy to suppress loop-derived hypotheses, got %#v", payloads)
	}
}

func TestBuildAutonomousHypothesisPayloadsGenerateOnlyUsesSeedFallback(t *testing.T) {
	result := &wisdev.LoopResult{
		ReasoningGraph: &wisdev.ReasoningGraph{
			Query: "sleep and memory",
			Nodes: []wisdev.ReasoningNode{
				{
					ID:         "hyp-1",
					Type:       wisdev.ReasoningNodeHypothesis,
					Label:      "Replay stabilizes overnight consolidation",
					Confidence: 0.79,
				},
			},
		},
	}

	enabled := true
	requireConfirmation := false
	generateOnlyPolicy := resolveAutonomousExecutionPolicy(
		nil,
		"guided",
		&enabled,
		[]string{wisdev.ActionResearchGenerateHypotheses},
		&requireConfirmation,
	)
	payloads := buildAutonomousHypothesisPayloads(context.Background(), nil, "sleep and memory", []string{"hippocampal replay"}, result, generateOnlyPolicy)
	if len(payloads) != 1 {
		t.Fatalf("expected one generated seed hypothesis, got %#v", payloads)
	}
	if got := wisdev.AsOptionalString(payloads[0]["claim"]); got != "hippocampal replay" {
		t.Fatalf("expected generate-only policy to fall back to seed hypothesis, got %q", got)
	}
}

func TestBuildAutonomousHypothesisPayloadsFromLoopSkipsBlankClaims(t *testing.T) {
	result := &wisdev.LoopResult{
		ReasoningGraph: &wisdev.ReasoningGraph{
			Query: "sleep and memory",
			Nodes: []wisdev.ReasoningNode{
				{
					ID:   "hyp-blank",
					Type: wisdev.ReasoningNodeHypothesis,
				},
				{
					ID:   "evidence-1",
					Type: wisdev.ReasoningNodeEvidence,
				},
			},
		},
	}

	if payloads := buildAutonomousHypothesisPayloadsFromLoop("sleep and memory", result); len(payloads) != 0 {
		t.Fatalf("expected blank hypothesis claims to be skipped, got %#v", payloads)
	}
}

func TestBuildAutonomousGapPayloads(t *testing.T) {
	payloads := buildAutonomousGapPayloads(map[string]any{
		"gaps": []any{"Need stronger longitudinal replication"},
	})
	if len(payloads) != 1 {
		t.Fatalf("expected one gap payload, got %d", len(payloads))
	}
	if got := wisdev.AsOptionalString(payloads[0]["description"]); got != "Need stronger longitudinal replication" {
		t.Fatalf("unexpected gap description %q", got)
	}
	if got := wisdev.AsOptionalString(payloads[0]["type"]); got != "theoretical" {
		t.Fatalf("expected normalized gap type, got %q", got)
	}
}

func TestMapStatsCoversKnownProviders(t *testing.T) {
	result := mapStats(map[string]int{
		"semantic_scholar": 2,
		"openalex":         3,
		"pubmed":           5,
		"core":             7,
		"arxiv":            11,
		"biorxiv":          13,
		"medrxiv":          17,
		"europe_pmc":       19,
		"crossref":         23,
		"dblp":             29,
		"ieee":             31,
		"nasa_ads":         37,
	})

	if result.SemanticScholar != 2 {
		t.Fatalf("expected semanticScholar 2, got %d", result.SemanticScholar)
	}
	if result.OpenAlex != 3 {
		t.Fatalf("expected openAlex 3, got %d", result.OpenAlex)
	}
	if result.PubMed != 5 {
		t.Fatalf("expected pubmed 5, got %d", result.PubMed)
	}
	if result.CORE != 7 {
		t.Fatalf("expected core 7, got %d", result.CORE)
	}
	if result.ArXiv != 11 {
		t.Fatalf("expected arXiv 11, got %d", result.ArXiv)
	}
	if result.BioRxiv != 30 {
		t.Fatalf("expected BioRxiv 30, got %d", result.BioRxiv)
	}
	if result.EuropePMC != 19 {
		t.Fatalf("expected europePMC 19, got %d", result.EuropePMC)
	}
	if result.CrossRef != 23 {
		t.Fatalf("expected crossRef 23, got %d", result.CrossRef)
	}
	if result.DBLP != 29 {
		t.Fatalf("expected dbLP 29, got %d", result.DBLP)
	}
	if result.IEEE != 31 {
		t.Fatalf("expected ieee 31, got %d", result.IEEE)
	}
	if result.NASAADS != 37 {
		t.Fatalf("expected nasaAds 37, got %d", result.NASAADS)
	}
}

func TestRunModularParallelSearchFallsBackToDefaultRegistryWhenNil(t *testing.T) {
	originalCanonical := runCanonicalWisdevParallelSearch
	originalGlobal := wisdev.GlobalSearchRegistry
	t.Cleanup(func() {
		runCanonicalWisdevParallelSearch = originalCanonical
		wisdev.GlobalSearchRegistry = originalGlobal
	})

	bridgingRegistry := search.NewProviderRegistry()
	wisdev.GlobalSearchRegistry = bridgingRegistry

	runCanonicalWisdevParallelSearch = func(_ context.Context, _ redis.UniversalClient, query string, opts wisdev.SearchOptions) (*wisdev.MultiSourceResult, error) {
		if query != "default-registry" {
			t.Fatalf("expected query default-registry, got %q", query)
		}
		if opts.Registry != bridgingRegistry {
			t.Fatalf("expected default bridging registry to be injected")
		}
		return &wisdev.MultiSourceResult{
			QueryUsed: "default-registry",
			Sources:   wisdev.SourcesStats{SemanticScholar: 1},
		}, nil
	}

	result, err := runModularParallelSearch(context.Background(), nil, bridgingRegistry, "default-registry", wisdev.SearchOptions{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if result == nil || result.QueryUsed != "default-registry" {
		t.Fatalf("expected queryUsed to be preserved, got %#v", result)
	}
}

func TestRunModularParallelSearchConvertsPanicsToErrors(t *testing.T) {
	originalCanonical := runCanonicalWisdevParallelSearch
	originalGlobal := wisdev.GlobalSearchRegistry
	t.Cleanup(func() {
		runCanonicalWisdevParallelSearch = originalCanonical
		wisdev.GlobalSearchRegistry = originalGlobal
	})

	wisdev.GlobalSearchRegistry = search.NewProviderRegistry()
	runCanonicalWisdevParallelSearch = func(_ context.Context, _ redis.UniversalClient, _ string, _ wisdev.SearchOptions) (*wisdev.MultiSourceResult, error) {
		panic("canonical panic")
	}

	result, err := runModularParallelSearch(context.Background(), nil, search.NewProviderRegistry(), "panic-path", wisdev.SearchOptions{})
	if err == nil {
		t.Fatalf("expected panic recovery error, got nil with result %#v", result)
	}
	if !strings.Contains(err.Error(), "modular parallel search panic") {
		t.Fatalf("unexpected error message: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result payload on panic")
	}
}

func TestWireLegacySearchDoesNotRewriteWisDevSearchCallbacks(t *testing.T) {
	originalParallel := wisdev.ParallelSearch
	originalFast := wisdev.FastParallelSearch
	originalParallelPtr := reflect.ValueOf(originalParallel).Pointer()
	originalFastPtr := reflect.ValueOf(originalFast).Pointer()

	WireLegacySearch(nil)

	if reflect.ValueOf(wisdev.ParallelSearch).Pointer() != originalParallelPtr {
		t.Fatalf("expected WireLegacySearch to leave wisdev.ParallelSearch untouched")
	}
	if reflect.ValueOf(wisdev.FastParallelSearch).Pointer() != originalFastPtr {
		t.Fatalf("expected WireLegacySearch to leave wisdev.FastParallelSearch untouched")
	}
}
