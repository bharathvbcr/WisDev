package rag

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

// RaptorNode represents a node in the RAPTOR tree.
type RaptorNode struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Embedding []float64 `json:"embedding,omitempty"`
	Level     int       `json:"level"`
	Children  []string  `json:"children,omitempty"`
	PaperIDs  []string  `json:"paper_ids,omitempty"`

	// Optional metadata from chunks
	CharStart int `json:"char_start,omitempty"`
	CharEnd   int `json:"char_end,omitempty"`
	Page      int `json:"page,omitempty"`
}

// RaptorTree represents the full hierarchical structure.
type RaptorTree struct {
	ID     string                 `json:"id"`
	Nodes  map[string]*RaptorNode `json:"nodes"`
	Root   *RaptorNode            `json:"root,omitempty"`
	Levels int                    `json:"levels"`
}

// RaptorService handles tree building and querying.
type RaptorService struct {
	mu        sync.RWMutex
	trees     map[string]*RaptorTree
	llmClient *llm.Client
}

var raptorClusterNodes = (*RaptorService).clusterNodes

// NewRaptorService creates a new RAPTOR service.
func NewRaptorService(client *llm.Client) *RaptorService {
	return &RaptorService{
		trees:     make(map[string]*RaptorTree),
		llmClient: client,
	}
}

// BuildTree builds an adaptive RAPTOR tree from paper chunks.
func (s *RaptorService) BuildTree(ctx context.Context, papers []PaperChunksRequest, minClusters int) (*RaptorTree, error) {
	treeID := "tree_" + uuid.New().String()[:8]
	tree := &RaptorTree{
		ID:    treeID,
		Nodes: make(map[string]*RaptorNode),
	}

	// 1. Initialize Level 0 (leaves)
	var leaves []*RaptorNode
	for _, p := range papers {
		for i, c := range p.Chunks {
			id := c.ID
			if id == "" {
				id = fmt.Sprintf("%s_chunk_%d", p.PaperID, i)
			}
			node := &RaptorNode{
				ID:        id,
				Content:   c.Content,
				Embedding: c.Embedding,
				Level:     0,
				PaperIDs:  []string{p.PaperID},
				CharStart: c.CharStart,
				CharEnd:   c.CharEnd,
				Page:      c.Page,
			}
			tree.Nodes[id] = node
			leaves = append(leaves, node)
		}
	}

	if len(leaves) == 0 {
		return tree, nil
	}

	// 2. Hierarchical Clustering (Adaptive)
	currentLevelNodes := leaves
	level := 1
	maxLevels := 4

	for level < maxLevels && len(currentLevelNodes) > minClusters {
		clusters := raptorClusterNodes(s, currentLevelNodes, minClusters)
		if len(clusters) <= 1 && level > 1 {
			break
		}

		var nextLevelNodes []*RaptorNode
		for i, cluster := range clusters {
			clusterID := fmt.Sprintf("level_%d_cluster_%d_%s", level, i, treeID)

			// Calculate centroid
			var embeddings [][]float64
			var paperIDsSet = make(map[string]bool)
			var childrenIDs []string

			for _, n := range cluster {
				if len(n.Embedding) > 0 {
					embeddings = append(embeddings, n.Embedding)
				}
				for _, pid := range n.PaperIDs {
					paperIDsSet[pid] = true
				}
				childrenIDs = append(childrenIDs, n.ID)
			}

			centroid := VectorMean(embeddings)
			summary, err := s.abstractiveSummary(ctx, cluster)
			if err != nil {
				summary = s.extractiveSummary(cluster)
			}

			var paperIDs []string
			for pid := range paperIDsSet {
				paperIDs = append(paperIDs, pid)
			}

			node := &RaptorNode{
				ID:        clusterID,
				Content:   summary,
				Embedding: centroid,
				Level:     level,
				Children:  childrenIDs,
				PaperIDs:  paperIDs,
			}

			tree.Nodes[clusterID] = node
			nextLevelNodes = append(nextLevelNodes, node)
		}

		currentLevelNodes = nextLevelNodes
		level++
	}

	tree.Levels = level
	if len(currentLevelNodes) > 0 {
		// Root is the mean of the top level or a single root if only one left
		if len(currentLevelNodes) == 1 {
			tree.Root = currentLevelNodes[0]
		} else {
			// Create a synthetic root
			var embeddings [][]float64
			var childrenIDs []string
			for _, n := range currentLevelNodes {
				embeddings = append(embeddings, n.Embedding)
				childrenIDs = append(childrenIDs, n.ID)
			}

			summary, err := s.abstractiveSummary(ctx, currentLevelNodes)
			if err != nil {
				summary = s.extractiveSummary(currentLevelNodes)
			}

			tree.Root = &RaptorNode{
				ID:        "root_" + treeID,
				Content:   summary,
				Embedding: VectorMean(embeddings),
				Level:     level,
				Children:  childrenIDs,
			}
			tree.Nodes[tree.Root.ID] = tree.Root
		}
	}

	s.mu.Lock()
	s.trees[treeID] = tree
	s.mu.Unlock()

	return tree, nil
}

// clusterNodes implements a basic HAC-like clustering.
// For production, a more optimized version or library should be used.
func (s *RaptorService) clusterNodes(nodes []*RaptorNode, minClusters int) [][]*RaptorNode {
	if len(nodes) <= minClusters {
		var result [][]*RaptorNode
		for _, n := range nodes {
			result = append(result, []*RaptorNode{n})
		}
		return result
	}

	// Simple k-means-like approach for now since full HAC is O(N^3)
	// We'll use a greedy approach to group nearest neighbors
	numNodes := len(nodes)
	k := int(math.Max(float64(minClusters), math.Sqrt(float64(numNodes))))
	if k > numNodes/2 {
		k = numNodes / 2
	}
	if k < 2 {
		k = 2
	}

	// Randomly pick initial centroids
	clusters := make([][]*RaptorNode, k)
	centroids := make([][]float64, k)
	for i := 0; i < k; i++ {
		centroids[i] = nodes[i*numNodes/k].Embedding
	}

	// 5 iterations of basic k-means
	for iter := 0; iter < 5; iter++ {
		newClusters := make([][]*RaptorNode, k)
		for _, n := range nodes {
			bestSim := -1.0
			bestIdx := 0
			for i, c := range centroids {
				sim := CosineSimilarity(n.Embedding, c)
				if sim > bestSim {
					bestSim = sim
					bestIdx = i
				}
			}
			newClusters[bestIdx] = append(newClusters[bestIdx], n)
		}

		// Update centroids
		for i, cluster := range newClusters {
			if len(cluster) > 0 {
				var embs [][]float64
				for _, n := range cluster {
					embs = append(embs, n.Embedding)
				}
				centroids[i] = VectorMean(embs)
			}
		}
		clusters = newClusters
	}

	// Filter empty clusters
	var finalClusters [][]*RaptorNode
	for _, c := range clusters {
		if len(c) > 0 {
			finalClusters = append(finalClusters, c)
		}
	}

	return finalClusters
}

func (s *RaptorService) extractiveSummary(nodes []*RaptorNode) string {
	var sentences []string
	for _, n := range nodes {
		// Just take the first sentence
		parts := strings.Split(n.Content, ".")
		if len(parts) > 0 {
			sent := strings.TrimSpace(parts[0])
			if len(sent) > 20 {
				sentences = append(sentences, sent+".")
			}
		}
		if len(sentences) >= 5 {
			break
		}
	}
	return strings.Join(sentences, " ")
}

func (s *RaptorService) abstractiveSummary(ctx context.Context, nodes []*RaptorNode) (string, error) {
	if s.llmClient == nil {
		return "", fmt.Errorf("llm client not available")
	}

	var contextBuilder strings.Builder
	for i, n := range nodes {
		fmt.Fprintf(&contextBuilder, "Chunk %d: %s\n\n", i+1, n.Content)
	}

	prompt := fmt.Sprintf(`Provide a concise, high-level research summary of the following related text chunks. 
Focus on the common themes, findings, and methodology across all chunks. 
The summary should be 2-4 sentences and serve as a representative index for these documents.

Chunks:
%s

Summary:`, contextBuilder.String())

	resp, err := s.llmClient.Generate(ctx, applyRAGLightGeneratePolicy(&llmv1.GenerateRequest{
		Prompt:      prompt,
		Model:       llm.ResolveLightModel(), // Use light model for intermediate summaries
		Temperature: 0.2,
	}))
	if err != nil {
		return "", err
	}

	return normalizeRAGGeneratedText("raptor abstractive summary", resp)
}

// QueryTree searches the tree for the most relevant nodes.
func (s *RaptorService) QueryTree(treeID string, queryEmbedding []float64, topK int, levels []int) ([]RaptorSearchResult, error) {
	s.mu.RLock()
	tree, ok := s.trees[treeID]
	s.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("tree not found: %s", treeID)
	}

	levelMap := make(map[int]bool)
	for _, l := range levels {
		levelMap[l] = true
	}

	var results []RaptorSearchResult
	for _, node := range tree.Nodes {
		if len(levels) > 0 && !levelMap[node.Level] {
			continue
		}

		score := CosineSimilarity(queryEmbedding, node.Embedding)
		results = append(results, RaptorSearchResult{
			NodeID:    node.ID,
			Content:   node.Content,
			Score:     score,
			Level:     node.Level,
			PaperIDs:  node.PaperIDs,
			CharStart: node.CharStart,
			CharEnd:   node.CharEnd,
			Page:      node.Page,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if len(results) > topK {
		results = results[:topK]
	}

	return results, nil
}

// Input/Output types
type PaperChunksRequest struct {
	PaperID string         `json:"paper_id"`
	Chunks  []ChunkDetails `json:"chunks"`
}

type ChunkDetails struct {
	ID        string    `json:"chunk_id,omitempty"`
	Content   string    `json:"content"`
	Embedding []float64 `json:"embedding"`
	CharStart int       `json:"char_start,omitempty"`
	CharEnd   int       `json:"char_end,omitempty"`
	Page      int       `json:"page,omitempty"`
}

type RaptorSearchResult struct {
	NodeID    string   `json:"node_id"`
	Content   string   `json:"content"`
	Score     float64  `json:"score"`
	Level     int      `json:"level"`
	PaperIDs  []string `json:"paper_ids"`
	CharStart int      `json:"char_start"`
	CharEnd   int      `json:"char_end"`
	Page      int      `json:"page"`
}
