package api

// Query intent and policy routes: Go-owned query intent classification and
// static query policy tables (precached expansions, strategies, variations).
//
// CANONICAL OWNER: Go. Heuristic Phase-1 patterns run before the LLM fallback
// in /expand/intent. The browser keeps only a thin expandQuery transport cache
// (cache → precache → SPLADE → identity) and empty-query guards.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
)

const queryIntentSchema = `{
	"type":"object",
	"additionalProperties":false,
	"required":["intent"],
	"properties":{
		"intent":{
			"type":"string",
			"enum":["papers","definition","comparison","review","methodology","trends","general"]
		}
	}
}`

// QueryIntentHandler serves POST /expand/intent (LLM fallback classification).
type QueryIntentHandler struct {
	llmClient analysisClient
}

// NewQueryIntentHandler constructs the handler.
func NewQueryIntentHandler(client analysisClient) *QueryIntentHandler {
	return &QueryIntentHandler{llmClient: client}
}

type queryIntentRequest struct {
	Query string `json:"query"`
}

type queryIntentResponse struct {
	Intent search.QueryIntent `json:"intent"`
}

// Handle serves POST /expand/intent.
func (h *QueryIntentHandler) Handle(w http.ResponseWriter, r *http.Request) {
	traceID := resolveRequestTraceID(r)
	logSearchRouteLifecycle(r, "expand_intent", "request_received", "", traceID, "result", "accepted")
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	if h == nil || h.llmClient == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "query intent backend unavailable", nil)
		return
	}

	var req queryIntentRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{"error": err.Error()})
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{"field": "query"})
		return
	}

	intent, err := h.classifyIntent(r, query)
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "intent classification failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(queryIntentResponse{Intent: intent})
}

func (h *QueryIntentHandler) classifyIntent(r *http.Request, query string) (search.QueryIntent, error) {
	if intent, ok := search.DetectQueryIntentHeuristic(query); ok {
		logSearchRouteLifecycle(r, "expand_intent", "heuristic_hit", query, resolveRequestTraceID(r),
			"result", "ok", "intent", string(intent), "source", "heuristic",
		)
		return intent, nil
	}

	prompt := fmt.Sprintf(`Classify this academic search query into exactly ONE category.

Query: "%s"

Categories:
- papers: General search for research papers on a topic
- definition: User wants to understand/define a concept
- comparison: User wants to compare two or more topics/methods
- review: User wants a literature review or overview
- methodology: User wants methods, protocols, or how-to information
- trends: User wants recent developments or emerging research
- general: Ambiguous or non-academic query

%s`, query, structuredOutputSchemaInstruction)

	llmCtx, cancel := analysisLLMContext(r.Context())
	defer cancel()

	structuredClient := analysisStructuredClient(llmCtx, h.llmClient)
	resp, err := structuredClient.StructuredOutput(llmCtx, llm.ApplyStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:      prompt,
		Model:       llm.ResolveLightModel(),
		JsonSchema:  queryIntentSchema,
		MaxTokens:   64,
		Temperature: 0,
	}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "light",
		Structured:    true,
		TaskType:      "analysis",
	})))
	if err != nil {
		return search.IntentPapers, err
	}

	var payload struct {
		Intent string `json:"intent"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(resp.GetJsonResult())), &payload); err != nil {
		return search.IntentPapers, err
	}
	if intent, ok := search.NormalizeQueryIntent(payload.Intent); ok {
		return intent, nil
	}
	return search.IntentPapers, nil
}

// QueryPolicyHandler serves POST /expand/query-policy?action=*.
type QueryPolicyHandler struct{}

type queryPolicyRequest struct {
	Query         string                  `json:"query"`
	Expansion     *search.QueryExpansion  `json:"expansion,omitempty"`
	MaxVariations int                     `json:"maxVariations,omitempty"`
	ResultsNeeded int                     `json:"resultsNeeded,omitempty"`
}

// Handle serves POST /expand/query-policy.
func (h *QueryPolicyHandler) Handle(w http.ResponseWriter, r *http.Request) {
	traceID := resolveRequestTraceID(r)
	logSearchRouteLifecycle(r, "expand_query_policy", "request_received", "", traceID, "result", "accepted")
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	action := strings.TrimSpace(r.URL.Query().Get("action"))
	switch action {
	case "precached":
		h.handlePrecached(w, r)
	case "lookup":
		h.handleLookup(w, r)
	case "strategies":
		h.handleStrategies(w, r)
	case "specificity":
		h.handleSpecificity(w, r)
	case "optimized":
		h.handleOptimized(w, r)
	case "variations":
		h.handleVariations(w, r)
	case "embedding-targets":
		h.handleEmbeddingTargets(w, r)
	default:
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "invalid action", map[string]any{
			"allowedActions": []string{
				"precached",
				"lookup",
				"strategies",
				"specificity",
				"optimized",
				"variations",
				"embedding-targets",
			},
		})
	}
}

func decodeQueryPolicyRequest(w http.ResponseWriter, r *http.Request) (queryPolicyRequest, bool) {
	var req queryPolicyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024)).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{"error": err.Error()})
		return queryPolicyRequest{}, false
	}
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" && r.URL.Query().Get("action") != "embedding-targets" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{"field": "query"})
		return queryPolicyRequest{}, false
	}
	return req, true
}

func (h *QueryPolicyHandler) handlePrecached(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeQueryPolicyRequest(w, r)
	if !ok {
		return
	}
	result := search.GetPrecachedExpansion(req.Query)
	w.Header().Set("Content-Type", "application/json")
	if result == nil {
		json.NewEncoder(w).Encode(map[string]any{"match": false})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"match":  true,
		"result": result,
	})
}

func (h *QueryPolicyHandler) handleLookup(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeQueryPolicyRequest(w, r)
	if !ok {
		return
	}
	result := search.LookupStaticExpansions(req.Query)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *QueryPolicyHandler) handleStrategies(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeQueryPolicyRequest(w, r)
	if !ok {
		return
	}
	expansion, ok := requireExpansion(w, req)
	if !ok {
		return
	}
	strategies := search.GenerateBroaderStrategies(req.Query, expansion)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"strategies": strategies})
}

func (h *QueryPolicyHandler) handleSpecificity(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeQueryPolicyRequest(w, r)
	if !ok {
		return
	}
	result := search.AnalyzeQuerySpecificity(req.Query)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *QueryPolicyHandler) handleOptimized(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeQueryPolicyRequest(w, r)
	if !ok {
		return
	}
	expansion, ok := requireExpansion(w, req)
	if !ok {
		return
	}
	queries := search.GetOptimizedQuerySet(req.Query, expansion, req.ResultsNeeded)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"queries": queries})
}

func (h *QueryPolicyHandler) handleVariations(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeQueryPolicyRequest(w, r)
	if !ok {
		return
	}
	expansion, ok := requireExpansion(w, req)
	if !ok {
		return
	}
	maxVariations := req.MaxVariations
	if maxVariations <= 0 {
		maxVariations = 3
	}
	variations := search.GenerateQueryVariationsForCoverage(req.Query, expansion, maxVariations)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"variations": variations})
}

func (h *QueryPolicyHandler) handleEmbeddingTargets(w http.ResponseWriter, r *http.Request) {
	_ = r
	targets := search.EmbeddingTargets()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"targets": targets})
}

func requireExpansion(w http.ResponseWriter, req queryPolicyRequest) (search.QueryExpansion, bool) {
	if req.Expansion == nil {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "expansion is required", map[string]any{"field": "expansion"})
		return search.QueryExpansion{}, false
	}
	expansion := *req.Expansion
	if expansion.Original == "" {
		expansion.Original = req.Query
	}
	return expansion, true
}
