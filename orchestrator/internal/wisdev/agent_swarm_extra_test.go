package wisdev

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

type swarmExtraMockModel struct {
	mock.Mock
}

func (m *swarmExtraMockModel) Generate(ctx context.Context, prompt string) (string, error) {
	args := m.Called(ctx, prompt)
	return args.String(0), args.Error(1)
}

func (m *swarmExtraMockModel) GenerateHypotheses(ctx context.Context, query string) ([]string, error) {
	args := m.Called(ctx, query)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]string), args.Error(1)
}

func (m *swarmExtraMockModel) ExtractClaims(ctx context.Context, t string) ([]string, error) {
	return nil, nil
}
func (m *swarmExtraMockModel) VerifyClaim(ctx context.Context, claim, evidence string) (bool, float64, error) {
	return true, 0.9, nil
}
func (m *swarmExtraMockModel) SynthesizeFindings(ctx context.Context, h []string, e map[string]interface{}) (string, error) {
	return "syn", nil
}
func (m *swarmExtraMockModel) CritiqueFindings(ctx context.Context, f []string) (string, error) {
	return "crit", nil
}
func (m *swarmExtraMockModel) Name() string    { return "swarm-extra-mock" }
func (m *swarmExtraMockModel) Tier() ModelTier { return ModelTierStandard }

type swarmExtraMockSearchProvider struct {
	mock.Mock
}

func (m *swarmExtraMockSearchProvider) Search(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
	args := m.Called(ctx, query, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]search.Paper), args.Error(1)
}

func (m *swarmExtraMockSearchProvider) Name() string      { return "swarm-extra-search" }
func (m *swarmExtraMockSearchProvider) Domains() []string { return []string{"general"} }
func (m *swarmExtraMockSearchProvider) Healthy() bool     { return true }
func (m *swarmExtraMockSearchProvider) Tools() []string   { return nil }

func TestAgentSwarmCoordinator_LaunchSwarm_Extra(t *testing.T) {
	ctx := context.Background()

	t.Run("Success", func(t *testing.T) {
		mm := &swarmExtraMockModel{}
		mp := &swarmExtraMockSearchProvider{}
		reg := search.NewProviderRegistry()
		reg.Register(mp)
		reg.SetDefaultOrder([]string{"swarm-extra-search"})

		s := NewAgentSwarmCoordinator("s1", "test query", AgentSwarmConfig{
			HypothesisCount:  1,
			ParallelismCount: 1,
		}, mm, reg, nil)

		// Step 1: generateHypotheses
		mm.On("GenerateHypotheses", mock.Anything, "test query").Return([]string{"Hypothesis 1"}, nil).Once()

		// Step 2: gatherEvidenceForHypothesis -> EvidenceAgent.Gather -> Search
		mp.On("Search", mock.Anything, mock.Anything, mock.Anything).Return([]search.Paper{
			{ID: "p1", Title: "Paper 1", Abstract: "Evidence for H1"},
		}, nil).Once()

		// Step 3: detectContradictionPairs
		mm.On("Generate", mock.Anything, mock.MatchedBy(func(prompt string) bool {
			return strings.Contains(prompt, "Identify any direct contradictions")
		})).Return(`[]`, nil).Once()

		hyps, err := s.LaunchSwarm(ctx)
		assert.NoError(t, err)
		assert.Len(t, hyps, 1)
		assert.NotEmpty(t, hyps[0].ID)
	})

	t.Run("GenerateHypotheses Error", func(t *testing.T) {
		mm := &swarmExtraMockModel{}
		reg := search.NewProviderRegistry()
		s := NewAgentSwarmCoordinator("s1", "test query", AgentSwarmConfig{}, mm, reg, nil)
		s.model = mm
		s.hypAgent = nil

		mm.On("GenerateHypotheses", mock.Anything, mock.Anything).Return(nil, errors.New("llm fail")).Once()

		_, err := s.LaunchSwarm(ctx)
		assert.NoError(t, err)
	})

	t.Run("Context Cancel", func(t *testing.T) {
		mm := &swarmExtraMockModel{}
		reg := search.NewProviderRegistry()
		s := NewAgentSwarmCoordinator("s1", "test query", AgentSwarmConfig{}, mm, reg, nil)

		mm.On("GenerateHypotheses", mock.Anything, mock.Anything).Return([]string{"H1"}, nil).Maybe()

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // pre-cancel

		_, err := s.LaunchSwarm(ctx)
		assert.NoError(t, err)
	})
}

func TestAgentSwarmCoordinator_AggregateConfidence_NoEvidence(t *testing.T) {
	s := &AgentSwarmCoordinator{}
	h := &Hypothesis{Evidence: nil}
	assert.Equal(t, 0.0, s.aggregateConfidence(h))
}

func TestAgentSwarmCoordinator_GatherEvidence_Error(t *testing.T) {
	mm := &swarmExtraMockModel{}
	mp := &swarmExtraMockSearchProvider{}
	reg := search.NewProviderRegistry()
	reg.Register(mp)
	reg.SetDefaultOrder([]string{"swarm-extra-search"})

	s := NewAgentSwarmCoordinator("s1", "q", AgentSwarmConfig{}, mm, reg, nil)
	h := &Hypothesis{ID: "h1", Query: "q", Text: "text"}

	mp.On("Search", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("search fail")).Once()

	err := s.gatherEvidenceForHypothesis(context.Background(), h)
	assert.NoError(t, err)
}

func TestAgentSwarmCoordinator_GatherEvidence_Empty(t *testing.T) {
	mm := &swarmExtraMockModel{}
	mp := &swarmExtraMockSearchProvider{}
	reg := search.NewProviderRegistry()
	reg.Register(mp)
	reg.SetDefaultOrder([]string{"swarm-extra-search"})

	s := NewAgentSwarmCoordinator("s1", "q", AgentSwarmConfig{}, mm, reg, nil)
	h := &Hypothesis{ID: "h1", Query: "q", Text: "text"}

	mp.On("Search", mock.Anything, mock.Anything, mock.Anything).Return([]search.Paper{}, nil).Once()

	err := s.gatherEvidenceForHypothesis(context.Background(), h)
	assert.NoError(t, err)
	assert.Len(t, h.Evidence, 0)
}

func TestAgentSwarmCoordinator_LaunchSwarm_NoHypotheses(t *testing.T) {
	mm := &swarmExtraMockModel{}
	reg := search.NewProviderRegistry()
	s := NewAgentSwarmCoordinator("s1", "q", AgentSwarmConfig{}, mm, reg, nil)
	s.hypAgent = NewHypothesisAgent(mm)

	mm.On("GenerateHypotheses", mock.Anything, mock.Anything).Return([]string{}, nil).Once()

	_, err := s.LaunchSwarm(context.Background())
	assert.NoError(t, err)
}

func TestAgentSwarmCoordinator_ScoreSourceCredibility_OldPaper(t *testing.T) {
	s := &AgentSwarmCoordinator{}
	ev := &EvidenceFinding{
		Year:         time.Now().Year() - 20,
		Confidence:   0.5,
		OverlapRatio: 0.5,
	}
	score := s.scoreSourceCredibility(ev)
	assert.InDelta(t, 0.44, score, 0.01)
}

func TestAgentSwarmCoordinator_ScoreSourceCredibility_Extra(t *testing.T) {
	s := NewAgentSwarmCoordinator("s1", "q", AgentSwarmConfig{}, &swarmExtraMockModel{}, nil, nil)
	t.Run("high overlap", func(t *testing.T) {
		ev := &EvidenceFinding{
			Year:         time.Now().Year(),
			Confidence:   0.5,
			OverlapRatio: 1.0,
		}
		score := s.scoreSourceCredibility(ev)
		assert.InDelta(t, 0.8, score, 0.01)
	})
}

func TestAgentSwarmCoordinator_AggregateConfidence_Contradiction_Extra(t *testing.T) {
	s := NewAgentSwarmCoordinator("s1", "q", AgentSwarmConfig{}, &swarmExtraMockModel{}, nil, nil)
	h := &Hypothesis{
		Text: "Networks",
		Evidence: []*EvidenceFinding{
			{
				ID:         "e1",
				Confidence: 0.9,
				Claim:      "demonstrates improvement in Networks",
				Snippet:    "demonstrates",
			},
			{
				ID:         "e2",
				Confidence: 0.9,
				Claim:      "contradicts improvement in Networks",
				Snippet:    "contradicts",
			},
		},
	}

	conf := s.aggregateConfidence(h)
	assert.InDelta(t, 0.75, conf, 0.01)
}
