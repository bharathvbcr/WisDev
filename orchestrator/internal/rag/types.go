package rag

import (
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

// AnswerRequest is the input for a RAG answer generation.
type AnswerRequest struct {
	Query      string            `json:"query"`
	Domain     string            `json:"domain,omitempty"`
	Limit      int               `json:"limit,omitempty"`
	Stream     bool              `json:"stream,omitempty"`
	Model      string            `json:"model,omitempty"`
	MaxTokens  int               `json:"maxTokens,omitempty"`
	UserID     string            `json:"userId,omitempty"`
	ProjectID  string            `json:"projectId,omitempty"`
	FileSearch *FileSearchConfig `json:"fileSearch,omitempty"`
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
	// Dossier contains cross-paper evidence consolidation: deduplicated claims,
	// scored contradictions, knowledge gaps, and hard blockers that require
	// human arbitration before synthesis proceeds.
	Dossier *ConsolidatedDossier `json:"dossier,omitempty"`
}

type ResponseMetadata struct {
	Backend             string              `json:"backend"`
	FallbackTriggered   bool                `json:"fallbackTriggered"`
	FallbackReason      string              `json:"fallbackReason,omitempty"`
	GlobalIntent        bool                `json:"globalIntent,omitempty"`
	QueryUsed           string              `json:"queryUsed,omitempty"`
	RetrievalTrace      []map[string]any    `json:"retrievalTrace,omitempty"`
	RetrievalStrategies []string            `json:"retrievalStrategies,omitempty"`
	ResearchMemoryUsed  bool                `json:"researchMemoryUsed,omitempty"`
	FileSearch          *FileSearchMetadata `json:"fileSearch,omitempty"`
	Policy              map[string]any      `json:"policy,omitempty"`
}

// Citation links a claim in the answer to a specific source paper.
type Citation struct {
	Claim           string         `json:"claim"`
	SourceID        string         `json:"sourceId"`
	SourceTitle     string         `json:"sourceTitle"`
	Confidence      float64        `json:"confidence"`
	CredibilityTier string         `json:"credibilityTier"` // High Credibility, Established, etc.
	Category        string         `json:"category"`        // methodological, empirical, theoretical
	SourceURI       string         `json:"sourceUri,omitempty"`
	FileSearchStore string         `json:"fileSearchStore,omitempty"`
	PageStart       int            `json:"pageStart,omitempty"`
	PageEnd         int            `json:"pageEnd,omitempty"`
	MediaID         string         `json:"mediaId,omitempty"`
	MediaURI        string         `json:"mediaUri,omitempty"`
	SourceKind      string         `json:"sourceKind,omitempty"`
	CustomMetadata  map[string]any `json:"customMetadata,omitempty"`
}

// FileSearchConfig enables Gemini API File Search as a grounding source for a
// RAG answer. Store ingestion is managed by Gemini API File Search; ScholarLM's
// Go RAG engine owns query-time orchestration and citation normalization.
type FileSearchConfig struct {
	Enabled        bool     `json:"enabled,omitempty"`
	Required       bool     `json:"required,omitempty"`
	StoreNames     []string `json:"storeNames,omitempty"`
	MetadataFilter string   `json:"metadataFilter,omitempty"`
	TopK           int      `json:"topK,omitempty"`
	Model          string   `json:"model,omitempty"`
	Multimodal     bool     `json:"multimodal,omitempty"`
	EmbeddingModel string   `json:"embeddingModel,omitempty"`
}

// FileSearchMetadata reports the Gemini File Search path used for the answer.
type FileSearchMetadata struct {
	Enabled            bool     `json:"enabled"`
	Required           bool     `json:"required,omitempty"`
	StoreNames         []string `json:"storeNames,omitempty"`
	StoreCount         int      `json:"storeCount,omitempty"`
	MetadataFilter     string   `json:"metadataFilter,omitempty"`
	TopK               int      `json:"topK,omitempty"`
	Model              string   `json:"model,omitempty"`
	Multimodal         bool     `json:"multimodal,omitempty"`
	EmbeddingModel     string   `json:"embeddingModel,omitempty"`
	Backend            string   `json:"backend,omitempty"`
	CitationCount      int      `json:"citationCount,omitempty"`
	PageCitationCount  int      `json:"pageCitationCount,omitempty"`
	MediaCitationCount int      `json:"mediaCitationCount,omitempty"`
	RetrievalQueries   []string `json:"retrievalQueries,omitempty"`
	FallbackReason     string   `json:"fallbackReason,omitempty"`
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

// CitationNode represents a paper in the citation graph.
type CitationNode struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Authors          []string `json:"authors"`
	Year             int      `json:"year"`
	Venue            string   `json:"venue"`
	CitationCount    int      `json:"citationCount"`
	CredibilityScore float64  `json:"credibilityScore"`
}

// CitationEdge represents a citation relationship between papers.
type CitationEdge struct {
	SourceID string `json:"sourceId"` // Citing paper
	TargetID string `json:"targetId"` // Cited paper
	Context  string `json:"context"`  // Snippet where citation occurs
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
