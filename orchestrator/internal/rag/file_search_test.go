package rag

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeFileSearchGenerator struct {
	req llm.FileSearchGenerationRequest
	res *llm.FileSearchGenerationResponse
	err error
}

func (f *fakeFileSearchGenerator) GenerateWithFileSearch(_ context.Context, req llm.FileSearchGenerationRequest) (*llm.FileSearchGenerationResponse, error) {
	f.req = req
	if f.err != nil {
		return nil, f.err
	}
	return f.res, nil
}

func TestEngineGenerateAnswerUsesGeminiFileSearchWhenConfigured(t *testing.T) {
	generator := &fakeFileSearchGenerator{res: &llm.FileSearchGenerationResponse{
		Text:             "The microscopy panel supports the localization claim.",
		Backend:          "gemini_api_file_search",
		RetrievalQueries: []string{"microscopy localization"},
		Citations: []llm.FileSearchCitation{{
			Claim:           "The microscopy panel supports the localization claim.",
			SourceID:        "supplement.pdf",
			SourceTitle:     "Supplemental Figure Pack",
			SourceURI:       "https://example.test/supplement.pdf",
			FileSearchStore: "fileSearchStores/science",
			PageStart:       7,
			PageEnd:         7,
			MediaID:         "figure-2",
			MediaURI:        "https://example.test/figure-2.png",
			SourceKind:      "file_search_image",
			Confidence:      0.93,
			CustomMetadata:  map[string]any{"modality": "image", "paper_type": "supplement"},
		}},
	}}
	embeddingModel := DefaultFileSearchEmbeddingModel()
	engine := NewEngineWithConfig(nil, nil, EngineConfig{
		FileSearchGenerator: generator,
		DefaultFileSearch: FileSearchConfig{
			Enabled:        true,
			StoreNames:     []string{"fileSearchStores/science"},
			MetadataFilter: `paper_type = "supplement"`,
			TopK:           5,
			Multimodal:     true,
			EmbeddingModel: embeddingModel,
		},
		CanonicalRetriever: func(context.Context, AnswerRequest) (*CanonicalRetrievalResult, error) {
			return &CanonicalRetrievalResult{
				QueryUsed: "protein localization microscopy",
				Backend:   "go-wisdev-canonical",
				Papers: []search.Paper{{
					ID:       "p1",
					Title:    "Protein localization study",
					Abstract: "Microscopy confirms localization.",
				}},
			}, nil
		},
	})

	resp, err := engine.GenerateAnswer(context.Background(), AnswerRequest{Query: "Where is the protein localized?"})
	require.NoError(t, err)
	assert.Equal(t, "The microscopy panel supports the localization claim.", resp.Answer)
	require.NotNil(t, resp.Metadata)
	assert.Equal(t, "gemini_api_file_search", resp.Metadata.Backend)
	assert.Contains(t, resp.Metadata.RetrievalStrategies, "gemini_file_search")
	require.NotNil(t, resp.Metadata.FileSearch)
	assert.True(t, resp.Metadata.FileSearch.Multimodal)
	assert.Equal(t, embeddingModel, resp.Metadata.FileSearch.EmbeddingModel)
	assert.Equal(t, 1, resp.Metadata.FileSearch.PageCitationCount)
	assert.Equal(t, 1, resp.Metadata.FileSearch.MediaCitationCount)
	require.Len(t, resp.Citations, 1)
	assert.Equal(t, 7, resp.Citations[0].PageStart)
	assert.Equal(t, "figure-2", resp.Citations[0].MediaID)
	assert.Equal(t, "file_search_image", resp.Citations[0].SourceKind)
	assert.Equal(t, "supplement", resp.Citations[0].CustomMetadata["paper_type"])
	assert.Equal(t, []string{"fileSearchStores/science"}, generator.req.StoreNames)
	assert.Equal(t, `paper_type = "supplement"`, generator.req.MetadataFilter)
	assert.Equal(t, 5, generator.req.TopK)
	assert.True(t, strings.Contains(generator.req.Prompt, "Search across text, PDF pages, tables, figures, and indexed images"))
}

func TestEngineGenerateAnswerFallsBackWhenOptionalFileSearchFails(t *testing.T) {
	generator := &fakeFileSearchGenerator{err: errors.New("file search unavailable")}
	engine := NewEngineWithConfig(nil, nil, EngineConfig{
		FileSearchGenerator: generator,
		DefaultFileSearch: FileSearchConfig{
			Enabled:    true,
			StoreNames: []string{"fileSearchStores/science"},
			Multimodal: true,
		},
		CanonicalRetriever: func(context.Context, AnswerRequest) (*CanonicalRetrievalResult, error) {
			return &CanonicalRetrievalResult{
				QueryUsed: "fallback query",
				Backend:   "go-wisdev-canonical",
				Papers: []search.Paper{{
					ID:       "p1",
					Title:    "Fallback paper",
					Abstract: "Fallback evidence.",
				}},
			}, nil
		},
	})

	resp, err := engine.GenerateAnswer(context.Background(), AnswerRequest{Query: "q"})
	require.NoError(t, err)
	assert.Contains(t, resp.Answer, "LLM synthesis is currently unavailable")
	require.NotNil(t, resp.Metadata)
	assert.True(t, resp.Metadata.FallbackTriggered)
	assert.Equal(t, "gemini_file_search_failed", resp.Metadata.FallbackReason)
	require.NotNil(t, resp.Metadata.FileSearch)
	assert.Equal(t, "gemini_file_search_failed", resp.Metadata.FileSearch.FallbackReason)
}

func TestEngineGenerateAnswerFailsWhenRequiredFileSearchFails(t *testing.T) {
	generator := &fakeFileSearchGenerator{err: errors.New("file search unavailable")}
	engine := NewEngineWithConfig(nil, nil, EngineConfig{
		FileSearchGenerator: generator,
		DefaultFileSearch: FileSearchConfig{
			Enabled:    true,
			Required:   true,
			StoreNames: []string{"fileSearchStores/science"},
		},
		CanonicalRetriever: func(context.Context, AnswerRequest) (*CanonicalRetrievalResult, error) {
			return &CanonicalRetrievalResult{QueryUsed: "q"}, nil
		},
	})

	_, err := engine.GenerateAnswer(context.Background(), AnswerRequest{Query: "q"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gemini file search failed")
}
