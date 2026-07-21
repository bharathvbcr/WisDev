package llm

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
)

func TestRunVertexGenkitModelMiddlewareRetriesTransientError(t *testing.T) {
	attempts := 0
	result, err := runVertexGenkitModelMiddleware(context.Background(), testVertexGenkitModelCallOptions(), func(context.Context) (string, error) {
		attempts++
		if attempts == 1 {
			return "", errors.New("503 unavailable")
		}
		return "ok", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "ok", result)
	assert.Equal(t, 2, attempts)
}

func TestRunVertexGenkitModelMiddlewareDoesNotRetryInvalidArgument(t *testing.T) {
	attempts := 0
	_, err := runVertexGenkitModelMiddleware(context.Background(), testVertexGenkitModelCallOptions(), func(context.Context) (string, error) {
		attempts++
		return "", errors.New("400 invalid argument")
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "400 invalid argument")
	assert.Equal(t, 1, attempts)
}

func TestRunVertexGenkitModelMiddlewareDoesNotRetryRateLimitCooldown(t *testing.T) {
	attempts := 0
	_, err := runVertexGenkitModelMiddleware(context.Background(), testVertexGenkitModelCallOptions(), func(context.Context) (string, error) {
		attempts++
		return "", errors.New("429 resource exhausted")
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "429 resource exhausted")
	assert.Equal(t, 1, attempts)
}

func TestRunVertexGenkitModelMiddlewareReturnsContextCancellationDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	_, err := runVertexGenkitModelMiddleware(ctx, testVertexGenkitModelCallOptions(), func(context.Context) (string, error) {
		attempts++
		cancel()
		return "", errors.New("503 unavailable")
	})

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, attempts)
}

func TestRunVertexGenkitModelMiddlewareRedactsFailureLog(t *testing.T) {
	var logBuffer bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previousLogger)

	secretSnippet := "sensitive structured response with patient id 12345"
	_, err := runVertexGenkitModelMiddleware(context.Background(), vertexGenkitModelCallOptions{
		Operation:    "structured_output",
		ModelID:      "gemini-test",
		RequestClass: string(RequestClassStandard),
		RetryProfile: string(RetryProfileStandard),
		Backend:      "vertex_ai",
		MaxRetries:   0,
	}, func(context.Context) (string, error) {
		return "", errors.New("gemini structured output is not valid JSON: " + secretSnippet)
	})

	require.Error(t, err)
	logs := logBuffer.String()
	assert.NotContains(t, logs, secretSnippet)
	assert.Contains(t, logs, "error_summary")
	assert.Contains(t, logs, "Vertex model call failed")
}

func TestClassifyVertexErrorTreatsContextCanceledAsCanceled(t *testing.T) {
	assert.Equal(t, "canceled", classifyVertexError(errors.New("context canceled")))
	assert.False(t, isRetryableVertexErrorClass("canceled"))
}

func TestVertexGenkitMiddlewareHandlesNilTextResponse(t *testing.T) {
	resetVertexStructuredRateLimitForTest()
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	mm.On("GenerateContent", mock.Anything, "gemini-test", mock.Anything, mock.Anything).
		Return(nil, nil).Twice()

	_, err := vc.GenerateText(context.Background(), "gemini-test", "prompt", "", 0.7, 128)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil response")
	mm.AssertExpectations(t)
}

func TestVertexGenkitMiddlewareHandlesNilStructuredResponse(t *testing.T) {
	resetVertexStructuredRateLimitForTest()
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	mm.On("GenerateContent", mock.Anything, "gemini-test", mock.Anything, mock.Anything).
		Return(nil, nil).Twice()

	_, err := vc.GenerateStructured(context.Background(), "gemini-test", "prompt", "", `{"type":"object"}`, 0.3, 128)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil response")
	mm.AssertExpectations(t)
}

func TestVertexGenkitMiddlewareSkipsNilTextParts(t *testing.T) {
	resetVertexStructuredRateLimitForTest()
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	mm.On("GenerateContent", mock.Anything, "gemini-test", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{{
				Content: &genai.Content{Parts: []*genai.Part{nil}},
			}},
		}, nil).Once()
	mm.On("GenerateContent", mock.Anything, "gemini-test", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{{
				Content: &genai.Content{Parts: []*genai.Part{{Text: "recovered"}}},
			}},
		}, nil).Once()

	result, err := vc.GenerateText(context.Background(), "gemini-test", "prompt", "", 0.7, 128)

	require.NoError(t, err)
	assert.Equal(t, "recovered", result)
	mm.AssertExpectations(t)
}

func testVertexGenkitModelCallOptions() vertexGenkitModelCallOptions {
	return vertexGenkitModelCallOptions{
		Operation:     "test_generate",
		ModelID:       "gemini-test",
		RequestClass:  string(RequestClassStandard),
		RetryProfile:  string(RetryProfileStandard),
		Backend:       "vertex_ai",
		MaxRetries:    1,
		InitialDelay:  time.Millisecond,
		MaxDelay:      time.Millisecond,
		BackoffFactor: 1,
		NoJitter:      true,
	}
}
