package llm

import (
	"context"
	"errors"
	"testing"

	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestModelProvider(t *testing.T) {
	is := assert.New(t)

	msc := &mockLLMServiceClient{}
	client := NewClient()
	client.SetClient(msc)
	provider := NewModelProvider(client)

	ctx := context.Background()

	t.Run("Call with heavy tier", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return req.Model == ResolveHeavyModel()
		})).Return(&llmv1.GenerateResponse{Text: "heavy-resp"}, nil).Once()

		resp, err := provider.Call(ctx, "heavy", "test prompt")
		is.NoError(err)
		is.Equal("heavy-resp", resp)
	})

	t.Run("Call with light tier", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return req.Model == ResolveLightModel()
		})).Return(&llmv1.GenerateResponse{Text: "light-resp"}, nil).Once()

		resp, err := provider.Call(ctx, "light", "test prompt")
		is.NoError(err)
		is.Equal("light-resp", resp)
	})

	t.Run("Call with standard tier", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return req.Model == ResolveStandardModel()
		})).Return(&llmv1.GenerateResponse{Text: "std-resp"}, nil).Once()

		resp, err := provider.Call(ctx, "standard", "test prompt")
		is.NoError(err)
		is.Equal("std-resp", resp)
	})

	t.Run("Call with unknown tier falls back to standard", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return req.Model == ResolveStandardModel()
		})).Return(&llmv1.GenerateResponse{Text: "fallback-resp"}, nil).Once()

		resp, err := provider.Call(ctx, "unknown", "test prompt")
		is.NoError(err)
		is.Equal("fallback-resp", resp)
	})

	t.Run("Call error", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.Anything).Return(nil, errors.New("provider fail")).Once()

		resp, err := provider.Call(ctx, "standard", "test prompt")
		is.Error(err)
		is.Empty(resp)
	})
}
