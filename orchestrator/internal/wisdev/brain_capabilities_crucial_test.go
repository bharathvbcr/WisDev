package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScoreHypothesisConfidenceCrucialBranches(t *testing.T) {
	caps := NewBrainCapabilities(nil)

	assert.Equal(t, 0.5, caps.ScoreHypothesisConfidence(t.Context(), "claim", nil, nil, false))

	supporting := []*EvidenceFinding{{ID: "ev-1"}, {ID: "ev-2"}, {ID: "ev-3"}}
	contradicting := []*EvidenceFinding{{ID: "bad-1"}}

	assert.InDelta(t, 0.8, caps.ScoreHypothesisConfidence(t.Context(), "claim", supporting, nil, false), 0.000001)
	assert.InDelta(t, 0.325, caps.ScoreHypothesisConfidence(t.Context(), "claim", supporting, contradicting, false), 0.000001)
	assert.InDelta(t, 0.08125, caps.ScoreHypothesisConfidence(t.Context(), "claim", supporting, contradicting, true), 0.000001)

	manySupporting := make([]*EvidenceFinding, 10)
	for i := range manySupporting {
		manySupporting[i] = &EvidenceFinding{ID: "ev"}
	}
	assert.Equal(t, 1.0, caps.ScoreHypothesisConfidence(t.Context(), "claim", manySupporting, nil, false))
}
