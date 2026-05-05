package wisdev

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInferTopic(t *testing.T) {
	tests := []struct {
		name     string
		paper    Source
		expected string
	}{
		{
			name:     "empty source",
			paper:    Source{},
			expected: "general",
		},
		{
			name: "machine learning",
			paper: Source{
				Title:   "Deep Learning with Transformers",
				Summary: "Using attention mechanisms for neural networks.",
			},
			expected: "machine_learning",
		},
		{
			name: "biomedical",
			paper: Source{
				Title:   "Cancer Drug Discovery",
				Summary: "Clinical trials for new therapy.",
			},
			expected: "biomedical",
		},
		{
			name: "nlp",
			paper: Source{
				Title:   "Language Models",
				Summary: "Text summarization and translation.",
			},
			expected: "nlp",
		},
		{
			name: "physics",
			paper: Source{
				Title:   "Quantum Entanglement",
				Summary: "Particle physics and relativity.",
			},
			expected: "physics",
		},
		{
			name: "no match",
			paper: Source{
				Title:   "Unknown Topic",
				Summary: "Something completely different.",
			},
			expected: "general",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, inferTopic(tt.paper))
		})
	}
}

func TestBuildSourceClusters(t *testing.T) {
	papers := []Source{
		{ID: "p1", Title: "Neural Networks", Summary: "deep learning transformer"},
		{ID: "p2", Title: "Gradient Descent", Summary: "backpropagation attention"},
		{ID: "p3", Title: "Clinical Trial", Summary: "drug discovery patient"},
		{ID: "p4", Title: "Random", Summary: "nothing related to topics"},
	}

	clusters := buildSourceClusters(papers)

	// biomedical, general, machine_learning
	assert.Len(t, clusters, 3)

	assert.Equal(t, "biomedical", clusters[0].Topic)
	assert.Len(t, clusters[0].PaperIDs, 1)
	assert.True(t, clusters[0].NoveltyFlag)

	assert.Equal(t, "general", clusters[1].Topic)
	assert.Len(t, clusters[1].PaperIDs, 1)
	assert.True(t, clusters[1].NoveltyFlag)

	assert.Equal(t, "machine_learning", clusters[2].Topic)
	assert.Len(t, clusters[2].PaperIDs, 2)
	assert.False(t, clusters[2].NoveltyFlag)
}

func TestScoutService_Run_Validation(t *testing.T) {
	svc := NewScoutService(nil, nil)
	_, err := svc.Run(context.Background(), "sess1", "  ", "domain", "model")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "query is required")
}
