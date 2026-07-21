package api

// Relevance-scoring routes: Go-owned port of the browser-side LLM relevance
// scoring that previously lived in frontend/services/relevanceService.ts
// (scoreRelevanceBatch / scoreRelevanceDeep prompt + schema construction).
//
// CANONICAL OWNER: Go. Part of the "thin the frontend, consolidate
// orchestration in Go" migration (Phase 2 close-out). The browser now POSTs
// paper metadata to /api/relevance?action=score-batch|score-deep and receives
// scores; prompts, JSON schemas, model-tier choice, chunking, and thresholds
// are decided here. The frontend keeps only its result cache, deterministic
// keyword/heuristic fallback (also the offline degraded path), and display
// helpers.
//
// Mirrors the handler conventions of research_paper_suggestion.go (action
// query param, structuredOutput via analysisStructuredClient + request policy).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/policy"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
)

const (
	// relevanceChunkSize mirrors CHUNK_SIZE in the TS implementation.
	relevanceChunkSize = 10
	// relevanceMaxConcurrentChunks mirrors MAX_CONCURRENT_CHUNKS.
	relevanceMaxConcurrentChunks = 2
	// relevanceMaxPapers bounds a single batch request.
	relevanceMaxPapers = 60
	// relevanceAbstractLimit mirrors the TS 120-char abstract truncation.
	relevanceAbstractLimit = 120

	relevanceBatchSchema = `{
		"type":"object",
		"additionalProperties":false,
		"required":["scores"],
		"properties":{
			"scores":{
				"type":"array",
				"items":{
					"type":"object",
					"additionalProperties":false,
					"required":["index","score"],
					"properties":{
						"index":{"type":"integer","minimum":1},
						"score":{"type":"number","minimum":0,"maximum":100},
						"reason":{"type":"string"}
					}
				}
			}
		}
	}`

	relevanceDeepSchema = `{
		"type":"object",
		"additionalProperties":false,
		"required":["score","reason","matchedConcepts"],
		"properties":{
			"score":{"type":"number","minimum":0,"maximum":100},
			"reason":{"type":"string"},
			"matchedConcepts":{"type":"array","items":{"type":"string"}}
		}
	}`
)

// RelevanceHandler serves Go-owned LLM relevance scoring.
type RelevanceHandler struct {
	llmClient analysisClient
}

// NewRelevanceHandler constructs the handler.
func NewRelevanceHandler(client analysisClient) *RelevanceHandler {
	return &RelevanceHandler{llmClient: client}
}

// Handle serves POST /api/relevance?action=score-batch|score-deep.
func (h *RelevanceHandler) Handle(w http.ResponseWriter, r *http.Request) {
	logAPIRouteLifecycle(r, "api.relevance", "relevance", "request_received", "", "result", "accepted")

	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	switch r.URL.Query().Get("action") {
	case "score-batch":
		h.handleScoreBatch(w, r)
	case "score-deep":
		h.handleScoreDeep(w, r)
	default:
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "invalid action", map[string]any{
			"allowedActions": []string{"score-batch", "score-deep"},
		})
	}
}

type relevancePaperInput struct {
	Title    string   `json:"title"`
	Abstract string   `json:"abstract"`
	Keywords []string `json:"keywords,omitempty"`
}

type relevanceScoreItem struct {
	Index  int     `json:"index"` // 1-based position in the request papers array
	Score  float64 `json:"score"`
	Reason string  `json:"reason,omitempty"`
}

func (h *RelevanceHandler) structuredOutput(
	ctx context.Context,
	prompt, schema, requestedTier, model string,
	maxTokens int32,
) (json.RawMessage, error) {
	if h.llmClient == nil {
		return nil, fmt.Errorf("llm client unavailable")
	}

	llmCtx, cancel := analysisLLMContext(ctx)
	defer cancel()

	structuredClient := analysisStructuredClient(llmCtx, h.llmClient)
	resp, err := structuredClient.StructuredOutput(llmCtx, llm.ApplyStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:      prompt,
		Model:       model,
		JsonSchema:  schema,
		MaxTokens:   maxTokens,
		Temperature: 0.2,
	}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: requestedTier,
		Structured:    true,
		TaskType:      "analysis",
	})))
	if err != nil {
		return nil, err
	}
	payload := strings.TrimSpace(resp.GetJsonResult())
	if payload == "" {
		return nil, fmt.Errorf("structured output returned empty payload")
	}
	return json.RawMessage(payload), nil
}

// buildBatchPrompt mirrors the chunk prompt from scoreRelevanceBatch.
func buildBatchPrompt(query string, chunk []relevancePaperInput, includeReasoning bool) string {
	descriptions := make([]string, 0, len(chunk))
	for idx, paper := range chunk {
		abstract := strings.TrimSpace(paper.Abstract)
		if abstract == "" {
			abstract = "No abstract available"
		}
		if len(abstract) > relevanceAbstractLimit {
			abstract = abstract[:relevanceAbstractLimit] + "..."
		}
		descriptions = append(descriptions, fmt.Sprintf("%d. Title: %q\n   Abstract: %q", idx+1, paper.Title, abstract))
	}

	reasoningLine := "- Keep reasons empty unless they are needed to disambiguate a borderline score."
	if includeReasoning {
		reasoningLine = "- Include a brief one-sentence reason for each score."
	}

	return fmt.Sprintf(`You are a research relevance assessor. Evaluate how relevant each paper is to the research query.

**Research Query:** %q

**Papers to Evaluate:**
%s

Return a score for every paper using the provided 1-based index.
- 80-100: Highly relevant and directly addresses the query
- 60-79: Moderately relevant and clearly related
- 40-59: Marginal or loosely connected
- 0-39: Off-topic or not relevant
%s
- Do not skip papers or invent extra indices.`, query, strings.Join(descriptions, "\n\n"), reasoningLine)
}

func clampRelevanceScore(score float64) float64 {
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

// handleScoreBatch serves action=score-batch: chunked structured scoring of
// paper title/abstract pairs against the query. Chunk failures are logged and
// omitted (the client treats missing indices as unscored), mirroring the
// previous browser behavior.
func (h *RelevanceHandler) handleScoreBatch(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req struct {
		Query            string                `json:"query"`
		SearchMode       string                `json:"searchMode,omitempty"`
		IncludeReasoning *bool                 `json:"includeReasoning,omitempty"`
		Papers           []relevancePaperInput `json:"papers"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512*1024)).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{"error": err.Error()})
		return
	}

	query := strings.TrimSpace(req.Query)
	if query == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", nil)
		return
	}
	if len(req.Papers) == 0 {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "papers required", map[string]any{"field": "papers"})
		return
	}
	if len(req.Papers) > relevanceMaxPapers {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "too many papers", map[string]any{
			"max": relevanceMaxPapers, "received": len(req.Papers),
		})
		return
	}

	includeReasoning := true
	if req.IncludeReasoning != nil {
		includeReasoning = *req.IncludeReasoning
	}

	type chunkResult struct {
		offset int
		items  []relevanceScoreItem
		err    error
	}

	var chunks [][]relevancePaperInput
	for i := 0; i < len(req.Papers); i += relevanceChunkSize {
		end := i + relevanceChunkSize
		if end > len(req.Papers) {
			end = len(req.Papers)
		}
		chunks = append(chunks, req.Papers[i:end])
	}

	results := make([]chunkResult, len(chunks))
	sem := make(chan struct{}, relevanceMaxConcurrentChunks)
	var wg sync.WaitGroup
	for chunkIndex, chunk := range chunks {
		wg.Add(1)
		go func(chunkIndex int, chunk []relevancePaperInput) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			prompt := buildBatchPrompt(query, chunk, includeReasoning)
			raw, err := h.structuredOutput(r.Context(), prompt, relevanceBatchSchema, "light", llm.ResolveLightModel(), 2048)
			if err != nil {
				results[chunkIndex] = chunkResult{err: err}
				return
			}
			var parsed struct {
				Scores []relevanceScoreItem `json:"scores"`
			}
			if err := json.Unmarshal(raw, &parsed); err != nil {
				results[chunkIndex] = chunkResult{err: err}
				return
			}
			globalOffset := chunkIndex * relevanceChunkSize
			items := make([]relevanceScoreItem, 0, len(parsed.Scores))
			seen := make(map[int]struct{}, len(parsed.Scores))
			for _, item := range parsed.Scores {
				localIdx := item.Index - 1
				if localIdx < 0 || localIdx >= len(chunk) {
					continue
				}
				if _, dup := seen[localIdx]; dup {
					continue
				}
				seen[localIdx] = struct{}{}
				items = append(items, relevanceScoreItem{
					Index:  globalOffset + localIdx + 1,
					Score:  clampRelevanceScore(item.Score),
					Reason: strings.TrimSpace(item.Reason),
				})
			}
			results[chunkIndex] = chunkResult{offset: globalOffset, items: items}
		}(chunkIndex, chunk)
	}
	wg.Wait()

	var scores []relevanceScoreItem
	failedChunks := 0
	for chunkIndex, res := range results {
		if res.err != nil {
			failedChunks++
			slog.Warn("relevance chunk scoring failed",
				"service", "go_orchestrator",
				"runtime", "go",
				"component", "api",
				"operation", "relevance.score_batch",
				"stage", "chunk",
				"provider", "llm",
				"result", "error",
				"attempt", 1,
				"chunk_index", chunkIndex,
				"error_code", "relevance_chunk_failed",
				"error", res.err.Error(),
			)
			continue
		}
		scores = append(scores, res.items...)
	}

	if failedChunks == len(chunks) {
		WriteError(w, http.StatusBadGateway, ErrServiceUnavailable, "relevance scoring unavailable", map[string]any{
			"failedChunks": failedChunks,
		})
		return
	}

	slog.Info("relevance batch scored",
		"service", "go_orchestrator",
		"runtime", "go",
		"component", "api",
		"operation", "relevance.score_batch",
		"stage", "response",
		"provider", "llm",
		"result", "ok",
		"paper_count", len(req.Papers),
		"chunk_count", len(chunks),
		"failed_chunks", failedChunks,
		"score_count", len(scores),
		"latency_ms", time.Since(start).Milliseconds(),
	)

	writeEnvelope(w, "relevance", map[string]any{
		"scores":           scores,
		"appliedThreshold": policy.RelevanceThreshold(req.SearchMode),
		"failedChunks":     failedChunks,
	})
}

// handleScoreDeep serves action=score-deep: single-paper deep relevance
// analysis (port of scoreRelevanceDeep; effective model tier "standard",
// matching the browser's heavy→standard downgrade).
func (h *RelevanceHandler) handleScoreDeep(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req struct {
		Query string              `json:"query"`
		Paper relevancePaperInput `json:"paper"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024)).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{"error": err.Error()})
		return
	}

	query := strings.TrimSpace(req.Query)
	if query == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", nil)
		return
	}
	if strings.TrimSpace(req.Paper.Title) == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "paper.title is required", nil)
		return
	}

	abstract := strings.TrimSpace(req.Paper.Abstract)
	if abstract == "" {
		abstract = "No abstract available"
	}
	keywords := "None provided"
	if len(req.Paper.Keywords) > 0 {
		keywords = strings.Join(req.Paper.Keywords, ", ")
	}

	prompt := fmt.Sprintf(`You are a research relevance expert. Analyze how relevant this paper is to the research query.

**Research Query:** %q

**Paper:**
- Title: %q
- Abstract: %q
- Keywords: %s

Provide:
1. A relevance score from 0-100
2. A concise explanation of why the score was assigned
3. Key matching concepts between the query and paper

Score based on topical fit, not paper quality or novelty.`, query, req.Paper.Title, abstract, keywords)

	raw, err := h.structuredOutput(r.Context(), prompt, relevanceDeepSchema, "standard", llm.ResolveStandardModel(), 800)
	if err != nil {
		slog.Warn("relevance deep scoring failed",
			"service", "go_orchestrator",
			"runtime", "go",
			"component", "api",
			"operation", "relevance.score_deep",
			"stage", "llm_call",
			"provider", "llm",
			"result", "error",
			"attempt", 1,
			"error_code", "relevance_deep_failed",
			"error", err.Error(),
		)
		WriteError(w, http.StatusBadGateway, ErrServiceUnavailable, "relevance scoring unavailable", nil)
		return
	}

	var parsed struct {
		Score           float64  `json:"score"`
		Reason          string   `json:"reason"`
		MatchedConcepts []string `json:"matchedConcepts"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		WriteError(w, http.StatusBadGateway, ErrServiceUnavailable, "relevance scoring returned invalid payload", nil)
		return
	}

	slog.Info("relevance deep scored",
		"service", "go_orchestrator",
		"runtime", "go",
		"component", "api",
		"operation", "relevance.score_deep",
		"stage", "response",
		"provider", "llm",
		"result", "ok",
		"latency_ms", time.Since(start).Milliseconds(),
	)

	writeEnvelope(w, "relevance", map[string]any{
		"score":           clampRelevanceScore(parsed.Score),
		"reason":          strings.TrimSpace(parsed.Reason),
		"matchedConcepts": parsed.MatchedConcepts,
	})
}
