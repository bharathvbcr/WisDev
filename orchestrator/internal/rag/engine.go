package rag

import (
	"context"
	"fmt"
	"log/slog"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/resilience"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"
	"sort"
	"strings"
	"time"
)

// Engine orchestrates the RAG pipeline.
type Engine struct {
	searchReg *search.ProviderRegistry
	llmClient *llm.Client
	raptor    *RaptorService
	bm25      *BM25
}

// NewEngine creates a new RAG engine.
func NewEngine(reg *search.ProviderRegistry, llm *llm.Client) *Engine {
	return &Engine{
		searchReg: reg,
		llmClient: llm,
		raptor:    NewRaptorService(llm),
		bm25:      NewBM25(),
	}
}

// GetRaptor returns the RAPTOR service.
func (e *Engine) GetRaptor() *RaptorService {
	return e.raptor
}

// GetBM25 returns the BM25 service.
func (e *Engine) GetBM25() *BM25 {
	return e.bm25
}


// GenerateAnswer performs retrieval and synthesis to answer a query.
func (e *Engine) GenerateAnswer(ctx context.Context, req AnswerRequest) (*AnswerResponse, error) {
	startTime := time.Now()

	// 1. Retrieval
	retrievalStart := time.Now()
	searchOpts := search.SearchOpts{
		Limit:       req.Limit,
		Domain:      req.Domain,
		QualitySort: true,
	}
	if searchOpts.Limit <= 0 {
		searchOpts.Limit = 10
	}

	searchResult := search.ParallelSearch(ctx, e.searchReg, req.Query, searchOpts)
	retrievalDuration := time.Since(retrievalStart).Milliseconds()

	papers := searchResult.Papers
	if search.ShouldRunPageIndexRerank(false) {
		papers = search.PageIndexRerankPapers(ctx, req.Query, papers, 20)
	}

	if len(papers) == 0 {
		return &AnswerResponse{
			Query:  req.Query,
			Answer: "I couldn't find any relevant academic papers to answer your question. Try broadening your query or selecting a different domain.",
			Timing: AnswerTiming{
				TotalMs:     time.Since(startTime).Milliseconds(),
				RetrievalMs: retrievalDuration,
			},
		}, nil
	}

	// Degraded mode fallback: if sidecar is down, return search results without synthesis
	if resilience.IsDegraded(ctx) {
		return &AnswerResponse{
			Query:  req.Query,
			Answer: "LLM synthesis is currently unavailable. Showing relevant research papers found for your query.",
			Papers: papers,
			Timing: AnswerTiming{
				TotalMs:     time.Since(startTime).Milliseconds(),
				RetrievalMs: retrievalDuration,
			},
		}, nil
	}

	// 2. Synthesis
	synthesisStart := time.Now()
	answer, citations, err := e.synthesize(ctx, req.Query, papers, req.Model)
	if err != nil {
		slog.Error("RAG synthesis failed", "error", err, "query", req.Query)
		return nil, fmt.Errorf("synthesis failed: %w", err)
	}
	synthesisDuration := time.Since(synthesisStart).Milliseconds()

	return &AnswerResponse{
		Query:     req.Query,
		Answer:    answer,
		Papers:    papers,
		Citations: citations,
		Timing: AnswerTiming{
			TotalMs:     time.Since(startTime).Milliseconds(),
			RetrievalMs: retrievalDuration,
			SynthesisMs: synthesisDuration,
		},
	}, nil
}

func (e *Engine) synthesize(ctx context.Context, query string, papers []search.Paper, model string) (string, []Citation, error) {
	// Prepare context from papers
	var contextBuilder strings.Builder
	for i, p := range papers {
		fmt.Fprintf(&contextBuilder, "[%d] Title: %s\n", i+1, p.Title)
		if p.Abstract != "" {
			fmt.Fprintf(&contextBuilder, "Abstract: %s\n", p.Abstract)
		}
		contextBuilder.WriteString("\n")
	}

	systemPrompt := `You are ScholarLM, an AI research assistant. Your task is to provide a comprehensive, 
accurate answer to the user's query based ONLY on the provided academic paper abstracts. 

Instructions:
1. Use the provided papers to ground your answer.
2. Provide specific citations using [1], [2], etc.
3. If the papers don't contain enough information, state what is missing.
4. Maintain a professional, scientific tone.
5. Format your output as a clear explanation followed by a list of findings.`

	userPrompt := fmt.Sprintf("Query: %s\n\nContext:\n%s", query, contextBuilder.String())

	// Call Python sidecar for synthesis
	resp, err := e.llmClient.Generate(ctx, &llmv1.GenerateRequest{
		Prompt:       userPrompt,
		SystemPrompt: systemPrompt,
		Model:        model,
		Temperature:  0.3,
	})
	if err != nil {
		return "", nil, err
	}

	// Simple citation extraction: look for [N] in text and map to paper
	// In a more advanced version, we'd use structured output to get explicit claim-source mappings.
	citations := e.extractCitations(resp.Text, papers)

	return resp.Text, citations, nil
}

func (e *Engine) extractCitations(text string, papers []search.Paper) []Citation {
	var citations []Citation
	seen := make(map[string]bool)

	// Very basic extraction logic for now
	for i, p := range papers {
		marker := fmt.Sprintf("[%d]", i+1)
		if strings.Contains(text, marker) {
			id := p.ID
			if !seen[id] {
				citations = append(citations, Citation{
					Claim:       fmt.Sprintf("Evidence from %s", p.Title),
					SourceID:    p.ID,
					SourceTitle: p.Title,
					Confidence:  0.9, // Default confidence
				})
				seen[id] = true
			}
		}
	}
	return citations
}

type sectionChunk struct {
	paperID    string
	paperTitle string
	text       string
}

// SelectSectionContext selects the most relevant passages for a document section.
func (e *Engine) SelectSectionContext(ctx context.Context, req SectionContextRequest) (*SectionContextResponse, error) {
	startTime := time.Now()

	chunkSize := req.ChunkSize
	if chunkSize <= 0 {
		chunkSize = 200
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 5
	}

	// 1. Chunking
	var chunks []sectionChunk
	for _, p := range req.Papers {
		if p.Abstract != "" {
			chunks = append(chunks, sectionChunk{
				paperID:    p.ID,
				paperTitle: p.Title,
				text:       p.Abstract,
			})
		}
	}

	if len(chunks) == 0 {
		return &SectionContextResponse{
			SectionName: req.SectionName,
			LatencyMs:   time.Since(startTime).Milliseconds(),
		}, nil
	}

	// 2. BM25 Ranking
	bm25 := NewBM25()
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.text
	}
	scores := bm25.Score(req.SectionGoal, texts)

	type scoredChunk struct {
		chunk sectionChunk
		score float64
	}
	scored := make([]scoredChunk, len(chunks))
	for i := range chunks {
		scored[i] = scoredChunk{chunks[i], scores[i]}
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	// 3. Selection (top K)
	numToSelect := limit
	if numToSelect > len(scored) {
		numToSelect = len(scored)
	}

	selected := make([]SelectedChunk, numToSelect)
	for i := 0; i < numToSelect; i++ {
		selected[i] = SelectedChunk{
			PaperID:        scored[i].chunk.paperID,
			PaperTitle:     scored[i].chunk.paperTitle,
			Text:           scored[i].chunk.text,
			RelevanceScore: scored[i].score,
			Reasoning:      "Highly relevant based on lexical match",
			UseFor:         "general",
		}
	}

	return &SectionContextResponse{
		SectionName:    req.SectionName,
		SelectedChunks: selected,
		Bm25Matches:    len(chunks),
		LatencyMs:      time.Since(startTime).Milliseconds(),
	}, nil
}

// MultiAgentExecute performs an iterative or committee-based RAG flow.
func (e *Engine) MultiAgentExecute(ctx context.Context, req AnswerRequest) (*AnswerResponse, error) {
	startTime := time.Now()

	// 1. Parallel Search from multiple queries (if iterative research was intended)
	// For now, we'll simulate multi-pass by using different search opts.
	pass1Opts := search.SearchOpts{Limit: 10, QualitySort: true, Domain: req.Domain}
	pass1 := search.ParallelSearch(ctx, e.searchReg, req.Query, pass1Opts)

	// Simulating a second pass with a slightly different query or broader scope
	pass2Opts := search.SearchOpts{Limit: 10, QualitySort: false, Domain: req.Domain}
	pass2 := search.ParallelSearch(ctx, e.searchReg, req.Query+" research findings", pass2Opts)

	// 2. Fusion
	fused := RRF([][]search.Paper{pass1.Papers, pass2.Papers}, 60)

	// 3. Local BM25 Reranking (optional, if we had full text, but here we use abstracts)
	if len(fused) > 0 {
		bm25 := NewBM25()
		abstracts := make([]string, len(fused))
		for i, p := range fused {
			abstracts[i] = p.Title + " " + p.Abstract
		}
		bm25Scores := bm25.Score(req.Query, abstracts)

		// Combine RRF and BM25 scores
		for i := range fused {
			fused[i].Score = (fused[i].Score * 0.7) + (bm25Scores[i] * 0.3)
		}

		sort.Slice(fused, func(i, j int) bool {
			return fused[i].Score > fused[j].Score
		})
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 15
	}
	if len(fused) > limit {
		fused = fused[:limit]
	}

	// 4. Synthesis
	synthesisStart := time.Now()
	answer, citations, err := e.synthesize(ctx, req.Query, fused, req.Model)
	if err != nil {
		return nil, err
	}
	synthesisDuration := time.Since(synthesisStart).Milliseconds()

	return &AnswerResponse{
		Query:     req.Query,
		Answer:    answer,
		Papers:    fused,
		Citations: citations,
		Timing: AnswerTiming{
			TotalMs:     time.Since(startTime).Milliseconds(),
			RetrievalMs: time.Since(startTime).Milliseconds() - synthesisDuration,
			SynthesisMs: synthesisDuration,
		},
	}, nil
}
