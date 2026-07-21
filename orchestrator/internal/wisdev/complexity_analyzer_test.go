package wisdev

import (
	"context"
	"strings"
	"testing"
)

func TestComplexityAnalyzerScoringAndTiers(t *testing.T) {
	analyzer := NewComplexityAnalyzer()

	base := analyzer.AnalyzeTask(context.Background(), "explain transformer attention", nil)
	if base.RecommendedTier != ModelTierLight {
		t.Fatalf("expected light tier for base query, got %q", base.RecommendedTier)
	}
	if base.EstimatedTokens != 12000+3*260 {
		t.Fatalf("unexpected base token estimate: %d", base.EstimatedTokens)
	}
	if got := base.Score; got < 0.305-0.0001 || got > 0.305+0.0001 {
		t.Fatalf("unexpected base score %f", got)
	}

	heavyQuery := strings.Repeat("word ", 90) + " systematic review benchmark"
	heavy := analyzer.AnalyzeTask(context.Background(), heavyQuery, map[string]interface{}{
		"agentic_mode":     "deep",
		"hypothesis_count": float64(8),
	})
	if heavy.RecommendedTier != ModelTierHeavy {
		t.Fatalf("expected heavy tier for deep + high complexity query, got %q", heavy.RecommendedTier)
	}
	if heavy.EstimatedTokens != 12000+len(strings.Fields(heavyQuery))*260+18000 {
		t.Fatalf("unexpected heavy token estimate: %d", heavy.EstimatedTokens)
	}
	if heavy.Score < 0.75 {
		t.Fatalf("expected heavy score, got %f", heavy.Score)
	}
}

func TestComplexityAnalyzerQuickModeAndStandardCutoff(t *testing.T) {
	analyzer := NewComplexityAnalyzer()

	metadata := map[string]interface{}{
		"agentic_mode": "quick",
	}
	result := analyzer.AnalyzeTask(context.Background(), "compare two optimization methods in deep learning", metadata)
	if result.RecommendedTier != ModelTierStandard {
		t.Fatalf("expected quick mode to fall back to standard, got %q", result.RecommendedTier)
	}

	standard := analyzer.AnalyzeTask(context.Background(), "compare benchmark ablation systematic review methods", map[string]interface{}{
		"hypothesis_count": 3,
	})
	if standard.RecommendedTier != ModelTierStandard {
		t.Fatalf("expected standard tier for review/benchmark-heavy query, got %q", standard.RecommendedTier)
	}
}

func TestComplexityMetadataConverters(t *testing.T) {
	if got := complexityMetadataFloat(3.5); got != 3.5 {
		t.Fatalf("expected float64 passthrough, got %f", got)
	}
	if got := complexityMetadataFloat(float32(2.25)); got != 2.25 {
		t.Fatalf("expected float32 conversion, got %f", got)
	}
	if got := complexityMetadataFloat(int(4)); got != 4 {
		t.Fatalf("expected int conversion, got %f", got)
	}
	if got := complexityMetadataFloat(int32(7)); got != 7 {
		t.Fatalf("expected int32 conversion, got %f", got)
	}
	if got := complexityMetadataFloat(int64(9)); got != 9 {
		t.Fatalf("expected int64 conversion, got %f", got)
	}
	if got := complexityMetadataFloat("bad"); got != 0 {
		t.Fatalf("expected default zero for invalid type, got %f", got)
	}

	if got := complexityMetadataString("deep"); got != "deep" {
		t.Fatalf("expected string passthrough")
	}
	if got := complexityMetadataString(7); got != "" {
		t.Fatalf("expected empty for non-string metadata")
	}
}

func TestComplexityAnalyzerClampBounds(t *testing.T) {
	analyzer := NewComplexityAnalyzer()

	// Upper clamp: absurdly long query + aggressive metadata should not exceed 1.0.
	upper := analyzer.AnalyzeTask(context.Background(), strings.Repeat("token ", 1000), map[string]interface{}{
		"agentic_mode":     "deep",
		"hypothesis_count": 999,
	})
	if upper.Score > 1.0 {
		t.Fatalf("expected upper-clamped score <= 1.0, got %f", upper.Score)
	}

	// Lower clamp: crafted negative score path is prevented by lower bound.
	lower := analyzer.AnalyzeTask(context.Background(), "", map[string]interface{}{
		"agentic_mode": "quick",
	})
	if lower.Score < 0.08 {
		t.Fatalf("expected lower-clamped score >= 0.08, got %f", lower.Score)
	}
}
