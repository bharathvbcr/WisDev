package wisdev

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
)

func TestGenerateAggressiveExpansionWithLLM(t *testing.T) {
	oldClient := GlobalLLMClient
	defer func() { GlobalLLMClient = oldClient }()

	msc := &mockLLMServiceClient{}
	client := llm.NewClient()
	client.SetClient(msc)
	GlobalLLMClient = client

	payload := structuredQueryVariations{
		Variations: []QueryVariation{
			{Query: "transformer attention models", Strategy: "synonym", Priority: 8},
			{Query: "foundation model transformers", Strategy: "broader", Priority: 7},
		},
		PrimaryKeywords: []string{"transformer", "models"},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Generate diverse academic search query variations")
	})).Return(&llmv1.StructuredResponse{JsonResult: string(data)}, nil).Once()

	query := "transformer models"
	resp := GenerateAggressiveExpansionWithContext(context.Background(), nil, query, 5, true, true, true, []string{"arxiv"})

	assert.Equal(t, query, resp.Original)
	assert.NotEmpty(t, resp.Variations)
	assert.LessOrEqual(t, len(resp.Variations), 5)
	assert.Contains(t, resp.Metadata.Strategies, "original")
}

func TestCalculateCoverageEstimate(t *testing.T) {
	cov := calculateCoverageEstimate(15, 10)
	assert.Equal(t, 1.0, cov)

	cov2 := calculateCoverageEstimate(0, 0)
	assert.Equal(t, 0.0, cov2)
}

func TestDeduplicateQueryVariations(t *testing.T) {
	vs := []QueryVariation{
		{Query: "Test", Strategy: "s1"},
		{Query: " test ", Strategy: "s2"},
		{Query: "unique", Strategy: "s3"},
	}
	deduped := deduplicateQueryVariations(vs)
	assert.Len(t, deduped, 2)
}
