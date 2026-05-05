package search

import (
	"context"
	"errors"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/resilience"
	"strings"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc"
)

type mockLLMServiceClient struct {
	mock.Mock
}

func (m *mockLLMServiceClient) Generate(ctx context.Context, in *llmv1.GenerateRequest, opts ...grpc.CallOption) (*llmv1.GenerateResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*llmv1.GenerateResponse), args.Error(1)
}

func (m *mockLLMServiceClient) GenerateStream(ctx context.Context, in *llmv1.GenerateRequest, opts ...grpc.CallOption) (llmv1.LLMService_GenerateStreamClient, error) {
	args := m.Called(ctx, in)
	return args.Get(0).(llmv1.LLMService_GenerateStreamClient), args.Error(1)
}

func (m *mockLLMServiceClient) StructuredOutput(ctx context.Context, in *llmv1.StructuredRequest, opts ...grpc.CallOption) (*llmv1.StructuredResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*llmv1.StructuredResponse), args.Error(1)
}

func (m *mockLLMServiceClient) Embed(ctx context.Context, in *llmv1.EmbedRequest, opts ...grpc.CallOption) (*llmv1.EmbedResponse, error) {
	args := m.Called(ctx, in)
	return args.Get(0).(*llmv1.EmbedResponse), args.Error(1)
}

func (m *mockLLMServiceClient) EmbedBatch(ctx context.Context, in *llmv1.EmbedBatchRequest, opts ...grpc.CallOption) (*llmv1.EmbedBatchResponse, error) {
	args := m.Called(ctx, in)
	return args.Get(0).(*llmv1.EmbedBatchResponse), args.Error(1)
}

func (m *mockLLMServiceClient) Health(ctx context.Context, in *llmv1.HealthRequest, opts ...grpc.CallOption) (*llmv1.HealthResponse, error) {
	args := m.Called(ctx, in)
	return args.Get(0).(*llmv1.HealthResponse), args.Error(1)
}

func TestSelectProvidersDynamic(t *testing.T) {
	r := NewProviderRegistry()
	r.Register(&MockProvider{name: "p1"})
	r.Register(&MockProvider{name: "p2"})

	msc := &mockLLMServiceClient{}
	client := llm.NewClient()
	client.SetClient(msc)

	t.Run("Success JSON", func(t *testing.T) {
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assert.Contains(t, req.GetPrompt(), searchStructuredOutputSchemaInstruction)
			assert.NotContains(t, req.GetPrompt(), "Return a JSON array")
			return req != nil &&
				req.GetModel() == llm.ResolveStandardModel() &&
				req.GetRequestClass() == "standard" &&
				req.GetRetryProfile() == "standard" &&
				req.GetServiceTier() == "standard" &&
				req.GetThinkingBudget() == 1024 &&
				req.GetLatencyBudgetMs() > 0
		})).Return(&llmv1.StructuredResponse{JsonResult: `["p1"]`}, nil).Once()
		providers := r.SelectProvidersDynamic(context.Background(), client, "query")
		assert.Len(t, providers, 1)
		assert.Equal(t, "p1", providers[0].Name())
	})

	t.Run("Rejects Text Fallback", func(t *testing.T) {
		msc.On("StructuredOutput", mock.Anything, mock.Anything).Return(&llmv1.StructuredResponse{JsonResult: `I recommend p2`}, nil).Once()
		providers := r.SelectProvidersDynamic(context.Background(), client, "query")
		names := make([]string, 0, len(providers))
		for _, provider := range providers {
			names = append(names, provider.Name())
		}
		assert.Len(t, providers, 2)
		assert.ElementsMatch(t, []string{"p1", "p2"}, names)
	})

	t.Run("LLM Error", func(t *testing.T) {
		msc.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, errors.New("fail")).Once()
		providers := r.SelectProvidersDynamic(context.Background(), client, "query")
		assert.NotEmpty(t, providers)
	})

	t.Run("No LLM client", func(t *testing.T) {
		providers := r.SelectProvidersDynamic(context.Background(), nil, "query")
		assert.NotEmpty(t, providers)
	})
}

func TestSetDefaultOrder(t *testing.T) {
	r := NewProviderRegistry()
	r.SetDefaultOrder([]string{"p1", "p2"})
	assert.Equal(t, []string{"p1", "p2"}, r.defaults)
}

func TestRRFFuse_Extra(t *testing.T) {
	t.Run("k <= 0", func(t *testing.T) {
		lists := [][]Paper{{{ID: "1", Title: "P1"}}}
		out := RRFFuse(lists, 0)
		assert.NotEmpty(t, out)
	})

	t.Run("Merge papers", func(t *testing.T) {
		lists := [][]Paper{
			{{ID: "1", Title: "P1", CitationCount: 10, Source: "pubmed", DOI: "10.1/x"}},
			{{ID: "1", Title: "P1", CitationCount: 20, SourceApis: []string{"semantic_scholar"}, Abstract: "abstract", Link: "https://example.com", DOI: "10.1/x", Year: 2024, Venue: "Venue"}},
		}
		out := RRFFuse(lists, 60)
		assert.Len(t, out, 1)
		assert.Equal(t, 20, out[0].CitationCount)
		assert.Equal(t, []string{"pubmed", "semantic_scholar"}, out[0].SourceApis)
		assert.Equal(t, "abstract", out[0].Abstract)
		assert.Equal(t, "https://example.com", out[0].Link)
		assert.Equal(t, "10.1/x", out[0].DOI)
		assert.Equal(t, 2024, out[0].Year)
		assert.Equal(t, "Venue", out[0].Venue)
	})

	t.Run("Scores and tie-breaks", func(t *testing.T) {
		lists := [][]Paper{
			{
				{ID: "a", Title: "A", CitationCount: 5, Source: "semantic_scholar"},
				{ID: "b", Title: "B", CitationCount: 20, Source: "semantic_scholar"},
			},
			{
				{ID: "b", Title: "B", CitationCount: 20, Source: "openalex"},
				{ID: "c", Title: "C", CitationCount: 1, Source: "openalex"},
			},
		}

		out := RRFFuse(lists, 60)
		assert.Len(t, out, 3)
		assert.Equal(t, "b", out[0].ID)
		assert.Greater(t, out[0].Score, out[1].Score)
	})

	t.Run("EqualScoresTieBreakByCitationCount", func(t *testing.T) {
		lists := [][]Paper{
			{{ID: "a", Title: "A", CitationCount: 5, Source: "semantic_scholar"}},
			{{ID: "b", Title: "B", CitationCount: 20, Source: "openalex"}},
		}

		out := RRFFuse(lists, 60)
		assert.Len(t, out, 2)
		assert.Equal(t, "b", out[0].ID)
		assert.Equal(t, "a", out[1].ID)
	})
}

func TestBoostByIntelligence_Extra(t *testing.T) {
	t.Run("No scores returns original slice", func(t *testing.T) {
		papers := []Paper{{ID: "1", Score: 1}}
		out := BoostByIntelligence(papers, nil)
		assert.Equal(t, papers, out)
	})

	t.Run("Boosts by highest matching provider score", func(t *testing.T) {
		papers := []Paper{
			{ID: "1", Score: 1, SourceApis: []string{"semantic_scholar", "openalex"}},
			{ID: "2", Score: 2, SourceApis: []string{"pubmed"}},
		}
		out := BoostByIntelligence(papers, map[string]float64{
			"semantic_scholar": 0.5,
			"openalex":         0.9,
			"pubmed":           0.1,
		})
		assert.Equal(t, "2", out[0].ID)
		assert.InDelta(t, 2.02, out[0].Score, 0.0001)
		assert.Equal(t, "1", out[1].ID)
		assert.InDelta(t, 1.09, out[1].Score, 0.0001)
	})
}

func TestBoostByClicks_Extra(t *testing.T) {
	t.Run("No clicks returns original slice", func(t *testing.T) {
		papers := []Paper{{ID: "1", Score: 1}}
		out := BoostByClicks(papers, nil)
		assert.Equal(t, papers, out)
	})

	t.Run("Boost thresholds", func(t *testing.T) {
		papers := []Paper{
			{ID: "low", Score: 1},
			{ID: "mid", Score: 1},
			{ID: "high", Score: 1},
		}
		out := BoostByClicks(papers, map[string]int{
			"low":  1,
			"mid":  11,
			"high": 51,
		})
		assert.InDelta(t, 1.2, out[0].Score, 0.0001)
		assert.InDelta(t, 1.1, out[1].Score, 0.0001)
		assert.InDelta(t, 1.05, out[2].Score, 0.0001)
	})
}

func TestScoreQuality_Inferred(t *testing.T) {
	papers := []Paper{{Title: "Systematic Review of something"}}
	ScoreQuality(papers)
	assert.Equal(t, "systematic-review", papers[0].EvidenceLevel)
}

func TestParallelSearch_Extra(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&MockProvider{name: "p1", papers: []Paper{{ID: "1"}}})

	t.Run("Empty query short-circuits", func(t *testing.T) {
		res := ParallelSearch(context.Background(), NewProviderRegistry(), "", SearchOpts{})
		assert.NotEmpty(t, res.Warnings)
		assert.Equal(t, "system", res.Warnings[0].Provider)
	})

	t.Run("No providers returns empty result", func(t *testing.T) {
		res := ParallelSearch(context.Background(), NewProviderRegistry(), "q", SearchOpts{})
		assert.Empty(t, res.Papers)
		assert.Empty(t, res.Providers)
	})

	t.Run("Limit exceeded", func(t *testing.T) {
		opts := SearchOpts{Limit: 1}
		reg := NewProviderRegistry()
		reg.Register(&MockProvider{name: "p1", papers: []Paper{{ID: "1"}, {ID: "2"}}})
		res := ParallelSearch(context.Background(), reg, "q", opts)
		assert.Len(t, res.Papers, 1)
	})

	t.Run("System too busy", func(t *testing.T) {
		regBusy := NewProviderRegistry()
		regBusy.Register(&MockProvider{name: "p1", papers: []Paper{{ID: "1"}}})
		regBusy.globalSem = semaphore.NewWeighted(1)
		_ = regBusy.globalSem.Acquire(context.Background(), 1)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()

		res := ParallelSearch(ctx, regBusy, "q", SearchOpts{})
		assert.NotEmpty(t, res.Warnings)
		assert.Equal(t, "system", res.Warnings[0].Provider)
	})

	t.Run("Circuit breaker open", func(t *testing.T) {
		regCB := NewProviderRegistry()
		regCB.Register(&MockProvider{name: "p1"})
		cb := regCB.breakers["p1"]
		for i := 0; i < 6; i++ {
			_ = cb.Call(context.Background(), func(context.Context) error { return errors.New("fail") })
		}
		assert.Equal(t, resilience.StateOpen, cb.State())

		res := ParallelSearch(context.Background(), regCB, "q", SearchOpts{})
		assert.NotEmpty(t, res.Warnings)
		assert.Contains(t, res.Warnings[0].Message, "circuit breaker open")
	})

	t.Run("Requested providers are hard constraints", func(t *testing.T) {
		regRequested := NewProviderRegistry()
		regRequested.Register(&MockProvider{name: "allowed", papers: []Paper{{ID: "allowed-1", Source: "allowed"}}})
		regRequested.Register(&MockProvider{name: "other", papers: []Paper{{ID: "other-1", Source: "other"}}})

		res := ParallelSearch(context.Background(), regRequested, "q", SearchOpts{
			Sources: []string{"allowed"},
		})

		assert.Len(t, res.Papers, 1)
		assert.Equal(t, "allowed-1", res.Papers[0].ID)
		assert.Contains(t, res.Providers, "allowed")
		assert.NotContains(t, res.Providers, "other")
	})

	t.Run("Unavailable requested provider returns warning without broadening", func(t *testing.T) {
		regRequested := NewProviderRegistry()
		healthy := false
		regRequested.Register(&MockProvider{name: "downstream", healthy: &healthy})
		regRequested.Register(&MockProvider{name: "fallback", papers: []Paper{{ID: "fallback-1", Source: "fallback"}}})

		res := ParallelSearch(context.Background(), regRequested, "q", SearchOpts{
			Sources: []string{"downstream"},
		})

		assert.Empty(t, res.Papers)
		assert.Len(t, res.Warnings, 1)
		assert.Equal(t, "downstream", res.Warnings[0].Provider)
		assert.Contains(t, res.Warnings[0].Message, "requested provider")
	})

	t.Run("Quality sort happens before final truncation", func(t *testing.T) {
		regRanked := NewProviderRegistry()
		regRanked.Register(&MockProvider{
			name: "ranked",
			papers: []Paper{
				{ID: "low-citation", Title: "Low citation", CitationCount: 0, Source: "ranked"},
				{ID: "high-citation", Title: "High citation", CitationCount: 10000, Source: "ranked"},
			},
		})

		res := ParallelSearch(context.Background(), regRanked, "q", SearchOpts{
			Limit:       1,
			QualitySort: true,
		})

		assert.Len(t, res.Papers, 1)
		assert.Equal(t, "high-citation", res.Papers[0].ID)
	})

	t.Run("Dynamic providers branch", func(t *testing.T) {
		regDynamic := NewProviderRegistry()
		regDynamic.Register(&MockProvider{name: "dynamic", papers: []Paper{{ID: "dyn-1", Source: "dynamic"}}})
		regDynamic.Register(&MockProvider{name: "fallback", papers: []Paper{{ID: "fallback-1", Source: "fallback"}}})

		msc := &mockLLMServiceClient{}
		client := llm.NewClient()
		client.SetClient(msc)
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return strings.Contains(req.GetPrompt(), searchStructuredOutputSchemaInstruction)
		})).Return(&llmv1.StructuredResponse{JsonResult: `["dynamic"]`}, nil).Once()

		res := ParallelSearch(context.Background(), regDynamic, "q", SearchOpts{
			DynamicProviders: true,
			LLMClient:        client,
			Limit:            1,
		})

		assert.Len(t, res.Papers, 1)
		assert.Equal(t, "dyn-1", res.Papers[0].ID)
	})

	t.Run("Dynamic providers skips LLM when only one provider is available", func(t *testing.T) {
		regSingle := NewProviderRegistry()
		regSingle.Register(&MockProvider{name: "single", papers: []Paper{{ID: "single-1", Source: "single"}}})

		msc := &mockLLMServiceClient{}
		client := llm.NewClient()
		client.SetClient(msc)

		res := ParallelSearch(context.Background(), regSingle, "q", SearchOpts{
			DynamicProviders: true,
			LLMClient:        client,
			Limit:            1,
		})

		assert.Len(t, res.Papers, 1)
		assert.Equal(t, "single-1", res.Papers[0].ID)
		msc.AssertNotCalled(t, "StructuredOutput", mock.Anything, mock.Anything)
	})

	t.Run("Requested sources branch", func(t *testing.T) {
		regRequested := NewProviderRegistry()
		regRequested.Register(&MockProvider{name: "allowed", papers: []Paper{{ID: "allowed-1", Source: "allowed"}}})
		regRequested.Register(&MockProvider{name: "other", papers: []Paper{{ID: "other-1", Source: "other"}}})

		res := ParallelSearch(context.Background(), regRequested, "q", SearchOpts{
			Sources: []string{"allowed"},
		})

		assert.Len(t, res.Papers, 1)
		assert.Equal(t, "allowed-1", res.Papers[0].ID)
		assert.Contains(t, res.Providers, "allowed")
		assert.NotContains(t, res.Providers, "other")
	})

	t.Run("Router branch", func(t *testing.T) {
		regRouter := NewProviderRegistry()
		regRouter.Register(&MockProvider{name: "alpha", domains: []string{"biomedical"}, papers: []Paper{{ID: "alpha-1", Source: "alpha"}}})
		regRouter.Register(&MockProvider{name: "beta", domains: []string{"physics"}, papers: []Paper{{ID: "beta-1", Source: "beta"}}})
		ApplyDomainRoutes(regRouter)
		regRouter.router = NewProviderRouter(nil, regRouter)

		res := ParallelSearch(context.Background(), regRouter, "cancer therapy", SearchOpts{Domain: "biomedical"})

		assert.Len(t, res.Papers, 1)
		assert.Contains(t, []string{"alpha-1", "beta-1"}, res.Papers[0].ID)
	})

	t.Run("No breaker and provider error branch", func(t *testing.T) {
		regErr := NewProviderRegistry()
		regErr.Register(&MockProvider{
			name: "errprov",
			searchFn: func(context.Context, string, SearchOpts) ([]Paper, error) {
				return nil, errors.New("boom")
			},
		})
		delete(regErr.breakers, "errprov")

		res := ParallelSearch(context.Background(), regErr, "q", SearchOpts{})
		assert.Len(t, res.Warnings, 1)
		assert.Contains(t, res.Warnings[0].Message, "boom")
	})
}

func TestStreamParallelSearch_NormalPath(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&MockProvider{name: "stream", papers: []Paper{{ID: "stream-1", Source: "stream"}}})

	out := StreamParallelSearch(context.Background(), reg, "q", SearchOpts{})
	var results []ProviderResult
	for r := range out {
		results = append(results, r)
	}

	assert.Len(t, results, 1)
	assert.Equal(t, "stream", results[0].Provider)
	assert.NoError(t, results[0].Err)
	assert.Len(t, results[0].Papers, 1)
}

func TestStreamParallelSearch_NoBreakerDirectSearch(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&MockProvider{
		name:   "stream",
		papers: []Paper{{ID: "stream-1", Source: "stream"}},
	})
	delete(reg.breakers, "stream")

	out := StreamParallelSearch(context.Background(), reg, "q", SearchOpts{})
	var results []ProviderResult
	for r := range out {
		results = append(results, r)
	}

	assert.Len(t, results, 1)
	assert.Equal(t, "stream", results[0].Provider)
	assert.NoError(t, results[0].Err)
	assert.Len(t, results[0].Papers, 1)
}

func TestStreamParallelSearch_ContextCanceledBeforePublish(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&MockProvider{name: "stream", papers: []Paper{{ID: "stream-1", Source: "stream"}}})
	reg.globalSem = nil

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out := StreamParallelSearch(ctx, reg, "q", SearchOpts{})
	var results []ProviderResult
	for r := range out {
		results = append(results, r)
	}

	assert.Empty(t, results)
}

func TestBaseProvider(t *testing.T) {
	bp := &BaseProvider{}
	assert.True(t, bp.Healthy())
	bp.RecordFailure()
	assert.False(t, bp.Healthy())
	bp.RecordSuccess()
	assert.True(t, bp.Healthy())
	assert.Nil(t, bp.Tools())
}

func TestProviderRouter_FallsBackToDomainRoutingWithoutIntelligence(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&MockProvider{name: "pubmed", domains: []string{"biomedical"}})
	reg.Register(&MockProvider{name: "arxiv", domains: []string{"physics"}})
	reg.Register(&MockProvider{name: "nasa_ads", domains: []string{"physics"}})
	ApplyDomainRoutes(reg)

	router := NewProviderRouter(nil, reg)
	providers := router.Route(context.Background(), "quantum gravity", "")

	names := make([]string, 0, len(providers))
	for _, provider := range providers {
		names = append(names, provider.Name())
	}

	assert.Contains(t, names, "arxiv")
	assert.Contains(t, names, "nasa_ads")
	assert.NotContains(t, names, "pubmed")
}

func TestRegistry_Extra(t *testing.T) {
	r := NewProviderRegistry()
	r.Register(&MockProvider{name: "p1"})

	t.Run("AdjustConcurrency", func(t *testing.T) {
		r.AdjustConcurrency("p1", errors.New("fail"))
		r.AdjustConcurrency("p1", nil)
		r.AdjustConcurrency("unknown", nil)
	})

	t.Run("SetConcurrencyLimit", func(t *testing.T) {
		r.SetConcurrencyLimit("p1", 5)
	})

	t.Run("All", func(t *testing.T) {
		all := r.All()
		assert.Len(t, all, 1)
	})
}

func TestInferEvidenceLevel_Preprints(t *testing.T) {
	tests := []struct {
		source   string
		expected string
	}{
		{"arXiv", "preprint"},
		{"BioRxiv", "preprint"},
		{"medRxiv", "preprint"},
	}
	for _, tt := range tests {
		p := Paper{Source: tt.source}
		assert.Equal(t, tt.expected, InferEvidenceLevel(p))
	}
}

func TestProviderError(t *testing.T) {
	err := providerError("p1", "msg %s", "arg")
	assert.Equal(t, "p1: msg arg", err.Error())
}

func TestParallelSearch_Dynamic(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&MockProvider{name: "p1", papers: []Paper{{ID: "1"}}})
	reg.Register(&MockProvider{name: "p2", papers: []Paper{{ID: "2"}}})
	msc := &mockLLMServiceClient{}
	client := llm.NewClient()
	client.SetClient(msc)
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.GetPrompt(), searchStructuredOutputSchemaInstruction)
	})).Return(&llmv1.StructuredResponse{JsonResult: `["p1"]`}, nil)

	opts := SearchOpts{
		DynamicProviders: true,
		LLMClient:        client,
	}
	res := ParallelSearch(context.Background(), reg, "q", opts)
	assert.Len(t, res.Papers, 1)
}

func TestResolveRequestedProviders_Extra(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&MockProvider{name: "semantic_scholar"})
	reg.Register(&MockProvider{name: "openalex"})
	down := false
	reg.Register(&MockProvider{name: "pubmed", healthy: &down})
	reg.Register(&MockProvider{name: "blocked"})

	cb := reg.breakers["blocked"]
	for i := 0; i < 6; i++ {
		_ = cb.Call(context.Background(), func(context.Context) error { return errors.New("fail") })
	}

	selected, warnings := reg.ResolveRequestedProviders([]string{
		" SemanticScholar ",
		"open-alex",
		"pubmed",
		"blocked",
		"missing",
		"openalex",
		"",
	})

	assert.Len(t, selected, 2)
	assert.Equal(t, "semantic_scholar", selected[0].Name())
	assert.Equal(t, "openalex", selected[1].Name())
	assert.Len(t, warnings, 3)
	assert.Equal(t, "pubmed", warnings[0].Provider)
	assert.Equal(t, "blocked", warnings[1].Provider)
	assert.Equal(t, "missing", warnings[2].Provider)
}

func TestSelectProvidersDynamic_Fallbacks(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&MockProvider{name: "p1"})
	ApplyDomainRoutes(reg)

	msc := &mockLLMServiceClient{}
	client := llm.NewClient()
	client.SetClient(msc)

	t.Run("falls back to general when LLM returns no usable names", func(t *testing.T) {
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return strings.Contains(req.GetPrompt(), searchStructuredOutputSchemaInstruction)
		})).Return(&llmv1.StructuredResponse{JsonResult: "nothing useful"}, nil).Once()
		providers := reg.SelectProvidersDynamic(context.Background(), client, "query")
		assert.Len(t, providers, 1)
		assert.Equal(t, "p1", providers[0].Name())
	})
}

func TestStreamParallelSearch_Extra(t *testing.T) {
	t.Run("streams results from multiple providers", func(t *testing.T) {
		reg := NewProviderRegistry()
		reg.Register(&MockProvider{
			name: "p1",
			papers: []Paper{
				{ID: "1", Title: "P1", Source: "p1"},
			},
		})
		reg.Register(&MockProvider{
			name: "p2",
			searchFn: func(context.Context, string, SearchOpts) ([]Paper, error) {
				return nil, errors.New("boom")
			},
		})
		ApplyDomainRoutes(reg)

		out := StreamParallelSearch(context.Background(), reg, "q", SearchOpts{})
		results := make(map[string]ProviderResult)
		for res := range out {
			results[res.Provider] = res
		}

		assert.Len(t, results, 2)
		assert.Len(t, results["p1"].Papers, 1)
		assert.NoError(t, results["p1"].Err)
		assert.Error(t, results["p2"].Err)
	})

	t.Run("global backpressure returns system busy", func(t *testing.T) {
		reg := NewProviderRegistry()
		reg.Register(&MockProvider{name: "p1", papers: []Paper{{ID: "1"}}})
		ApplyDomainRoutes(reg)
		reg.globalSem = semaphore.NewWeighted(1)
		_ = reg.globalSem.Acquire(context.Background(), 1)

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		out := StreamParallelSearch(ctx, reg, "q", SearchOpts{})
		res, ok := <-out
		assert.True(t, ok)
		assert.Equal(t, "system", res.Provider)
		assert.Error(t, res.Err)
	})
}

func TestProviderRouter_lookupProviders_DefaultsAndFiltering(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&MockProvider{name: "allowed"})
	down := false
	reg.Register(&MockProvider{name: "down", healthy: &down})
	reg.Register(&MockProvider{name: "blocked"})
	reg.SetDefaultOrder([]string{"allowed", "down", "blocked", "allowed"})

	cb := reg.breakers["blocked"]
	for i := 0; i < 6; i++ {
		_ = cb.Call(context.Background(), func(context.Context) error { return errors.New("fail") })
	}

	router := NewProviderRouter(nil, reg)

	t.Run("defaults branch respects health and circuit breaker", func(t *testing.T) {
		providers := router.lookupProviders(nil, "", nil)
		assert.Len(t, providers, 1)
		assert.Equal(t, "allowed", providers[0].Name())
	})

	t.Run("named providers branch enforces domain allowlist and dedupe", func(t *testing.T) {
		allowed := map[string]struct{}{"allowed": {}}
		providers := router.lookupProviders([]string{"down", "allowed", "unknown", "allowed"}, "biomedical", allowed)
		assert.Len(t, providers, 1)
		assert.Equal(t, "allowed", providers[0].Name())
	})
}

func TestProviderRouter_Route_WithIntelligence(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&MockProvider{name: "pubmed", domains: []string{"biomedical"}})
	reg.Register(&MockProvider{name: "arxiv", domains: []string{"cs"}})
	ApplyDomainRoutes(reg)

	db := &spyDB{
		queryPlan: []queryResult{
			{
				rows: &fakeRows{
					values: [][]any{
						{"pubmed", 3},
						{"arxiv", 1},
					},
					index: -1,
				},
			},
			{
				rows: &fakeRows{
					values: [][]any{
						{"pubmed", "cancer treatment", 1.0},
						{"arxiv", "market trend", 0.4},
					},
					index: -1,
				},
			},
		},
	}

	si := NewSearchIntelligence(db)
	router := NewProviderRouter(si, reg)
	providers := router.Route(context.Background(), "cancer gene therapy", "biomedical")

	assert.Len(t, providers, 1)
	assert.Equal(t, "pubmed", providers[0].Name())
}

func TestProviderRouter_Route_IntelligenceNoMatchFallsBackToDomainProviders(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&MockProvider{name: "nasa_ads", domains: []string{"physics"}})
	ApplyDomainRoutes(reg)

	db := &spyDB{
		queryPlan: []queryResult{
			{
				rows: &fakeRows{
					values: [][]any{
						{"unknown", 1},
					},
					index: -1,
				},
			},
			{
				rows: &fakeRows{
					values: [][]any{
						{"unknown", "cancer therapy", 0.2},
					},
					index: -1,
				},
			},
			{
				rows: &fakeRows{
					values: [][]any{},
					index:  -1,
				},
			},
		},
	}
	si := NewSearchIntelligence(db)
	router := NewProviderRouter(si, reg)
	providers := router.Route(context.Background(), "quantum star data", "physics")

	assert.Len(t, providers, 1)
	assert.Equal(t, "nasa_ads", providers[0].Name())
}

func TestProviderRouter_Route_IntelligenceErrorFallsBack(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&MockProvider{name: "pubmed", domains: []string{"biomedical"}})
	ApplyDomainRoutes(reg)

	router := NewProviderRouter(NewSearchIntelligence(nil), reg)
	providers := router.Route(context.Background(), "treatment", "biomedical")

	assert.Len(t, providers, 1)
	assert.Equal(t, "pubmed", providers[0].Name())
}

func TestProviderRouter_Route_NoDomainProvidersFallsBackToDefaults(t *testing.T) {
	reg := NewProviderRegistry()
	router := NewProviderRouter(nil, reg)

	providers := router.Route(context.Background(), "any", "biomedical")

	assert.Len(t, providers, 0)
}

func TestProviderRegistryCoverageMore(t *testing.T) {
	t.Run("nil dynamic helpers", func(t *testing.T) {
		var reg *ProviderRegistry

		names, tools := reg.dynamicProviderCandidates()
		assert.Nil(t, names)
		assert.Nil(t, tools)
		assert.False(t, reg.dynamicProviderSelectionReady())
		assert.Nil(t, reg.dynamicProviderFallback())
	})

	t.Run("SelectProvidersDynamic falls back when LLM returns no usable names", func(t *testing.T) {
		reg := NewProviderRegistry()
		reg.Register(&MockProvider{name: "p1"})
		reg.Register(&MockProvider{name: "p2"})

		msc := &mockLLMServiceClient{}
		client := llm.NewClient()
		client.SetClient(msc)
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return strings.Contains(req.GetPrompt(), searchStructuredOutputSchemaInstruction)
		})).Return(&llmv1.StructuredResponse{JsonResult: "nothing useful"}, nil).Once()

		providers := reg.SelectProvidersDynamic(context.Background(), client, "query")
		assert.Len(t, providers, 2)
		assert.ElementsMatch(t, []string{"p1", "p2"}, []string{providers[0].Name(), providers[1].Name()})
	})

	t.Run("SelectProvidersDynamic falls back when selected providers are unavailable", func(t *testing.T) {
		reg := NewProviderRegistry()
		reg.Register(&MockProvider{name: "good"})
		reg.Register(&MockProvider{name: "other"})

		msc := &mockLLMServiceClient{}
		client := llm.NewClient()
		client.SetClient(msc)
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return strings.Contains(req.GetPrompt(), searchStructuredOutputSchemaInstruction)
		})).Return(&llmv1.StructuredResponse{JsonResult: `["ghost"]`}, nil).Once()

		providers := reg.SelectProvidersDynamic(context.Background(), client, "query")
		assert.Len(t, providers, 2)
		assert.ElementsMatch(t, []string{"good", "other"}, []string{providers[0].Name(), providers[1].Name()})
	})

	t.Run("ParallelSearch respects canceled context before dispatch", func(t *testing.T) {
		reg := NewProviderRegistry()
		reg.Register(&MockProvider{name: "p1"})
		reg.globalSem = nil

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		res := ParallelSearch(ctx, reg, "q", SearchOpts{})
		assert.NotEmpty(t, res.Warnings)
		assert.Equal(t, "system", res.Warnings[0].Provider)
		assert.Contains(t, res.Warnings[0].Message, "canceled")
	})

	t.Run("ParallelSearch returns nil-registry warning", func(t *testing.T) {
		res := ParallelSearch(context.Background(), nil, "q", SearchOpts{})
		assert.NotEmpty(t, res.Warnings)
		assert.Equal(t, "system", res.Warnings[0].Provider)
		assert.Contains(t, res.Warnings[0].Message, "registry")
	})

	t.Run("StreamParallelSearch drops send when context cancels during provider completion", func(t *testing.T) {
		reg := NewProviderRegistry()
		ctx, cancel := context.WithCancel(context.Background())
		reg.Register(&MockProvider{
			name: "stream",
			searchFn: func(context.Context, string, SearchOpts) ([]Paper, error) {
				cancel()
				return []Paper{{ID: "stream-1", Source: "stream"}}, nil
			},
		})

		out := StreamParallelSearch(ctx, reg, "q", SearchOpts{})
		time.Sleep(10 * time.Millisecond)
		var results []ProviderResult
		for res := range out {
			results = append(results, res)
		}

		assert.Empty(t, results)
	})

	t.Run("Deduplicate merges richer metadata from duplicate papers", func(t *testing.T) {
		papers := []Paper{
			{
				ID:    "p1",
				Title: "Deep Learning for Tests",
			},
			{
				ID:                       "p2",
				Title:                    "Deep Learning for Tests",
				Abstract:                 "abstract",
				Link:                     "https://example.com/paper",
				ArxivID:                  "arXiv:1234",
				Source:                   "source2",
				Authors:                  []string{"Ada", "Grace"},
				Year:                     2024,
				Month:                    3,
				Venue:                    "Venue",
				CitationCount:            10,
				ReferenceCount:           5,
				InfluentialCitationCount: 2,
				OpenAccessUrl:            "https://example.com/oa",
				PdfUrl:                   "https://example.com/pdf",
				Score:                    0.9,
				EvidenceLevel:            "systematic-review",
				FullText:                 "full text",
				StructureMap:             []any{"node"},
				Keywords:                 []string{"keyword"},
			},
		}

		deduped := Deduplicate(papers)
		assert.Len(t, deduped, 1)

		merged := deduped[0]
		assert.Equal(t, "abstract", merged.Abstract)
		assert.Equal(t, "https://example.com/paper", merged.Link)
		assert.Equal(t, "arXiv:1234", merged.ArxivID)
		assert.Equal(t, "source2", merged.Source)
		assert.Equal(t, []string{"source2"}, merged.SourceApis)
		assert.Equal(t, []string{"Ada", "Grace"}, merged.Authors)
		assert.Equal(t, 2024, merged.Year)
		assert.Equal(t, 3, merged.Month)
		assert.Equal(t, "Venue", merged.Venue)
		assert.Equal(t, 10, merged.CitationCount)
		assert.Equal(t, 5, merged.ReferenceCount)
		assert.Equal(t, 2, merged.InfluentialCitationCount)
		assert.Equal(t, "https://example.com/oa", merged.OpenAccessUrl)
		assert.Equal(t, "https://example.com/pdf", merged.PdfUrl)
		assert.Equal(t, 0.9, merged.Score)
		assert.Equal(t, "systematic-review", merged.EvidenceLevel)
		assert.Equal(t, "full text", merged.FullText)
		assert.Len(t, merged.StructureMap, 1)
		assert.Equal(t, []string{"keyword"}, merged.Keywords)
	})
}
