package llm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

func TestClient_Uninitialized(t *testing.T) {
	c := &Client{grpcAddr: "localhost:1"} // Invalid addr
	ctx := context.Background()

	t.Run("Generate", func(t *testing.T) {
		_, err := c.Generate(ctx, &llmv1.GenerateRequest{})
		assert.Error(t, err)
	})

	t.Run("StructuredOutput", func(t *testing.T) {
		_, err := c.StructuredOutput(ctx, &llmv1.StructuredRequest{})
		assert.Error(t, err)
	})

	t.Run("Embed", func(t *testing.T) {
		_, err := c.Embed(ctx, &llmv1.EmbedRequest{})
		assert.Error(t, err)
	})

	t.Run("EmbedBatch", func(t *testing.T) {
		_, err := c.EmbedBatch(ctx, &llmv1.EmbedBatchRequest{})
		assert.Error(t, err)
	})

	t.Run("Health", func(t *testing.T) {
		_, err := c.Health(ctx)
		assert.Error(t, err)
	})
}

func TestInjectMetadata_Method(t *testing.T) {
	c := &Client{}
	ctx := context.Background()
	reqMetadata := map[string]string{"test-key": "test-val"}

	newCtx := c.injectMetadata(ctx, reqMetadata)
	assert.NotNil(t, newCtx)
}
