package wisdev

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/rag"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

type swarmMockModel struct {
	name string
}

func (m *swarmMockModel) Generate(ctx context.Context, p string) (string, error) { return "gen", nil }
func (m *swarmMockModel) GenerateHypotheses(ctx context.Context, q string) ([]string, error) {
	return nil, nil
}
func (m *swarmMockModel) ExtractClaims(ctx context.Context, t string) ([]string, error) {
	return nil, nil
}
func (m *swarmMockModel) VerifyClaim(ctx context.Context, c, e string) (bool, float64, error) {
	return true, 0.9, nil
}
func (m *swarmMockModel) SynthesizeFindings(ctx context.Context, h []string, e map[string]interface{}) (string, error) {
	return "syn", nil
}
func (m *swarmMockModel) CritiqueFindings(ctx context.Context, f []string) (string, error) {
	return "crit", nil
}
func (m *swarmMockModel) Name() string    { return m.name }
func (m *swarmMockModel) Tier() ModelTier { return ModelTierStandard }

func TestAgentSwarmCoordinator_VerificationLayer(t *testing.T) {
	is := assert.New(t)
	m := &swarmMockModel{name: "test"}
	reg := search.BuildRegistry()
	engine := rag.NewEngine(reg, nil)
	coord := NewAgentSwarmCoordinator("s1", "q1", AgentSwarmConfig{}, m, reg, engine)

	t.Run("empty hypothesis", func(t *testing.T) {
		err := coord.verificationLayer(context.Background(), &Hypothesis{})
		is.Error(err)
	})

	t.Run("basic verification", func(t *testing.T) {
		h := &Hypothesis{
			Text: "Graph neural networks improve drug discovery outcomes",
			Evidence: []*EvidenceFinding{
				{
					ID:         "ev1",
					Claim:      "Graph neural networks significantly improves drug discovery success rates",
					Snippet:    "Our study demonstrates that GNNs improve outcomes.",
					Confidence: 0.8,
				},
				{
					ID:         "ev2",
					Claim:      "Standard ML models failed to show improvement in drug discovery using networks",
					Snippet:    "Previous research contradicts the positive effect of Graph neural networks.",
					Confidence: 0.8,
				},
			},
		}

		err := coord.verificationLayer(context.Background(), h)
		is.NoError(err)
		is.Greater(h.ContradictionCount, 0)
		is.NotEmpty(h.Contradictions)
		is.Greater(h.ConfidenceScore, 0.0)
	})
}

func TestAgentSwarm_AggregateConfidence_Specialists(t *testing.T) {
	is := assert.New(t)
	m := &swarmMockModel{name: "test"}
	reg := search.BuildRegistry()
	engine := rag.NewEngine(reg, nil)
	coord := NewAgentSwarmCoordinator("s1", "q1", AgentSwarmConfig{}, m, reg, engine)

	t.Run("specialist weights", func(t *testing.T) {
		h := &Hypothesis{
			Text: "test",
			Evidence: []*EvidenceFinding{
				{
					Confidence: 0.8,
					Specialist: SpecialistStatus{Verification: 1}, // verified (double weight)
				},
				{
					Confidence: 0.8,
					Specialist: SpecialistStatus{Verification: -1}, // rejected (0.25 weight)
				},
			},
		}

		conf := coord.aggregateConfidence(h)
		// weightedConf = (0.8 * 2.0) + (0.8 * 0.25) = 1.6 + 0.2 = 1.8
		// totalWeight = 2.0 + 0.25 = 2.25
		// baseConf = 1.8 / 2.25 = 0.8
		is.InDelta(0.8, conf, 0.01)
	})

	t.Run("heavy contradiction penalty", func(t *testing.T) {
		h := &Hypothesis{
			Text: "Graph neural networks",
			Evidence: []*EvidenceFinding{
				{ID: "1", Confidence: 0.9, Claim: "Graph neural networks support drug discovery", Snippet: "demonstrates"},
				{ID: "2", Confidence: 0.9, Claim: "Graph neural networks contradicts drug discovery", Snippet: "refutes"},
			},
		}

		conf := coord.aggregateConfidence(h)
		is.Less(conf, 0.9) // base 0.9 - penalty (0.15 for high severity)
	})
}
