package rag

import (
	"context"
	"errors"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
)

func TestRaptorService_BuildTreeBranches(t *testing.T) {
	t.Run("empty leaves returns empty tree", func(t *testing.T) {
		service := NewRaptorService(nil)
		tree, err := service.BuildTree(context.Background(), nil, 1)
		assert.NoError(t, err)
		assert.NotNil(t, tree)
		assert.Empty(t, tree.Nodes)
		assert.Zero(t, tree.Levels)
	})

	t.Run("empty chunk ID gets generated", func(t *testing.T) {
		service := NewRaptorService(nil)
		tree, err := service.BuildTree(context.Background(), []PaperChunksRequest{
			{
				PaperID: "paper1",
				Chunks: []ChunkDetails{
					{
						Content: "This chunk is long enough to survive extractive summarization. Another sentence follows.",
					},
				},
			},
		}, 1)
		assert.NoError(t, err)
		assert.NotNil(t, tree.Root)
		assert.Equal(t, "paper1_chunk_0", tree.Root.ID)
		assert.Contains(t, tree.Nodes, "paper1_chunk_0")
	})

	t.Run("multi-node tree falls back to extractive summary", func(t *testing.T) {
		stub := &ragLLMStub{generateErr: errors.New("boom")}
		service := NewRaptorService(newRagLLMClient(stub))

		tree, err := service.BuildTree(context.Background(), []PaperChunksRequest{
			{
				PaperID: "p1",
				Chunks: []ChunkDetails{
					{ID: "a", Content: "First chunk sentence one. First chunk sentence two.", Embedding: []float64{1, 0}},
					{ID: "b", Content: "Second chunk sentence one. Second chunk sentence two.", Embedding: []float64{0, 1}},
				},
			},
		}, 1)
		assert.NoError(t, err)
		assert.NotNil(t, tree.Root)
		assert.Contains(t, tree.Root.Content, ".")
		assert.Greater(t, tree.Levels, 1)
	})

	t.Run("second-level single cluster stops tree growth", func(t *testing.T) {
		originalClusterNodes := raptorClusterNodes
		defer func() {
			raptorClusterNodes = originalClusterNodes
		}()

		calls := 0
		raptorClusterNodes = func(_ *RaptorService, nodes []*RaptorNode, _ int) [][]*RaptorNode {
			calls++
			switch calls {
			case 1:
				return [][]*RaptorNode{
					{nodes[0], nodes[1]},
					{nodes[2], nodes[3]},
				}
			default:
				return [][]*RaptorNode{
					{nodes[0]},
				}
			}
		}

		service := NewRaptorService(nil)
		tree, err := service.BuildTree(context.Background(), []PaperChunksRequest{
			{
				PaperID: "p2",
				Chunks: []ChunkDetails{
					{ID: "a", Content: "First chunk sentence one. First chunk sentence two.", Embedding: []float64{1, 0}},
					{ID: "b", Content: "Second chunk sentence one. Second chunk sentence two.", Embedding: []float64{0.9, 0.1}},
					{ID: "c", Content: "Third chunk sentence one. Third chunk sentence two.", Embedding: []float64{0, 1}},
					{ID: "d", Content: "Fourth chunk sentence one. Fourth chunk sentence two.", Embedding: []float64{0.1, 0.9}},
				},
			},
		}, 1)
		assert.NoError(t, err)
		assert.NotNil(t, tree.Root)
		assert.Equal(t, 2, tree.Levels)
		assert.Equal(t, 2, calls)
	})
}

func TestRaptorService_ClusterNodesBranches(t *testing.T) {
	service := NewRaptorService(nil)

	t.Run("len <= minClusters returns singleton clusters", func(t *testing.T) {
		nodes := []*RaptorNode{
			{ID: "a", Embedding: []float64{1, 0}},
			{ID: "b", Embedding: []float64{0, 1}},
		}
		clusters := service.clusterNodes(nodes, 2)
		assert.Len(t, clusters, 2)
		assert.Len(t, clusters[0], 1)
		assert.Len(t, clusters[1], 1)
	})

	t.Run("k less than two is clamped", func(t *testing.T) {
		nodes := []*RaptorNode{
			{ID: "a", Embedding: []float64{1, 0}},
			{ID: "b", Embedding: []float64{0, 1}},
			{ID: "c", Embedding: []float64{0.5, 0.5}},
		}
		clusters := service.clusterNodes(nodes, 0)
		assert.NotEmpty(t, clusters)
	})

	t.Run("k capped at half of node count", func(t *testing.T) {
		nodes := []*RaptorNode{
			{ID: "a", Embedding: []float64{1, 0}},
			{ID: "b", Embedding: []float64{0, 1}},
			{ID: "c", Embedding: []float64{0.4, 0.6}},
			{ID: "d", Embedding: []float64{0.6, 0.4}},
		}
		clusters := service.clusterNodes(nodes, 3)
		assert.NotEmpty(t, clusters)
	})
}

func TestRaptorService_SummaryHelpers(t *testing.T) {
	service := NewRaptorService(nil)

	t.Run("extractive summary keeps long first sentence", func(t *testing.T) {
		summary := service.extractiveSummary([]*RaptorNode{
			{Content: "Short."},
			{Content: "This sentence is long enough to be included. Another sentence follows."},
		})
		assert.Contains(t, summary, "This sentence is long enough to be included.")
	})

	t.Run("extractive summary ignores short fragments", func(t *testing.T) {
		summary := service.extractiveSummary([]*RaptorNode{
			{Content: "Tiny."},
		})
		assert.Empty(t, summary)
	})

	t.Run("extractive summary stops at five sentences", func(t *testing.T) {
		summary := service.extractiveSummary([]*RaptorNode{
			{Content: "Sentence one is long enough to keep. Sentence two stays here."},
			{Content: "Sentence three is also long enough to keep. Sentence four follows."},
			{Content: "Sentence five is long enough to keep. Sentence six should not appear."},
			{Content: "Sentence seven is long enough to keep."},
			{Content: "Sentence eight is long enough to keep."},
			{Content: "Sentence nine is long enough to keep."},
		})
		assert.Contains(t, summary, "Sentence five is long enough to keep.")
		assert.NotContains(t, summary, "Sentence six should not appear.")
	})

	t.Run("abstractive summary nil client", func(t *testing.T) {
		summary, err := service.abstractiveSummary(context.Background(), []*RaptorNode{{Content: "Chunk text"}})
		assert.Error(t, err)
		assert.Empty(t, summary)
	})

	t.Run("abstractive summary success and error", func(t *testing.T) {
		stub := &ragLLMStub{
			generateResp: &llmv1.GenerateResponse{Text: " concise summary \n"},
		}
		withClient := NewRaptorService(newRagLLMClient(stub))
		summary, err := withClient.abstractiveSummary(context.Background(), []*RaptorNode{{Content: "Chunk one"}})
		assert.NoError(t, err)
		assert.Equal(t, "concise summary", summary)
		assert.Equal(t, llm.ResolveLightModel(), stub.generateReq.GetModel())
		assert.Equal(t, "light", stub.generateReq.GetRequestClass())
		assert.Equal(t, "conservative", stub.generateReq.GetRetryProfile())
		assert.Equal(t, "standard", stub.generateReq.GetServiceTier())
		assert.EqualValues(t, 0, stub.generateReq.GetThinkingBudget())
		assert.Greater(t, stub.generateReq.GetLatencyBudgetMs(), int32(0))

		stubErr := &ragLLMStub{generateErr: errors.New("fail")}
		withErrClient := NewRaptorService(newRagLLMClient(stubErr))
		_, err = withErrClient.abstractiveSummary(context.Background(), []*RaptorNode{{Content: "Chunk one"}})
		assert.Error(t, err)
	})

	t.Run("abstractive summary rejects empty output", func(t *testing.T) {
		stub := &ragLLMStub{generateResp: &llmv1.GenerateResponse{Text: "   "}}
		withClient := NewRaptorService(newRagLLMClient(stub))
		summary, err := withClient.abstractiveSummary(context.Background(), []*RaptorNode{{Content: "Chunk one"}})
		assert.Error(t, err)
		assert.Empty(t, summary)
		assert.Contains(t, err.Error(), "empty text")
	})
}

func TestRaptorService_QueryTreeFiltersAndLimits(t *testing.T) {
	service := NewRaptorService(nil)
	service.mu.Lock()
	service.trees["tree1"] = &RaptorTree{
		ID: "tree1",
		Nodes: map[string]*RaptorNode{
			"a": {ID: "a", Content: "A", Embedding: []float64{1, 0}, Level: 0},
			"b": {ID: "b", Content: "B", Embedding: []float64{0, 1}, Level: 1},
			"c": {ID: "c", Content: "C", Embedding: []float64{1, 1}, Level: 1},
		},
	}
	service.mu.Unlock()

	results, err := service.QueryTree("tree1", []float64{0, 1}, 1, []int{1})
	assert.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "b", results[0].NodeID)
}
