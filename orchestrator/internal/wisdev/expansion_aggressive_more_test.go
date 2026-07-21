package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAggressiveExpansion_Empty(t *testing.T) {
	resp := GenerateAggressiveExpansion(nil, "", 10, false, false, false, nil)
	assert.Equal(t, "", resp.Original)
}

func TestMin2(t *testing.T) {
	assert.Equal(t, 1, min2(1, 2))
	assert.Equal(t, 1, min2(2, 1))
}

func TestLimitVariations(t *testing.T) {
	vs := []QueryVariation{{Query: "v1"}, {Query: "v2"}}
	assert.Len(t, limitVariations(vs, 1), 1)
	assert.Len(t, limitVariations(vs, 5), 2)
}
