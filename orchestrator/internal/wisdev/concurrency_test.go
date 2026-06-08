package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAdaptiveConcurrencyController(t *testing.T) {
	c := NewAdaptiveConcurrencyController(5, 2, 10)

	assert.Equal(t, 5, c.Current())

	// Test success increases current
	c.RecordSuccess()
	assert.Equal(t, 6, c.Current())

	// Test failure decreases current (multiplicative)
	c.RecordFailure()
	assert.Equal(t, 3, c.Current())

	// Test min boundary
	c.RecordFailure()
	assert.Equal(t, 2, c.Current())
	c.RecordFailure()
	assert.Equal(t, 2, c.Current())

	// Test max boundary
	for i := 0; i < 20; i++ {
		c.RecordSuccess()
	}
	assert.Equal(t, 10, c.Current())

	// Test per-provider limit
	assert.Equal(t, 3, c.ProviderLimit("semantic-scholar"))
	assert.Equal(t, 10, c.ProviderLimit("unknown"))
}

func TestNewAdaptiveConcurrencyController_Boundaries(t *testing.T) {
	c1 := NewAdaptiveConcurrencyController(0, 0, 0)
	assert.Equal(t, 1, c1.Current())

	c2 := NewAdaptiveConcurrencyController(10, 5, 2)
	assert.Equal(t, 5, c2.Current())
}
