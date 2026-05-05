package wisdev

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockMethodologyModel struct {
	critiqueResult string
	critiqueErr    error
}

func (m *mockMethodologyModel) Generate(ctx context.Context, prompt string) (string, error) {
	return "", nil
}
func (m *mockMethodologyModel) GenerateHypotheses(ctx context.Context, query string) ([]string, error) {
	return nil, nil
}
func (m *mockMethodologyModel) ExtractClaims(ctx context.Context, text string) ([]string, error) {
	return nil, nil
}
func (m *mockMethodologyModel) VerifyClaim(ctx context.Context, claim, evidence string) (bool, float64, error) {
	return false, 0, nil
}
func (m *mockMethodologyModel) SynthesizeFindings(ctx context.Context, hypotheses []string, evidence map[string]interface{}) (string, error) {
	return "", nil
}
func (m *mockMethodologyModel) CritiqueFindings(ctx context.Context, findings []string) (string, error) {
	return m.critiqueResult, m.critiqueErr
}
func (m *mockMethodologyModel) Name() string    { return "test-model" }
func (m *mockMethodologyModel) Tier() ModelTier { return ModelTierStandard }

func TestMethodologyAgent_Critique(t *testing.T) {
	agent := NewMethodologyAgent(&mockMethodologyModel{critiqueResult: "strong evidence"})
	empty, err := agent.Critique(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "No hypotheses provided for critique.", empty)

	h := &Hypothesis{
		Text:            "Learning changes with sleep",
		ConfidenceScore: 0.84,
		Evidence: []*EvidenceFinding{
			{
				PaperTitle:   "Sleep Review",
				Claim:        "Sleep supports consolidation",
				OverlapRatio: 0.73,
			},
		},
	}
	got, err := agent.Critique(context.Background(), []*Hypothesis{h})
	require.NoError(t, err)
	assert.Equal(t, "strong evidence", got)

	nilModelAgent := NewMethodologyAgent(nil)
	got, err = nilModelAgent.Critique(context.Background(), []*Hypothesis{h})
	require.NoError(t, err)
	assert.Contains(t, got, "Methodology critique (heuristic fallback):")
	assert.Contains(t, got, "relies on a single supporting source")

	blankAgent := NewMethodologyAgent(&mockMethodologyModel{critiqueResult: "   "})
	got, err = blankAgent.Critique(context.Background(), []*Hypothesis{h})
	require.NoError(t, err)
	assert.Contains(t, got, "Methodology critique (heuristic fallback):")

	errAgent := NewMethodologyAgent(&mockMethodologyModel{critiqueErr: errors.New("llm failed")})
	got, err = errAgent.Critique(context.Background(), []*Hypothesis{h})
	require.NoError(t, err)
	assert.Contains(t, got, "Methodology critique (heuristic fallback):")
}

func TestMethodologyAgent_IdentifyWeaknesses(t *testing.T) {
	agent := NewMethodologyAgent(&mockMethodologyModel{critiqueResult: "methodology gap"})
	weaknesses, err := agent.IdentifyWeaknesses(context.Background(), []*Hypothesis{})
	require.NoError(t, err)
	assert.Equal(t, []string{"No hypotheses provided for critique."}, weaknesses)

	agentErr := NewMethodologyAgent(&mockMethodologyModel{critiqueErr: errors.New("llm failed")})
	weaknesses, err = agentErr.IdentifyWeaknesses(context.Background(), []*Hypothesis{{Text: "x"}})
	require.NoError(t, err)
	assert.NotEmpty(t, weaknesses)
}

func TestMethodologyAgent_IdentifyWeaknesses_HeuristicFallback(t *testing.T) {
	agent := NewMethodologyAgent(nil)
	weaknesses, err := agent.IdentifyWeaknesses(context.Background(), []*Hypothesis{{
		Text:                "Intervention effect",
		ConfidenceScore:     0.34,
		ConfidenceThreshold: 0.7,
		ContradictionCount:  1,
		Evidence: []*EvidenceFinding{{
			Claim:        "weakly aligned finding",
			Confidence:   0.3,
			OverlapRatio: 0.2,
		}},
	}})
	require.NoError(t, err)
	assert.Contains(t, weaknesses, "Intervention effect relies on a single supporting source, so source diversity and replication are weak.")
	assert.Contains(t, weaknesses, "Intervention effect remains low-confidence (0.34) and needs stronger support or narrower scope.")
	assert.Contains(t, weaknesses, "Intervention effect is below its confidence threshold (0.34 < 0.70).")
	assert.Contains(t, weaknesses, "Intervention effect has 1 contradictory evidence item(s) that are not yet resolved.")
	assert.Contains(t, weaknesses, "Intervention effect has 1 evidence item(s) with weak provenance metadata.")
}
