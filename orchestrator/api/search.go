package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/semaphore"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

const (
	batchMaxQueries    = 20
	batchMaxConcurrent = 5
	batchDefaultLimit  = 5
)

type SearchHandler struct {
	registry     *search.ProviderRegistry
	fastRegistry *search.ProviderRegistry
	openSearch   OpenSearchExecutor
	redis        redis.UniversalClient
}

type searchRequest struct {
	Query       string `json:"query"`
	Limit       int    `json:"limit"`
	ExpandQuery *bool  `json:"expandQuery,omitempty"`
	QualitySort *bool  `json:"qualitySort,omitempty"`
	SkipCache   *bool  `json:"skipCache,omitempty"`
	Domain      string `json:"domain,omitempty"`
	PageIndex   *bool  `json:"pageIndex,omitempty"`
	L3          *bool  `json:"l3,omitempty"`
}

type batchSearchRequest struct {
	Queries []string `json:"queries"`
	Limit   int      `json:"limit"`
}

type hybridSearchRequest struct {
	Query            string `json:"query"`
	Limit            int    `json:"limit"`
	ExpandQuery      *bool  `json:"expandQuery,omitempty"`
	QualitySort      *bool  `json:"qualitySort,omitempty"`
	SkipCache        *bool  `json:"skipCache,omitempty"`
	Domain           string `json:"domain,omitempty"`
	PageIndex        *bool  `json:"pageIndex,omitempty"`
	L3               *bool  `json:"l3,omitempty"`
	RetrievalBackend string `json:"retrievalBackend,omitempty"`
	RetrievalMode    string `json:"retrievalMode,omitempty"`
	FusionMode       string `json:"fusionMode,omitempty"`
	LatencyBudgetMs  int    `json:"latencyBudgetMs,omitempty"`
}

type queryIntroductionRequest struct {
	Query         string                   `json:"query"`
	Papers        []queryIntroductionPaper `json:"papers"`
	ProvidersUsed []string                 `json:"providersUsed"`
}

type batchSummariesRequest struct {
	Papers      []batchSummaryPaper `json:"papers"`
	MaxFindings int                 `json:"max_findings"`
}

type gatewayEnhancedQuery struct {
	Original string   `json:"original"`
	Expanded string   `json:"expanded"`
	Intent   string   `json:"intent"`
	Keywords []string `json:"keywords"`
	Synonyms []string `json:"synonyms"`
}

type gatewayTiming struct {
	Total     int64 `json:"total"`
	Expansion int64 `json:"expansion"`
	Search    int64 `json:"search"`
}

type gatewaySearchMetadata struct {
	Backend           string                   `json:"backend"`
	RequestedDomain   string                   `json:"requestedDomain,omitempty"`
	SelectedProviders []string                 `json:"selectedProviders,omitempty"`
	ResultCount       int                      `json:"resultCount"`
	WarningCount      int                      `json:"warningCount"`
	FallbackTriggered bool                     `json:"fallbackTriggered"`
	FallbackReason    string                   `json:"fallbackReason,omitempty"`
	Warnings          []search.ProviderWarning `json:"warnings,omitempty"`
}

type gatewayParallelResponse struct {
	Papers        []search.Paper           `json:"papers"`
	Results       []search.Paper           `json:"results"`
	EnhancedQuery gatewayEnhancedQuery     `json:"enhancedQuery"`
	Providers     map[string]int           `json:"providers"`
	ProvidersUsed []string                 `json:"providersUsed"`
	Timing        gatewayTiming            `json:"timing"`
	LatencyMs     int64                    `json:"latencyMs"`
	CacheHit      bool                     `json:"cacheHit"`
	Cached        bool                     `json:"cached"`
	Metadata      gatewaySearchMetadata    `json:"metadata"`
	Warnings      []search.ProviderWarning `json:"warnings,omitempty"`
}

type gatewayHybridResponse struct {
	Papers            []search.Paper           `json:"papers"`
	EnhancedQuery     gatewayEnhancedQuery     `json:"enhancedQuery"`
	Providers         map[string]int           `json:"providers,omitempty"`
	ProvidersUsed     []string                 `json:"providersUsed,omitempty"`
	Timing            gatewayTiming            `json:"timing"`
	TotalFound        int                      `json:"totalFound"`
	CacheHit          bool                     `json:"cacheHit"`
	LatencyMs         int64                    `json:"latencyMs"`
	BackendUsed       string                   `json:"backendUsed"`
	FallbackTriggered bool                     `json:"fallbackTriggered"`
	FallbackReason    string                   `json:"fallbackReason,omitempty"`
	Metadata          gatewaySearchMetadata    `json:"metadata"`
	Warnings          []search.ProviderWarning `json:"warnings,omitempty"`
}

type OpenSearchRequest struct {
	Query           string
	Limit           int
	LatencyBudgetMs int
	RetrievalMode   string
	FusionMode      string
}

type OpenSearchResponse struct {
	Papers            []map[string]any
	TotalFound        int
	LatencyMs         int
	BackendUsed       string
	FallbackTriggered bool
	FallbackReason    string
}

type OpenSearchExecutor func(ctx context.Context, req OpenSearchRequest) (OpenSearchResponse, error)

func NewSearchHandler(registry *search.ProviderRegistry, fastRegistry *search.ProviderRegistry, rdb redis.UniversalClient) *SearchHandler {
	return &SearchHandler{
		registry:     registry,
		fastRegistry: fastRegistry,
		redis:        rdb,
	}
}

func (h *SearchHandler) WithOpenSearchExecutor(executor OpenSearchExecutor) *SearchHandler {
	h.openSearch = executor
	return h
}

func (h *SearchHandler) HandleLegacySearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeSearchError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeSearchError(w, http.StatusBadRequest, "MISSING_QUERY", "Query parameter 'q' is required")
		return
	}

	limit := parseIntValue(r.URL.Query().Get("limit"), 10)
	opts := search.SearchOpts{
		Limit:       limit,
		QualitySort: true,
	}
	result := search.ParallelSearch(r.Context(), h.resolveFastRegistry(), query, opts)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result.Papers)
}

func (h *SearchHandler) HandleParallelSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeSearchError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}

	query, opts, err := resolveSearchOptions(r)
	if err != nil {
		writeSearchError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}

	result := search.ParallelSearch(r.Context(), h.resolveRegistry(), query, opts)
	response := mapParallelResponse(query, opts.Domain, result)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (h *SearchHandler) HandleBatchSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSearchError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}

	var req batchSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeSearchError(w, http.StatusBadRequest, "INVALID_BODY", "Failed to parse request body: "+err.Error())
		return
	}

	if len(req.Queries) == 0 {
		writeSearchError(w, http.StatusBadRequest, "EMPTY_QUERIES", "At least one query is required")
		return
	}
	if len(req.Queries) > batchMaxQueries {
		writeSearchError(w, http.StatusBadRequest, "TOO_MANY_QUERIES", "Maximum 20 queries per batch request")
		return
	}

	limit := req.Limit
	if limit <= 0 {
		limit = batchDefaultLimit
	}

	type queryResult struct {
		Query  string
		Papers []search.Paper
	}

	resultsCh := make(chan queryResult, len(req.Queries))
	sem := semaphore.NewWeighted(batchMaxConcurrent)
	registry := h.resolveRegistry()

	var wg sync.WaitGroup
	for _, query := range req.Queries {
		query := strings.TrimSpace(query)
		if query == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sem.Acquire(r.Context(), 1); err != nil {
				resultsCh <- queryResult{Query: query, Papers: []search.Paper{}}
				return
			}
			defer sem.Release(1)

			result := search.ParallelSearch(r.Context(), registry, query, search.SearchOpts{
				Limit:       limit,
				QualitySort: true,
			})
			resultsCh <- queryResult{Query: query, Papers: result.Papers}
		}()
	}

	wg.Wait()
	close(resultsCh)

	resultsMap := make(map[string][]search.Paper, len(req.Queries))
	seenDOIs := make(map[string]bool)
	for result := range resultsCh {
		var unique []search.Paper
		for _, paper := range result.Papers {
			doi := strings.ToLower(strings.TrimSpace(paper.DOI))
			if doi != "" && seenDOIs[doi] {
				continue
			}
			if doi != "" {
				seenDOIs[doi] = true
			}
			unique = append(unique, paper)
		}
		if unique == nil {
			unique = []search.Paper{}
		}
		resultsMap[result.Query] = unique
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"results": resultsMap,
	})
}

func (h *SearchHandler) HandleHybridSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeSearchError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}

	req, err := resolveHybridSearchRequest(r)
	if err != nil {
		writeSearchError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}

	started := time.Now()
	backend := strings.ToLower(strings.TrimSpace(req.RetrievalBackend))
	if backend == "" {
		backend = "parallel"
	}

	var response gatewayHybridResponse
	switch backend {
	case "opensearch_hybrid", "opensearch-hybrid", "opensearch":
		if h.openSearch == nil {
			h.openSearch = func(ctx context.Context, req OpenSearchRequest) (OpenSearchResponse, error) {
				result, err := wisdev.OpenSearchHybridSearch(ctx, wisdev.OpenSearchHybridRequest{
					Query:           req.Query,
					Limit:           req.Limit,
					LatencyBudgetMs: req.LatencyBudgetMs,
					RetrievalMode:   req.RetrievalMode,
					FusionMode:      req.FusionMode,
				})
				if err != nil {
					return OpenSearchResponse{}, err
				}
				return OpenSearchResponse{
					Papers:            result.Papers,
					TotalFound:        result.TotalFound,
					LatencyMs:         result.LatencyMs,
					BackendUsed:       result.BackendUsed,
					FallbackTriggered: result.FallbackTriggered,
					FallbackReason:    result.FallbackReason,
				}, nil
			}
		}

		ctx := r.Context()
		if req.LatencyBudgetMs > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(req.LatencyBudgetMs)*time.Millisecond)
			defer cancel()
		}

		result, err := h.openSearch(ctx, OpenSearchRequest{
			Query:           req.Query,
			Limit:           req.Limit,
			LatencyBudgetMs: req.LatencyBudgetMs,
			RetrievalMode:   req.RetrievalMode,
			FusionMode:      req.FusionMode,
		})
		if err != nil {
			writeSearchError(w, http.StatusInternalServerError, "SEARCH_ERROR", err.Error())
			return
		}

		response = mapHybridOpenSearchResponse(req.Query, req.Domain, result, time.Since(started).Milliseconds())
	default:
		result := search.ParallelSearch(r.Context(), h.resolveRegistry(), req.Query, search.SearchOpts{
			Limit:       req.Limit,
			QualitySort: req.QualitySort == nil || *req.QualitySort,
			SkipCache:   req.SkipCache != nil && *req.SkipCache,
			Domain:      strings.TrimSpace(req.Domain),
		})
		response = mapHybridParallelResponse(req.Query, req.Domain, result, time.Since(started).Milliseconds())
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

type toolSearchRequest struct {
	Tool   string         `json:"tool"`
	Params map[string]any `json:"params"`
}

func (h *SearchHandler) HandleToolSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSearchError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}

	var req toolSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeSearchError(w, http.StatusBadRequest, "INVALID_BODY", "Failed to parse request body: "+err.Error())
		return
	}

	if req.Tool == "" {
		writeSearchError(w, http.StatusBadRequest, "MISSING_TOOL", "tool field is required")
		return
	}

	result, err := search.HandleToolSearch(r.Context(), h.resolveRegistry(), req.Tool, req.Params)
	if err != nil {
		writeSearchError(w, http.StatusInternalServerError, "TOOL_SEARCH_FAILED", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (h *SearchHandler) HandleRelatedArticles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "Method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req struct {
		Query         string   `json:"query"`
		SourceTitle   string   `json:"source_title"`
		SourceLink    string   `json:"source_link"`
		ExistingLinks []string `json:"existing_links"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "Failed to parse request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	query := strings.TrimSpace(req.Query)
	sourceTitle := strings.TrimSpace(req.SourceTitle)
	if query == "" {
		query = sourceTitle
	}
	if query == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", nil)
		return
	}

	searchQuery := query
	if sourceTitle != "" && !strings.Contains(strings.ToLower(query), strings.ToLower(sourceTitle)) {
		searchQuery = fmt.Sprintf("%s %s", query, sourceTitle)
	}
	hits, err := wisdev.FastParallelSearch(r.Context(), h.redis, searchQuery, 8)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "Failed to load related articles", map[string]any{
			"error": err.Error(),
		})
		return
	}

	excluded := map[string]struct{}{}
	if link := strings.TrimSpace(req.SourceLink); link != "" {
		excluded[link] = struct{}{}
	}
	for _, link := range req.ExistingLinks {
		link = strings.TrimSpace(link)
		if link != "" {
			excluded[link] = struct{}{}
		}
	}

	articles := make([]map[string]any, 0, 5)
	for _, hit := range hits {
		link := strings.TrimSpace(hit.Link)
		if link == "" || link == "#" {
			continue
		}
		if _, exists := excluded[link]; exists {
			continue
		}
		articles = append(articles, map[string]any{
			"link":    link,
			"title":   strings.TrimSpace(hit.Title),
			"authors": strings.Join(hit.Authors, ", "),
			"summary": strings.TrimSpace(hit.Summary),
		})
		if len(articles) >= 5 {
			break
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"articles": articles,
	})
}

func (h *SearchHandler) HandleQueryField(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "Method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "Failed to parse request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", nil)
		return
	}
	fieldID, confidence := ClassifyQueryField(query)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"fieldId":    fieldID,
		"confidence": confidence,
	})
}

func (h *SearchHandler) HandleQueryIntroduction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "Method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	var req queryIntroductionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "Failed to parse request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", nil)
		return
	}
	body := BuildQueryIntroductionMarkdown(query, req.Papers, req.ProvidersUsed)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"text": body,
	})
}

func (h *SearchHandler) HandleBatchSummaries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "Method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	var req batchSummariesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "Failed to parse request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if len(req.Papers) == 0 {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "papers is required", nil)
		return
	}
	results := make([]map[string]any, 0, len(req.Papers))
	for _, paper := range req.Papers {
		title := strings.TrimSpace(paper.Title)
		if title == "" {
			title = "Untitled paper"
		}
		paperID := strings.TrimSpace(paper.PaperID)
		if paperID == "" {
			paperID = title
		}
		results = append(results, map[string]any{
			"paper_id":     paperID,
			"title":        title,
			"summary":      BuildLocalPaperSummary(title, paper.Abstract, req.MaxFindings),
			"key_findings": BuildKeyFindings(paper.Abstract, req.MaxFindings),
			"success":      true,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"results": results,
	})
}

func (h *SearchHandler) resolveRegistry() *search.ProviderRegistry {
	if h.registry != nil {
		return h.registry
	}
	return search.NewProviderRegistry()
}

func (h *SearchHandler) resolveFastRegistry() *search.ProviderRegistry {
	if h.fastRegistry != nil {
		return h.fastRegistry
	}
	return h.resolveRegistry()
}

func resolveSearchOptions(r *http.Request) (string, search.SearchOpts, error) {
	values := r.URL.Query()

	switch r.Method {
	case http.MethodGet:
		query := strings.TrimSpace(values.Get("q"))
		if query == "" {
			return "", search.SearchOpts{}, fmt.Errorf("query parameter 'q' is required")
		}
		return query, search.SearchOpts{
			Limit:       parseIntValue(values.Get("limit"), 15),
			QualitySort: parseBoolValue(values.Get("qualitySort"), true),
			SkipCache:   parseBoolValue(values.Get("skipCache"), false),
			Domain:      strings.TrimSpace(values.Get("domain")),
		}, nil
	case http.MethodPost:
		req, err := readSearchRequestBody(r)
		if err != nil {
			return "", search.SearchOpts{}, fmt.Errorf("failed to parse request body: %w", err)
		}

		query := strings.TrimSpace(req.Query)
		if query == "" {
			query = strings.TrimSpace(values.Get("query"))
		}
		if query == "" {
			query = strings.TrimSpace(values.Get("q"))
		}
		if query == "" {
			return "", search.SearchOpts{}, fmt.Errorf("query field is required")
		}

		limit := req.Limit
		if limit <= 0 {
			limit = parseIntValue(values.Get("limit"), 15)
		}

		qualitySort := true
		if req.QualitySort != nil {
			qualitySort = *req.QualitySort
		} else if value := strings.TrimSpace(values.Get("qualitySort")); value != "" {
			qualitySort = parseBoolValue(value, true)
		}

		skipCache := false
		if req.SkipCache != nil {
			skipCache = *req.SkipCache
		} else if value := strings.TrimSpace(values.Get("skipCache")); value != "" {
			skipCache = parseBoolValue(value, false)
		}

		domain := strings.TrimSpace(req.Domain)
		if domain == "" {
			domain = strings.TrimSpace(values.Get("domain"))
		}

		return query, search.SearchOpts{
			Limit:       limit,
			QualitySort: qualitySort,
			SkipCache:   skipCache,
			Domain:      domain,
		}, nil
	default:
		return "", search.SearchOpts{}, fmt.Errorf("method not allowed")
	}
}

func readSearchRequestBody(r *http.Request) (searchRequest, error) {
	var req searchRequest
	if r.Body == nil {
		return req, nil
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if err == io.EOF {
			return req, nil
		}
		return req, err
	}
	return req, nil
}

func mapParallelResponse(query string, requestedDomain string, result search.SearchResult) gatewayParallelResponse {
	providersUsed := make([]string, 0, len(result.Providers))
	for provider := range result.Providers {
		providersUsed = append(providersUsed, provider)
	}
	sort.Strings(providersUsed)

	return gatewayParallelResponse{
		Papers:  result.Papers,
		Results: result.Papers,
		EnhancedQuery: gatewayEnhancedQuery{
			Original: query,
			Expanded: query,
			Intent:   "general",
			Keywords: []string{},
			Synonyms: []string{},
		},
		Providers:     result.Providers,
		ProvidersUsed: providersUsed,
		Timing: gatewayTiming{
			Total:     result.LatencyMs,
			Expansion: 0,
			Search:    result.LatencyMs,
		},
		LatencyMs: result.LatencyMs,
		CacheHit:  result.Cached,
		Cached:    result.Cached,
		Metadata: gatewaySearchMetadata{
			Backend:           "go-parallel",
			RequestedDomain:   strings.TrimSpace(requestedDomain),
			SelectedProviders: providersUsed,
			ResultCount:       len(result.Papers),
			WarningCount:      len(result.Warnings),
			FallbackTriggered: false,
			Warnings:          result.Warnings,
		},
		Warnings: result.Warnings,
	}
}

func mapHybridParallelResponse(query string, requestedDomain string, result search.SearchResult, latencyMs int64) gatewayHybridResponse {
	providersUsed := make([]string, 0, len(result.Providers))
	for provider := range result.Providers {
		providersUsed = append(providersUsed, provider)
	}
	sort.Strings(providersUsed)

	return gatewayHybridResponse{
		Papers:        result.Papers,
		EnhancedQuery: gatewayEnhancedQuery{Original: query, Expanded: query, Intent: "general", Keywords: []string{}, Synonyms: []string{}},
		Providers:     result.Providers,
		ProvidersUsed: providersUsed,
		Timing: gatewayTiming{
			Total:     result.LatencyMs,
			Expansion: 0,
			Search:    result.LatencyMs,
		},
		TotalFound:        len(result.Papers),
		CacheHit:          result.Cached,
		LatencyMs:         latencyMs,
		BackendUsed:       "go-parallel",
		FallbackTriggered: false,
		Metadata: gatewaySearchMetadata{
			Backend:           "go-parallel",
			RequestedDomain:   strings.TrimSpace(requestedDomain),
			SelectedProviders: providersUsed,
			ResultCount:       len(result.Papers),
			WarningCount:      len(result.Warnings),
			FallbackTriggered: false,
			Warnings:          result.Warnings,
		},
		Warnings: result.Warnings,
	}
}

func mapHybridOpenSearchResponse(query string, requestedDomain string, result OpenSearchResponse, latencyMs int64) gatewayHybridResponse {
	papers := make([]search.Paper, 0, len(result.Papers))
	for _, raw := range result.Papers {
		papers = append(papers, mapOpenSearchPaper(raw))
	}

	intent := strings.TrimSpace(result.BackendUsed)
	if intent == "" {
		intent = "opensearch_hybrid"
	}

	return gatewayHybridResponse{
		Papers:        papers,
		EnhancedQuery: gatewayEnhancedQuery{Original: query, Expanded: query, Intent: intent, Keywords: []string{}, Synonyms: []string{}},
		Timing: gatewayTiming{
			Total:     int64(result.LatencyMs),
			Expansion: 0,
			Search:    int64(result.LatencyMs),
		},
		TotalFound:        result.TotalFound,
		CacheHit:          false,
		LatencyMs:         latencyMs,
		BackendUsed:       result.BackendUsed,
		FallbackTriggered: result.FallbackTriggered,
		FallbackReason:    result.FallbackReason,
		Metadata: gatewaySearchMetadata{
			Backend:           result.BackendUsed,
			RequestedDomain:   strings.TrimSpace(requestedDomain),
			SelectedProviders: []string{"opensearch_hybrid"},
			ResultCount:       len(papers),
			WarningCount:      0,
			FallbackTriggered: result.FallbackTriggered,
			FallbackReason:    result.FallbackReason,
		},
	}
}

func mapOpenSearchPaper(raw map[string]any) search.Paper {
	id, _ := raw["id"].(string)
	title, _ := raw["title"].(string)
	abstract, _ := raw["abstract"].(string)
	doi, _ := raw["doi"].(string)
	link, _ := raw["url"].(string)
	if link == "" {
		link, _ = raw["landingUrl"].(string)
	}

	var citationCount int
	switch value := raw["citationCount"].(type) {
	case int:
		citationCount = value
	case float64:
		citationCount = int(value)
	}

	var score float64
	switch value := raw["score"].(type) {
	case float64:
		score = value
	case int:
		score = float64(value)
	}
	if score == 0 {
		switch value := raw["relevanceScore"].(type) {
		case float64:
			score = value
		case int:
			score = float64(value)
		}
	}

	return search.Paper{
		ID:            id,
		Title:         title,
		Abstract:      abstract,
		Link:          link,
		DOI:           doi,
		Source:        "opensearch_hybrid",
		Score:         score,
		CitationCount: citationCount,
	}
}

func resolveHybridSearchRequest(r *http.Request) (hybridSearchRequest, error) {
	values := r.URL.Query()
	switch r.Method {
	case http.MethodGet:
		query := strings.TrimSpace(values.Get("q"))
		if query == "" {
			return hybridSearchRequest{}, fmt.Errorf("query parameter 'q' is required")
		}

		return hybridSearchRequest{
			Query:            query,
			Limit:            parseIntValue(values.Get("limit"), 20),
			ExpandQuery:      boolPtr(parseBoolValue(values.Get("expand"), true)),
			QualitySort:      boolPtr(parseBoolValue(values.Get("qualitySort"), true)),
			SkipCache:        boolPtr(parseBoolValue(values.Get("skipCache"), false)),
			Domain:           strings.TrimSpace(values.Get("domain")),
			PageIndex:        boolPtr(parseBoolValue(values.Get("pageIndex"), false)),
			L3:               boolPtr(parseBoolValue(values.Get("l3"), false)),
			RetrievalBackend: strings.TrimSpace(values.Get("retrievalBackend")),
			RetrievalMode:    strings.TrimSpace(values.Get("retrievalMode")),
			FusionMode:       strings.TrimSpace(values.Get("fusionMode")),
			LatencyBudgetMs:  parseIntValue(values.Get("latencyBudgetMs"), 0),
		}, nil
	case http.MethodPost:
		req, err := readHybridSearchRequestBody(r)
		if err != nil {
			return hybridSearchRequest{}, fmt.Errorf("failed to parse request body: %w", err)
		}

		if strings.TrimSpace(req.Query) == "" {
			req.Query = strings.TrimSpace(values.Get("query"))
		}
		if strings.TrimSpace(req.Query) == "" {
			req.Query = strings.TrimSpace(values.Get("q"))
		}
		if strings.TrimSpace(req.Query) == "" {
			return hybridSearchRequest{}, fmt.Errorf("query field is required")
		}

		if req.Limit <= 0 {
			req.Limit = parseIntValue(values.Get("limit"), 20)
		}
		if req.ExpandQuery == nil {
			req.ExpandQuery = boolPtr(parseBoolValue(values.Get("expandQuery"), parseBoolValue(values.Get("expand"), true)))
		}
		if req.QualitySort == nil {
			req.QualitySort = boolPtr(parseBoolValue(values.Get("qualitySort"), true))
		}
		if req.SkipCache == nil {
			req.SkipCache = boolPtr(parseBoolValue(values.Get("skipCache"), false))
		}
		if strings.TrimSpace(req.Domain) == "" {
			req.Domain = strings.TrimSpace(values.Get("domain"))
		}
		if req.PageIndex == nil && req.L3 == nil {
			req.PageIndex = boolPtr(parseBoolValue(values.Get("pageIndex"), false))
			req.L3 = boolPtr(parseBoolValue(values.Get("l3"), false))
		}
		if strings.TrimSpace(req.RetrievalBackend) == "" {
			req.RetrievalBackend = strings.TrimSpace(values.Get("retrievalBackend"))
		}
		if strings.TrimSpace(req.RetrievalMode) == "" {
			req.RetrievalMode = strings.TrimSpace(values.Get("retrievalMode"))
		}
		if strings.TrimSpace(req.FusionMode) == "" {
			req.FusionMode = strings.TrimSpace(values.Get("fusionMode"))
		}
		if req.LatencyBudgetMs <= 0 {
			req.LatencyBudgetMs = parseIntValue(values.Get("latencyBudgetMs"), 0)
		}
		return req, nil
	default:
		return hybridSearchRequest{}, fmt.Errorf("method not allowed")
	}
}

func readHybridSearchRequestBody(r *http.Request) (hybridSearchRequest, error) {
	var req hybridSearchRequest
	if r.Body == nil {
		return req, nil
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if err == io.EOF {
			return req, nil
		}
		return req, err
	}
	return req, nil
}

func boolPtr(v bool) *bool {
	return &v
}

func writeSearchError(w http.ResponseWriter, status int, code string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok": false,
		"error": map[string]any{
			"code":    code,
			"message": message,
			"status":  status,
		},
	})
}
