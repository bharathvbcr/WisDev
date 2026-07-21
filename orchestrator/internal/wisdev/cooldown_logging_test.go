package wisdev

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestShouldLogWisDevCooldownFallbackThrottlesByOperation(t *testing.T) {
	resetWisDevCooldownFallbackLogForTest()

	now := time.Unix(100, 0)
	assert.True(t, shouldLogWisDevCooldownFallback("verify_claims", now))
	assert.False(t, shouldLogWisDevCooldownFallback("verify_claims", now.Add(time.Second)))
	assert.True(t, shouldLogWisDevCooldownFallback("generate_thoughts", now.Add(time.Second)))
	assert.True(t, shouldLogWisDevCooldownFallback("verify_claims", now.Add(wisdevCooldownFallbackLogInterval+time.Second)))
}
