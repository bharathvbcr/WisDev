package rag

import (
	"context"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestRaptorService(t *testing.T) {
	is := assert.New(t)
	msc := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(msc)
	service := NewRaptorService(client)

	ctx := context.Background()

	t.Run("Build and Query Tree", func(t *testing.T) {
		papers := []PaperChunksRequest{
			{
				PaperID: "p1",
				Chunks: []ChunkDetails{
					{ID: "c1", Content: "This is content of chunk 1", Embedding: []float64{0.1, 0.2}},
					{ID: "c2", Content: "This is content of chunk 2", Embedding: []float64{0.3, 0.4}},
				},
			},
		}

		// Mock summary call
		msc.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{
			Text: "Abstractive summary of chunks",
		}, nil)

		tree, err := service.BuildTree(ctx, papers, 1)
		is.NoError(err)
		is.NotNil(tree)
		is.GreaterOrEqual(tree.Levels, 1)
		is.NotEmpty(tree.Nodes)

		// Query the tree, specifying level 0 to get the leaf
		results, err := service.QueryTree(tree.ID, []float64{0.1, 0.2}, 1, []int{0})
		is.NoError(err)
		is.Len(results, 1)
		is.Equal("c1", results[0].NodeID)
	})

	t.Run("Query Non-existent Tree", func(t *testing.T) {
		_, err := service.QueryTree("ghost", []float64{0.1}, 1, nil)
		is.Error(err)
		is.Contains(err.Error(), "not found")
	})
}
