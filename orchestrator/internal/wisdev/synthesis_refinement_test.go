package wisdev

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockSynthesisModel struct {
	lastPrompt   string
	generate     string
	generateErr  error
	synthesis    string
	synthesisErr error
}

func (m *mockSynthesisModel) Generate(ctx context.Context, prompt string) (string, error) {
	m.lastPrompt = prompt
	if m.generateErr != nil {
		return "", m.generateErr
	}
	if m.generate != "" {
		return m.generate, nil
	}
	return "draft-v1", nil
}
func (m *mockSynthesisModel) GenerateHypotheses(ctx context.Context, query string) ([]string, error) {
	return nil, nil
}
func (m *mockSynthesisModel) ExtractClaims(ctx context.Context, text string) ([]string, error) {
	return nil, nil
}
func (m *mockSynthesisModel) VerifyClaim(ctx context.Context, claim, evidence string) (bool, float64, error) {
	return false, 0, nil
}
func (m *mockSynthesisModel) SynthesizeFindings(ctx context.Context, hypotheses []string, evidence map[string]interface{}) (string, error) {
	m.lastPrompt = ""
	for _, h := range hypotheses {
		m.lastPrompt += h
	}
	m.lastPrompt += fmt.Sprintf("%d", len(evidence))
	if m.synthesisErr != nil {
		return "", m.synthesisErr
	}
	if m.synthesis != "" {
		return m.synthesis, nil
	}
	return "synthesized", nil
}
func (m *mockSynthesisModel) CritiqueFindings(ctx context.Context, findings []string) (string, error) {
	return "critique", nil
}
func (m *mockSynthesisModel) Name() string    { return "test-model" }
func (m *mockSynthesisModel) Tier() ModelTier { return ModelTierStandard }

func TestSynthesisRefiner_InitialSynthesis(t *testing.T) {
	model := &mockSynthesisModel{}
	refiner := NewSynthesisRefiner(model)
	output, err := refiner.InitialSynthesis(context.Background(), []*Hypothesis{
		{ID: "h1", Text: "A"},
		{ID: "h2", Text: "B"},
	})
	require.NoError(t, err)
	assert.Equal(t, "synthesized", output)
	assert.Contains(t, model.lastPrompt, "AB")
}

func TestSynthesisRefiner_RefineDraft(t *testing.T) {
	model := &mockSynthesisModel{}
	refiner := NewSynthesisRefiner(model)
	output, err := refiner.RefineDraft(context.Background(), "draft text", "revise this", []*EvidenceFinding{{ID: "e1"}})
	require.NoError(t, err)
	assert.Equal(t, "draft-v1", output)
	assert.Contains(t, model.lastPrompt, "Original Draft")
	assert.Contains(t, model.lastPrompt, "New Evidence")
	assert.Contains(t, model.lastPrompt, "revise this")

	nilRefiner := NewSynthesisRefiner(nil)
	output, err = nilRefiner.RefineDraft(context.Background(), " draft text ", "revise this", nil)
	require.NoError(t, err)
	assert.Equal(t, "draft text", output)

	blankModel := &mockSynthesisModel{generate: "   "}
	refiner = NewSynthesisRefiner(blankModel)
	output, err = refiner.RefineDraft(context.Background(), " draft text ", "revise this", nil)
	require.NoError(t, err)
	assert.Equal(t, "draft text", output)

	cooldownModel := &mockSynthesisModel{generateErr: errors.New("vertex text generation provider cooldown active; retry after 45s")}
	refiner = NewSynthesisRefiner(cooldownModel)
	output, err = refiner.RefineDraft(context.Background(), " draft text ", "revise this", nil)
	require.NoError(t, err)
	assert.Equal(t, "draft text", output)
}

func TestSynthesisRefiner_InitialSynthesisFallbacks(t *testing.T) {
	refiner := NewSynthesisRefiner(nil)
	output, err := refiner.InitialSynthesis(context.Background(), []*Hypothesis{{Text: "A"}})
	require.NoError(t, err)
	assert.Equal(t, "Synthesis: A", output)

	blankModel := &mockSynthesisModel{synthesis: "   "}
	refiner = NewSynthesisRefiner(blankModel)
	output, err = refiner.InitialSynthesis(context.Background(), []*Hypothesis{{ID: "h1", Text: "A"}})
	require.NoError(t, err)
	assert.Equal(t, "Synthesis: A", output)

	cooldownModel := &mockSynthesisModel{synthesisErr: errors.New("vertex structured output provider cooldown active; retry after 45s")}
	refiner = NewSynthesisRefiner(cooldownModel)
	output, err = refiner.InitialSynthesis(context.Background(), []*Hypothesis{{ID: "h1", Text: "A"}})
	require.NoError(t, err)
	assert.Equal(t, "Synthesis: A", output)
}
