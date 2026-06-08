package rag

import (
	"context"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

type FileSearchGenerator interface {
	GenerateWithFileSearch(context.Context, llm.FileSearchGenerationRequest) (*llm.FileSearchGenerationResponse, error)
}

// EngineConfig injects canonical retrieval and memory surfaces into the RAG
// engine without introducing a package cycle with wisdev.
type EngineConfig struct {
	CanonicalRetriever   func(context.Context, AnswerRequest) (*CanonicalRetrievalResult, error)
	ResearchMemoryLookup func(context.Context, AnswerRequest) (*ResearchMemoryPrimer, error)
	FileSearchGenerator  FileSearchGenerator
	DefaultFileSearch    FileSearchConfig
}

// CanonicalRetrievalResult captures the canonical retrieval output used by the
// upgraded /rag/answer path.
type CanonicalRetrievalResult struct {
	Papers               []search.Paper
	QueryUsed            string
	TraceID              string
	RetrievalTrace       []map[string]any
	RetrievalStrategies  []string
	Backend              string
	MemoryExpansionTrace []map[string]any
}

// ResearchMemoryPrimer is retrieval-time memory context. It can bias
// retrieval and packet selection, but it is never treated as direct evidence.
type ResearchMemoryPrimer struct {
	Findings           []string
	RecommendedQueries []string
	RelatedTopics      []string
	RelatedMethods     []string
	QuerySummary       string
}
