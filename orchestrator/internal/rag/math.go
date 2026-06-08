package rag

import (
	"math"
)

// CosineSimilarity calculates the cosine similarity between two vectors.
func CosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0.0
	}

	var dotProduct, normA, normB float64
	for i := 0; i < len(a); i++ {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0.0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

// VectorMean calculates the element-wise mean of a set of vectors.
func VectorMean(vectors [][]float64) []float64 {
	if len(vectors) == 0 {
		return nil
	}

	dim := len(vectors[0])
	mean := make([]float64, dim)

	for _, v := range vectors {
		for i := 0; i < dim; i++ {
			mean[i] += v[i]
		}
	}

	count := float64(len(vectors))
	for i := 0; i < dim; i++ {
		mean[i] /= count
	}

	return mean
}
