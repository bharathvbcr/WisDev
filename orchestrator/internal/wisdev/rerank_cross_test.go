package wisdev

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

type stubCrossRerankStructuredGenerator struct {
	text   string
	err    error
	model  string
	prompt string
	schema string
}

func (s *stubCrossRerankStructuredGenerator) GenerateStructured(ctx context.Context, modelID, prompt, systemPrompt string, jsonSchemaStr string, temperature float32, maxTokens int32) (string, error) {
	s.model = modelID
	s.prompt = prompt
	s.schema = jsonSchemaStr
	return s.text, s.err
}

func TestRerankPapersStage2_Fallback(t *testing.T) {
	// Ensure no API key to force fallback
	// os.Unsetenv("GOOGLE_API_KEY") // Caution: may affect other tests if run in parallel

	query := "machine learning"
	papers := []Source{
		{Title: "Cooking recipes", Summary: "How to bake a cake"},
		{Title: "Deep Learning", Summary: "Neural networks and machine learning"},
	}

	reranked := rerankPapersStage2(context.Background(), query, papers, "ai", 2)
	assert.Len(t, reranked, 2)
	assert.Equal(t, "Deep Learning", reranked[0].Title)
}

func TestLexicalRelevance(t *testing.T) {
	query := "quantum computing"
	paper := Source{
		Title:   "Intro to Quantum Computing",
		Summary: "Basics of qubits and gates",
	}
	score := lexicalRelevance(query, paper)
	assert.Greater(t, score, 0.0)

	paper2 := Source{Title: "Baking", Summary: "Flour and water"}
	score2 := lexicalRelevance(query, paper2)
	assert.Equal(t, 0.0, score2)
}

func TestComputePaperQualitySignal(t *testing.T) {
	paper := Source{
		CitationCount: 1000,
		Summary:       "Important paper",
		DOI:           "10.1234/test",
	}
	score := computePaperQualitySignal(paper)
	assert.Greater(t, score, 0.5)
}

func TestComputeDomainBoost(t *testing.T) {
	paper := Source{
		Title:   "Randomized Clinical Trial of drug X",
		Summary: "Medicine study",
	}
	boost := computeDomainBoost("medicine", paper)
	assert.Equal(t, 0.8, boost)

	boost2 := computeDomainBoost("history", paper)
	assert.Equal(t, 0.5, boost2)
}

func TestFetchGeminiRerankScoresStructuredFailure(t *testing.T) {
	origGenerator := newCrossRerankStructuredGenerator
	t.Cleanup(func() { newCrossRerankStructuredGenerator = origGenerator })
	newCrossRerankStructuredGenerator = func(context.Context) (rerankStructuredGenerator, error) {
		return nil, errors.New("native structured unavailable")
	}

	scores, ok := fetchGeminiRerankScores(context.Background(), "machine learning", []Source{{Title: "A"}})
	assert.False(t, ok)
	assert.Equal(t, []float64{0}, scores)
}

func TestFetchGeminiRerankScoresStructuredSuccess(t *testing.T) {
	origGenerator := newCrossRerankStructuredGenerator
	t.Cleanup(func() { newCrossRerankStructuredGenerator = origGenerator })
	stub := &stubCrossRerankStructuredGenerator{text: `{"scores":[{"index":0,"score":0.91},{"index":1,"score":0.33}]}`}
	newCrossRerankStructuredGenerator = func(context.Context) (rerankStructuredGenerator, error) {
		return stub, nil
	}

	t.Setenv("AI_MODEL_RERANK_ID", "rerank-model")
	scores, ok := fetchGeminiRerankScores(context.Background(), "machine learning", []Source{
		{Title: "Deep Learning", Summary: "Neural networks"},
		{Title: "Cooking", Summary: "Recipes"},
	})

	assert.True(t, ok)
	assert.Equal(t, []float64{0.91, 0.33}, scores)
	assert.Equal(t, "rerank-model", stub.model)
	assert.Contains(t, stub.schema, `"scores"`)
	assert.Contains(t, stub.prompt, "supplied schema")
	assert.NotContains(t, buildRerankPrompt("ml", []Source{{Title: "A"}}), "Return strict JSON")
	assert.True(t, strings.Contains(stub.prompt, "machine learning"))
}
