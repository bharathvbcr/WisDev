package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetectExpertiseLevel(t *testing.T) {
	assert.Equal(t, ExpertiseBeginner, DetectExpertiseLevel(""))
	assert.Equal(t, ExpertiseBeginner, DetectExpertiseLevel("what is the basics of neural networks?"))
	assert.Equal(t, ExpertiseIntermediate, DetectExpertiseLevel("comparative analysis of transformer systems"))
	assert.Equal(t, ExpertiseExpert, DetectExpertiseLevel("bayesian transformer rlhf gpt vs bert with gradient descent and attention mechanism"))
}
