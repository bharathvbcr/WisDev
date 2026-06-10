package wisdev

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

func TestApplyMultiSourceScoreBoost(t *testing.T) {
	sources := []Source{
		{ID: "s1", SourceCount: 1, Score: 0.5},
		{ID: "s2", SourceCount: 2, Score: 0.5},
		{ID: "s3", SourceCount: 3, Score: 0.0},
	}

	boosted := applyMultiSourceScoreBoost(sources)
	assert.Equal(t, 0.5, boosted[0].Score)
	assert.Equal(t, 0.7, boosted[1].Score)
	assert.Equal(t, 0.2, boosted[2].Score)
}

func TestExpandQueryWithLLMPrep(t *testing.T) {
	preparedQueryCache = sync.Map{}
	msc := &mockLLMServiceClient{}
	client := llm.NewClient()
	client.SetClient(msc)
	payload := structuredResearchQueryPrep{
		CorrectedQuery: "large language model transformer",
		SearchQuery:    "large language model transformer",
		Domain:         "cs",
		Intent:         "computer_science",
		Keywords:       []string{"large", "language", "model", "transformer"},
		Synonyms:       []string{"LLM", "foundation model"},
		SeedQueries:    []string{"large language model transformer"},
		AgendaQueries:  []string{"large language model transformer systematic review"},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Prepare this academic research query")
	})).Return(&llmv1.StructuredResponse{JsonResult: string(data)}, nil).Once()

	expanded := ExpandQueryWithContext(context.Background(), "large language model transformer", client)
	assert.Contains(t, expanded.Expanded, "LLM")
	assert.Equal(t, "computer_science", expanded.Intent)
	assert.Contains(t, expanded.Keywords, "transformer")
}

func TestExpandQueryOfflineFallback(t *testing.T) {
	oldClient := GlobalLLMClient
	GlobalLLMClient = nil
	defer func() { GlobalLLMClient = oldClient }()
	preparedQueryCache = sync.Map{}

	expanded := ExpandQueryWithContext(context.Background(), "abc def ghi", nil)
	assert.Equal(t, "abc def ghi", expanded.Expanded)
	assert.Equal(t, "academic", expanded.Intent)
}
