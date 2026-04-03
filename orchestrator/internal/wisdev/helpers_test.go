package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMinInt(t *testing.T) {
	assert.Equal(t, 1, MinInt(1, 2))
	assert.Equal(t, 1, MinInt(2, 1))
	assert.Equal(t, 1, MinInt(1, 1))
}

func TestMaxInt(t *testing.T) {
	assert.Equal(t, 2, MaxInt(1, 2))
	assert.Equal(t, 2, MaxInt(2, 1))
	assert.Equal(t, 2, MaxInt(2, 2))
}

func TestToPolicyRisk(t *testing.T) {
	// Models.go has ToPolicyRisk
	assert.NotEmpty(t, ToPolicyRisk(RiskLevelHigh))
	assert.NotEmpty(t, ToPolicyRisk(RiskLevelMedium))
	assert.NotEmpty(t, ToPolicyRisk(RiskLevelLow))
	assert.NotEmpty(t, ToPolicyRisk("unknown"))
}

func TestNowMillis(t *testing.T) {
	assert.True(t, NowMillis() > 0)
}
