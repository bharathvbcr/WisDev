package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/telemetry"
)

// InitStructuredLogging sets up the global logger to use JSON.
func InitStructuredLogging() {
	level := slog.LevelInfo
	if envLevel := os.Getenv("LOG_LEVEL"); envLevel != "" {
		switch strings.ToUpper(envLevel) {
		case "DEBUG":
			level = slog.LevelDebug
		case "WARN":
			level = slog.LevelWarn
		case "ERROR":
			level = slog.LevelError
		}
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})
	logger := slog.New(handler)
	slog.SetDefault(logger)
}

// WithContext returns a logger with trace and request metadata from the context.
func WithContext(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return slog.Default()
	}
	logger := slog.Default()
	if traceID := telemetry.TraceIDFrom(ctx); traceID != "" {
		return logger.With("trace_id", traceID)
	}
	return logger
}

// LogAgentDecision records why an agent made a specific choice.
func LogAgentDecision(ctx context.Context, agent, step, reason string, metadata map[string]any) {
	attrs := []any{
		slog.String("agent", agent),
		slog.String("step", step),
		slog.String("reason", reason),
	}
	for k, v := range metadata {
		attrs = append(attrs, slog.Any(k, v))
	}
	WithContext(ctx).InfoContext(ctx, "agent decision", attrs...)
}
