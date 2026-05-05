package rag

import (
	"context"
	"fmt"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/pycompute"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/resilience"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// Engine orchestrates the RAG pipeline.
type Engine struct {
	searchReg      *search.ProviderRegistry
	llmClient      *llm.Client
	pythonClient   *pycompute.Client
	raptor         *RaptorService
	bm25           *BM25
	claimExtractor *ClaimExtractor
	citationGraph  *CitationGraph
	citationScorer *CitationScorer
	config         EngineConfig
}

// NewEngine creates a new RAG engine.
func NewEngine(reg *search.ProviderRegistry, llm *llm.Client) *Engine {
	return NewEngineWithConfig(reg, llm, EngineConfig{})
}

// NewEngineWithConfig creates a new RAG engine with optional canonical
// retrieval and research-memory adapters.
func NewEngineWithConfig(reg *search.ProviderRegistry, llm *llm.Client, cfg EngineConfig) *Engine {
	return &Engine{
		searchReg:      reg,
		llmClient:      llm,
		pythonClient:   pycompute.NewClient(),
		raptor:         NewRaptorService(llm),
		bm25:           NewBM25(),
		claimExtractor: NewClaimExtractor(llm),
		citationGraph:  NewCitationGraph(),
		citationScorer: NewDefaultCitationScorer(),
		config:         cfg,
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

func (e *Engine) DelegateBM25Index(ctx context.Context, documents []string, docIDs []string) error {
	slog.Info("Delegating BM25 indexing to Python compute plane", "doc_count", len(documents))
	return e.pythonClient.DelegateBM25Index(ctx, documents, docIDs)
}

func (e *Engine) DelegateBM25Search(ctx context.Context, query string, topK int) ([]map[string]any, error) {
	slog.Info("Delegating BM25 search to Python compute plane", "query", query)
	return e.pythonClient.DelegateBM25Search(ctx, query, topK)
}

// GetEngine returns the engine itself.
func (e *Engine) GetEngine() *Engine {
	return e
}

func shouldUseResearchMemory(userID string) bool {
	switch strings.TrimSpace(userID) {
	case "", "anonymous", "internal-service":
		return false
	default:
		return true
	}
}

func hasResearchMemory(primer *ResearchMemoryPrimer) bool {
	return primer != nil && (len(primer.Findings) > 0 || len(primer.RecommendedQueries) > 0 || len(primer.RelatedTopics) > 0 || len(primer.RelatedMethods) > 0 || strings.TrimSpace(primer.QuerySummary) != "")
}

func isGlobalResearchQuery(query string) bool {
	globalQueryScore := 0
	queryLower := strings.ToLower(strings.TrimSpace(query))
	globalPhrases := []string{
		"summarize", "summary of", "overview", "landscape", "state of the art",
		"trends in", "recent advances", "survey", "review of", "compare across",
		"broad", "comprehensive", "meta-analysis", "systematic review",
	}
	for _, phrase := range globalPhrases {
		if strings.Contains(queryLower, phrase) {
			globalQueryScore++
		}
	}
	return globalQueryScore >= 2 || (globalQueryScore >= 1 && len(strings.Fields(queryLower)) <= 6)
}

func (e *Engine) lookupResearchMemory(ctx context.Context, req AnswerRequest) *ResearchMemoryPrimer {
	if e.config.ResearchMemoryLookup == nil || !shouldUseResearchMemory(req.UserID) {
		return nil
	}
	primer, err := e.config.ResearchMemoryLookup(ctx, req)
	if err != nil {
		slog.Warn("research memory lookup failed; continuing without memory bias", "user_id", req.UserID, "error", err)
		return nil
	}
	return primer
}

func (e *Engine) retrievePapers(ctx context.Context, req AnswerRequest) (*CanonicalRetrievalResult, error) {
	if e.config.CanonicalRetriever != nil {
		result, err := e.config.CanonicalRetriever(ctx, req)
		if err == nil && result != nil {
			if strings.TrimSpace(result.QueryUsed) == "" {
				result.QueryUsed = strings.TrimSpace(req.Query)
			}
			if strings.TrimSpace(result.Backend) == "" {
				result.Backend = "go-rag-canonical"
			}
			return result, nil
		}
		if err != nil {
			slog.Warn("canonical retrieval failed; falling back to legacy search", "query", req.Query, "error", err)
		}
	}

	return e.retrieveLegacyPapers(ctx, req), nil
}

func (e *Engine) retrieveLegacyPapers(ctx context.Context, req AnswerRequest) *CanonicalRetrievalResult {
	result := &CanonicalRetrievalResult{
		QueryUsed: strings.TrimSpace(req.Query),
		Backend:   "go-rag",
	}
	if e.searchReg == nil {
		return result
	}

	searchOpts := search.SearchOpts{
		Limit:       req.Limit,
		Domain:      req.Domain,
		QualitySort: true,
	}
	if searchOpts.Limit <= 0 {
		searchOpts.Limit = 10
	}

	searchResult := search.ParallelSearch(ctx, e.searchReg, req.Query, searchOpts)
	result.Papers = searchResult.Papers
	if len(searchResult.Warnings) > 0 {
		result.RetrievalTrace = make([]map[string]any, 0, len(searchResult.Warnings))
		for _, warning := range searchResult.Warnings {
			result.RetrievalTrace = append(result.RetrievalTrace, map[string]any{
				"provider": warning.Provider,
				"status":   "warning",
				"message":  warning.Message,
			})
		}
	}

	if len(result.Papers) >= 3 {
		return result
	}

	slog.Info("insufficient papers found, triggering legacy query expansion", "query", req.Query)
	expandedQuery := strings.TrimSpace(req.Query + " academic research peer-reviewed")
	expandedResult := search.ParallelSearch(ctx, e.searchReg, expandedQuery, searchOpts)
	result.Papers = dedupPapers(append(result.Papers, expandedResult.Papers...))
	result.RetrievalTrace = append(result.RetrievalTrace, map[string]any{
		"strategy":  "legacy_query_expansion",
		"status":    "applied",
		"queryUsed": expandedQuery,
	})
	return result
}

func (e *Engine) expandWithMemoryQueries(ctx context.Context, req AnswerRequest, primer *ResearchMemoryPrimer, retrieval *CanonicalRetrievalResult) {
	if retrieval == nil || e.searchReg == nil || primer == nil {
		return
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	if len(retrieval.Papers) >= max(3, limit/2) {
		return
	}

	seenQueries := map[string]struct{}{
		strings.ToLower(strings.TrimSpace(req.Query)):           {},
		strings.ToLower(strings.TrimSpace(retrieval.QueryUsed)): {},
	}
	searchLimit := 3
	if limit < searchLimit {
		searchLimit = limit
	}
	if searchLimit <= 0 {
		searchLimit = 3
	}
	searchOpts := search.SearchOpts{
		Limit:       searchLimit,
		Domain:      req.Domain,
		QualitySort: true,
	}

	for _, memoryQuery := range primer.RecommendedQueries {
		normalized := strings.TrimSpace(memoryQuery)
		if normalized == "" {
			continue
		}
		key := strings.ToLower(normalized)
		if _, ok := seenQueries[key]; ok {
			continue
		}
		seenQueries[key] = struct{}{}

		before := len(retrieval.Papers)
		extra := search.ParallelSearch(ctx, e.searchReg, normalized, searchOpts)
		retrieval.Papers = dedupPapers(append(retrieval.Papers, extra.Papers...))
		if len(retrieval.Papers) > before {
			retrieval.RetrievalTrace = append(retrieval.RetrievalTrace, map[string]any{
				"strategy":    "research_memory_expansion",
				"status":      "applied",
				"queryUsed":   normalized,
				"addedPapers": len(retrieval.Papers) - before,
			})
		}
		if len(retrieval.Papers) >= limit {
			break
		}
	}
}

// GenerateAnswer performs retrieval and synthesis to answer a query.
func (e *Engine) GenerateAnswer(ctx context.Context, req AnswerRequest) (*AnswerResponse, error) {
	startTime := time.Now()
	req.Query = strings.TrimSpace(req.Query)
	req.UserID = strings.TrimSpace(req.UserID)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	primer := e.lookupResearchMemory(ctx, req)

	// 1. Initial Retrieval
	retrievalStart := time.Now()
	retrievalResult, err := e.retrievePapers(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("retrieval failed: %w", err)
	}
	e.expandWithMemoryQueries(ctx, req, primer, retrievalResult)
	retrievalDuration := time.Since(retrievalStart).Milliseconds()
	papers := dedupPapers(retrievalResult.Papers)
	queryUsed := strings.TrimSpace(retrievalResult.QueryUsed)
	if queryUsed == "" {
		queryUsed = req.Query
	}
	isGlobal := isGlobalResearchQuery(queryUsed)

	if len(papers) == 0 {
		return &AnswerResponse{
			Query:  req.Query,
			Answer: "I couldn't find any relevant academic papers. Please try a more specific query.",
			Timing: AnswerTiming{
				TotalMs:     time.Since(startTime).Milliseconds(),
				RetrievalMs: retrievalDuration,
			},
			TraceID: retrievalResult.TraceID,
			Metadata: &ResponseMetadata{
				Backend:             retrievalResult.Backend,
				FallbackTriggered:   false,
				GlobalIntent:        isGlobal,
				QueryUsed:           queryUsed,
				RetrievalTrace:      append([]map[string]any(nil), retrievalResult.RetrievalTrace...),
				RetrievalStrategies: append([]string(nil), retrievalResult.RetrievalStrategies...),
				ResearchMemoryUsed:  hasResearchMemory(primer),
			},
		}, nil
	}

	// 3. Synthesis
	if resilience.IsDegraded(ctx) || e.llmClient == nil {
		return &AnswerResponse{
			Query:  req.Query,
			Answer: "LLM synthesis is currently unavailable (degraded mode). Here are the relevant papers found:",
			Papers: papers,
			Timing: AnswerTiming{
				TotalMs:     time.Since(startTime).Milliseconds(),
				RetrievalMs: retrievalDuration,
			},
			TraceID: retrievalResult.TraceID,
			Metadata: &ResponseMetadata{
				Backend:             retrievalResult.Backend,
				FallbackTriggered:   false,
				GlobalIntent:        isGlobal,
				QueryUsed:           queryUsed,
				RetrievalTrace:      append([]map[string]any(nil), retrievalResult.RetrievalTrace...),
				RetrievalStrategies: append([]string(nil), retrievalResult.RetrievalStrategies...),
				ResearchMemoryUsed:  hasResearchMemory(primer),
			},
		}, nil
	}

	synthesisStart := time.Now()
	plan := buildSynthesisPlan(ctx, req.Query, queryUsed, papers, primer, isGlobal, e.raptor)
	answer, citations, err := e.synthesizeWithPlan(ctx, plan, papers, req.Model)
	if err != nil {
		return nil, fmt.Errorf("synthesis failed: %w", err)
	}
	synthesisDuration := time.Since(synthesisStart).Milliseconds()

	// 4. Evidence Gate (Post-synthesis Verification)
	gate := NewEvidenceGate(e.llmClient)
	gateResult, err := gate.Run(ctx, answer, papers)
	if err != nil {
		slog.Warn("Evidence gate failed", "error", err)
	} else if gateResult.Verdict == "failed" || (gateResult.Confidence > 0 && gateResult.Confidence < 0.7) {
		slog.Info("Evidence gate flagged synthesis, triggering Hindsight Refinement", "verdict", gateResult.Verdict, "confidence", gateResult.Confidence)

		hindsight := NewHindsightRefinementAgent(e.llmClient)
		refinedAnswer, err := hindsight.Refine(ctx, queryUsed, answer, papers)
		if err != nil {
			slog.Error("Hindsight refinement failed", "error", err)
		} else {
			answer = refinedAnswer
			// Re-verify with gate after refinement
			gateResult, _ = gate.Run(ctx, answer, papers)
		}
	}

	fallbackTriggered := false
	fallbackReason := ""
	if gateResult != nil {
		fallbackTriggered = gateResult.Verdict == "failed"
		fallbackReason = gateResult.Verdict
	}

	return &AnswerResponse{
		Query:     req.Query,
		Answer:    answer,
		Papers:    papers,
		Citations: citations,
		TraceID:   retrievalResult.TraceID,
		Timing: AnswerTiming{
			TotalMs:     time.Since(startTime).Milliseconds(),
			RetrievalMs: retrievalDuration,
			SynthesisMs: synthesisDuration,
		},
		Metadata: &ResponseMetadata{
			Backend:             firstNonEmpty(strings.TrimSpace(retrievalResult.Backend), "go-rag"),
			FallbackTriggered:   fallbackTriggered,
			FallbackReason:      fallbackReason,
			GlobalIntent:        isGlobal,
			QueryUsed:           queryUsed,
			RetrievalTrace:      append([]map[string]any(nil), retrievalResult.RetrievalTrace...),
			RetrievalStrategies: append([]string(nil), retrievalResult.RetrievalStrategies...),
			ResearchMemoryUsed:  hasResearchMemory(primer),
			Policy: map[string]any{
				"contextPackets":      len(plan.Packets),
				"raptorOverviewUsed":  strings.TrimSpace(plan.RaptorOverview) != "",
				"researchMemoryHints": hasResearchMemory(primer),
			},
		},
	}, nil
}

func dedupPapers(papers []search.Paper) []search.Paper {
	seen := make(map[string]bool)
	unique := make([]search.Paper, 0, len(papers))
	for _, p := range papers {
		id := p.ID
		if id == "" {
			id = p.DOI
		}
		if id == "" {
			id = p.Title
		}
		if !seen[id] {
			seen[id] = true
			unique = append(unique, p)
		}
	}
	return unique
}

func truncateContextRune(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func renderMemoryPrimer(primer *ResearchMemoryPrimer) string {
	if !hasResearchMemory(primer) {
		return ""
	}

	lines := make([]string, 0, 8)
	if summary := strings.TrimSpace(primer.QuerySummary); summary != "" {
		lines = append(lines, "Prior query summary: "+summary)
	}

	findings := append([]string(nil), primer.Findings...)
	if len(findings) > 3 {
		findings = findings[:3]
	}
	for _, finding := range findings {
		if trimmed := strings.TrimSpace(finding); trimmed != "" {
			lines = append(lines, "Prior non-citable finding: "+trimmed)
		}
	}

	if len(primer.RelatedTopics) > 0 {
		lines = append(lines, "Related topics: "+strings.Join(primer.RelatedTopics, ", "))
	}
	if len(primer.RelatedMethods) > 0 {
		lines = append(lines, "Related methods: "+strings.Join(primer.RelatedMethods, ", "))
	}

	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func buildCitationGroundingTexts(papers []search.Paper) []string {
	texts := make([]string, len(papers))
	for i, paper := range papers {
		texts[i] = paperGroundingText(paper, maxGroundingChars)
		if strings.TrimSpace(texts[i]) == "" {
			texts[i] = strings.TrimSpace(paper.Title)
		}
	}
	return texts
}

func normalizeMultiAgentBackendName(backend string) string {
	trimmed := firstNonEmpty(backend, "go-rag")
	if strings.HasSuffix(trimmed, "-multi-agent") {
		return trimmed
	}
	return trimmed + "-multi-agent"
}

func (e *Engine) mapClaimTextsToCitations(claims []string, papers []search.Paper, category string) []Citation {
	if len(claims) == 0 || len(papers) == 0 {
		return nil
	}

	bm25 := NewBM25()
	paperTexts := buildCitationGroundingTexts(papers)
	citations := make([]Citation, 0, len(claims))
	seen := make(map[string]struct{}, len(claims))

	for _, claimText := range claims {
		trimmedClaim := strings.TrimSpace(claimText)
		if trimmedClaim == "" {
			continue
		}

		scores := bm25.Score(trimmedClaim, paperTexts)
		bestIdx := -1
		bestScore := 0.0
		for i, score := range scores {
			if score > bestScore {
				bestScore = score
				bestIdx = i
			}
		}
		if bestIdx < 0 || bestScore <= 0.1 {
			continue
		}

		paper := papers[bestIdx]
		citationKey := trimmedClaim + "|" + strings.TrimSpace(paper.ID)
		if _, ok := seen[citationKey]; ok {
			continue
		}
		seen[citationKey] = struct{}{}

		node := CitationNode{
			ID:            paper.ID,
			Title:         paper.Title,
			Year:          paper.Year,
			CitationCount: paper.CitationCount,
		}
		confidence := e.citationScorer.CalculateScore(node, bestScore)
		citations = append(citations, Citation{
			Claim:           trimmedClaim,
			SourceID:        paper.ID,
			SourceTitle:     paper.Title,
			Confidence:      confidence,
			CredibilityTier: GetCredibilityTier(confidence),
			Category:        category,
		})
	}

	return citations
}

func (e *Engine) mapExtractedClaimsToCitations(claims []ExtractedClaim, papers []search.Paper) []Citation {
	if len(claims) == 0 || len(papers) == 0 {
		return nil
	}

	bm25 := NewBM25()
	paperTexts := buildCitationGroundingTexts(papers)
	citations := make([]Citation, 0, len(claims))
	seen := make(map[string]struct{}, len(claims))

	for _, claim := range claims {
		trimmedClaim := strings.TrimSpace(claim.Text)
		if trimmedClaim == "" {
			continue
		}

		scores := bm25.Score(trimmedClaim, paperTexts)
		bestIdx := -1
		bestScore := 0.0
		for i, score := range scores {
			if score > bestScore {
				bestScore = score
				bestIdx = i
			}
		}
		if bestIdx < 0 || bestScore <= 0.1 {
			continue
		}

		paper := papers[bestIdx]
		citationKey := trimmedClaim + "|" + strings.TrimSpace(paper.ID)
		if _, ok := seen[citationKey]; ok {
			continue
		}
		seen[citationKey] = struct{}{}

		node := CitationNode{
			ID:            paper.ID,
			Title:         paper.Title,
			Year:          paper.Year,
			CitationCount: paper.CitationCount,
		}
		confidence := e.citationScorer.CalculateScore(node, bestScore)
		citations = append(citations, Citation{
			Claim:           trimmedClaim,
			SourceID:        paper.ID,
			SourceTitle:     paper.Title,
			Confidence:      confidence,
			CredibilityTier: GetCredibilityTier(confidence),
			Category:        string(claim.Category),
		})
	}

	return citations
}

func (e *Engine) synthesizeWithPlan(ctx context.Context, plan synthesisPlan, papers []search.Paper, model string) (string, []Citation, error) {
	if e == nil || e.llmClient == nil {
		return "", nil, fmt.Errorf("llm client is not configured")
	}

	for _, p := range papers {
		e.citationGraph.AddNode(&CitationNode{
			ID:            p.ID,
			Title:         p.Title,
			Authors:       p.Authors,
			Year:          p.Year,
			Venue:         p.Venue,
			CitationCount: p.CitationCount,
		})
	}

	var promptBuilder strings.Builder
	fmt.Fprintf(&promptBuilder, "User Query: %s\n", strings.TrimSpace(plan.Query))
	if queryUsed := strings.TrimSpace(plan.QueryUsed); queryUsed != "" && queryUsed != strings.TrimSpace(plan.Query) {
		fmt.Fprintf(&promptBuilder, "Retrieval Query Used: %s\n", queryUsed)
	}
	fmt.Fprintf(&promptBuilder, "Broad Query: %t\n\n", plan.GlobalIntent)

	if memoryHints := renderMemoryPrimer(plan.MemoryPrimer); memoryHints != "" {
		promptBuilder.WriteString("Research Memory Hints (not direct evidence, do not cite):\n")
		promptBuilder.WriteString(memoryHints)
		promptBuilder.WriteString("\n\n")
	}

	if overview := strings.TrimSpace(plan.RaptorOverview); overview != "" {
		promptBuilder.WriteString("RAPTOR Overview (high-level guidance, do not cite directly):\n")
		promptBuilder.WriteString(overview)
		promptBuilder.WriteString("\n\n")
	}

	promptBuilder.WriteString("Evidence Packets:\n")
	if len(plan.Packets) > 0 {
		for _, packet := range plan.Packets {
			fmt.Fprintf(
				&promptBuilder,
				"[%d] %s\nSource Kind: %s\nSection: %s\nEvidence: %s\n\n",
				packet.PaperOrdinal,
				packet.PaperTitle,
				firstNonEmpty(packet.SourceKind, "paper_context"),
				firstNonEmpty(packet.Section, "general"),
				packet.Text,
			)
		}
	} else {
		for i, paper := range papers {
			fmt.Fprintf(&promptBuilder, "[%d] %s\nEvidence: %s\n\n", i+1, paper.Title, paperGroundingText(paper, maxGroundingChars))
		}
	}

	systemPrompt := `You are WisDev, an academic research assistant.

Answer the user's question using only the provided evidence packets.

Instructions:
1. Cite evidence with bracketed paper ordinals such as [1] and [2].
2. Do not cite the research-memory hints or RAPTOR overview directly; they are routing aids only.
3. Prefer precise, evidence-backed statements over broad generic summaries.
4. If the evidence is incomplete or conflicting, say so explicitly.
5. Keep the answer concise, scientific, and readable.`

	resp, err := e.llmClient.Generate(ctx, applyRAGGeneratePolicy(&llmv1.GenerateRequest{
		Prompt:       promptBuilder.String(),
		SystemPrompt: systemPrompt,
		Model:        model,
		Temperature:  0.2,
	}, model))
	if err != nil {
		return "", nil, err
	}

	answerText, err := normalizeRAGGeneratedText("rag synthesis", resp)
	if err != nil {
		return "", nil, err
	}

	extractedClaims, err := e.claimExtractor.ExtractClaimsCategorized(ctx, answerText)
	if err != nil {
		slog.Warn("advanced claim extraction failed, falling back to heuristic claim grounding", "error", err)
		heuristicClaims := NewEvidenceGate(nil).extractHeuristicClaims(answerText)
		citations := e.mapClaimTextsToCitations(heuristicClaims, papers, string(CategoryEmpirical))
		if len(citations) == 0 {
			citations = e.extractCitations(answerText, papers)
		}
		return answerText, citations, nil
	}
	citations := e.mapExtractedClaimsToCitations(extractedClaims, papers)

	for i := 0; i < len(citations); i++ {
		for j := i + 1; j < len(citations); j++ {
			if citations[i].SourceID == citations[j].SourceID {
				continue
			}
			if tokenJaccard(citations[i].Claim, citations[j].Claim) >= 0.25 {
				e.citationGraph.AddEdge(
					citations[i].SourceID,
					citations[j].SourceID,
					"co-supports: "+truncateRune(citations[i].Claim, 80),
				)
			}
		}
	}

	return answerText, citations, nil
}

func (e *Engine) synthesize(ctx context.Context, query string, papers []search.Paper, model string) (string, []Citation, error) {
	plan := buildSynthesisPlan(ctx, query, query, papers, nil, false, e.raptor)
	return e.synthesizeWithPlan(ctx, plan, papers, model)
}

func (e *Engine) extractCitations(text string, papers []search.Paper) []Citation {
	var citations []Citation
	seen := make(map[string]bool)

	// Preserve explicit [n] marker handling when the model already emitted them.
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

	if len(citations) > 0 {
		return citations
	}

	heuristicClaims := NewEvidenceGate(nil).extractHeuristicClaims(text)
	if len(heuristicClaims) == 0 {
		return nil
	}
	return e.mapClaimTextsToCitations(heuristicClaims, papers, string(CategoryEmpirical))
}

type sectionChunk struct {
	paperID    string
	paperTitle string
	text       string
	section    string
	sourceKind string
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
		if fullText := strings.TrimSpace(p.FullText); fullText != "" {
			beforeCount := len(chunks)
			overlap := chunkSize / 6
			if overlap < 24 {
				overlap = 24
			}
			for _, chunk := range AdaptiveChunking(fullText, p.ID, chunkSize, overlap) {
				text := strings.TrimSpace(chunk.Content)
				if len(text) < 40 {
					continue
				}
				chunks = append(chunks, sectionChunk{
					paperID:    p.ID,
					paperTitle: p.Title,
					text:       truncateContextRune(text, maxPacketChars),
					section:    inferChunkSection(text),
					sourceKind: "full_text_chunk",
				})
			}
			if len(chunks) > beforeCount {
				continue
			}
		}
		if abstract := strings.TrimSpace(p.Abstract); abstract != "" {
			chunks = append(chunks, sectionChunk{
				paperID:    p.ID,
				paperTitle: p.Title,
				text:       abstract,
				section:    "abstract",
				sourceKind: "abstract",
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
	sectionQuery := strings.TrimSpace(strings.TrimSpace(req.SectionGoal) + " " + strings.TrimSpace(req.SectionName))
	scores := bm25.Score(sectionQuery, texts)

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
		useFor := firstNonEmpty(scored[i].chunk.section, detectSectionType(req.SectionName), "general")
		selected[i] = SelectedChunk{
			PaperID:        scored[i].chunk.paperID,
			PaperTitle:     scored[i].chunk.paperTitle,
			Text:           scored[i].chunk.text,
			RelevanceScore: scored[i].score,
			Reasoning:      fmt.Sprintf("Selected from %s for the %s section goal", firstNonEmpty(scored[i].chunk.sourceKind, "paper context"), useFor),
			UseFor:         useFor,
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
	multiReq := req
	if multiReq.Limit <= 0 {
		multiReq.Limit = 15
	}

	resp, err := e.GenerateAnswer(ctx, multiReq)
	if err != nil {
		return nil, err
	}

	rawItems := make([]RawEvidenceItem, 0, len(resp.Citations))
	for _, c := range resp.Citations {
		rawItems = append(rawItems, RawEvidenceItem{
			Claim:      c.Claim,
			SourceID:   c.SourceID,
			Confidence: c.Confidence,
		})
	}
	consolidator := NewEvidenceConsolidator()
	dossier := consolidator.Consolidate(rawItems)

	resp.Dossier = &dossier
	if resp.Metadata == nil {
		resp.Metadata = &ResponseMetadata{}
	}
	resp.Metadata.Backend = normalizeMultiAgentBackendName(resp.Metadata.Backend)
	if resp.Metadata.Policy == nil {
		resp.Metadata.Policy = map[string]any{}
	}
	resp.Metadata.Policy["multiAgent"] = true
	resp.Metadata.Policy["evidenceConsolidated"] = true
	return resp, nil
}
