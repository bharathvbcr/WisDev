package rag

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCosineSimilarity(t *testing.T) {
	is := assert.New(t)

	t.Run("Identical vectors", func(t *testing.T) {
		v1 := []float64{1, 0, 0}
		v2 := []float64{1, 0, 0}
		is.InDelta(1.0, CosineSimilarity(v1, v2), 0.0001)
	})

	t.Run("Orthogonal vectors", func(t *testing.T) {
		v1 := []float64{1, 0, 0}
		v2 := []float64{0, 1, 0}
		is.InDelta(0.0, CosineSimilarity(v1, v2), 0.0001)
	})

	t.Run("Opposite vectors", func(t *testing.T) {
		v1 := []float64{1, 0, 0}
		v2 := []float64{-1, 0, 0}
		is.InDelta(-1.0, CosineSimilarity(v1, v2), 0.0001)
	})

	t.Run("Different lengths", func(t *testing.T) {
		v1 := []float64{1, 0}
		v2 := []float64{1, 0, 0}
		is.Equal(0.0, CosineSimilarity(v1, v2))
	})

	t.Run("Zero vectors", func(t *testing.T) {
		v1 := []float64{0, 0, 0}
		v2 := []float64{1, 0, 0}
		is.Equal(0.0, CosineSimilarity(v1, v2))
	})
	
	t.Run("Empty vectors", func(t *testing.T) {
		is.Equal(0.0, CosineSimilarity(nil, nil))
	})
}

func TestVectorMean(t *testing.T) {
	is := assert.New(t)

	t.Run("Basic mean", func(t *testing.T) {
		v1 := []float64{1, 2, 3}
		v2 := []float64{3, 4, 5}
		mean := VectorMean([][]float64{v1, v2})
		is.Equal([]float64{2, 3, 4}, mean)
	})

	t.Run("Empty input", func(t *testing.T) {
		is.Nil(VectorMean(nil))
	})
}
