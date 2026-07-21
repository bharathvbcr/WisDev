package wisdev

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

type testHypModel struct{}

func (m *testHypModel) GenerateHypotheses(ctx context.Context, query string) ([]string, error) {
	return []string{"h-a", "h-b"}, nil
}

func (m *testHypModel) ExtractClaims(ctx context.Context, text string) ([]string, error) {
	return []string{"c1"}, nil
}

func (m *testHypModel) VerifyClaim(ctx context.Context, claim, evidence string) (bool, float64, error) {
	return true, 0.9, nil
}

func (m *testHypModel) SynthesizeFindings(ctx context.Context, hypotheses []string, evidence map[string]interface{}) (string, error) {
	return "ok", nil
}

func (m *testHypModel) CritiqueFindings(ctx context.Context, findings []string) (string, error) {
	return "critique", nil
}

func (m *testHypModel) Generate(ctx context.Context, prompt string) (string, error) {
	return "", nil
}

func (m *testHypModel) Name() string { return "test" }

func (m *testHypModel) Tier() ModelTier { return ModelTierLight }

func TestHypothesisAgent_Generate_FromModel(t *testing.T) {
	agent := NewHypothesisAgent(&testHypModel{})
	items, err := agent.Generate(context.Background(), "sleep and memory", 5)
	assert.NoError(t, err)
	assert.Len(t, items, 2)
	assert.Equal(t, "h-a", items[0].Text)
}

func TestHypothesisAgent_Generate_Fallback(t *testing.T) {
	agent := NewHypothesisAgent(nil)
	items, err := agent.Generate(context.Background(), "sleep and memory", 3)
	assert.NoError(t, err)
	assert.Len(t, items, 3)
	assert.Contains(t, items[0].Text, "Hypothesis 1")
}
