package resilience

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCircuitBreaker_LoggingFailureModes(t *testing.T) {
	var logBuffer bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() {
		slog.SetDefault(originalLogger)
	})

	t.Run("Terminal Failure Structured Log Fields", func(t *testing.T) {
		cb := NewCircuitBreaker("test-logging")
		cb.maxFailures = 1

		// First failure to trip it
		_ = cb.Call(context.Background(), func(ctx context.Context) error {
			return errors.New("initial failure")
		})

		logBuffer.Reset()

		// This call should fail fast and log the event
		err := cb.Call(context.Background(), func(ctx context.Context) error {
			return nil
		})

		assert.Error(t, err)
		logs := logBuffer.String()

		assert.Contains(t, logs, "circuit_breaker_open_reject")
		assert.Contains(t, logs, "test-logging")
		assert.Contains(t, logs, "system_overload")
	})
}
