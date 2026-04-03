package search

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"

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

func (m *mockLLMServiceClient) GenerateImages(ctx context.Context, in *llmv1.GenerateImagesRequest, opts ...grpc.CallOption) (*llmv1.GenerateImagesResponse, error) {
    return &llmv1.GenerateImagesResponse{}, nil
}


func TestSelectProvidersDynamic(t *testing.T) {
	r := NewProviderRegistry()
	r.Register(&MockProvider{name: "p1"})
	r.Register(&MockProvider{name: "p2"})

	msc := &mockLLMServiceClient{}
	client := llm.NewClient()
	client.SetClient(msc)

	t.Run("Success JSON", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: `["p1"]`}, nil).Once()
		providers := r.SelectProvidersDynamic(context.Background(), client, "query")
		assert.Len(t, providers, 1)
		assert.Equal(t, "p1", providers[0].Name())
	})

	t.Run("Success Text Fallback", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: `I recommend p2`}, nil).Once()
		providers := r.SelectProvidersDynamic(context.Background(), client, "query")
		assert.Len(t, providers, 1)
		assert.Equal(t, "p2", providers[0].Name())
	})

	t.Run("LLM Error", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.Anything).Return(nil, errors.New("fail")).Once()
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
			{{ID: "1", Title: "P1", CitationCount: 10}},
			{{ID: "1", Title: "P1", CitationCount: 20}},
		}
		out := RRFFuse(lists, 60)
		assert.Len(t, out, 1)
		assert.Equal(t, 20, out[0].CitationCount)
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

	t.Run("Limit exceeded", func(t *testing.T) {
		opts := SearchOpts{Limit: 1}
		reg := NewProviderRegistry()
		reg.Register(&MockProvider{name: "p1", papers: []Paper{{ID: "1"}, {ID: "2"}}})
		res := ParallelSearch(context.Background(), reg, "q", opts)
		assert.Len(t, res.Papers, 1)
	})

	t.Run("System too busy", func(t *testing.T) {
		regBusy := NewProviderRegistry()
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
			cb.RecordResult(errors.New("fail"))
		}
		assert.False(t, cb.Allow())

		res := ParallelSearch(context.Background(), regCB, "q", SearchOpts{})
		assert.NotEmpty(t, res.Warnings)
		assert.Contains(t, res.Warnings[0].Message, "circuit breaker open")
	})
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
	msc := &mockLLMServiceClient{}
	client := llm.NewClient()
	client.SetClient(msc)
	msc.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: `["p1"]`}, nil)

	opts := SearchOpts{
		DynamicProviders: true,
		LLMClient:        client,
	}
	res := ParallelSearch(context.Background(), reg, "q", opts)
	assert.Len(t, res.Papers, 1)
}
