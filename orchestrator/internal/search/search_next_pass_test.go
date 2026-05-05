package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/resilience"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/go-redis/redismock/v9"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sync/semaphore"
)

type selectableToolProvider struct {
	name    string
	tools   []string
	healthy bool
	papers  []Paper
}

type stubPageIndexStructuredGenerator struct {
	text   string
	err    error
	model  string
	prompt string
	schema string
}

func (s *stubPageIndexStructuredGenerator) GenerateStructured(ctx context.Context, modelID, prompt, systemPrompt string, jsonSchemaStr string, temperature float32, maxTokens int32) (string, error) {
	s.model = modelID
	s.prompt = prompt
	s.schema = jsonSchemaStr
	return s.text, s.err
}

func (p *selectableToolProvider) Name() string      { return p.name }
func (p *selectableToolProvider) Domains() []string { return []string{"general"} }
func (p *selectableToolProvider) Healthy() bool     { return p.healthy }
func (p *selectableToolProvider) Tools() []string   { return p.tools }
func (p *selectableToolProvider) Search(context.Context, string, SearchOpts) ([]Paper, error) {
	return p.papers, nil
}

func TestHandleToolSearch_NoProviderFound(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&testAuthorSearcher{name: "author-only", tools: []string{"paper_lookup"}})
	reg.Register(&testPaperLookup{name: "paper-only", tools: []string{"author_lookup"}})

	if _, err := HandleToolSearch(context.Background(), reg, "author_lookup", map[string]any{"authorId": "auth-1"}); err == nil {
		t.Fatal("expected no provider found for author lookup")
	}
	if _, err := HandleToolSearch(context.Background(), reg, "paper_lookup", map[string]any{"paperId": "paper-1"}); err == nil {
		t.Fatal("expected no provider found for paper lookup")
	}
}

func TestHandleToolSearch_PaperLookupRetry(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&testPaperLookup{name: "first", tools: []string{"paper_lookup"}, err: io.EOF})
	reg.Register(&testPaperLookup{name: "second", tools: []string{"paper_lookup"}, paper: &Paper{ID: "p2"}})

	res, err := HandleToolSearch(context.Background(), reg, "paper_lookup", map[string]any{"paperId": "paper-2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Papers) != 1 || res.Papers[0].ID != "p2" {
		t.Fatalf("unexpected paper lookup retry result: %+v", res.Papers)
	}
}

func TestSelectProvidersDynamic_ToolMapAndFallback(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&selectableToolProvider{name: "tooly", tools: []string{"author_lookup"}, healthy: true})
	reg.Register(&selectableToolProvider{name: "broken", tools: []string{"author_lookup"}, healthy: false})

	t.Run("tool map is passed to LLM and selected provider is returned", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		client := llm.NewClient()
		client.SetClient(msc)
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			if !strings.Contains(req.GetPrompt(), searchStructuredOutputSchemaInstruction) {
				t.Fatalf("expected structured output instruction in prompt, got %q", req.GetPrompt())
			}
			return req != nil &&
				req.GetModel() == llm.ResolveStandardModel() &&
				req.GetRequestClass() == "standard" &&
				req.GetRetryProfile() == "standard" &&
				req.GetServiceTier() == "standard" &&
				req.GetThinkingBudget() == 1024 &&
				req.GetLatencyBudgetMs() > 0
		})).Return(&llmv1.StructuredResponse{JsonResult: `["tooly"]`}, nil).Once()
		providers := reg.SelectProvidersDynamic(context.Background(), client, "search query")
		if len(providers) != 1 || providers[0].Name() != "tooly" {
			t.Fatalf("unexpected providers: %+v", providers)
		}
	})

	t.Run("unhealthy selection falls back to general", func(t *testing.T) {
		msc := &mockLLMServiceClient{}
		client := llm.NewClient()
		client.SetClient(msc)
		msc.On("StructuredOutput", mock.Anything, mock.Anything).Return(&llmv1.StructuredResponse{JsonResult: `["broken"]`}, nil).Once()
		providers := reg.SelectProvidersDynamic(context.Background(), client, "search query")
		if len(providers) == 0 {
			t.Fatal("expected fallback providers")
		}
	})
}

func TestParallelSearchCacheConcurrencyAndIntelligence(t *testing.T) {
	t.Run("cache hit short-circuits provider dispatch", func(t *testing.T) {
		reg := NewProviderRegistry()
		reg.Register(&MockProvider{
			name: "alpha",
			searchFn: func(context.Context, string, SearchOpts) ([]Paper, error) {
				t.Fatal("provider search should not run on cache hit")
				return nil, nil
			},
		})

		client, mock := redismock.NewClientMock()
		key := getCacheKey("cached query", SearchOpts{Domain: "general", Limit: 1})
		payload, err := json.Marshal(SearchResult{
			Papers: []Paper{{ID: "cached"}},
			Providers: map[string]int{
				"cached": 1,
			},
		})
		if err != nil {
			t.Fatalf("marshal cache payload: %v", err)
		}
		mock.ExpectGet(key).SetVal(string(payload))
		reg.SetRedis(client)

		res := ParallelSearch(context.Background(), reg, "cached query", SearchOpts{Domain: "general", Limit: 1})
		if !res.Cached || len(res.Papers) != 1 || res.Papers[0].ID != "cached" {
			t.Fatalf("unexpected cached result: %+v", res)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("redis expectations not met: %v", err)
		}
	})

	t.Run("concurrency limit reports warning", func(t *testing.T) {
		reg := NewProviderRegistry()
		reg.Register(&MockProvider{name: "slow", papers: []Paper{{ID: "live"}}})
		ApplyDomainRoutes(reg)
		reg.SetConcurrencyLimit("slow", 0)

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()

		res := ParallelSearch(ctx, reg, "query", SearchOpts{Limit: 1})
		if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0].Message, "concurrency limit reached") {
			t.Fatalf("expected concurrency warning, got %+v", res.Warnings)
		}
	})

	t.Run("intelligence path boosts results", func(t *testing.T) {
		db := &spyDB{
			queryPlan: []queryResult{
				{
					rows: &fakeRows{
						values: [][]any{{"alpha", 1.5}},
						index:  -1,
					},
				},
				{
					rows: &fakeRows{
						values: [][]any{{"query", 4}},
						index:  -1,
					},
				},
			},
		}

		reg := NewProviderRegistry()
		reg.Register(&MockProvider{name: "alpha", papers: []Paper{{ID: "paper-1", Title: "Alpha", Source: "alpha"}}})
		ApplyDomainRoutes(reg)
		reg.intelligence = NewSearchIntelligence(db)

		res := ParallelSearch(context.Background(), reg, "query", SearchOpts{Limit: 1})
		if len(res.Papers) != 1 {
			t.Fatalf("unexpected intelligence result: %+v", res.Papers)
		}
	})
}

func TestParallelSearchSystemBusyBranches(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&MockProvider{name: "alpha"})
	reg.globalSem = semaphore.NewWeighted(0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := ParallelSearch(ctx, reg, "query", SearchOpts{Limit: 1})
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0].Message, "System too busy") {
		t.Fatalf("expected busy warning, got %+v", res.Warnings)
	}

	out := StreamParallelSearch(ctx, reg, "query", SearchOpts{})
	r, ok := <-out
	if !ok || r.Provider != "system" || r.Err == nil {
		t.Fatalf("expected busy stream result, got %+v ok=%v", r, ok)
	}
}

func TestStreamParallelSearch_EmptyRegistry(t *testing.T) {
	out := StreamParallelSearch(context.Background(), NewProviderRegistry(), "query", SearchOpts{})
	if res, ok := <-out; ok || res.Provider != "" {
		t.Fatalf("expected closed empty stream, got %+v ok=%v", res, ok)
	}
}

func TestPageIndexRerankBranches(t *testing.T) {
	t.Run("empty and topK adjustment", func(t *testing.T) {
		if got := PageIndexRerankPapers(context.Background(), "q", nil, 2); got != nil {
			t.Fatalf("expected nil/empty passthrough, got %+v", got)
		}
		if got := PageIndexRerankPapers(context.Background(), "q", []Paper{{Title: "a"}, {Title: "b"}}, 0); len(got) != 2 {
			t.Fatalf("expected topK adjustment to keep all papers, got %+v", got)
		}
	})

	t.Run("invalid ranking indexes are skipped and tail is appended", func(t *testing.T) {
		origGenerator := newPageIndexStructuredGenerator
		t.Cleanup(func() { newPageIndexStructuredGenerator = origGenerator })
		newPageIndexStructuredGenerator = func(context.Context) (pageIndexStructuredGenerator, error) {
			return &stubPageIndexStructuredGenerator{
				text: `{"rankings":[{"index":9,"score":99,"reason":"skip"},{"index":1,"score":88,"reason":"keep"}]}`,
			}, nil
		}
		t.Setenv("AI_MODEL_RERANK_ID", "test-model")

		papers := []Paper{
			{Title: "first", Score: 1},
			{Title: "second", Score: 2},
			{Title: "third", Score: 3},
		}
		got := PageIndexRerankPapers(context.Background(), "query", papers, 2)
		if len(got) != 3 {
			t.Fatalf("expected reordered papers plus tail, got %+v", got)
		}
		if got[0].Title != "second" || got[1].Title != "first" || got[2].Title != "third" {
			t.Fatalf("unexpected ranking order: %+v", got)
		}
	})

	t.Run("all invalid rankings return original working set", func(t *testing.T) {
		origGenerator := newPageIndexStructuredGenerator
		t.Cleanup(func() { newPageIndexStructuredGenerator = origGenerator })
		newPageIndexStructuredGenerator = func(context.Context) (pageIndexStructuredGenerator, error) {
			return &stubPageIndexStructuredGenerator{
				text: `{"rankings":[{"index":9,"score":99,"reason":"skip"}]}`,
			}, nil
		}
		t.Setenv("AI_MODEL_RERANK_ID", "test-model")

		papers := []Paper{
			{Title: "first", Score: 1},
			{Title: "second", Score: 2},
		}
		got := PageIndexRerankPapers(context.Background(), "query", papers, 1)
		if len(got) != 2 || got[0].Title != "first" || got[1].Title != "second" {
			t.Fatalf("expected original order when all rankings are invalid, got %+v", got)
		}
	})
}

func TestProviderRouterLookupDefaults(t *testing.T) {
	reg := NewProviderRegistry()
	reg.SetDefaultOrder([]string{"alpha", "beta"})
	alphaHealthy := true
	betaHealthy := false
	reg.Register(&MockProvider{name: "alpha", healthy: &alphaHealthy})
	reg.Register(&MockProvider{name: "beta", healthy: &betaHealthy})

	router := NewProviderRouter(nil, reg)
	got := router.lookupProviders(nil, "", nil)
	if len(got) != 1 || got[0].Name() != "alpha" {
		t.Fatalf("unexpected default provider selection: %+v", got)
	}
}

func TestProviderRouterLookupDomainFilter(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&MockProvider{name: "alpha"})
	reg.Register(&MockProvider{name: "beta"})

	router := NewProviderRouter(nil, reg)
	got := router.lookupProviders([]string{"alpha", "beta"}, "cs", map[string]struct{}{"alpha": {}})
	if len(got) != 1 || got[0].Name() != "alpha" {
		t.Fatalf("unexpected domain-filtered providers: %+v", got)
	}
}

func TestProviderRouterLookupSkipsUnknownAndDuplicates(t *testing.T) {
	reg := NewProviderRegistry()
	betaHealthy := false
	reg.Register(&MockProvider{name: "alpha"})
	reg.Register(&MockProvider{name: "beta", healthy: &betaHealthy})

	router := NewProviderRouter(nil, reg)
	got := router.lookupProviders([]string{"alpha", "alpha", "missing", "beta"}, "", nil)
	if len(got) != 1 || got[0].Name() != "alpha" {
		t.Fatalf("unexpected skipped provider selection: %+v", got)
	}
}

func TestProviderRouterLookupSkipsBlankNames(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&MockProvider{name: "alpha"})
	router := NewProviderRouter(nil, reg)

	got := router.lookupProviders([]string{" ", "\t", "alpha"}, "", nil)
	if len(got) != 1 || got[0].Name() != "alpha" {
		t.Fatalf("unexpected blank-name selection: %+v", got)
	}
}

func TestProviderRouterNilAndBreakerBranches(t *testing.T) {
	var router *ProviderRouter
	if got := router.Route(context.Background(), "query", "general"); got != nil {
		t.Fatalf("expected nil router to return nil, got %+v", got)
	}

	reg := NewProviderRegistry()
	reg.Register(&MockProvider{name: "alpha"})
	reg.SetDefaultOrder([]string{"alpha"})
	cb := resilience.NewCircuitBreaker("alpha")
	for i := 0; i < 3; i++ {
		if err := cb.Call(context.Background(), func(context.Context) error { return io.EOF }); err == nil {
			t.Fatal("expected circuit breaker failure to be returned")
		}
	}
	reg.breakers["alpha"] = cb

	router = NewProviderRouter(nil, reg)
	if got := router.lookupProviders([]string{"alpha"}, "", nil); len(got) != 0 {
		t.Fatalf("expected open breaker to skip provider, got %+v", got)
	}
}

func TestRRFFuseBranches(t *testing.T) {
	t.Run("merge duplicate papers", func(t *testing.T) {
		lists := [][]Paper{
			{
				{ID: "p1", Title: "First", DOI: "10.1/x", Source: "alpha"},
			},
			{
				{ID: "p1b", Title: "First", Abstract: "abstract", DOI: "10.1/x", Link: "https://example.com", Year: 2024, Venue: "Journal", CitationCount: 10, SourceApis: []string{"beta"}},
			},
		}

		got := RRFFuse(lists, 0)
		if len(got) != 1 {
			t.Fatalf("expected one fused paper, got %+v", got)
		}
		paper := got[0]
		if paper.Title != "First" || paper.CitationCount != 10 || paper.DOI != "10.1/x" || paper.Year != 2024 {
			t.Fatalf("unexpected fused metadata: %+v", paper)
		}
		if len(paper.SourceApis) != 2 {
			t.Fatalf("expected merged provenance, got %+v", paper.SourceApis)
		}
	})

	t.Run("tie breaks on citation count", func(t *testing.T) {
		lists := [][]Paper{
			{{ID: "a", Title: "A", DOI: "10.1/a", CitationCount: 5}},
			{{ID: "b", Title: "B", DOI: "10.1/b", CitationCount: 10}},
		}

		got := RRFFuse(lists, 0)
		if len(got) != 2 {
			t.Fatalf("expected two fused papers, got %+v", got)
		}
		if got[0].ID != "b" || got[1].ID != "a" {
			t.Fatalf("expected higher citation count to win tie-break, got %+v", got)
		}
	})
}

func TestPageIndexFetchFailureBranches(t *testing.T) {
	t.Setenv("AI_MODEL_RERANK_ID", "")
	origGenerator := newPageIndexStructuredGenerator
	origMarshal := jsonMarshalFn
	t.Cleanup(func() {
		newPageIndexStructuredGenerator = origGenerator
		jsonMarshalFn = origMarshal
	})

	t.Run("constructor error returns failure", func(t *testing.T) {
		newPageIndexStructuredGenerator = func(context.Context) (pageIndexStructuredGenerator, error) {
			return nil, io.ErrUnexpectedEOF
		}
		rankings, ok := fetchGeminiPageIndexRankings(context.Background(), "query", []Paper{{Title: "a"}}, 1)
		if ok || len(rankings) != 0 {
			t.Fatalf("expected constructor error to fail, got %#v ok=%v", rankings, ok)
		}
	})

	t.Run("empty structured text returns failure", func(t *testing.T) {
		newPageIndexStructuredGenerator = func(context.Context) (pageIndexStructuredGenerator, error) {
			return &stubPageIndexStructuredGenerator{text: ""}, nil
		}
		rankings, ok := fetchGeminiPageIndexRankings(context.Background(), "query", []Paper{{Title: "a"}}, 1)
		if ok || rankings != nil {
			t.Fatalf("expected empty text to fail, got %#v ok=%v", rankings, ok)
		}
	})

	t.Run("generation error returns failure", func(t *testing.T) {
		newPageIndexStructuredGenerator = func(context.Context) (pageIndexStructuredGenerator, error) {
			return &stubPageIndexStructuredGenerator{err: io.ErrUnexpectedEOF}, nil
		}
		rankings, ok := fetchGeminiPageIndexRankings(context.Background(), "query", []Paper{{Title: "a"}}, 1)
		if ok || rankings != nil {
			t.Fatalf("expected generation error to fail, got %#v ok=%v", rankings, ok)
		}
	})

	t.Run("invalid ranking JSON returns failure", func(t *testing.T) {
		newPageIndexStructuredGenerator = func(context.Context) (pageIndexStructuredGenerator, error) {
			return &stubPageIndexStructuredGenerator{text: "not-json"}, nil
		}
		rankings, ok := fetchGeminiPageIndexRankings(context.Background(), "query", []Paper{{Title: "a"}}, 1)
		if ok || rankings != nil {
			t.Fatalf("expected invalid ranking json to fail, got %#v ok=%v", rankings, ok)
		}
	})
}

func TestPageIndexFetchMissingApiKey(t *testing.T) {
	origGenerator := newPageIndexStructuredGenerator
	t.Cleanup(func() { newPageIndexStructuredGenerator = origGenerator })
	newPageIndexStructuredGenerator = func(context.Context) (pageIndexStructuredGenerator, error) {
		return nil, fmt.Errorf("no credentials available")
	}

	rankings, ok := fetchGeminiPageIndexRankings(context.Background(), "query", []Paper{{Title: "a"}}, 1)
	if ok || len(rankings) != 0 {
		t.Fatalf("expected missing structured runtime to fail, got %#v ok=%v", rankings, ok)
	}
}
