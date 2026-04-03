package paper

import (
	"context"
	"errors"
	"testing"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"
)

type mockLLMService struct {
	mock.Mock
}

func (m *mockLLMService) Generate(ctx context.Context, in *llmv1.GenerateRequest, opts ...grpc.CallOption) (*llmv1.GenerateResponse, error) {
	return nil, nil
}
func (m *mockLLMService) GenerateStream(ctx context.Context, in *llmv1.GenerateRequest, opts ...grpc.CallOption) (llmv1.LLMService_GenerateStreamClient, error) {
	return nil, nil
}
func (m *mockLLMService) StructuredOutput(ctx context.Context, in *llmv1.StructuredRequest, opts ...grpc.CallOption) (*llmv1.StructuredResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*llmv1.StructuredResponse), args.Error(1)
}
func (m *mockLLMService) Embed(ctx context.Context, in *llmv1.EmbedRequest, opts ...grpc.CallOption) (*llmv1.EmbedResponse, error) {
	return nil, nil
}
func (m *mockLLMService) EmbedBatch(ctx context.Context, in *llmv1.EmbedBatchRequest, opts ...grpc.CallOption) (*llmv1.EmbedBatchResponse, error) {
	return nil, nil
}
func (m *mockLLMService) Health(ctx context.Context, in *llmv1.HealthRequest, opts ...grpc.CallOption) (*llmv1.HealthResponse, error) {
	return nil, nil
}
func (m *mockLLMService) GenerateImages(ctx context.Context, in *llmv1.GenerateImagesRequest, opts ...grpc.CallOption) (*llmv1.GenerateImagesResponse, error) {
	return &llmv1.GenerateImagesResponse{}, nil
}

func TestProfiler_ExtractProfile(t *testing.T) {
	lc := llm.NewClient()
	msc := &mockLLMService{}
	lc.SetClient(msc)
	p := NewProfiler(lc)
	ctx := context.Background()
	paper := search.Paper{ID: "p1", Title: "Title", Abstract: "Abs", DOI: "10.1/1"}

	t.Run("Success", func(t *testing.T) {
		msc.On("StructuredOutput", mock.Anything, mock.Anything).Return(&llmv1.StructuredResponse{
			JsonResult: `{"summary":"sum","keyFindings":["f1"],"methodology":"meth","methodologicalRigor":"high","sampleSize":"100","limitations":["l1"],"impactScore":0.9,"noveltyScore":0.8}`,
		}, nil).Once()

		profile, err := p.ExtractProfile(ctx, paper)
		assert.NoError(t, err)
		assert.Equal(t, "p1", profile.PaperID)
		assert.Equal(t, "10.1/1", profile.DOI)
		assert.Equal(t, "sum", profile.Summary)
	})

	t.Run("LLM Error", func(t *testing.T) {
		msc.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, errors.New("llm fail")).Once()
		_, err := p.ExtractProfile(ctx, paper)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "llm structured output failed")
	})

	t.Run("Unmarshal Error", func(t *testing.T) {
		msc.On("StructuredOutput", mock.Anything, mock.Anything).Return(&llmv1.StructuredResponse{
			JsonResult: `invalid json`,
		}, nil).Once()
		_, err := p.ExtractProfile(ctx, paper)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode profile json")
	})
}
