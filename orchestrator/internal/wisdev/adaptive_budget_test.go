package wisdev

import (
	"testing"
)

func TestAutonomousLoop_ComputeAdaptiveBudgets(t *testing.T) {
	loop := NewAutonomousLoop(nil, nil)

	tests := []struct {
		name        string
		hypotheses  []*Hypothesis
		totalBudget int
		validate    func(t *testing.T, hypotheses []*Hypothesis)
	}{
		{
			name: "Equal uncertainty distribution",
			hypotheses: []*Hypothesis{
				{ID: "h1", ConfidenceScore: 0.5, IsTerminated: false},
				{ID: "h2", ConfidenceScore: 0.5, IsTerminated: false},
				{ID: "h3", ConfidenceScore: 0.5, IsTerminated: false},
			},
			totalBudget: 9,
			validate: func(t *testing.T, hypotheses []*Hypothesis) {
				for _, h := range hypotheses {
					if h.AllocatedQueryBudget != 3 {
						t.Errorf("Expected equal budget of 3, got %d for %s",
							h.AllocatedQueryBudget, h.ID)
					}
				}
			},
		},
		{
			name: "Uncertain hypothesis gets more budget",
			hypotheses: []*Hypothesis{
				{ID: "h1", ConfidenceScore: 0.9, IsTerminated: false}, // High confidence = low uncertainty
				{ID: "h2", ConfidenceScore: 0.3, IsTerminated: false}, // Low confidence = high uncertainty
				{ID: "h3", ConfidenceScore: 0.6, IsTerminated: false}, // Medium confidence
			},
			totalBudget: 10,
			validate: func(t *testing.T, hypotheses []*Hypothesis) {
				// h2 (0.3 confidence = 0.7 uncertainty) should get most budget
				// h3 (0.6 confidence = 0.4 uncertainty) should get medium
				// h1 (0.9 confidence = 0.1 uncertainty) should get least
				if hypotheses[1].AllocatedQueryBudget <= hypotheses[0].AllocatedQueryBudget {
					t.Error("Most uncertain hypothesis should get most budget")
				}
				if hypotheses[1].AllocatedQueryBudget <= hypotheses[2].AllocatedQueryBudget {
					t.Error("Most uncertain hypothesis should get more than medium")
				}

				// Total should equal budget
				total := 0
				for _, h := range hypotheses {
					total += h.AllocatedQueryBudget
				}
				if total != 10 {
					t.Errorf("Total allocated budget should be 10, got %d", total)
				}
			},
		},
		{
			name: "Terminated hypotheses get zero budget",
			hypotheses: []*Hypothesis{
				{ID: "h1", ConfidenceScore: 0.5, IsTerminated: false},
				{ID: "h2", ConfidenceScore: 0.5, IsTerminated: true},
				{ID: "h3", ConfidenceScore: 0.5, IsTerminated: false},
			},
			totalBudget: 10,
			validate: func(t *testing.T, hypotheses []*Hypothesis) {
				if hypotheses[1].AllocatedQueryBudget != 0 {
					t.Errorf("Terminated hypothesis should have 0 budget, got %d",
						hypotheses[1].AllocatedQueryBudget)
				}

				// Active hypotheses should share the budget
				activeTotal := hypotheses[0].AllocatedQueryBudget + hypotheses[2].AllocatedQueryBudget
				if activeTotal != 10 {
					t.Errorf("Active hypotheses should share total budget of 10, got %d", activeTotal)
				}
			},
		},
		{
			name: "Minimum 1 query per active hypothesis",
			hypotheses: []*Hypothesis{
				{ID: "h1", ConfidenceScore: 0.99, IsTerminated: false}, // Almost certain
				{ID: "h2", ConfidenceScore: 0.1, IsTerminated: false},  // Very uncertain
			},
			totalBudget: 5,
			validate: func(t *testing.T, hypotheses []*Hypothesis) {
				for _, h := range hypotheses {
					if !h.IsTerminated && h.AllocatedQueryBudget < 1 {
						t.Errorf("Active hypothesis %s should have at least 1 query, got %d",
							h.ID, h.AllocatedQueryBudget)
					}
				}
			},
		},
		{
			name: "All high confidence (zero uncertainty)",
			hypotheses: []*Hypothesis{
				{ID: "h1", ConfidenceScore: 1.0, IsTerminated: false},
				{ID: "h2", ConfidenceScore: 1.0, IsTerminated: false},
			},
			totalBudget: 10,
			validate: func(t *testing.T, hypotheses []*Hypothesis) {
				// Should distribute evenly when all have zero uncertainty
				total := 0
				for _, h := range hypotheses {
					if h.AllocatedQueryBudget < 1 {
						t.Error("Should allocate at least 1 query even with 1.0 confidence")
					}
					total += h.AllocatedQueryBudget
				}
				if total != 10 {
					t.Errorf("Total should be 10, got %d", total)
				}
			},
		},
		{
			name:        "Empty hypotheses list",
			hypotheses:  []*Hypothesis{},
			totalBudget: 10,
			validate: func(t *testing.T, hypotheses []*Hypothesis) {
				// Should not panic
			},
		},
		{
			name: "Zero budget",
			hypotheses: []*Hypothesis{
				{ID: "h1", ConfidenceScore: 0.5, IsTerminated: false},
			},
			totalBudget: 0,
			validate: func(t *testing.T, hypotheses []*Hypothesis) {
				// Should handle gracefully without panic
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset budgets
			for _, h := range tt.hypotheses {
				h.AllocatedQueryBudget = 0
			}

			loop.computeAdaptiveBudgets(tt.hypotheses, tt.totalBudget)
			tt.validate(t, tt.hypotheses)
		})
	}
}

func TestAutonomousLoop_ComputeAdaptiveBudgets_Uncertainty(t *testing.T) {
	loop := NewAutonomousLoop(nil, nil)

	// Test uncertainty calculation: uncertainty = 1 - confidence
	hypotheses := []*Hypothesis{
		{ID: "h1", ConfidenceScore: 0.2, IsTerminated: false}, // uncertainty = 0.8
		{ID: "h2", ConfidenceScore: 0.5, IsTerminated: false}, // uncertainty = 0.5
		{ID: "h3", ConfidenceScore: 0.8, IsTerminated: false}, // uncertainty = 0.2
	}

	loop.computeAdaptiveBudgets(hypotheses, 15)

	// Total uncertainty = 0.8 + 0.5 + 0.2 = 1.5
	// h1 should get: 15 * (0.8 / 1.5) = 8
	// h2 should get: 15 * (0.5 / 1.5) = 5
	// h3 should get: 15 * (0.2 / 1.5) = 2

	if hypotheses[0].AllocatedQueryBudget < hypotheses[1].AllocatedQueryBudget {
		t.Error("Lower confidence (higher uncertainty) should get more budget")
	}
	if hypotheses[1].AllocatedQueryBudget < hypotheses[2].AllocatedQueryBudget {
		t.Error("Medium confidence should get more than high confidence")
	}

	// Verify total
	total := hypotheses[0].AllocatedQueryBudget +
		hypotheses[1].AllocatedQueryBudget +
		hypotheses[2].AllocatedQueryBudget

	if total != 15 {
		t.Errorf("Total budget should be 15, got %d", total)
	}
}

func TestToHypothesisPtrs(t *testing.T) {
	hypotheses := []Hypothesis{
		{ID: "h1", Claim: "Hypothesis 1"},
		{ID: "h2", Claim: "Hypothesis 2"},
		{ID: "h3", Claim: "Hypothesis 3"},
	}

	ptrs := toHypothesisPtrs(hypotheses)

	if len(ptrs) != len(hypotheses) {
		t.Errorf("Expected %d pointers, got %d", len(hypotheses), len(ptrs))
	}

	for i, ptr := range ptrs {
		if ptr == nil {
			t.Errorf("Pointer at index %d is nil", i)
			continue
		}
		if ptr.ID != hypotheses[i].ID {
			t.Errorf("Expected ID %s, got %s", hypotheses[i].ID, ptr.ID)
		}
		// Verify it's actually pointing to the original
		if &hypotheses[i] != ptr {
			t.Error("Pointer should reference original hypothesis")
		}
	}
}

func TestAutonomousLoop_ComputeAdaptiveBudgets_Integration(t *testing.T) {
	loop := NewAutonomousLoop(nil, nil)

	// Simulate a realistic scenario after hypothesis evaluation
	hypotheses := []*Hypothesis{
		{
			ID:              "h1",
			Claim:           "Well-supported hypothesis",
			ConfidenceScore: 0.85,
			IsTerminated:    false,
			Status:          "supported",
		},
		{
			ID:              "h2",
			Claim:           "Uncertain hypothesis",
			ConfidenceScore: 0.45,
			IsTerminated:    false,
			Status:          "uncertain",
		},
		{
			ID:              "h3",
			Claim:           "Weak hypothesis",
			ConfidenceScore: 0.25,
			IsTerminated:    false,
			Status:          "uncertain",
		},
		{
			ID:              "h4",
			Claim:           "Refuted hypothesis",
			ConfidenceScore: 0.15,
			IsTerminated:    true,
			Status:          "refuted",
		},
	}

	totalBudget := 12

	loop.computeAdaptiveBudgets(hypotheses, totalBudget)

	// Expectations:
	// h1 (0.85 conf = 0.15 uncertainty): should get least budget among active
	// h2 (0.45 conf = 0.55 uncertainty): should get medium budget
	// h3 (0.25 conf = 0.75 uncertainty): should get most budget
	// h4 (terminated): should get 0 budget

	if hypotheses[3].AllocatedQueryBudget != 0 {
		t.Error("Terminated hypothesis should have 0 budget")
	}

	if hypotheses[2].AllocatedQueryBudget <= hypotheses[1].AllocatedQueryBudget {
		t.Error("h3 (most uncertain) should get more budget than h2")
	}

	if hypotheses[1].AllocatedQueryBudget <= hypotheses[0].AllocatedQueryBudget {
		t.Error("h2 (medium uncertain) should get more budget than h1")
	}

	// All active hypotheses should have at least 1 query
	for i := 0; i < 3; i++ {
		if hypotheses[i].AllocatedQueryBudget < 1 {
			t.Errorf("Active hypothesis h%d should have at least 1 query", i+1)
		}
	}

	// Total for active should equal budget
	activeTotal := hypotheses[0].AllocatedQueryBudget +
		hypotheses[1].AllocatedQueryBudget +
		hypotheses[2].AllocatedQueryBudget

	if activeTotal != totalBudget {
		t.Errorf("Active hypotheses should share total budget of %d, got %d",
			totalBudget, activeTotal)
	}

	t.Logf("Budget allocation: h1=%d, h2=%d, h3=%d, h4=%d",
		hypotheses[0].AllocatedQueryBudget,
		hypotheses[1].AllocatedQueryBudget,
		hypotheses[2].AllocatedQueryBudget,
		hypotheses[3].AllocatedQueryBudget)
}
