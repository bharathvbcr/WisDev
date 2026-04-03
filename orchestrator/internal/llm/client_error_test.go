package llm

import (
	"context"
	"os"
	"testing"
	"time"

	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"

	"github.com/stretchr/testify/assert"
)

func TestClient_DialError(t *testing.T) {
	// Set an address that will fail fast or timeout
	os.Setenv("LLM_SIDECAR_ADDR", "localhost:1")
	defer os.Unsetenv("LLM_SIDECAR_ADDR")

	c := NewClient()
	// Use a very short timeout to avoid hanging the test
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	t.Run("Generate - Dial Fail", func(t *testing.T) {
		_, err := c.Generate(ctx, &llmv1.GenerateRequest{})
		assert.Error(t, err)
	})

	t.Run("Health - Dial Fail", func(t *testing.T) {
		_, err := c.Health(ctx)
		assert.Error(t, err)
	})
}
