package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestStructuredLoggingHelpers(t *testing.T) {
	oldLogger := slog.Default()
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	assert.NoError(t, err)
	os.Stdout = w
	t.Cleanup(func() {
		slog.SetDefault(oldLogger)
		os.Stdout = oldStdout
		_ = w.Close()
		_ = r.Close()
	})

	InitStructuredLogging()
	assert.NotNil(t, WithContext(nil))
	assert.NotNil(t, WithContext(context.Background()))

	traceID, err := oteltrace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	assert.NoError(t, err)
	spanID, err := oteltrace.SpanIDFromHex("0123456789abcdef")
	assert.NoError(t, err)
	ctx := oteltrace.ContextWithSpanContext(context.Background(), oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
	}))

	assert.NotNil(t, WithContext(ctx))
	LogAgentDecision(ctx, "planner", "choose_strategy", "trace-aware logging", map[string]any{
		"attempt": 1,
		"safety":  "strict",
	})

	_ = w.Close()
	output, err := io.ReadAll(r)
	assert.NoError(t, err)
	assert.Contains(t, string(output), "agent decision")
	assert.Contains(t, string(output), "trace_id")
	assert.Contains(t, string(output), "planner")
}

func TestInitStructuredLoggingLevels(t *testing.T) {
	cases := []struct {
		name         string
		level        string
		wantContains []string
		wantMissing  []string
	}{
		{
			name:         "default-info",
			level:        "",
			wantContains: []string{"info message", "warn message", "error message"},
			wantMissing:  []string{"debug message"},
		},
		{
			name:         "debug",
			level:        "debug",
			wantContains: []string{"debug message", "info message", "warn message", "error message"},
		},
		{
			name:         "warn",
			level:        "WARN",
			wantContains: []string{"warn message", "error message"},
			wantMissing:  []string{"debug message", "info message"},
		},
		{
			name:         "error",
			level:        "ERROR",
			wantContains: []string{"error message"},
			wantMissing:  []string{"debug message", "info message", "warn message"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldLogger := slog.Default()
			oldStdout := os.Stdout
			r, w, err := os.Pipe()
			require.NoError(t, err)
			os.Stdout = w
			t.Cleanup(func() {
				slog.SetDefault(oldLogger)
				os.Stdout = oldStdout
				_ = w.Close()
				_ = r.Close()
			})

			t.Setenv("LOG_LEVEL", tc.level)
			InitStructuredLogging()

			slog.Debug("debug message")
			slog.Info("info message")
			slog.Warn("warn message")
			slog.Error("error message")

			_ = w.Close()
			output, err := io.ReadAll(r)
			require.NoError(t, err)

			text := string(output)
			for _, want := range tc.wantContains {
				assert.Contains(t, text, want)
			}
			for _, missing := range tc.wantMissing {
				assert.NotContains(t, text, missing)
			}
		})
	}
}

func TestInitStructuredLoggingIgnoresUnknownLevels(t *testing.T) {
	oldLogger := slog.Default()
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	t.Cleanup(func() {
		slog.SetDefault(oldLogger)
		os.Stdout = oldStdout
		_ = w.Close()
		_ = r.Close()
	})

	t.Setenv("LOG_LEVEL", "trace")
	InitStructuredLogging()
	slog.Info("info message")

	_ = w.Close()
	output, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(output), "info message"))
}
