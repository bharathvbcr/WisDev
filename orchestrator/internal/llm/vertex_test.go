package llm

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"google.golang.org/genai"
)

type mockGenAIModels struct {
	mock.Mock
}

func (m *mockGenAIModels) GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	args := m.Called(ctx, model, contents, config)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*genai.GenerateContentResponse), args.Error(1)
}

func (m *mockGenAIModels) EmbedContent(ctx context.Context, model string, contents []*genai.Content, config *genai.EmbedContentConfig) (*genai.EmbedContentResponse, error) {
	args := m.Called(ctx, model, contents, config)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*genai.EmbedContentResponse), args.Error(1)
}

func (m *mockGenAIModels) GenerateImages(ctx context.Context, model string, prompt string, config *genai.GenerateImagesConfig) (*genai.GenerateImagesResponse, error) {
	args := m.Called(ctx, model, prompt, config)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*genai.GenerateImagesResponse), args.Error(1)
}

func TestVertexClient(t *testing.T) {
	is := assert.New(t)
	resetModelsFromTempDir := func() {
		resetVertexStructuredRateLimitForTest()
		origDir, err := os.Getwd()
		assert.NoError(t, err)
		tmpDir := t.TempDir()
		assert.NoError(t, os.Chdir(tmpDir))
		modelsOnce = sync.Once{}
		cachedModels = ModelConfig{}
		t.Cleanup(func() {
			assert.NoError(t, os.Chdir(origDir))
			modelsOnce = sync.Once{}
			cachedModels = ModelConfig{}
			resetVertexStructuredRateLimitForTest()
		})
	}

	mm := new(mockGenAIModels)
	vc := &VertexClient{client: mm}

	ctx := context.Background()

	t.Run("GenerateText Success", func(t *testing.T) {
		resetModelsFromTempDir()
		mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash", mock.Anything, mock.Anything).
			Return(&genai.GenerateContentResponse{
				Candidates: []*genai.Candidate{
					{
						Content: &genai.Content{
							Parts: []*genai.Part{{Text: "hello world"}},
						},
					},
				},
			}, nil).Once()

		resp, err := vc.GenerateText(ctx, "", "prompt", "system", 0.7, 100)
		is.NoError(err)
		is.Equal("hello world", resp)
	})

	t.Run("GenerateText Error", func(t *testing.T) {
		resetModelsFromTempDir()
		mm.On("GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return(nil, errors.New("fail")).Twice()

		_, err := vc.GenerateText(ctx, "", "prompt", "", 0.7, 100)
		is.Error(err)
	})

	t.Run("GenerateText invalid request fails fast without retry", func(t *testing.T) {
		resetModelsFromTempDir()
		mm.ExpectedCalls = nil
		mm.Calls = nil
		mm.On("GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return(nil, errors.New("400 invalid argument")).Once()

		_, err := vc.GenerateText(ctx, "", "prompt", "", 0.7, 100)
		is.Error(err)
		is.Contains(err.Error(), "400 invalid argument")
		mm.AssertExpectations(t)
	})

	t.Run("GenerateText rate limit opens cooldown without retry", func(t *testing.T) {
		resetModelsFromTempDir()
		mm.ExpectedCalls = nil
		mm.Calls = nil
		mm.On("GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return(nil, errors.New("Error 429, Message: Resource exhausted")).Once()

		_, err := vc.GenerateText(ctx, "", "prompt", "", 0.7, 100)
		is.Error(err)
		is.Contains(err.Error(), "Resource exhausted")
		is.True(VertexProviderRateLimitRemaining() > 0)
		mm.AssertExpectations(t)
	})

	t.Run("EmbedText Success", func(t *testing.T) {
		resetModelsFromTempDir()
		mm.On("EmbedContent", mock.Anything, "text-embedding-005", mock.Anything, mock.Anything).
			Return(&genai.EmbedContentResponse{
				Embeddings: []*genai.ContentEmbedding{
					{Values: []float32{0.1, 0.2}},
				},
			}, nil).Once()

		resp, err := vc.EmbedText(ctx, "", "text", "query")
		is.NoError(err)
		is.Equal([]float32{0.1, 0.2}, resp)
	})

	t.Run("EmbedBatch Success", func(t *testing.T) {
		resetModelsFromTempDir()
		mm.On("EmbedContent", mock.Anything, "text-embedding-005", mock.Anything, mock.Anything).
			Return(&genai.EmbedContentResponse{
				Embeddings: []*genai.ContentEmbedding{
					{Values: []float32{0.1}},
					{Values: []float32{0.2}},
				},
			}, nil).Once()

		resp, err := vc.EmbedBatch(ctx, "", []string{"t1", "t2"}, "query")
		is.NoError(err)
		is.Len(resp, 2)
	})

	t.Run("GenerateImages Success", func(t *testing.T) {
		resetModelsFromTempDir()
		data := []byte("fake-image-data")
		mm.On("GenerateImages", mock.Anything, "imagen-3.0-generate-001", "prompt", mock.Anything).
			Return(&genai.GenerateImagesResponse{
				GeneratedImages: []*genai.GeneratedImage{
					{Image: &genai.Image{ImageBytes: data}},
				},
			}, nil).Once()

		resp, err := vc.GenerateImages(ctx, "", "prompt", 1, "1:1")
		is.NoError(err)
		is.Len(resp, 1)
		is.Equal(data, resp[0].ImageBytes)
	})

	t.Run("GenerateStructured Success", func(t *testing.T) {
		resetModelsFromTempDir()
		jsonResp := `{"values":["cs","neuro"],"explanation":"RLHF is a CS/neuro topic."}`
		mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash-lite", mock.Anything, mock.MatchedBy(func(cfg *genai.GenerateContentConfig) bool {
			return cfg.ResponseMIMEType == "application/json" && cfg.ResponseJsonSchema != nil
		})).Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{{Text: jsonResp}}}},
			},
		}, nil).Once()

		result, err := vc.GenerateStructured(ctx, "gemini-2.5-flash-lite", "select domains", "", `{"type":"object"}`, 0.3, 512)
		is.NoError(err)
		is.Equal(jsonResp, result)
	})

	t.Run("GenerateStructured invalid JSON schema", func(t *testing.T) {
		resetModelsFromTempDir()
		_, err := vc.GenerateStructured(ctx, "gemini-2.5-flash-lite", "prompt", "", `{bad json`, 0.3, 512)
		is.Error(err)
		is.Contains(err.Error(), "invalid json_schema")
	})

	t.Run("GenerateStructured model returns non-JSON", func(t *testing.T) {
		resetModelsFromTempDir()
		// The retry loop will call GenerateContent twice (first attempt returns
		// non-JSON, second attempt also returns non-JSON → final error).
		nonJSONResp := &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{{Text: "not json at all"}}}},
			},
		}
		mm.On("GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return(nonJSONResp, nil).Once()
		mm.On("GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return(nonJSONResp, nil).Once()

		_, err := vc.GenerateStructured(ctx, "", "prompt", "", `{"type":"object"}`, 0.3, 512)
		is.Error(err)
		is.Contains(err.Error(), "not valid JSON")
	})

	t.Run("GenerateStructured API error", func(t *testing.T) {
		resetModelsFromTempDir()
		// Both attempts fail → should propagate the error.
		mm.On("GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return(nil, errors.New("vertex api error")).Once()
		mm.On("GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return(nil, errors.New("vertex api error")).Once()

		_, err := vc.GenerateStructured(ctx, "", "prompt", "", `{"type":"object"}`, 0.3, 512)
		is.Error(err)
		is.Contains(err.Error(), "vertex api error")
	})

	t.Run("GenerateStructured rate limit opens cooldown without retry", func(t *testing.T) {
		resetModelsFromTempDir()
		mm.ExpectedCalls = nil
		mm.Calls = nil
		mm.On("GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return(nil, errors.New("Error 429, Message: Resource exhausted")).Once()

		_, err := vc.GenerateStructured(ctx, "", "prompt", "", `{"type":"object"}`, 0.3, 512)
		is.Error(err)
		is.Contains(err.Error(), "Resource exhausted")
		is.True(VertexProviderRateLimitRemaining() > 0)
		mm.AssertExpectations(t)
	})

	t.Run("GenerateStructured skips provider during active cooldown", func(t *testing.T) {
		resetModelsFromTempDir()
		mm.ExpectedCalls = nil
		mm.Calls = nil
		recordVertexProviderRateLimit(time.Now())

		_, err := vc.GenerateStructured(ctx, "", "prompt", "", `{"type":"object"}`, 0.3, 512)
		is.Error(err)
		is.Contains(err.Error(), "rate limited")
		mm.AssertNotCalled(t, "GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("GenerateStructured unsupported parameter error fails fast without retry", func(t *testing.T) {
		resetModelsFromTempDir()
		mm.ExpectedCalls = nil
		mm.Calls = nil
		mm.On("GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return(nil, errors.New("requestClass parameter is not supported in Vertex AI")).Once()

		_, err := vc.GenerateStructured(ctx, "", "prompt", "", `{"type":"object"}`, 0.3, 512)
		is.Error(err)
		is.Contains(err.Error(), "requestClass parameter is not supported in Vertex AI")
		mm.AssertExpectations(t)
	})

	t.Run("GenerateStructured no schema is still valid", func(t *testing.T) {
		resetModelsFromTempDir()
		mm.On("GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.MatchedBy(func(cfg *genai.GenerateContentConfig) bool {
			return cfg.ResponseMIMEType == "application/json" && cfg.ResponseJsonSchema == nil
		})).Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{{Text: `{"ok":true}`}}}},
			},
		}, nil).Once()

		result, err := vc.GenerateStructured(ctx, "", "prompt", "", "", 0.3, 512)
		is.NoError(err)
		is.Equal(`{"ok":true}`, result)
	})

	t.Run("GenerateStructured retries once then succeeds", func(t *testing.T) {
		resetModelsFromTempDir()
		// First call fails, second call succeeds — verify retry works.
		mm.On("GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return(nil, errors.New("transient error")).Once()
		mm.On("GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return(&genai.GenerateContentResponse{
				Candidates: []*genai.Candidate{
					{Content: &genai.Content{Parts: []*genai.Part{{Text: `{"recovered":true}`}}}},
				},
				UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
					PromptTokenCount:     120,
					CandidatesTokenCount: 30,
				},
			}, nil).Once()

		result, err := vc.GenerateStructured(ctx, "", "prompt", "", `{"type":"object"}`, 0.3, 512)
		is.NoError(err)
		is.Equal(`{"recovered":true}`, result)
	})

	t.Run("generateStructuredWithTokens reads real token counts", func(t *testing.T) {
		resetModelsFromTempDir()
		mm.On("GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return(&genai.GenerateContentResponse{
				Candidates: []*genai.Candidate{
					{Content: &genai.Content{Parts: []*genai.Part{{Text: `{"x":1}`}}}},
				},
				UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
					PromptTokenCount:     200,
					CandidatesTokenCount: 15,
				},
			}, nil).Once()

		text, inTok, outTok, err := vc.generateStructuredWithTokens(ctx, "", "prompt", "", `{"type":"object"}`, 0.3, 512, "", nil, "", "")
		is.NoError(err)
		is.Equal(`{"x":1}`, text)
		is.Equal(int32(200), inTok)
		is.Equal(int32(15), outTok)
	})

	t.Run("generateStructuredWithTokens does not forward unsupported service tier", func(t *testing.T) {
		resetModelsFromTempDir()
		mm.ExpectedCalls = nil
		mm.Calls = nil
		mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash-lite", mock.Anything, mock.MatchedBy(func(cfg *genai.GenerateContentConfig) bool {
			return cfg.ServiceTier == ""
		})).Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{{Text: `{"ok":true}`}}}},
			},
		}, nil).Once()

		text, _, _, err := vc.generateStructuredWithTokens(ctx, "gemini-2.5-flash-lite", "prompt", "", `{"type":"object"}`, 0.3, 512, "priority", nil, "structured_high_value", "standard")
		is.NoError(err)
		is.Equal(`{"ok":true}`, text)
	})

	t.Run("generateStructuredWithTokens annotates propertyOrdering for object schemas", func(t *testing.T) {
		resetModelsFromTempDir()
		mm.ExpectedCalls = nil
		mm.Calls = nil
		mm.On("GenerateContent", mock.Anything, "gemini-2.5-flash-lite", mock.Anything, mock.MatchedBy(func(cfg *genai.GenerateContentConfig) bool {
			root, ok := cfg.ResponseJsonSchema.(map[string]any)
			if !ok {
				return false
			}
			ordering, ok := root["propertyOrdering"].([]string)
			if !ok || len(ordering) != 2 {
				return false
			}
			nested, ok := root["properties"].(map[string]any)["payload"].(map[string]any)
			if !ok {
				return false
			}
			nestedOrdering, ok := nested["propertyOrdering"].([]string)
			return ok && len(nestedOrdering) == 1 && ordering[0] == "payload" && nestedOrdering[0] == "value"
		})).Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{{Text: `{"payload":{"value":"ok"},"status":"done"}`}}}},
			},
		}, nil).Once()

		text, _, _, err := vc.generateStructuredWithTokens(
			ctx,
			"gemini-2.5-flash-lite",
			"prompt",
			"",
			`{"type":"object","properties":{"payload":{"type":"object","properties":{"value":{"type":"string"}}},"status":{"type":"string"}}}`,
			0.3,
			512,
			"",
			nil,
			"",
			"",
		)
		is.NoError(err)
		is.Equal(`{"payload":{"value":"ok"},"status":"done"}`, text)
	})
}

func TestNewVertexClientPinsStableAPIVersion(t *testing.T) {
	origNewModelsClient := newModelsClient
	origResolveProjectID := resolveProjectID
	t.Cleanup(func() {
		newModelsClient = origNewModelsClient
		resolveProjectID = origResolveProjectID
	})

	var capturedCfg *genai.ClientConfig
	newModelsClient = func(ctx context.Context, cfg *genai.ClientConfig) (modelsClient, error) {
		captured := *cfg
		capturedCfg = &captured
		return &mockGenAIModels{}, nil
	}
	resolveProjectID = func(ctx context.Context) (string, string) {
		return "project-1", "test"
	}

	client, err := NewVertexClient(context.Background(), "", "us-central1")
	assert.NoError(t, err)
	assert.NotNil(t, client)
	if assert.NotNil(t, capturedCfg) {
		assert.Equal(t, genai.BackendVertexAI, capturedCfg.Backend)
		assert.Equal(t, stableGenAIAPIVersion, capturedCfg.HTTPOptions.APIVersion)
	}
}

func TestClassifyVertexError(t *testing.T) {
	assert.Equal(t, "unsupported_parameter", classifyVertexError(errors.New("serviceTier parameter is not supported in Vertex AI")))
	assert.Equal(t, "unsupported_parameter", classifyVertexError(errors.New("unexpected keyword argument 'service_tier'")))
}

func TestIsRetryableVertexErrorClass(t *testing.T) {
	assert.True(t, isRetryableVertexErrorClass("timeout"))
	assert.True(t, isRetryableVertexErrorClass("rate_limit"))
	assert.True(t, isRetryableVertexErrorClass("unavailable"))
	assert.True(t, isRetryableVertexErrorClass("unknown"))
	assert.False(t, isRetryableVertexErrorClass("invalid_request"))
	assert.False(t, isRetryableVertexErrorClass("unsupported_parameter"))
}

func TestNewVertexClientUsesResolvedProjectID(t *testing.T) {
	oldFactory := newModelsClient
	oldSecretResolver := resolveSecret
	oldProjectResolver := resolveProjectID
	t.Cleanup(func() {
		newModelsClient = oldFactory
		resolveSecret = oldSecretResolver
		resolveProjectID = oldProjectResolver
	})

	var capturedConfig *genai.ClientConfig
	newModelsClient = func(ctx context.Context, cfg *genai.ClientConfig) (modelsClient, error) {
		capturedConfig = cfg
		return new(mockGenAIModels), nil
	}
	resolveSecret = func(ctx context.Context, name string) (string, error) {
		t.Fatalf("unexpected secret lookup for %s", name)
		return "", nil
	}
	resolveProjectID = func(ctx context.Context) (string, string) {
		return "resolved-project", "gcloud_config"
	}

	client, err := NewVertexClient(context.Background(), "", "us-central1")
	assert.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, "vertex_ai", client.BackendName())
	assert.Equal(t, "vertex_ai:gcloud_config", client.CredentialSource())
	assert.Equal(t, "resolved-project", capturedConfig.Project)
	assert.Equal(t, genai.BackendVertexAI, capturedConfig.Backend)
}

func TestNewVertexClientFallsBackToSecretManagerAPIKey(t *testing.T) {
	oldFactory := newModelsClient
	oldSecretResolver := resolveSecret
	oldProjectResolver := resolveProjectID
	t.Cleanup(func() {
		newModelsClient = oldFactory
		resolveSecret = oldSecretResolver
		resolveProjectID = oldProjectResolver
	})

	var configs []*genai.ClientConfig
	newModelsClient = func(ctx context.Context, cfg *genai.ClientConfig) (modelsClient, error) {
		configs = append(configs, cfg)
		if len(configs) == 1 {
			return nil, errors.New("vertex credentials missing")
		}
		return new(mockGenAIModels), nil
	}
	resolveSecret = func(ctx context.Context, name string) (string, error) {
		if name == "GOOGLE_API_KEY" {
			return "secret-api-key", nil
		}
		return "", nil
	}
	resolveProjectID = func(ctx context.Context) (string, string) {
		return "vertex-project", "gcloud_config"
	}

	client, err := NewVertexClient(context.Background(), "", "us-central1")
	assert.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, "gemini_api_secret", client.BackendName())
	assert.Equal(t, "secret_manager:GOOGLE_API_KEY", client.CredentialSource())
	assert.Len(t, configs, 2)
	assert.Equal(t, genai.BackendVertexAI, configs[0].Backend)
	assert.Equal(t, genai.BackendGeminiAPI, configs[1].Backend)
	assert.Equal(t, "secret-api-key", configs[1].APIKey)
}

func TestNewVertexClientUsesSecretManagerAPIKeyWithoutProject(t *testing.T) {
	oldFactory := newModelsClient
	oldSecretResolver := resolveSecret
	oldProjectResolver := resolveProjectID
	t.Cleanup(func() {
		newModelsClient = oldFactory
		resolveSecret = oldSecretResolver
		resolveProjectID = oldProjectResolver
	})

	var capturedConfig *genai.ClientConfig
	newModelsClient = func(ctx context.Context, cfg *genai.ClientConfig) (modelsClient, error) {
		capturedConfig = cfg
		return new(mockGenAIModels), nil
	}
	resolveSecret = func(ctx context.Context, name string) (string, error) { return "", nil }
	resolveProjectID = func(ctx context.Context) (string, string) {
		return "", "none"
	}
	t.Setenv("GOOGLE_API_KEY", "env-api-key")

	client, err := NewVertexClient(context.Background(), "", "us-central1")
	assert.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, "gemini_api_secret", client.BackendName())
	assert.Equal(t, "env:GOOGLE_API_KEY", client.CredentialSource())
	assert.Equal(t, genai.BackendGeminiAPI, capturedConfig.Backend)
	assert.Equal(t, "env-api-key", capturedConfig.APIKey)
}
