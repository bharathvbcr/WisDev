package search

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetPrecachedExpansion_DirectMatch(t *testing.T) {
	result := GetPrecachedExpansion("machine learning")
	assert.NotNil(t, result)
	assert.Equal(t, "machine learning", result.Original)
	assert.Contains(t, result.Keywords, "ML")
	assert.Equal(t, IntentPapers, result.Intent)
}

func TestGetPrecachedExpansion_PartialMatch(t *testing.T) {
	result := GetPrecachedExpansion("recent machine learning advances")
	assert.NotNil(t, result)
	assert.Equal(t, "recent machine learning advances", result.Original)
	assert.Contains(t, result.Expanded, "recent machine learning advances")
}

func TestGetPrecachedExpansion_SpecializedMetadata(t *testing.T) {
	result := GetPrecachedExpansion("pytorch")
	assert.NotNil(t, result)
	assert.NotNil(t, result.SpecializedMetadata)
	assert.Equal(t, "ml", result.SpecializedMetadata.ModelType)
	assert.Contains(t, result.SpecializedMetadata.Frameworks, "PyTorch")
}

func TestLookupStaticExpansions(t *testing.T) {
	result := LookupStaticExpansions("BERT fairness")
	assert.Contains(t, result.RelatedConcepts, "transformer")
	assert.Contains(t, result.BroaderTerms, "natural language processing")
}

func TestLookupMesh(t *testing.T) {
	assert.Contains(t, LookupMesh("heart attack"), "myocardial infarction")
}

func TestAnalyzeQuerySpecificity(t *testing.T) {
	year := AnalyzeQuerySpecificity("machine learning 2023")
	assert.True(t, year.IsSpecific)
	assert.Contains(t, year.Reason, "year")

	many := AnalyzeQuerySpecificity("deep learning transformer attention mechanism neural network optimization")
	assert.True(t, many.IsSpecific)
	assert.Contains(t, many.Reason, "many terms")

	balanced := AnalyzeQuerySpecificity("machine learning fairness")
	assert.False(t, balanced.IsSpecific)
	assert.Contains(t, balanced.Reason, "well-balanced")
}

func TestGenerateQueryVariationsForCoverage(t *testing.T) {
	expansion := QueryExpansion{
		Original: "machine learning",
		Expansions: QueryExpansionCategories{
			Synonyms:        []string{"ML", "statistical learning"},
			BroaderTerms:    []string{"artificial intelligence"},
			RelatedConcepts: []string{"deep learning", "neural networks"},
		},
	}

	variations := GenerateQueryVariationsForCoverage("machine learning", expansion, 4)
	assert.Contains(t, variations, "machine learning")
	assert.True(t, len(variations) >= 2)
}

func TestGenerateBroaderStrategies(t *testing.T) {
	expansion := QueryExpansion{
		Expansions: QueryExpansionCategories{
			RelatedConcepts: []string{"bias detection", "model evaluation"},
			BroaderTerms:    []string{"transformer models", "NLP evaluation"},
		},
	}

	strategies := GenerateBroaderStrategies("BERT fairness metrics evaluation", expansion)
	names := make([]string, 0, len(strategies))
	for _, strategy := range strategies {
		names = append(names, strategy.Name)
		assert.NotEmpty(t, strategy.Reason)
	}
	assert.Contains(t, names, "Simplified")
	assert.Contains(t, names, "Generalized")
}

func TestGetOptimizedQuerySet_SpecificQuery(t *testing.T) {
	expansion := QueryExpansion{
		Expansions: QueryExpansionCategories{
			Synonyms:     []string{"syn"},
			BroaderTerms: []string{"broader topic"},
		},
	}

	queries := GetOptimizedQuerySet("machine learning 2023 AND neural", expansion, 25)
	assert.NotEmpty(t, queries)
	assert.Contains(t, queries, "machine learning 2023 AND neural")
}

func TestEmbeddingTargets(t *testing.T) {
	targets := EmbeddingTargets()
	assert.Contains(t, targets, "machine learning")
	assert.Contains(t, targets, "myocardial infarction")
}

func TestNormalizeQueryIntent(t *testing.T) {
	intent, ok := NormalizeQueryIntent("TRENDS")
	assert.True(t, ok)
	assert.Equal(t, IntentTrends, intent)

	_, ok = NormalizeQueryIntent("unknown-intent")
	assert.False(t, ok)
}

func TestDetectQueryIntentHeuristic(t *testing.T) {
	cases := []struct {
		query  string
		intent QueryIntent
		ok     bool
	}{
		{"what is machine learning", IntentDefinition, true},
		{"define neural network", IntentDefinition, true},
		{"meaning of BERT", IntentDefinition, true},
		{"BERT vs LLM", IntentComparison, true},
		{"compare CNN and RNN", IntentComparison, true},
		{"difference between transformers and RNNs", IntentComparison, true},
		{"literature review on NLP", IntentReview, true},
		{"overview of deep learning", IntentReview, true},
		{"state of the art in computer vision", IntentReview, true},
		{"how to train a neural network", IntentMethodology, true},
		{"methods for text classification", IntentMethodology, true},
		{"protocol for sentiment analysis", IntentMethodology, true},
		{"recent advances in AI", IntentTrends, true},
		{"latest developments in LLMs", IntentTrends, true},
		{"AI trends 2024", IntentTrends, true},
		{"transformer architecture", IntentPapers, false},
		{"attention mechanism", IntentPapers, false},
	}
	for _, tc := range cases {
		intent, ok := DetectQueryIntentHeuristic(tc.query)
		assert.Equal(t, tc.ok, ok, tc.query)
		if tc.ok {
			assert.Equal(t, tc.intent, intent, tc.query)
		}
	}
}

