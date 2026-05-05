package wisdev

import (
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
)

func TestAgentSwarmCoordinator_LaunchSwarm(t *testing.T) {
	reg := search.NewProviderRegistry()
	s := NewAgentSwarmCoordinator("s1", "test query", AgentSwarmConfig{
		HypothesisCount:  2,
		ParallelismCount: 2,
	}, &mockModel{}, reg, nil)
	assert.NotNil(t, s)
}

func TestAgentSwarmCoordinator_Ranking(t *testing.T) {
	is := assert.New(t)
	s := &AgentSwarmCoordinator{}

	hyps := []*Hypothesis{
		{ID: "h1", ConfidenceScore: 0.5},
		{ID: "h2", ConfidenceScore: 0.9},
		{ID: "h3", ConfidenceScore: 0.2},
	}

	s.rankHypothesesList(hyps)

	is.Equal("h2", hyps[0].ID)
	is.Equal("h1", hyps[1].ID)
	is.Equal("h3", hyps[2].ID)
}

func TestAgentSwarmCoordinator_ConfidenceAggregation(t *testing.T) {
	is := assert.New(t)
	s := NewAgentSwarmCoordinator("s1", "q", AgentSwarmConfig{}, &mockModel{}, nil, nil)

	h := &Hypothesis{
		Evidence: []*EvidenceFinding{
			{ID: "e1", Confidence: 0.8},
			{ID: "e2", Confidence: 0.6},
		},
	}

	// No contradictions
	conf := s.aggregateConfidence(h)
	is.InDelta(0.7, conf, 0.01)

	// With contradiction
	// We need to mock detectContradictionPairs or ensure it returns something
	// Let's mock it by adding a pair manually if possible (but it's a method)
}

func TestAgentSwarmCoordinator_ScoreSourceCredibility(t *testing.T) {
	is := assert.New(t)
	s := &AgentSwarmCoordinator{}

	t.Run("recent paper high confidence", func(t *testing.T) {
		ev := &EvidenceFinding{
			Year:         time.Now().Year(),
			Confidence:   0.8,
			OverlapRatio: 0.7,
		}
		score := s.scoreSourceCredibility(ev)
		// 0.8*0.4 + 0.7*0.3 + 1.0*0.3 = 0.32 + 0.21 + 0.3 = 0.83
		is.InDelta(0.83, score, 0.01)
	})

	t.Run("old paper", func(t *testing.T) {
		ev := &EvidenceFinding{
			Year:         time.Now().Year() - 15,
			Confidence:   0.5,
			OverlapRatio: 0.5,
		}
		score := s.scoreSourceCredibility(ev)
		// 0.5*0.4 + 0.5*0.3 + 0.3*0.3 = 0.2 + 0.15 + 0.09 = 0.44
		is.InDelta(0.44, score, 0.01)
	})
}
