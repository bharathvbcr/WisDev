package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"github.com/stretchr/testify/mock"
)

func TestNewVertexClientLocationDefaultsToUsCentral1WhenEmpty(t *testing.T) {
	oldFactory := newModelsClient
	oldResolver := resolveProjectID
	t.Cleanup(func() {
		newModelsClient = oldFactory
		resolveProjectID = oldResolver
	})

	var capturedConfig *genai.ClientConfig
	newModelsClient = func(ctx context.Context, cfg *genai.ClientConfig) (modelsClient, error) {
		captured := *cfg
		capturedConfig = &captured
		return new(mockGenAIModels), nil
	}
	resolveProjectID = func(ctx context.Context) (string, string) {
		t.Fatalf("resolveProjectID should not be called when projectID is provided")
		return "", ""
	}

	client, err := NewVertexClient(context.Background(), "provided-project", "")
	assert.NoError(t, err)
	assert.NotNil(t, client)
	if assert.NotNil(t, capturedConfig) {
		assert.Equal(t, "provided-project", capturedConfig.Project)
		assert.Equal(t, "us-central1", capturedConfig.Location)
		assert.Equal(t, genai.BackendVertexAI, capturedConfig.Backend)
	}
	assert.Equal(t, "vertex_ai:argument", client.CredentialSource())
}

func TestNewVertexClientUsesFallbackSecretManagerWhenVertexClientInitFails(t *testing.T) {
	oldFactory := newModelsClient
	oldResolver := resolveProjectID
	oldSecretResolver := resolveSecret
	t.Cleanup(func() {
		newModelsClient = oldFactory
		resolveProjectID = oldResolver
		resolveSecret = oldSecretResolver
	})

	var calls int
	newModelsClient = func(ctx context.Context, cfg *genai.ClientConfig) (modelsClient, error) {
		calls++
		return nil, errors.New("vertex unavailable")
	}
	resolveProjectID = func(ctx context.Context) (string, string) {
		t.Fatalf("resolveProjectID should not be called when projectID is provided")
		return "", ""
	}
	resolveSecret = func(ctx context.Context, secret string) (string, error) {
		return "", errors.New("secret missing")
	}

	client, err := NewVertexClient(context.Background(), "provided-project", "us-east1")
	assert.Error(t, err)
	assert.Nil(t, client)
	assert.Equal(t, 1, calls)
	assert.Contains(t, err.Error(), "fallback unavailable")
}

func TestNewVertexClientFallsBackToGeminiApiKeyWhenVertexInitFailsAndFallbackInitFails(t *testing.T) {
	oldFactory := newModelsClient
	oldResolver := resolveProjectID
	oldSecretResolver := resolveSecret
	t.Cleanup(func() {
		newModelsClient = oldFactory
		resolveProjectID = oldResolver
		resolveSecret = oldSecretResolver
	})

	var configs []*genai.ClientConfig
	newModelsClient = func(ctx context.Context, cfg *genai.ClientConfig) (modelsClient, error) {
		captured := *cfg
		configs = append(configs, &captured)
		if len(configs) == 1 {
			return nil, errors.New("vertex init failed")
		}
		return nil, errors.New("gemini init failed")
	}
	resolveProjectID = func(ctx context.Context) (string, string) {
		t.Fatalf("resolveProjectID should not be called when projectID is provided")
		return "", ""
	}
	resolveSecret = func(ctx context.Context, name string) (string, error) {
		if name == "GOOGLE_API_KEY" {
			return "secret-key", nil
		}
		return "", nil
	}

	client, err := NewVertexClient(context.Background(), "provided-project", "us-east1")
	assert.Error(t, err)
	assert.Nil(t, client)
	assert.Len(t, configs, 2)
	assert.Equal(t, genai.BackendVertexAI, configs[0].Backend)
	assert.Equal(t, genai.BackendGeminiAPI, configs[1].Backend)
	assert.Equal(t, "secret-key", configs[1].APIKey)
	assert.Contains(t, err.Error(), "GOOGLE_API_KEY fallback failed")
}

func TestResolveGoogleAPIKeyFallsBackToEnvAndPrefersGoogleApiKey(t *testing.T) {
	oldResolver := resolveSecret
	t.Cleanup(func() {
		resolveSecret = oldResolver
	})

	resolveSecret = func(ctx context.Context, name string) (string, error) {
		t.Fatalf("ResolveGoogleAPIKey should skip secret lookup when projectID is empty")
		return "", nil
	}

	t.Setenv("GOOGLE_API_KEY", "  env-google-key  ")
	t.Setenv("GEMINI_API_KEY", "env-gemini-key")
	key, source, err := ResolveGoogleAPIKey(context.Background(), "")
	assert.NoError(t, err)
	assert.Equal(t, "env-google-key", key)
	assert.Equal(t, "env:GOOGLE_API_KEY", source)
}

func TestResolveGoogleAPIKeyReturnsFirstSecretManagerErrorWhenNoSecretOrEnv(t *testing.T) {
	oldResolver := resolveSecret
	t.Cleanup(func() { resolveSecret = oldResolver })

	expectedErr := errors.New("secret store down")
	callOrder := []string{}
	resolveSecret = func(ctx context.Context, name string) (string, error) {
		callOrder = append(callOrder, name)
		return "", expectedErr
	}
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	_, _, err := ResolveGoogleAPIKey(context.Background(), "project-1")
	assert.Error(t, err)
	assert.Equal(t, []string{"GOOGLE_API_KEY", "GEMINI_API_KEY"}, callOrder)
	assert.EqualError(t, err, expectedErr.Error())
}

func TestResolveGoogleAPIKeyContinuesAfterFirstSecretFailureAndReturnsSecondSecret(t *testing.T) {
	oldResolver := resolveSecret
	t.Cleanup(func() { resolveSecret = oldResolver })

	callOrder := []string{}
	resolveSecret = func(ctx context.Context, name string) (string, error) {
		callOrder = append(callOrder, name)
		switch name {
		case "GOOGLE_API_KEY":
			return "", errors.New("google key unavailable")
		case "GEMINI_API_KEY":
			return " gemini-secret-key ", nil
		default:
			return "", nil
		}
	}

	key, source, err := ResolveGoogleAPIKey(context.Background(), "project-1")
	assert.NoError(t, err)
	assert.Equal(t, "gemini-secret-key", key)
	assert.Equal(t, "secret_manager:GEMINI_API_KEY", source)
	assert.Equal(t, []string{"GOOGLE_API_KEY", "GEMINI_API_KEY"}, callOrder)
}

func TestVertexClientCredentialSourceNilSafe(t *testing.T) {
	var vc *VertexClient
	assert.Equal(t, "vertex_ai", vc.BackendName())
	assert.Equal(t, "", vc.CredentialSource())
}

func TestDefaultTextThinkingBudgetVariants(t *testing.T) {
	tests := []struct {
		name     string
		modelID  string
		expected *int32
	}{
		{name: "gemini-2.5-flash-lite", modelID: "gemini-2.5-flash-lite", expected: ptr(int32(0))},
		{name: "gemini-3.0-flash", modelID: "gemini-3.0-flash", expected: ptr(int32(1024))},
		{name: "gemini-2.5-flash", modelID: "gemini-2.5-flash", expected: ptr(int32(1024))},
		{name: "gemini-2.5-pro", modelID: "gemini-2.5-pro", expected: ptr(int32(2048))},
		{name: "gemini-3-pro-unknown", modelID: "gemini-3-pro", expected: ptr(int32(2048))},
		{name: "non-gemini model", modelID: "other-model", expected: nil},
		{name: "upper and spaces", modelID: "  GEMINI-2.5-FLASH  ", expected: ptr(int32(1024))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := defaultTextThinkingBudget(tt.modelID)
			if tt.expected == nil {
				assert.Nil(t, actual)
				return
			}
			require.NotNil(t, actual)
			assert.Equal(t, *tt.expected, *actual)
		})
	}
}

func TestThinkingConfigForModel(t *testing.T) {
	t.Run("nil budget", func(t *testing.T) {
		assert.Nil(t, thinkingConfigForModel("gemini-2.5-flash", nil))
	})

	t.Run("gemini-3 minimal", func(t *testing.T) {
		level := int32(0)
		cfg := thinkingConfigForModel("gemini-3-flash", &level)
		assert.Equal(t, genai.ThinkingLevelMinimal, cfg.ThinkingLevel)
		assert.Nil(t, cfg.ThinkingBudget)
	})

	t.Run("gemini-3 low", func(t *testing.T) {
		level := int32(512)
		cfg := thinkingConfigForModel("gemini-3-flash", &level)
		assert.Equal(t, genai.ThinkingLevelLow, cfg.ThinkingLevel)
		assert.Nil(t, cfg.ThinkingBudget)
	})

	t.Run("gemini-3 medium", func(t *testing.T) {
		level := int32(4096)
		cfg := thinkingConfigForModel("gemini-3-pro", &level)
		assert.Equal(t, genai.ThinkingLevelMedium, cfg.ThinkingLevel)
		assert.Nil(t, cfg.ThinkingBudget)
	})

	t.Run("gemini-3 high", func(t *testing.T) {
		level := int32(16384)
		cfg := thinkingConfigForModel("gemini-3-pro", &level)
		assert.Equal(t, genai.ThinkingLevelHigh, cfg.ThinkingLevel)
		assert.Nil(t, cfg.ThinkingBudget)
	})

	t.Run("gemini-2.5 returns raw budget", func(t *testing.T) {
		level := int32(77)
		cfg := thinkingConfigForModel("gemini-2.5-flash", &level)
		assert.NotNil(t, cfg)
		require.NotNil(t, cfg)
		assert.Equal(t, level, *cfg.ThinkingBudget)
	})

	t.Run("unknown model", func(t *testing.T) {
		level := int32(1)
		assert.Nil(t, thinkingConfigForModel("other-model", &level))
	})
}

func TestPrepareSchemaForVertexSupportsArrays(t *testing.T) {
	raw := []any{
		"one",
		map[string]any{
			"properties": map[string]any{
				"b": map[string]any{"type": "string"},
				"a": map[string]any{"type": "string"},
			},
		},
	}

	normalized := prepareSchemaForVertex(raw)
	list, ok := normalized.([]any)
	assert.True(t, ok)
	assert.Len(t, list, 2)
	assert.Equal(t, "one", list[0])

	node, ok := list[1].(map[string]any)
	assert.True(t, ok)
	ordering, ok := node["propertyOrdering"].([]string)
	assert.True(t, ok)
	assert.Equal(t, []string{"a", "b"}, ordering)
}

func TestClassifyVertexErrorAllPaths(t *testing.T) {
	assert.Equal(t, "timeout", classifyVertexError(errors.New("deadline exceeded during request")))
	assert.Equal(t, "rate_limit", classifyVertexError(errors.New("Resource Exhausted 429 too many requests")))
	assert.Equal(t, "unavailable", classifyVertexError(errors.New("503 unavailable")))
	assert.Equal(t, "invalid_request", classifyVertexError(errors.New("400 invalid payload")))
	assert.Equal(t, "", classifyVertexError(nil))
	assert.Equal(t, "unknown", classifyVertexError(errors.New("some other unexpected failure")))
}

func TestIsRetryableVertexErrorClassAllCases(t *testing.T) {
	assert.False(t, isRetryableVertexErrorClass("invalid_request"))
	assert.False(t, isRetryableVertexErrorClass("unsupported_parameter"))
	assert.True(t, isRetryableVertexErrorClass("timeout"))
	assert.True(t, isRetryableVertexErrorClass("rate_limit"))
	assert.True(t, isRetryableVertexErrorClass("unavailable"))
	assert.True(t, isRetryableVertexErrorClass("unknown"))
	assert.False(t, isRetryableVertexErrorClass(""))
}

func TestNewVertexClientWithoutProjectAndNoKeySource(t *testing.T) {
	oldFactory := newModelsClient
	oldResolver := resolveProjectID
	oldSecretResolver := resolveSecret
	t.Cleanup(func() {
		newModelsClient = oldFactory
		resolveProjectID = oldResolver
		resolveSecret = oldSecretResolver
	})

	resolveProjectID = func(ctx context.Context) (string, string) {
		return "", "missing"
	}
	resolveSecret = func(ctx context.Context, name string) (string, error) {
		t.Fatalf("resolveSecret should not be called when projectID is empty")
		return "", nil
	}
	newModelsClient = func(ctx context.Context, cfg *genai.ClientConfig) (modelsClient, error) {
		t.Fatalf("newModelsClient should not be called when projectID is missing")
		return nil, nil
	}

	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	client, err := NewVertexClient(context.Background(), "", "us-central1")
	assert.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "GOOGLE_CLOUD_PROJECT/GCLOUD_PROJECT is not set")
}

func TestNewVertexClientResolvedProjectIDFallsBackToSecretManager(t *testing.T) {
	oldFactory := newModelsClient
	oldResolver := resolveProjectID
	oldSecretResolver := resolveSecret
	t.Cleanup(func() {
		newModelsClient = oldFactory
		resolveProjectID = oldResolver
		resolveSecret = oldSecretResolver
	})

	resolveProjectID = func(ctx context.Context) (string, string) {
		return "resolved-project", "gcloud_config"
	}
	resolveSecret = func(ctx context.Context, name string) (string, error) {
		if name == "GOOGLE_API_KEY" {
			return "secret-api-key", nil
		}
		return "", nil
	}
	calls := 0
	newModelsClient = func(ctx context.Context, cfg *genai.ClientConfig) (modelsClient, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("vertex init failed")
		}
		return &mockGenAIModels{}, nil
	}

	client, err := NewVertexClient(context.Background(), "", "us-east1")
	assert.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, "gemini_api_secret", client.BackendName())
	assert.Equal(t, "secret_manager:GOOGLE_API_KEY", client.CredentialSource())
	assert.Equal(t, 2, calls)
}

func TestNewVertexClientResolvedProjectIDFallsBackToGeminiWhenFallbackAPIKeyMissing(t *testing.T) {
	oldFactory := newModelsClient
	oldResolver := resolveProjectID
	oldSecretResolver := resolveSecret
	t.Cleanup(func() {
		newModelsClient = oldFactory
		resolveProjectID = oldResolver
		resolveSecret = oldSecretResolver
	})

	resolveProjectID = func(ctx context.Context) (string, string) {
		return "resolved-project", "gcloud_config"
	}
	resolveSecret = func(ctx context.Context, name string) (string, error) {
		return "", nil
	}
	newModelsClient = func(ctx context.Context, cfg *genai.ClientConfig) (modelsClient, error) {
		return nil, errors.New("vertex init failed")
	}

	client, err := NewVertexClient(context.Background(), "", "us-east1")
	assert.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "failed to create Vertex AI genai client")
	assert.NotContains(t, err.Error(), "fallback failed")
	assert.NotContains(t, err.Error(), "fallback unavailable")
}

func TestVertexClient_GenerateText_NoCandidatesRetryThenSuccess(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx := context.Background()

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{},
		}, nil).Once()
	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{{Text: "Recovered"}}}},
			},
		}, nil).Once()

	result, err := vc.GenerateText(ctx, "gemini-2.5-flash", "prompt", "", 0.7, 128)
	assert.NoError(t, err)
	assert.Equal(t, "Recovered", result)
	mm.AssertExpectations(t)
}

func TestVertexClient_GenerateText_EmptyTextThenError(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx := context.Background()

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{{Text: "   "}}}},
			},
		}, nil).Once()
	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{{Text: "fallback"}}}},
			},
		}, nil).Once()

	result, err := vc.GenerateText(ctx, "gemini-2.5-flash", "prompt", "", 0.7, 128)
	assert.NoError(t, err)
	assert.Equal(t, "fallback", result)
	mm.AssertExpectations(t)
}

func TestVertexClient_GenerateText_NoCandidatesAfterRetry(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx := context.Background()

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{},
		}, nil).Once()
	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{},
		}, nil).Once()

	_, err := vc.GenerateText(ctx, "gemini-2.5-flash", "prompt", "", 0.7, 128)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no candidates returned")
	mm.AssertExpectations(t)
}

func TestVertexClient_GenerateText_ContextCancelledBeforeAttempt(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := vc.GenerateText(ctx, "gemini-2.5-flash", "prompt", "", 0.7, 128)
	assert.ErrorIs(t, err, context.Canceled)
	mm.AssertNotCalled(t, "GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestVertexClient_GenerateText_CancelledDuringRetry(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx, cancel := context.WithCancel(context.Background())

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash", mock.Anything, mock.Anything).
		Return(nil, errors.New("503 unavailable")).
		Run(func(args mock.Arguments) {
			cancel()
		}).
		Once()

	_, err := vc.GenerateText(ctx, "gemini-2.5-flash", "prompt", "", 0.7, 128)
	assert.ErrorIs(t, err, context.Canceled)
	mm.AssertExpectations(t)
}

func TestVertexClient_GenerateTextSkipsRetryWhenContextBudgetIsTooLow(t *testing.T) {
	resetVertexStructuredRateLimitForTest()
	t.Cleanup(resetVertexStructuredRateLimitForTest)

	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash", mock.Anything, mock.Anything).
		Return(nil, errors.New("resource exhausted 429")).Once()

	_, err := vc.GenerateText(ctx, "gemini-2.5-flash", "prompt", "", 0.7, 128)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "resource exhausted 429")
	mm.AssertNumberOfCalls(t, "GenerateContent", 1)
}

func TestVertexClient_GenerateTextRateLimitCooldownFailsFast(t *testing.T) {
	resetVertexStructuredRateLimitForTest()
	t.Cleanup(resetVertexStructuredRateLimitForTest)

	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash", mock.Anything, mock.Anything).
		Return(nil, errors.New("resource exhausted 429")).Once()

	_, err := vc.GenerateText(ctx, "gemini-2.5-flash", "prompt", "", 0.7, 128)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resource exhausted 429")

	_, err = vc.GenerateText(context.Background(), "gemini-2.5-flash", "prompt", "", 0.7, 128)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited")
	mm.AssertNumberOfCalls(t, "GenerateContent", 1)
}

func TestClientProviderCooldownRemainingReflectsVertexDirectCooldown(t *testing.T) {
	resetVertexStructuredRateLimitForTest()
	t.Cleanup(resetVertexStructuredRateLimitForTest)

	assert.Zero(t, (&Client{}).ProviderCooldownRemaining())
	recordVertexProviderRateLimit(time.Now())

	client := &Client{VertexDirect: &VertexClient{}}
	assert.Greater(t, client.ProviderCooldownRemaining(), time.Duration(0))
}

func TestIsProviderRateLimitError(t *testing.T) {
	assert.False(t, IsProviderRateLimitError(nil))
	assert.True(t, IsProviderRateLimitError(errors.New("vertex structured output rate limited; retry after 57s")))
	assert.True(t, IsProviderRateLimitError(errors.New("vertex structured output provider cooldown active; retry after 45s")))
	assert.True(t, IsProviderRateLimitError(errors.New("Error 429, Message: Resource exhausted")))
	assert.True(t, IsProviderRateLimitError(errors.New("429 RESOURCE_EXHAUSTED")))
	assert.False(t, IsProviderRateLimitError(errors.New("context canceled")))
}

func TestVertexClient_GenerateText_NilCandidateContentThenSuccess(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx := context.Background()

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{},
			},
		}, nil).Once()
	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{{Text: "Recovered"}}}},
			},
		}, nil).Once()

	result, err := vc.GenerateText(ctx, "gemini-2.5-flash", "prompt", "", 0.7, 128)
	assert.NoError(t, err)
	assert.Equal(t, "Recovered", result)
	mm.AssertExpectations(t)
}

func TestVertexClient_GenerateText_NoCandidatePartsThenRetryThenSuccess(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx := context.Background()

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{}}},
			},
		}, nil).Once()
	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{{Text: "Recovered"}}}},
			},
		}, nil).Once()

	result, err := vc.GenerateText(ctx, "gemini-2.5-flash", "prompt", "", 0.7, 128)
	assert.NoError(t, err)
	assert.Equal(t, "Recovered", result)
	mm.AssertExpectations(t)
}

func TestVertexClient_GenerateText_NonRetryableError(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx := context.Background()

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash", mock.Anything, mock.Anything).
		Return(nil, errors.New("400 invalid argument")).Once()

	_, err := vc.GenerateText(ctx, "gemini-2.5-flash", "prompt", "", 0.7, 128)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "400 invalid argument")
	mm.AssertNumberOfCalls(t, "GenerateContent", 1)
	mm.AssertExpectations(t)
}

func TestVertexClient_generateStructuredWithTokens_NoCandidatesThenRetry(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx := context.Background()

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash-lite", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{},
		}, nil).Once()
	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash-lite", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{{Text: "{\"ok\":true}"}}}},
			},
		}, nil).Once()

	text, inTok, outTok, err := vc.generateStructuredWithTokens(ctx, "gemini-2.5-flash-lite", "prompt", "", `{"type":"object"}`, 0.3, 128, "", nil, "", "")
	assert.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, text)
	assert.Equal(t, int32(0), inTok)
	assert.Equal(t, int32(0), outTok)
	mm.AssertExpectations(t)
}

func TestVertexClient_GenerateStructuredWithTokens_EmptyTextThenError(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx := context.Background()

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash-lite", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{{Text: "   "}}}},
			},
		}, nil).Once()
	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash-lite", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{{Text: "{\"ok\":true}"}}}},
			},
		}, nil).Once()

	text, _, _, err := vc.generateStructuredWithTokens(ctx, "gemini-2.5-flash-lite", "prompt", "", `{"type":"object"}`, 0.3, 128, "", nil, "", "")
	assert.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, text)
	mm.AssertExpectations(t)
}

func TestVertexClient_GenerateStructuredWithTokens_ContextCancelledBeforeAttempt(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, _, err := vc.generateStructuredWithTokens(ctx, "gemini-2.5-flash-lite", "prompt", "", `{"type":"object"}`, 0.3, 128, "", nil, "", "")
	assert.ErrorIs(t, err, context.Canceled)
	mm.AssertNotCalled(t, "GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestVertexClient_generateStructuredWithTokens_NoCandidatePartsThenRetry(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx := context.Background()

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash-lite", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{}}},
			},
		}, nil).Once()
	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash-lite", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{{Text: "{\"ok\":true}"}}}},
			},
		}, nil).Once()

	text, inTok, outTok, err := vc.generateStructuredWithTokens(ctx, "gemini-2.5-flash-lite", "prompt", "", `{"type":"object"}`, 0.3, 128, "", nil, "", "")
	assert.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, text)
	assert.Equal(t, int32(0), inTok)
	assert.Equal(t, int32(0), outTok)
	mm.AssertExpectations(t)
}

func TestVertexClient_GenerateStructuredWithTokens_InvalidJSONThenRetry(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx := context.Background()

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash-lite", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{{Text: "not json"}}}},
			},
		}, nil).Once()
	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash-lite", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{{Text: `{"ok":true}`}}}},
			},
		}, nil).Once()

	text, inTok, outTok, err := vc.generateStructuredWithTokens(ctx, "gemini-2.5-flash-lite", "prompt", "", `{"type":"object"}`, 0.3, 128, "", nil, "", "")
	assert.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, text)
	assert.Equal(t, int32(0), inTok)
	assert.Equal(t, int32(0), outTok)
	mm.AssertExpectations(t)
}

func TestVertexClient_GenerateStructuredWithTokens_NonRetryableError(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx := context.Background()

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash-lite", mock.Anything, mock.Anything).
		Return(nil, errors.New("serviceTier parameter is not supported in Vertex AI")).Once()

	_, _, _, err := vc.generateStructuredWithTokens(ctx, "gemini-2.5-flash-lite", "prompt", "", `{"type":"object"}`, 0.3, 128, "", nil, "", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "serviceTier parameter is not supported in Vertex AI")
	mm.AssertNumberOfCalls(t, "GenerateContent", 1)
}

func TestVertexClient_GenerateStructuredWithTokensSkipsRetryWhenContextBudgetIsTooLow(t *testing.T) {
	resetVertexStructuredRateLimitForTest()
	t.Cleanup(resetVertexStructuredRateLimitForTest)

	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash-lite", mock.Anything, mock.Anything).
		Return(nil, errors.New("503 unavailable")).Once()

	_, _, _, err := vc.generateStructuredWithTokens(ctx, "gemini-2.5-flash-lite", "prompt", "", `{"type":"object"}`, 0.3, 128, "", nil, "", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "503 unavailable")
	mm.AssertNumberOfCalls(t, "GenerateContent", 1)
}

func TestVertexClient_GenerateStructuredWithTokensRateLimitCooldownFailsFast(t *testing.T) {
	resetVertexStructuredRateLimitForTest()
	t.Cleanup(resetVertexStructuredRateLimitForTest)

	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash-lite", mock.Anything, mock.Anything).
		Return(nil, errors.New("resource exhausted 429")).Once()

	_, _, _, err := vc.generateStructuredWithTokens(ctx, "gemini-2.5-flash-lite", "prompt", "", `{"type":"object"}`, 0.3, 128, "", nil, "structured_high_value", "standard")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resource exhausted 429")

	_, _, _, err = vc.generateStructuredWithTokens(context.Background(), "gemini-2.5-flash-lite", "prompt", "", `{"type":"object"}`, 0.3, 128, "", nil, "structured_high_value", "standard")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited")
	mm.AssertNumberOfCalls(t, "GenerateContent", 1)
}

func TestVertexClient_GenerateStructuredWithTokensDefaultPolicyUsesRateLimitCooldown(t *testing.T) {
	resetVertexStructuredRateLimitForTest()
	t.Cleanup(resetVertexStructuredRateLimitForTest)

	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash-lite", mock.Anything, mock.Anything).
		Return(nil, errors.New("resource exhausted 429")).Once()

	_, _, _, err := vc.generateStructuredWithTokens(ctx, "gemini-2.5-flash-lite", "prompt", "", `{"type":"object"}`, 0.3, 128, "", nil, "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resource exhausted 429")

	_, _, _, err = vc.generateStructuredWithTokens(context.Background(), "gemini-2.5-flash-lite", "prompt", "", `{"type":"object"}`, 0.3, 128, "", nil, "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited")
	mm.AssertNumberOfCalls(t, "GenerateContent", 1)
}

func TestVertexClient_GenerateStructuredDefaultPolicyUsesRateLimitCooldown(t *testing.T) {
	resetVertexStructuredRateLimitForTest()
	t.Cleanup(resetVertexStructuredRateLimitForTest)

	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash-lite", mock.Anything, mock.Anything).
		Return(nil, errors.New("resource exhausted 429")).Once()

	_, err := vc.GenerateStructured(ctx, "gemini-2.5-flash-lite", "prompt", "", `{"type":"object"}`, 0.3, 128)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resource exhausted 429")

	_, err = vc.GenerateStructured(context.Background(), "gemini-2.5-flash-lite", "prompt", "", `{"type":"object"}`, 0.3, 128)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited")
	mm.AssertNumberOfCalls(t, "GenerateContent", 1)
}

func TestVertexClient_generateStructuredWithTokens_NilCandidateContentThenRetry(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx := context.Background()

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash-lite", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{},
			},
		}, nil).Once()
	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash-lite", mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{{Text: "{\"ok\":true}"}}}},
			},
		}, nil).Once()

	text, inTok, outTok, err := vc.generateStructuredWithTokens(ctx, "gemini-2.5-flash-lite", "prompt", "", `{"type":"object"}`, 0.3, 128, "", nil, "", "")
	assert.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, text)
	assert.Equal(t, int32(0), inTok)
	assert.Equal(t, int32(0), outTok)
	mm.AssertExpectations(t)
}

func TestVertexClient_EmbedText_NoEmbeddingReturned(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx := context.Background()

	mm.On("EmbedContent", mock.Anything, "text-embedding-005", mock.Anything, mock.Anything).
		Return(&genai.EmbedContentResponse{
			Embeddings: []*genai.ContentEmbedding{},
		}, nil).Once()

	_, err := vc.EmbedText(ctx, "text-embedding-005", "query text", "query")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no embedding returned")
	mm.AssertExpectations(t)
}

func TestVertexClient_EmbedBatch_EmptyEmbeddings(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx := context.Background()

	mm.On("EmbedContent", mock.Anything, "text-embedding-005", mock.Anything, mock.Anything).
		Return(&genai.EmbedContentResponse{
			Embeddings: []*genai.ContentEmbedding{},
		}, nil).Once()

	embeddings, err := vc.EmbedBatch(ctx, "", []string{"one"}, "query")
	assert.NoError(t, err)
	assert.Empty(t, embeddings)
	mm.AssertExpectations(t)
}

func TestVertexClient_EmbedBatch_Error(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx := context.Background()

	mm.On("EmbedContent", mock.Anything, "text-embedding-005", mock.Anything, mock.Anything).
		Return(nil, errors.New("batch embed failed")).Once()

	_, err := vc.EmbedBatch(ctx, "", []string{"first", "second"}, "query")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "batch embed content failed")
	mm.AssertExpectations(t)
}

func TestVertexClient_GenerateImages_SkipsNilImages(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx := context.Background()

	mm.On("GenerateImages", mock.Anything, "imagen-3.0-generate-001", mock.Anything, mock.Anything).
		Return(&genai.GenerateImagesResponse{
			GeneratedImages: []*genai.GeneratedImage{
				{},
				{Image: &genai.Image{ImageBytes: []byte("ok")}},
			},
		}, nil).Once()

	resp, err := vc.GenerateImages(ctx, "", "prompt", 2, "1:1")
	assert.NoError(t, err)
	assert.Len(t, resp, 1)
	assert.Equal(t, []byte("ok"), resp[0].ImageBytes)
	mm.AssertExpectations(t)
}

func TestVertexClient_GenerateImages_Error(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx := context.Background()

	mm.On("GenerateImages", mock.Anything, "imagen-3.0-generate-001", mock.Anything, mock.Anything).
		Return(nil, errors.New("images failed")).Once()

	_, err := vc.GenerateImages(ctx, "", "prompt", 1, "1:1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "generate images failed")
	mm.AssertExpectations(t)
}

func TestNewModelsClientDefaultFactoryBuildsClient(t *testing.T) {
	client, err := newModelsClient(context.Background(), &genai.ClientConfig{
		APIKey:  "test-api-key",
		Backend: genai.BackendGeminiAPI,
		HTTPOptions: genai.HTTPOptions{
			APIVersion: stableGenAIAPIVersion,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestVertexClient_GenerateTextRetryCancelDuringBackoff(t *testing.T) {
	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash", mock.Anything, mock.Anything).
		Return(nil, errors.New("deadline exceeded")).Once()

	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	_, err := vc.GenerateText(ctx, "gemini-2.5-flash", "prompt", "", 0.7, 100)
	assert.ErrorIs(t, err, context.Canceled)
	mm.AssertExpectations(t)
}

func TestVertexClient_GenerateStructuredWithTokensRetryCancelDuringBackoff(t *testing.T) {
	resetVertexStructuredRateLimitForTest()
	t.Cleanup(resetVertexStructuredRateLimitForTest)

	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm, backend: "vertex_ai"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash-lite", mock.Anything, mock.Anything).
		Return(nil, errors.New("503 unavailable")).Once()

	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	_, _, _, err := vc.generateStructuredWithTokens(ctx, "gemini-2.5-flash-lite", "prompt", "", `{"type":"object"}`, 0.3, 128, "", nil, "", "")
	assert.ErrorIs(t, err, context.Canceled)
	mm.AssertExpectations(t)
}

func ptr[T any](value T) *T {
	return &value
}
