package wisdev

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
	"google.golang.org/grpc"
)

type specialistMockLLMServiceClient struct {
	mock.Mock
}

func (m *specialistMockLLMServiceClient) Generate(ctx context.Context, in *llmpb.GenerateRequest, opts ...grpc.CallOption) (*llmpb.GenerateResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*llmpb.GenerateResponse), args.Error(1)
}

func (m *specialistMockLLMServiceClient) GenerateStream(ctx context.Context, in *llmpb.GenerateRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[llmpb.GenerateChunk], error) {
	args := m.Called(ctx, in)
	return args.Get(0).(grpc.ServerStreamingClient[llmpb.GenerateChunk]), args.Error(1)
}

func (m *specialistMockLLMServiceClient) StructuredOutput(ctx context.Context, in *llmpb.StructuredRequest, opts ...grpc.CallOption) (*llmpb.StructuredResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*llmpb.StructuredResponse), args.Error(1)
}

func (m *specialistMockLLMServiceClient) Embed(ctx context.Context, in *llmpb.EmbedRequest, opts ...grpc.CallOption) (*llmpb.EmbedResponse, error) {
	args := m.Called(ctx, in)
	return args.Get(0).(*llmpb.EmbedResponse), args.Error(1)
}

func (m *specialistMockLLMServiceClient) EmbedBatch(ctx context.Context, in *llmpb.EmbedBatchRequest, opts ...grpc.CallOption) (*llmpb.EmbedBatchResponse, error) {
	args := m.Called(ctx, in)
	return args.Get(0).(*llmpb.EmbedBatchResponse), args.Error(1)
}

func (m *specialistMockLLMServiceClient) Health(ctx context.Context, in *llmpb.HealthRequest, opts ...grpc.CallOption) (*llmpb.HealthResponse, error) {
	args := m.Called(ctx, in)
	return args.Get(0).(*llmpb.HealthResponse), args.Error(1)
}

func TestResearchSpecialist_Execute(t *testing.T) {
	is := assert.New(t)
	msc := &specialistMockLLMServiceClient{}
	client := llm.NewClient()
	client.SetClient(msc)

	spec := NewResearchSpecialist(PersonaHypothesisGenerator, client, "test-model")

	t.Run("success", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmpb.GenerateRequest) bool {
			return req != nil &&
				req.Model == "test-model" &&
				req.RequestClass == "standard" &&
				req.RetryProfile == "standard" &&
				req.ServiceTier == "standard" &&
				req.GetThinkingBudget() == 1024 &&
				req.LatencyBudgetMs > 0
		})).Return(&llmpb.GenerateResponse{Text: " Findings text "}, nil).Once()
		res, err := spec.Execute(context.Background(), "query", []string{"doc1"})
		is.NoError(err)
		is.Equal("Findings text", res.RawOutput)
		is.Equal(PersonaHypothesisGenerator, res.Persona)
		is.Equal(0.8, res.Confidence)
		is.Equal([]string{"Findings text"}, res.Findings)
	})

	t.Run("structured findings and confidence", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmpb.GenerateRequest) bool {
			return req != nil && req.RequestClass == "standard"
		})).Return(&llmpb.GenerateResponse{Text: "Findings:\n1. Sleep improves consolidation\n- Evidence is mixed by age group\nConfidence: 72%"}, nil).Once()
		res, err := spec.Execute(context.Background(), "query", []string{"doc1"})
		is.NoError(err)
		is.Equal(0.72, res.Confidence)
		is.Equal([]string{"Sleep improves consolidation", "Evidence is mixed by age group"}, res.Findings)
	})

	t.Run("failure", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmpb.GenerateRequest) bool {
			return req != nil && req.RequestClass == "standard"
		})).Return(nil, assert.AnError).Once()
		res, err := spec.Execute(context.Background(), "query", nil)
		is.Error(err)
		is.Nil(res)
	})

	t.Run("cooldown error fallback", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmpb.GenerateRequest) bool {
			return req != nil && req.RequestClass == "standard"
		})).Return(nil, errors.New("vertex text generation provider cooldown active; retry after 45s")).Once()
		res, err := spec.Execute(context.Background(), "query", []string{"doc"})
		is.NoError(err)
		is.Equal(PersonaHypothesisGenerator, res.Persona)
		is.Equal(0.55, res.Confidence)
		is.Contains(res.RawOutput, "fallback analysis")
	})

	t.Run("empty output", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmpb.GenerateRequest) bool {
			return req != nil && req.RequestClass == "standard"
		})).Return(&llmpb.GenerateResponse{Text: "   "}, nil).Once()
		res, err := spec.Execute(context.Background(), "query", nil)
		is.Error(err)
		is.Nil(res)
	})

	t.Run("nil client", func(t *testing.T) {
		spec := NewResearchSpecialist(PersonaHypothesisGenerator, nil, "test-model")
		res, err := spec.Execute(context.Background(), "query", nil)
		is.Error(err)
		is.Nil(res)
	})
}

func TestSpecialistAgent_Analyze(t *testing.T) {
	is := assert.New(t)
	msc := &specialistMockLLMServiceClient{}
	client := llm.NewClient()
	client.SetClient(msc)

	finding := EvidenceFinding{PaperTitle: "T", Snippet: "S"}

	t.Run("Methodologist Support", func(t *testing.T) {
		agent := NewMethodologist(client)
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmpb.GenerateRequest) bool {
			return req != nil &&
				req.Model == llm.ResolveLightModel() &&
				req.RequestClass == "light" &&
				req.RetryProfile == "conservative" &&
				req.ServiceTier == "standard" &&
				req.GetThinkingBudget() == 0 &&
				req.LatencyBudgetMs > 0
		})).Return(&llmpb.GenerateResponse{Text: "I support this"}, nil).Once()
		res, err := agent.Analyze(context.Background(), "q", finding)
		is.NoError(err)
		is.Equal(1, res.Verification)
	})

	t.Run("Skeptic Contradict", func(t *testing.T) {
		agent := NewSkeptic(client)
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmpb.GenerateRequest) bool {
			return req != nil && req.RequestClass == "light" && req.GetThinkingBudget() == 0
		})).Return(&llmpb.GenerateResponse{Text: "I contradict this"}, nil).Once()
		res, err := agent.Analyze(context.Background(), "q", finding)
		is.NoError(err)
		is.Equal(-1, res.Verification)
	})

	t.Run("Explicit Negative Verification", func(t *testing.T) {
		agent := NewSkeptic(client)
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmpb.GenerateRequest) bool {
			return req != nil && req.RequestClass == "light" && req.GetThinkingBudget() == 0
		})).Return(&llmpb.GenerateResponse{Text: "Verification: negative\nThe evidence is underpowered."}, nil).Once()
		res, err := agent.Analyze(context.Background(), "q", finding)
		is.NoError(err)
		is.Equal(-1, res.Verification)
	})

	t.Run("Synthesizer Neutral", func(t *testing.T) {
		agent := NewSynthesizer(client)
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmpb.GenerateRequest) bool {
			return req != nil && req.RequestClass == "light" && req.GetThinkingBudget() == 0
		})).Return(&llmpb.GenerateResponse{Text: "Neutral info"}, nil).Once()
		res, err := agent.Analyze(context.Background(), "q", finding)
		is.NoError(err)
		is.Equal(0, res.Verification)
	})

	t.Run("Curator", func(t *testing.T) {
		agent := NewCurator(client)
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmpb.GenerateRequest) bool {
			return req != nil && req.RequestClass == "light" && req.GetThinkingBudget() == 0
		})).Return(&llmpb.GenerateResponse{Text: "Visuals"}, nil).Once()
		res, _ := agent.Analyze(context.Background(), "q", finding)
		is.Equal(SpecialistTypeCurator, res.Type)
	})

	t.Run("Nil client fallback", func(t *testing.T) {
		agent := NewMethodologist(nil)
		res, err := agent.Analyze(context.Background(), "q", finding)
		is.NoError(err)
		is.Contains(res.DeepAnalysis, "analysis: S")
	})

	t.Run("LLM Error", func(t *testing.T) {
		agent := NewMethodologist(client)
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmpb.GenerateRequest) bool {
			return req != nil && req.RequestClass == "light"
		})).Return(nil, assert.AnError).Once()
		_, err := agent.Analyze(context.Background(), "q", finding)
		is.Error(err)
	})

	t.Run("Cooldown Error Fallback", func(t *testing.T) {
		agent := NewMethodologist(client)
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmpb.GenerateRequest) bool {
			return req != nil && req.RequestClass == "light"
		})).Return(nil, errors.New("vertex text generation provider cooldown active; retry after 45s")).Once()
		res, err := agent.Analyze(context.Background(), "q", finding)
		is.NoError(err)
		is.Equal(0, res.Verification)
		is.Contains(res.Reasoning, "Provider cooldown active")
	})

	t.Run("Empty Output", func(t *testing.T) {
		agent := NewMethodologist(client)
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmpb.GenerateRequest) bool {
			return req != nil && req.RequestClass == "light"
		})).Return(&llmpb.GenerateResponse{Text: "   "}, nil).Once()
		_, err := agent.Analyze(context.Background(), "q", finding)
		is.Error(err)
	})
}
