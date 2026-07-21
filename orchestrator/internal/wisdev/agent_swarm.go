package wisdev

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/rag"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

// AgentSwarmConfig controls the behavior of the agent swarm.
type AgentSwarmConfig struct {
	HypothesisCount    int           // Number of hypotheses to generate (default: 7)
	ParallelismCount   int           // Concurrent agents (default: 3)
	MaxIterations      int           // Max refinement loops (default: 5)
	TokenBudget        int           // Max tokens per request (default: 50000)
	TimeoutPerAgent    time.Duration // Per-agent timeout (default: 30s)
	VerificationStages int           // Stages in verification pipeline (default: 4)
}

// AgentSwarmCoordinator orchestrates multiple research agents.
type AgentSwarmCoordinator struct {
	config    AgentSwarmConfig
	sessionID string
	query     string
	model     Model

	// Dependencies
	searchRegistry *search.ProviderRegistry
	ragEngine      *rag.Engine
	hypAgent       *HypothesisAgent
	evAgent        *EvidenceAgent
	verLayer       *VerificationLayer

	// State
	mu         sync.RWMutex
	hypotheses []*Hypothesis
	tokenUsed  int
	swarmStart time.Time
}

// NewAgentSwarmCoordinator creates a new agent swarm coordinator.
func NewAgentSwarmCoordinator(
	sessionID string,
	query string,
	cfg AgentSwarmConfig,
	model Model,
	searchReg *search.ProviderRegistry,
	rag *rag.Engine,
) *AgentSwarmCoordinator {
	unleashed := unleashedBudgetMode()
	if cfg.HypothesisCount == 0 {
		cfg.HypothesisCount = 7
		if unleashed {
			cfg.HypothesisCount = 12
		}
	}
	if cfg.ParallelismCount == 0 {
		cfg.ParallelismCount = 3
		if unleashed {
			cfg.ParallelismCount = 5
		}
	}
	if cfg.MaxIterations == 0 {
		cfg.MaxIterations = 5
		if unleashed {
			cfg.MaxIterations = 14
		}
	}
	if cfg.TokenBudget == 0 {
		cfg.TokenBudget = 50000
		if unleashed {
			cfg.TokenBudget = 150000
		}
	}
	if cfg.TimeoutPerAgent == 0 {
		cfg.TimeoutPerAgent = 30 * time.Second
		if unleashed {
			cfg.TimeoutPerAgent = 90 * time.Second
		}
	}

	return &AgentSwarmCoordinator{
		config:         cfg,
		sessionID:      sessionID,
		query:          query,
		model:          model,
		searchRegistry: searchReg,
		ragEngine:      rag,
		hypAgent:       NewHypothesisAgent(model),
		evAgent:        NewEvidenceAgent(searchReg),
		verLayer: NewVerificationLayer(VerificationLayerConfig{
			EnableSourceScoring:          true,
			EnableClaimLinking:           true,
			EnableContradictionDetection: true,
			EnableConfidenceAggregation:  true,
		}),
		hypotheses: make([]*Hypothesis, 0),
		swarmStart: time.Now(),
	}
}

// LaunchSwarm initiates the hypothesis + evidence agent swarm.
func (s *AgentSwarmCoordinator) LaunchSwarm(ctx context.Context) ([]*Hypothesis, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	slog.Info("Launching agent swarm", "session", s.sessionID, "query", s.query)

	// Step 1: Generate initial hypotheses
	hypotheses, err := s.generateHypotheses(ctx, s.query)
	if err != nil {
		return nil, fmt.Errorf("hypothesis generation failed: %w", err)
	}

	// Step 2: Launch evidence agents in parallel with throttling
	var wg sync.WaitGroup
	sem := make(chan struct{}, s.config.ParallelismCount)

	for _, h := range hypotheses {
		wg.Add(1)
		go func(hyp *Hypothesis) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
				if err := s.gatherEvidenceForHypothesis(ctx, hyp); err != nil {
					slog.Error("Evidence agent failed", "hyp_id", hyp.ID, "error", err)
				}
			case <-ctx.Done():
				return
			}
		}(h)
	}

	wg.Wait()

	// Step 3: Verify claims in each hypothesis
	for _, h := range hypotheses {
		if err := s.verLayer.Verify(ctx, h); err != nil {
			slog.Error("Verification layer failed", "hyp_id", h.ID, "error", err)
		}
	}

	// Step 4: Rank hypotheses
	s.rankHypothesesList(hypotheses)

	// Update shared state
	s.mu.Lock()
	s.hypotheses = hypotheses
	s.mu.Unlock()

	return hypotheses, nil
}

// rankHypothesesList sorts a provided slice of hypotheses by confidence score.
func (s *AgentSwarmCoordinator) rankHypothesesList(list []*Hypothesis) {
	NewHypothesisRanker().Rank(list)
}

// generateHypotheses generates initial hypotheses from a query.
func (s *AgentSwarmCoordinator) generateHypotheses(ctx context.Context, query string) ([]*Hypothesis, error) {
	if s.hypAgent == nil {
		s.hypAgent = NewHypothesisAgent(s.model)
	}

	hypotheses, err := s.hypAgent.Generate(ctx, query, s.config.HypothesisCount)
	if err != nil {
		return nil, err
	}
	return hypotheses, nil
}

// gatherEvidenceForHypothesis gathers evidence for a single hypothesis.
func (s *AgentSwarmCoordinator) gatherEvidenceForHypothesis(ctx context.Context, hypothesis *Hypothesis) error {
	hypothesis.Status = "searching"
	if s.evAgent == nil {
		s.evAgent = NewEvidenceAgent(s.searchRegistry)
	}

	evidence, err := s.evAgent.Gather(ctx, hypothesis.Query, hypothesis.Text, 20)
	if err != nil {
		return err
	}
	hypothesis.Evidence = evidence
	hypothesis.EvidenceCount = len(evidence)
	hypothesis.UpdatedAt = time.Now().UnixMilli()

	hypothesis.Status = "complete"
	return nil
}

// verificationLayer applies a four-stage verification pipeline to a hypothesis.
// Stage 1: source credibility scoring.
// Stage 2: cross-pair contradiction detection (semantic claim divergence).
// Stage 3: specialist-verification weighting.
// Stage 4: confidence aggregation with contradiction penalty.
func (s *AgentSwarmCoordinator) verificationLayer(ctx context.Context, hypothesis *Hypothesis) error {
	if s.verLayer == nil {
		s.verLayer = NewVerificationLayer(VerificationLayerConfig{
			EnableSourceScoring:          true,
			EnableClaimLinking:           true,
			EnableContradictionDetection: true,
			EnableConfidenceAggregation:  true,
		})
	}
	return s.verLayer.Verify(ctx, hypothesis)
}

func (s *AgentSwarmCoordinator) scoreSourceCredibility(ev *EvidenceFinding) float64 {
	return scoreEvidenceSourceCredibility(ev)
}

// detectContradictionPairs identifies oppositely-valenced evidence pairs.
// Two findings contradict when one is positively framed ("X improves Y") and another
// uses strong negation language that disputes the same subject ("X does not improve Y").
// This replaces the former single-evidence keyword scan with a pairwise cross-check.
// contradictionPairsFor is the package-level implementation; see agent_swarm.go.
func (s *AgentSwarmCoordinator) detectContradictionPairs(h *Hypothesis) []ContradictionPair {
	return contradictionPairsFor(h)
}

// contradictionPairsFor is a package-level helper so that other verification
// code (e.g. VerificationLayer) can reuse the same pairwise detection logic.
func contradictionPairsFor(h *Hypothesis) []ContradictionPair {
	// Negation phrases that signal a counter-claim.
	negationPhrases := []string{
		"does not", "did not", "no significant", "failed to", "contradicts",
		"disputes", "inconsistent with", "contrary to", "no evidence",
		"challenges", "refutes", "not supported", "not replicated",
	}
	affirmPhrases := []string{
		"demonstrates", "shows that", "confirms", "supports", "evidence for",
		"significant improvement", "significantly improves", "beneficial",
		"positively associated", "positive effect",
	}

	isNegated := func(text string) bool {
		t := strings.ToLower(text)
		for _, p := range negationPhrases {
			if strings.Contains(t, p) {
				return true
			}
		}
		return false
	}
	isAffirmed := func(text string) bool {
		t := strings.ToLower(text)
		for _, p := range affirmPhrases {
			if strings.Contains(t, p) {
				return true
			}
		}
		return false
	}

	type polarized struct {
		ev      *EvidenceFinding
		negated bool
	}
	var polarized_evs []polarized
	for _, ev := range h.Evidence {
		text := ev.Snippet + " " + ev.Claim
		neg := isNegated(text)
		aff := isAffirmed(text)
		if neg || aff {
			polarized_evs = append(polarized_evs, polarized{ev, neg})
		}
	}

	hypKeywords := strings.Fields(strings.ToLower(h.Text))
	subjectOverlap := func(a, b *EvidenceFinding) bool {
		aText := strings.ToLower(a.Snippet + " " + a.Claim)
		bText := strings.ToLower(b.Snippet + " " + b.Claim)
		for _, kw := range hypKeywords {
			if len(kw) >= 5 && strings.Contains(aText, kw) && strings.Contains(bText, kw) {
				return true
			}
		}
		return false
	}

	var pairs []ContradictionPair
	for i := 0; i < len(polarized_evs); i++ {
		for j := i + 1; j < len(polarized_evs); j++ {
			a, b := polarized_evs[i], polarized_evs[j]
			if a.negated != b.negated && subjectOverlap(a.ev, b.ev) {
				severity := ContradictionMedium
				if a.ev.Confidence > 0.7 && b.ev.Confidence > 0.7 {
					severity = ContradictionHigh
				} else if a.ev.Confidence < 0.4 || b.ev.Confidence < 0.4 {
					severity = ContradictionLow
				}
				pairs = append(pairs, ContradictionPair{
					FindingA:    *a.ev,
					FindingB:    *b.ev,
					Severity:    severity,
					Explanation: fmt.Sprintf("Finding '%s' affirms while '%s' negates a shared subject.", a.ev.SourceID, b.ev.SourceID),
				})
			}
		}
	}
	return pairs
}

func (s *AgentSwarmCoordinator) aggregateConfidence(h *Hypothesis) float64 {
	return aggregateHypothesisConfidence(h, s.pairsForHypothesis(h))
}

// pairsForHypothesis re-runs contradiction pair detection (read-only) to feed aggregation.
// Storing pairs on Hypothesis would be cleaner but avoids model changes here.
func (s *AgentSwarmCoordinator) pairsForHypothesis(h *Hypothesis) []ContradictionPair {
	return s.detectContradictionPairs(h)
}
