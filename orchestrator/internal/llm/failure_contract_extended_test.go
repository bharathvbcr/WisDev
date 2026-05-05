package llm

import (
	"context"
	"errors"
	"testing"

	llmpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestClient_FailureModes_Extended(t *testing.T) {
	ctx := context.Background()

	t.Run("Retry Exhaustion (Mocked)", func(t *testing.T) {
		msc := new(mockLLMServiceClient)
		c := NewClient()
		c.SetClient(msc)

		// Mock multiple failures
		msc.On("Generate", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("transient error")).Times(3)

		_, err := c.Generate(ctx, &llmpb.GenerateRequest{Prompt: "test"})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "transient error")
	})

	t.Run("Fallback Selection (Vertex Direct to Sidecar)", func(t *testing.T) {
		msc := new(mockLLMServiceClient)
		c := NewClient()
		c.SetClient(msc)

		mdm := new(mockGenAIModels)
		c.VertexDirect = &VertexClient{client: mdm, backend: "vertex_ai"}
		
		unsupportedErr := errors.New("serviceClass parameter is not supported in Vertex AI")
		mdm.On("GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, unsupportedErr).Once()
		
		msc.On("StructuredOutput", mock.Anything, mock.Anything, mock.Anything).Return(&llmpb.StructuredResponse{JsonResult: "{}"}, nil).Once()

		resp, err := c.StructuredOutput(ctx, &llmpb.StructuredRequest{
			Prompt:     "test",
			JsonSchema: `{"type":"object"}`,
			RequestClass: "standard",
		})
		
		assert.NoError(t, err)
		assert.NotNil(t, resp)
		msc.AssertExpectations(t)
		mdm.AssertExpectations(t)
	})
}
