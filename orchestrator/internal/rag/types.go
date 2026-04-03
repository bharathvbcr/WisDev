package rag

import (
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
)

// AnswerRequest is the input for a RAG answer generation.
type AnswerRequest struct {
	Query     string `json:"query"`
	Domain    string `json:"domain,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Stream    bool   `json:"stream,omitempty"`
	Model     string `json:"model,omitempty"`
	MaxTokens int    `json:"maxTokens,omitempty"`
}

// AnswerResponse is the unified result of a RAG pipeline.
type AnswerResponse struct {
	Query     string            `json:"query"`
	Answer    string            `json:"answer"`
	Papers    []search.Paper    `json:"papers"`
	Citations []Citation        `json:"citations"`
	Timing    AnswerTiming      `json:"timing"`
	TraceID   string            `json:"traceId,omitempty"`
	Metadata  *ResponseMetadata `json:"metadata,omitempty"`
}

type ResponseMetadata struct {
	Backend           string `json:"backend"`
	FallbackTriggered bool   `json:"fallbackTriggered"`
	FallbackReason    string `json:"fallbackReason,omitempty"`
}

// Citation links a claim in the answer to a specific source paper.
type Citation struct {
	Claim       string  `json:"claim"`
	SourceID    string  `json:"sourceId"`
	SourceTitle string  `json:"sourceTitle"`
	Confidence  float64 `json:"confidence"`
}

// AnswerTiming tracks performance of each step in the RAG pipeline.
type AnswerTiming struct {
	TotalMs     int64 `json:"totalMs"`
	RetrievalMs int64 `json:"retrievalMs"`
	SynthesisMs int64 `json:"synthesisMs"`
}

// SectionContextRequest is the input for optimized section context selection.
type SectionContextRequest struct {
	SectionName string         `json:"sectionName"`
	SectionGoal string         `json:"sectionGoal"`
	Papers      []search.Paper `json:"papers"`
	Limit       int            `json:"limit,omitempty"`
	ChunkSize   int            `json:"chunkSize,omitempty"`
}

// SectionContextResponse is the result of section context selection.
type SectionContextResponse struct {
	SectionName    string          `json:"sectionName"`
	SelectedChunks []SelectedChunk `json:"selectedChunks"`
	Bm25Matches    int             `json:"bm25Matches"`
	LatencyMs      int64           `json:"latencyMs"`
}

// SelectedChunk is a specific passage selected for a document section.
type SelectedChunk struct {
	PaperID        string  `json:"paperId"`
	PaperTitle     string  `json:"paperTitle"`
	Text           string  `json:"text"`
	RelevanceScore float64 `json:"relevanceScore"`
	Reasoning      string  `json:"reasoning"`
	UseFor         string  `json:"useFor"` // background, methods, results, etc.
}

// RaptorBuildRequest is the input for building a RAPTOR tree.
type RaptorBuildRequest struct {
	Papers      []PaperChunksRequest `json:"papers"`
	MinClusters int                  `json:"minClusters"`
	MaxLevels   int                  `json:"maxLevels"`
}

// RaptorQueryRequest is the input for querying a RAPTOR tree.
type RaptorQueryRequest struct {
	TreeID         string    `json:"treeId"`
	Query          string    `json:"query"`
	QueryEmbedding []float64 `json:"queryEmbedding,omitempty"`
	TopK           int       `json:"topK"`
	Levels         []int     `json:"levels,omitempty"`
}

// BM25IndexRequest is the input for indexing documents for BM25.
type BM25IndexRequest struct {
	Documents []string `json:"documents"`
	DocIds    []string `json:"docIds"`
}

// BM25QueryRequest is the input for searching documents via BM25.
type BM25QueryRequest struct {
	Query string `json:"query"`
	TopK  int    `json:"topK"`
}

