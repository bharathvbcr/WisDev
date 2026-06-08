package wisdev

import (
	"strings"
	"testing"
)

func TestBeliefStateManager_CreateBeliefFromHypothesis(t *testing.T) {
	manager := NewBeliefStateManager()

	hypothesis := &Hypothesis{
		ID:              "h1",
		Claim:           "Test hypothesis",
		ConfidenceScore: 0.8,
		Evidence: []*EvidenceFinding{
			{ID: "ev1", Confidence: 0.9},
			{ID: "ev2", Confidence: 0.8},
		},
		Contradictions: []*EvidenceFinding{
			{ID: "ev3", Confidence: 0.3},
		},
	}

	evidence := []EvidenceFinding{
		{ID: "ev1", Confidence: 0.9},
		{ID: "ev2", Confidence: 0.8},
		{ID: "ev3", Confidence: 0.3},
	}

	belief := manager.CreateBeliefFromHypothesis(hypothesis, evidence, "gap1", "query1")

	if belief == nil {
		t.Fatal("Belief should have been created")
	}

	if belief.Claim != hypothesis.Claim {
		t.Errorf("Expected claim %s, got %s", hypothesis.Claim, belief.Claim)
	}

	if belief.Confidence != hypothesis.ConfidenceScore {
		t.Errorf("Expected confidence %.2f, got %.2f", hypothesis.ConfidenceScore, belief.Confidence)
	}

	if len(belief.SupportingEvidence) != 2 {
		t.Errorf("Expected 2 supporting evidence, got %d", len(belief.SupportingEvidence))
	}

	if len(belief.ContradictingEvidence) != 1 {
		t.Errorf("Expected 1 contradicting evidence, got %d", len(belief.ContradictingEvidence))
	}

	if len(belief.ProvenanceChain) == 0 {
		t.Error("Belief should have provenance chain")
	}

	if belief.Status != BeliefStatusActive {
		t.Errorf("Expected status %s, got %s", BeliefStatusActive, belief.Status)
	}

	// Verify belief was added to state
	if len(manager.GetState().Beliefs) != 1 {
		t.Errorf("Expected 1 belief in state, got %d", len(manager.GetState().Beliefs))
	}
}

func TestBeliefStateManager_UpdateBeliefWithNewEvidence(t *testing.T) {
	manager := NewBeliefStateManager()

	hypothesis := &Hypothesis{
		ID:              "h1",
		Claim:           "Test hypothesis",
		ConfidenceScore: 0.7,
	}

	belief := manager.CreateBeliefFromHypothesis(hypothesis, []EvidenceFinding{}, "gap1", "query1")
	if belief == nil {
		t.Fatal("Initial belief creation failed")
	}

	initialProvenanceLen := len(belief.ProvenanceChain)

	newEvidence := []EvidenceFinding{
		{ID: "ev_new1", Confidence: 0.85},
		{ID: "ev_new2", Confidence: 0.75},
	}

	manager.UpdateBeliefWithNewEvidence(belief.ID, newEvidence, "gap2", "query2")

	updatedBelief := manager.GetState().Beliefs[belief.ID]
	if updatedBelief == nil {
		t.Fatal("Belief not found after update")
	}

	if len(updatedBelief.SupportingEvidence) != 2 {
		t.Errorf("Expected 2 supporting evidence, got %d", len(updatedBelief.SupportingEvidence))
	}

	if len(updatedBelief.ProvenanceChain) <= initialProvenanceLen {
		t.Error("Provenance chain should have grown with new evidence")
	}
}

func TestBeliefStateManager_ReviseBelief(t *testing.T) {
	manager := NewBeliefStateManager()
	state := manager.GetState()

	oldHypothesis := &Hypothesis{
		ID:              "h1",
		Claim:           "Old hypothesis",
		ConfidenceScore: 0.5,
	}

	oldBelief := manager.CreateBeliefFromHypothesis(oldHypothesis, []EvidenceFinding{}, "", "")

	newBelief := &Belief{
		ID:         "belief2",
		Claim:      "Revised hypothesis",
		Confidence: 0.8,
		Status:     BeliefStatusActive,
		CreatedAt:  NowMillis(),
		UpdatedAt:  NowMillis(),
	}

	state.ReviseBelief(oldBelief.ID, newBelief)

	// Check old belief is marked as revised
	if oldBelief.Status != BeliefStatusRevised {
		t.Errorf("Old belief should be marked as revised, got %s", oldBelief.Status)
	}

	if oldBelief.SupersededByBeliefID != newBelief.ID {
		t.Error("Old belief should reference new belief")
	}

	// Check new belief references old belief
	if newBelief.RevisedFromBeliefID != oldBelief.ID {
		t.Error("New belief should reference old belief")
	}

	// Check both beliefs exist in state
	if len(state.Beliefs) != 2 {
		t.Errorf("Expected 2 beliefs in state, got %d", len(state.Beliefs))
	}
}

func TestBeliefStateManager_RefuteBelief(t *testing.T) {
	manager := NewBeliefStateManager()
	state := manager.GetState()

	hypothesis := &Hypothesis{
		ID:              "h1",
		Claim:           "Refuted hypothesis",
		ConfidenceScore: 0.6,
	}

	belief := manager.CreateBeliefFromHypothesis(hypothesis, []EvidenceFinding{}, "", "")

	state.RefuteBelief(belief.ID)

	if belief.Status != BeliefStatusRefuted {
		t.Errorf("Belief should be marked as refuted, got %s", belief.Status)
	}
}

func TestBeliefStateManager_GetActiveBeliefs(t *testing.T) {
	manager := NewBeliefStateManager()
	state := manager.GetState()

	// Create 3 beliefs with different statuses
	h1 := &Hypothesis{ID: "h1", Claim: "Active 1", ConfidenceScore: 0.8}
	h2 := &Hypothesis{ID: "h2", Claim: "Active 2", ConfidenceScore: 0.7}
	h3 := &Hypothesis{ID: "h3", Claim: "Refuted", ConfidenceScore: 0.3}

	_ = manager.CreateBeliefFromHypothesis(h1, []EvidenceFinding{}, "", "")
	_ = manager.CreateBeliefFromHypothesis(h2, []EvidenceFinding{}, "", "")
	b3 := manager.CreateBeliefFromHypothesis(h3, []EvidenceFinding{}, "", "")

	// Refute one belief
	state.RefuteBelief(b3.ID)

	activeBeliefs := state.GetActiveBeliefs()

	if len(activeBeliefs) != 2 {
		t.Errorf("Expected 2 active beliefs, got %d", len(activeBeliefs))
	}

	for _, b := range activeBeliefs {
		if b.Status != BeliefStatusActive {
			t.Errorf("Found non-active belief %s with status %s", b.ID, b.Status)
		}
	}
}

func TestBeliefStateManager_GetMostProductiveGaps(t *testing.T) {
	manager := NewBeliefStateManager()

	// Create beliefs with different gap IDs in provenance
	for i := 0; i < 3; i++ {
		h := &Hypothesis{
			ID:              "h" + string(rune('1'+i)),
			Claim:           "Hypothesis " + string(rune('1'+i)),
			ConfidenceScore: 0.7,
		}
		gapID := "gap1" // Same gap for all
		if i == 2 {
			gapID = "gap2" // Different gap for last one
		}
		manager.CreateBeliefFromHypothesis(h, []EvidenceFinding{}, gapID, "query")
	}

	productivity := manager.GetMostProductiveGaps()

	if len(productivity) == 0 {
		t.Error("Should have productivity data")
	}

	if productivity["gap1"] != 2 {
		t.Errorf("Expected gap1 to have 2 discoveries, got %d", productivity["gap1"])
	}

	if productivity["gap2"] != 1 {
		t.Errorf("Expected gap2 to have 1 discovery, got %d", productivity["gap2"])
	}
}

func TestBeliefStateManager_RecalculateConfidence(t *testing.T) {
	manager := NewBeliefStateManager()

	hypothesis := &Hypothesis{
		ID:              "h1",
		Claim:           "Test hypothesis",
		ConfidenceScore: 0.5,
		Evidence: []*EvidenceFinding{
			{ID: "ev1", Confidence: 0.9},
			{ID: "ev2", Confidence: 0.8},
		},
	}

	belief := manager.CreateBeliefFromHypothesis(hypothesis, []EvidenceFinding{}, "", "")
	initialConfidence := belief.Confidence

	// Create evidence map
	allEvidence := map[string]EvidenceFinding{
		"ev1": {ID: "ev1", Confidence: 0.95}, // Increased confidence
		"ev2": {ID: "ev2", Confidence: 0.85}, // Increased confidence
	}

	manager.RecalculateConfidence(allEvidence)

	updatedBelief := manager.GetState().Beliefs[belief.ID]
	if updatedBelief.Confidence == initialConfidence {
		t.Error("Confidence should have been recalculated")
	}

	// With high-confidence supporting evidence, confidence should increase
	if updatedBelief.Confidence <= initialConfidence {
		t.Errorf("Expected confidence to increase from %.2f, got %.2f",
			initialConfidence, updatedBelief.Confidence)
	}
}

func TestBeliefStateManager_RecalibrateEvidenceConfidenceUsesPosterior(t *testing.T) {
	manager := NewBeliefStateManager()
	manager.GetState().AddBelief(&Belief{
		ID:                    "belief-posterior",
		Claim:                 "posterior claim",
		Confidence:            0.8,
		Status:                BeliefStatusActive,
		SupportingEvidence:    []string{"ev-support"},
		ContradictingEvidence: []string{"ev-contradict"},
		CreatedAt:             NowMillis(),
		UpdatedAt:             NowMillis(),
	})

	evidence := manager.RecalibrateEvidenceConfidence([]EvidenceFinding{
		{ID: "ev-support", Confidence: 0.55},
		{ID: "ev-contradict", Confidence: 0.55},
	})

	if len(evidence) != 2 {
		t.Fatalf("expected two evidence items, got %d", len(evidence))
	}
	if evidence[0].Confidence <= 0.55 {
		t.Fatalf("supporting evidence should be raised by posterior update, got %.3f", evidence[0].Confidence)
	}
	if evidence[1].Confidence >= 0.55 {
		t.Fatalf("contradicting evidence should be lowered by posterior update, got %.3f", evidence[1].Confidence)
	}
}

func TestBeliefStateManager_DetectEvidenceSaturationUsesJaccardAndInformationGain(t *testing.T) {
	manager := NewBeliefStateManager()
	manager.GetState().AddBelief(&Belief{
		ID:                 "belief-saturation",
		Claim:              "sleep improves memory consolidation",
		Confidence:         0.7,
		Status:             BeliefStatusActive,
		SupportingEvidence: []string{"old-1", "old-2"},
		CreatedAt:          NowMillis(),
		UpdatedAt:          NowMillis(),
	})

	repetitive := []EvidenceFinding{
		{ID: "old-1", SourceID: "paper-a", Claim: "sleep improves memory consolidation through replay", Snippet: "sleep improves memory consolidation"},
		{ID: "old-2", SourceID: "paper-a", Claim: "sleep improves memory consolidation through replay", Snippet: "sleep improves memory consolidation"},
		{ID: "old-1", SourceID: "paper-a", Claim: "sleep improves memory consolidation through replay", Snippet: "sleep improves memory consolidation"},
		{ID: "old-2", SourceID: "paper-a", Claim: "sleep improves memory consolidation through replay", Snippet: "sleep improves memory consolidation"},
		{ID: "old-1", SourceID: "paper-a", Claim: "sleep improves memory consolidation through replay", Snippet: "sleep improves memory consolidation"},
	}
	saturated := manager.DetectEvidenceSaturation(repetitive)
	if !saturated.IsSaturated {
		t.Fatalf("expected repetitive low-gain evidence to saturate, got %+v", saturated)
	}
	if saturated.InformationGain > 0.2 {
		t.Fatalf("expected low information gain, got %.3f", saturated.InformationGain)
	}

	novel := []EvidenceFinding{
		{ID: "new-a", SourceID: "paper-a", Claim: "REM sleep changes emotional memory", Snippet: "REM evidence"},
		{ID: "new-b", SourceID: "paper-b", Claim: "slow wave sleep supports declarative recall", Snippet: "NREM evidence"},
		{ID: "new-c", SourceID: "paper-c", Claim: "hippocampal replay predicts later retention", Snippet: "replay evidence"},
		{ID: "new-d", SourceID: "paper-d", Claim: "sleep deprivation impairs encoding", Snippet: "encoding evidence"},
		{ID: "new-e", SourceID: "paper-e", Claim: "targeted memory reactivation boosts recall", Snippet: "reactivation evidence"},
	}
	open := manager.DetectEvidenceSaturation(novel)
	if open.IsSaturated {
		t.Fatalf("expected diverse high-gain evidence to keep retrieval open, got %+v", open)
	}
}

func TestBeliefStateManager_CrucialLookupBranches(t *testing.T) {
	var nilManager *BeliefStateManager
	if got := nilManager.GetUncertainBeliefs(0.8); got != nil {
		t.Fatalf("nil manager should return nil uncertain beliefs, got %+v", got)
	}
	if got := nilManager.EvidenceForBelief("belief", nil); got != nil {
		t.Fatalf("nil manager should return nil evidence, got %+v", got)
	}

	manager := NewBeliefStateManager()
	manager.GetState().AddBelief(&Belief{
		ID:                 "belief-low",
		Claim:              "low confidence claim",
		Confidence:         0.42,
		Status:             BeliefStatusActive,
		SupportingEvidence: []string{"ev-1", "ev-3"},
		ProvenanceChain: []ProvenanceEntry{
			{GapID: "gap-a", QueryID: "query-a", EvidenceID: "ev-1"},
		},
		CreatedAt: NowMillis(),
		UpdatedAt: NowMillis(),
	})
	manager.GetState().AddBelief(&Belief{
		ID:         "belief-high",
		Claim:      "high confidence claim",
		Confidence: 0.91,
		Status:     BeliefStatusActive,
		CreatedAt:  NowMillis(),
		UpdatedAt:  NowMillis(),
	})

	uncertain := manager.GetUncertainBeliefs(0.5)
	if len(uncertain) != 1 || uncertain[0].ID != "belief-low" {
		t.Fatalf("expected only low-confidence belief, got %+v", uncertain)
	}

	chain := manager.GetProvenanceChainForBelief("belief-low")
	if len(chain) != 1 || chain[0].GapID != "gap-a" {
		t.Fatalf("expected provenance chain for belief-low, got %+v", chain)
	}
	if missing := manager.GetProvenanceChainForBelief("missing"); missing != nil {
		t.Fatalf("missing belief should return nil provenance, got %+v", missing)
	}

	evidence := manager.EvidenceForBelief("belief-low", []EvidenceFinding{
		{ID: "ev-1", Claim: "support one"},
		{ID: "ev-2", Claim: "unrelated"},
		{ID: "ev-3", Claim: "support three"},
	})
	if len(evidence) != 2 || evidence[0].ID != "ev-1" || evidence[1].ID != "ev-3" {
		t.Fatalf("expected supporting evidence only, got %+v", evidence)
	}
	if missing := manager.EvidenceForBelief("missing", evidence); missing != nil {
		t.Fatalf("missing belief should return nil evidence, got %+v", missing)
	}
}

func TestBeliefStateManager_ExportBeliefSummary(t *testing.T) {
	manager := NewBeliefStateManager()

	// Create some beliefs
	for i := 0; i < 3; i++ {
		h := &Hypothesis{
			ID:              "h" + string(rune('1'+i)),
			Claim:           "Hypothesis " + string(rune('1'+i)),
			ConfidenceScore: 0.7 + float64(i)*0.1,
		}
		manager.CreateBeliefFromHypothesis(h, []EvidenceFinding{}, "", "")
	}

	summary := manager.ExportBeliefSummary()

	if summary == "No active beliefs." {
		t.Error("Should have active beliefs in summary")
	}

	if !hasSubstringInText(summary, "Active Beliefs: 3") {
		t.Error("Summary should mention 3 active beliefs")
	}
}

func hasSubstringInText(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && strings.Contains(s, substr)
}
