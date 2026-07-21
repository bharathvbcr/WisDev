package llm

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
)

// closedLoopbackAddr reserves a loopback port and closes it so dials are
// refused deterministically — fixed low ports (e.g. :1) can be bound,
// firewalled, or sit in a Windows excluded range and answer differently.
func closedLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve closed port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestClient_DialError(t *testing.T) {
	// Use an address that fails fast: a just-released loopback port.
	os.Setenv("PYTHON_SIDECAR_GRPC_ADDR", closedLoopbackAddr(t))
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
