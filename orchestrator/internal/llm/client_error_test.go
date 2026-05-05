package llm

import (
	"context"
	"os"
	"testing"
	"time"

	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
)

func TestClient_DialError(t *testing.T) {
	// Set an address that will fail fast or timeout
	os.Setenv("PYTHON_SIDECAR_GRPC_ADDR", "localhost:1")
	defer os.Unsetenv("PYTHON_SIDECAR_GRPC_ADDR")

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
