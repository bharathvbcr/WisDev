package wisdev

import (
	"context"
	"strings"
)

type ComplexityAssessment struct {
	Score           float64
	RecommendedTier ModelTier
	EstimatedTokens int
}

type ComplexityAnalyzer struct{}

func NewComplexityAnalyzer() *ComplexityAnalyzer {
	return &ComplexityAnalyzer{}
}

func (a *ComplexityAnalyzer) AnalyzeTask(_ context.Context, query string, metadata map[string]interface{}) ComplexityAssessment {
	score := 0.18
	wordCount := len(strings.Fields(strings.TrimSpace(query)))
	score += ClampFloat(float64(wordCount)/24.0, 0, 0.22)

	switch strings.TrimSpace(strings.ToLower(complexityMetadataString(metadata["agentic_mode"]))) {
	case "deep":
		score += 0.22
	case "quick":
		score -= 0.05
	}

	if hypothesisCount := complexityMetadataFloat(metadata["hypothesis_count"]); hypothesisCount > 0 {
		score += ClampFloat((hypothesisCount-3)/12.0, 0, 0.18)
	}

	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	if strings.Contains(lowerQuery, "systematic review") || strings.Contains(lowerQuery, "meta-analysis") {
		score += 0.12
	}
	if strings.Contains(lowerQuery, "benchmark") || strings.Contains(lowerQuery, "ablation") {
		score += 0.08
	}

	score = ClampFloat(score, 0.08, 1.0)

	recommendedTier := ModelTierLight
	switch {
	case score >= 0.75:
		recommendedTier = ModelTierHeavy
	case score >= 0.35:
		recommendedTier = ModelTierStandard
	}

	estimatedTokens := 12000 + wordCount*260
	switch recommendedTier {
	case ModelTierHeavy:
		estimatedTokens += 18000
	case ModelTierStandard:
		estimatedTokens += 7000
	}

	return ComplexityAssessment{
		Score:           score,
		RecommendedTier: recommendedTier,
		EstimatedTokens: estimatedTokens,
	}
}

func complexityMetadataFloat(v interface{}) float64 {
	switch value := v.(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int32:
		return float64(value)
	case int64:
		return float64(value)
	default:
		return 0
	}
}

func complexityMetadataString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
