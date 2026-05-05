package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	llmpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestClient_FailureModes(t *testing.T) {
	ctx := context.Background()

	t.Run("Provider timeout (Context Deadline)", func(t *testing.T) {
		msc := new(mockLLMServiceClient)
		c := NewClient()
		c.SetClient(msc)

		msc.On("Generate", mock.Anything, mock.Anything, mock.Anything).Return(nil, context.DeadlineExceeded).Once()

		timedCtx, cancel := context.WithTimeout(ctx, 1*time.Millisecond)
		defer cancel()
		time.Sleep(2 * time.Millisecond) // Ensure timeout

		_, err := c.Generate(timedCtx, &llmpb.GenerateRequest{Prompt: "test"})
		assert.Error(t, err)
		assert.True(t, errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded)
	})

	t.Run("Provider malformed response (Structured Output)", func(t *testing.T) {
		msc := new(mockLLMServiceClient)
		c := NewClient()
		c.SetClient(msc)

		// Provider returns invalid JSON
		msc.On("StructuredOutput", mock.Anything, mock.Anything, mock.Anything).Return(&llmpb.StructuredResponse{
			JsonResult: "{ invalid json",
		}, nil).Once()

		resp, err := c.StructuredOutput(ctx, &llmpb.StructuredRequest{
			Prompt:     "test",
			JsonSchema: `{"type":"object"}`,
		})
		
		assert.NoError(t, err)
		assert.Equal(t, "{ invalid json", resp.JsonResult)
	})

	t.Run("Missing secret/config behavior", func(t *testing.T) {
		c := &Client{grpcAddr: ""}
		err := c.ensureClient(ctx)
		assert.ErrorIs(t, err, errNilClient)
	})
}
