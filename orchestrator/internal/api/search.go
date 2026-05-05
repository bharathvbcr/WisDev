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

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
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
	Query         string   `json:"query"`
	Limit         int      `json:"limit"`
	Sources       []string `json:"sources,omitempty"`
	YearFrom      int      `json:"yearFrom,omitempty"`
	YearTo        int      `json:"yearTo,omitempty"`
	TraceID       string   `json:"traceId,omitempty"`
	LegacyTraceID string   `json:"trace_id,omitempty"`
	ExpandQuery   *bool    `json:"expandQuery,omitempty"`
	QualitySort   *bool    `json:"qualitySort,omitempty"`
	SkipCache     *bool    `json:"skipCache,omitempty"`
	Domain        string   `json:"domain,omitempty"`
	PageIndex     *bool    `json:"pageIndex,omitempty"`
	L3            *bool    `json:"l3,omitempty"`
}

type batchSearchRequest struct {
	Queries []string `json:"queries"`
	Limit   int      `json:"limit"`
}

type hybridSearchRequest struct {
	Query            string   `json:"query"`
	Limit            int      `json:"limit"`
	Sources          []string `json:"sources,omitempty"`
	YearFrom         int      `json:"yearFrom,omitempty"`
	YearTo           int      `json:"yearTo,omitempty"`
	TraceID          string   `json:"traceId,omitempty"`
	LegacyTraceID    string   `json:"trace_id,omitempty"`
	ExpandQuery      *bool    `json:"expandQuery,omitempty"`
	QualitySort      *bool    `json:"qualitySort,omitempty"`
	SkipCache        *bool    `json:"skipCache,omitempty"`
	Domain           string   `json:"domain,omitempty"`
	PageIndex        *bool    `json:"pageIndex,omitempty"`
	L3               *bool    `json:"l3,omitempty"`
	RetrievalBackend string   `json:"retrievalBackend,omitempty"`
	RetrievalMode    string   `json:"retrievalMode,omitempty"`
	FusionMode       string   `json:"fusionMode,omitempty"`
	LatencyBudgetMs  int      `json:"latencyBudgetMs,omitempty"`
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
	QueryUsed     string                   `json:"queryUsed,omitempty"`
	TraceID       string                   `json:"traceId,omitempty"`
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
	QueryUsed         string                   `json:"queryUsed,omitempty"`
	TraceID           string                   `json:"traceId,omitempty"`
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

	result, err := runModularParallelSearch(r.Context(), h.redis, h.resolveRegistry(), query, wisdev.SearchOptions{
		Limit:           opts.Limit,
		ExpandQuery:     opts.ExpandQuery,
		QualitySort:     opts.QualitySort,
		SkipCache:       opts.SkipCache,
		Domain:          opts.Domain,
		Sources:         append([]string(nil), opts.Sources...),
		YearFrom:        opts.YearFrom,
		YearTo:          opts.YearTo,
		TraceID:         opts.TraceID,
		PageIndexRerank: opts.PageIndexRerank,
		Stage2Rerank:    opts.Stage2Rerank,
		Registry:        h.resolveRegistry(),
	})
	if err != nil {
		writeSearchError(w, http.StatusInternalServerError, "SEARCH_ERROR", err.Error())
		return
	}
	response := mapParallelResponse(query, opts.Domain, result)
	if response.TraceID == "" {
		response.TraceID = strings.TrimSpace(opts.TraceID)
	}

	w.Header().Set("Content-Type", "application/json")
	if response.TraceID != "" {
		w.Header().Set("X-Trace-Id", response.TraceID)
	}
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
	err = withConcurrencyGuard("gateway_search", wisdev.EnvInt("WISDEV_GATEWAY_SEARCH_CONCURRENCY", 30), func() error {
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

			result, searchErr := h.openSearch(ctx, OpenSearchRequest{
				Query:           req.Query,
				Limit:           req.Limit,
				LatencyBudgetMs: req.LatencyBudgetMs,
				RetrievalMode:   req.RetrievalMode,
				FusionMode:      req.FusionMode,
			})
			if searchErr != nil {
				return searchErr
			}

			if result.FallbackTriggered {
				parallelResult, err := runModularParallelSearch(r.Context(), h.redis, h.resolveRegistry(), req.Query, wisdev.SearchOptions{
					Limit:           req.Limit,
					ExpandQuery:     req.ExpandQuery == nil || *req.ExpandQuery,
					QualitySort:     req.QualitySort == nil || *req.QualitySort,
					SkipCache:       req.SkipCache != nil && *req.SkipCache,
					Domain:          strings.TrimSpace(req.Domain),
					Sources:         append([]string(nil), req.Sources...),
					YearFrom:        req.YearFrom,
					YearTo:          req.YearTo,
					TraceID:         req.TraceID,
					PageIndexRerank: req.PageIndex != nil && *req.PageIndex,
					Stage2Rerank:    req.L3 != nil && *req.L3,
					Registry:        h.resolveRegistry(),
				})
				if err != nil {
					return err
				}
				response = mapHybridWisdevResponse(req.Query, req.Domain, parallelResult, time.Since(started).Milliseconds())
				response.BackendUsed = "opensearch_hybrid"
				response.FallbackTriggered = true
				response.FallbackReason = result.FallbackReason
				response.Metadata.Backend = "opensearch_hybrid"
				response.Metadata.FallbackTriggered = true
				response.Metadata.FallbackReason = result.FallbackReason
			} else {
				response = mapHybridOpenSearchResponse(req.Query, req.Domain, result, time.Since(started).Milliseconds())
			}
		default:
			result, err := runModularParallelSearch(r.Context(), h.redis, h.resolveRegistry(), req.Query, wisdev.SearchOptions{
				Limit:           req.Limit,
				ExpandQuery:     req.ExpandQuery == nil || *req.ExpandQuery,
				QualitySort:     req.QualitySort == nil || *req.QualitySort,
				SkipCache:       req.SkipCache != nil && *req.SkipCache,
				Domain:          strings.TrimSpace(req.Domain),
				Sources:         append([]string(nil), req.Sources...),
				YearFrom:        req.YearFrom,
				YearTo:          req.YearTo,
				TraceID:         req.TraceID,
				PageIndexRerank: req.PageIndex != nil && *req.PageIndex,
				Stage2Rerank:    req.L3 != nil && *req.L3,
				Registry:        h.resolveRegistry(),
			})
			if err != nil {
				return err
			}
			response = mapHybridWisdevResponse(req.Query, req.Domain, result, time.Since(started).Milliseconds())
		}
		return nil
	})

	if err != nil {
		if strings.Contains(err.Error(), "concurrency limit reached") {
			writeSearchError(w, http.StatusTooManyRequests, "CONCURRENCY_LIMIT", "Global search concurrency limit reached")
		} else {
			writeSearchError(w, http.StatusInternalServerError, "SEARCH_ERROR", err.Error())
		}
		return
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

	if strings.TrimSpace(req.Tool) == "" {
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
	meta := BuildQueryIntroductionMeta(query, req.Papers, req.ProvidersUsed)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"text":             body,
		"markdown":         body,
		"introductionMeta": meta,
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
		sources, err := parseRequestedProviders(values["sources"], values["source"])
		if err != nil {
			return "", search.SearchOpts{}, err
		}
		yearFrom, yearTo := normalizeYearRange(parseIntValue(values.Get("yearFrom"), 0), parseIntValue(values.Get("yearTo"), 0))
		return query, search.SearchOpts{
			Limit:           parseIntValue(values.Get("limit"), 15),
			QualitySort:     parseBoolValue(values.Get("qualitySort"), true),
			SkipCache:       parseBoolValue(values.Get("skipCache"), false),
			Domain:          strings.TrimSpace(values.Get("domain")),
			Sources:         sources,
			YearFrom:        yearFrom,
			YearTo:          yearTo,
			ExpandQuery:     parseBoolValue(values.Get("expandQuery"), parseBoolValue(values.Get("expand"), true)),
			PageIndexRerank: parseBoolValue(values.Get("pageIndex"), false),
			Stage2Rerank:    parseBoolValue(values.Get("l3"), false),
			TraceID:         resolveRequestTraceID(r),
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

		sources, err := parseRequestedProviders(req.Sources)
		if err != nil {
			return "", search.SearchOpts{}, err
		}
		if len(sources) == 0 && len(req.Sources) == 0 {
			sources, err = parseRequestedProviders(values["sources"], values["source"])
			if err != nil {
				return "", search.SearchOpts{}, err
			}
		}

		yearFrom, yearTo := normalizeYearRange(req.YearFrom, req.YearTo)
		if yearFrom == 0 && yearTo == 0 {
			yearFrom, yearTo = normalizeYearRange(parseIntValue(values.Get("yearFrom"), 0), parseIntValue(values.Get("yearTo"), 0))
		}

		expandQuery := true
		if req.ExpandQuery != nil {
			expandQuery = *req.ExpandQuery
		} else if value := strings.TrimSpace(values.Get("expandQuery")); value != "" {
			expandQuery = parseBoolValue(value, true)
		} else if value := strings.TrimSpace(values.Get("expand")); value != "" {
			expandQuery = parseBoolValue(value, true)
		}
		pageIndex := false
		if req.PageIndex != nil {
			pageIndex = *req.PageIndex
		} else if value := strings.TrimSpace(values.Get("pageIndex")); value != "" {
			pageIndex = parseBoolValue(value, false)
		}
		stage2 := false
		if req.L3 != nil {
			stage2 = *req.L3
		} else if value := strings.TrimSpace(values.Get("l3")); value != "" {
			stage2 = parseBoolValue(value, false)
		}

		return query, search.SearchOpts{
			Limit:           limit,
			QualitySort:     qualitySort,
			SkipCache:       skipCache,
			Domain:          domain,
			Sources:         sources,
			YearFrom:        yearFrom,
			YearTo:          yearTo,
			ExpandQuery:     expandQuery,
			PageIndexRerank: pageIndex,
			Stage2Rerank:    stage2,
			TraceID:         firstNonEmptyTrimmed(req.TraceID, req.LegacyTraceID, resolveRequestTraceID(r)),
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

func normalizeSearchProviderHint(raw string) (string, bool) {
	normalized := search.NormalizeProviderName(raw)
	if normalized == "" || !search.IsCanonicalProviderName(normalized) {
		return "", false
	}
	return normalized, true
}

func parseRequestedProviders(groups ...[]string) ([]string, error) {
	seen := map[string]struct{}{}
	for _, group := range groups {
		for _, raw := range group {
			for _, part := range strings.Split(raw, ",") {
				trimmed := strings.TrimSpace(part)
				if trimmed == "" {
					continue
				}
				normalized, ok := normalizeSearchProviderHint(trimmed)
				if !ok {
					return nil, fmt.Errorf("unsupported provider hint %q", trimmed)
				}
				seen[normalized] = struct{}{}
			}
		}
	}
	if len(seen) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(seen))
	for provider := range seen {
		out = append(out, provider)
	}
	sort.Strings(out)
	return out, nil
}

func normalizeYearRange(yearFrom int, yearTo int) (int, int) {
	if yearFrom > 0 && yearTo > 0 && yearFrom > yearTo {
		return yearTo, yearFrom
	}
	return yearFrom, yearTo
}

func mapParallelResponse(query string, requestedDomain string, result any) gatewayParallelResponse {
	switch typed := result.(type) {
	case search.SearchResult:
		return mapParallelSearchResponse(query, requestedDomain, typed, "")
	case *wisdev.MultiSourceResult:
		if typed == nil {
			return mapParallelSearchResponse(query, requestedDomain, search.SearchResult{}, "")
		}
		return mapWisdevParallelResponse(query, requestedDomain, typed)
	default:
		return mapParallelSearchResponse(query, requestedDomain, search.SearchResult{}, "")
	}
}

func mapParallelSearchResponse(query string, requestedDomain string, result search.SearchResult, traceID string) gatewayParallelResponse {
	providersUsed := make([]string, 0, len(result.Providers))
	for provider := range result.Providers {
		providersUsed = append(providersUsed, provider)
	}
	sort.Strings(providersUsed)

	return gatewayParallelResponse{
		Papers:    result.Papers,
		Results:   result.Papers,
		QueryUsed: strings.TrimSpace(query),
		TraceID:   strings.TrimSpace(traceID),
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

func mapWisdevParallelResponse(query string, requestedDomain string, result *wisdev.MultiSourceResult) gatewayParallelResponse {
	papers := make([]search.Paper, 0, len(result.Papers))
	for _, paper := range result.Papers {
		papers = append(papers, search.Paper{
			ID:       paper.ID,
			Title:    paper.Title,
			Abstract: firstNonEmptyTrimmed(paper.Abstract, paper.Summary),
			DOI:      paper.DOI,
			Link:     paper.Link,
			Authors:  paper.Authors,
			Year:     paper.Year,
			Venue:    paper.Publication,
			Source:   paper.Source,
			Score:    paper.Score,
		})
	}
	providersUsed := providersUsedFromCounts(mapWisdevProviders(result.Sources))
	warnings, fallbackTriggered, fallbackReason := extractWisdevSearchTrace(result.RetrievalTrace)
	queryUsed := firstNonEmptyTrimmed(result.QueryUsed, result.EnhancedQuery.Expanded, query)
	return gatewayParallelResponse{
		Papers:    papers,
		Results:   papers,
		QueryUsed: queryUsed,
		TraceID:   strings.TrimSpace(result.TraceID),
		EnhancedQuery: gatewayEnhancedQuery{
			Original: result.EnhancedQuery.Original,
			Expanded: firstNonEmptyTrimmed(result.EnhancedQuery.Expanded, query),
			Intent:   result.EnhancedQuery.Intent,
			Keywords: result.EnhancedQuery.Keywords,
			Synonyms: result.EnhancedQuery.Synonyms,
		},
		Providers:     mapWisdevProviders(result.Sources),
		ProvidersUsed: providersUsed,
		Timing: gatewayTiming{
			Total:     result.Timing.Total,
			Expansion: result.Timing.Expansion,
			Search:    result.Timing.Search,
		},
		LatencyMs: result.Timing.Total,
		CacheHit:  result.Cached,
		Cached:    result.Cached,
		Metadata: gatewaySearchMetadata{
			Backend:           "go-parallel",
			RequestedDomain:   strings.TrimSpace(requestedDomain),
			SelectedProviders: providersUsed,
			ResultCount:       len(papers),
			WarningCount:      len(warnings),
			FallbackTriggered: fallbackTriggered,
			FallbackReason:    fallbackReason,
			Warnings:          warnings,
		},
		Warnings: warnings,
	}
}

func mapWisdevProviders(stats wisdev.SourcesStats) map[string]int {
	providers := map[string]int{}
	add := func(name string, count int) {
		if count > 0 {
			providers[name] = count
		}
	}
	add("semantic_scholar", stats.SemanticScholar)
	add("openalex", stats.OpenAlex)
	add("pubmed", stats.PubMed)
	add("core", stats.CORE)
	add("arxiv", stats.ArXiv)
	add("biorxiv", stats.BioRxiv)
	add("europe_pmc", stats.EuropePMC)
	add("crossref", stats.CrossRef)
	add("dblp", stats.DBLP)
	add("ieee", stats.IEEE)
	add("nasa_ads", stats.NASAADS)
	return providers
}

func providersUsedFromCounts(counts map[string]int) []string {
	providers := make([]string, 0, len(counts))
	for provider, count := range counts {
		if count > 0 {
			providers = append(providers, provider)
		}
	}
	sort.Strings(providers)
	return providers
}

func extractWisdevSearchTrace(trace []map[string]any) ([]search.ProviderWarning, bool, string) {
	warnings := extractProviderWarnings(trace)
	fallbackTriggered := false
	fallbackReason := ""
	for _, entry := range trace {
		status := strings.TrimSpace(wisdev.AsOptionalString(entry["status"]))
		if strings.Contains(status, "fallback") {
			fallbackTriggered = true
			strategy := strings.TrimSpace(wisdev.AsOptionalString(entry["strategy"]))
			if strategy != "" {
				fallbackReason = strategy + ":" + status
			} else {
				fallbackReason = status
			}
		}
	}
	return warnings, fallbackTriggered, fallbackReason
}

func extractProviderWarnings(trace []map[string]any) []search.ProviderWarning {
	warnings := []search.ProviderWarning{}
	for _, entry := range trace {
		if !strings.EqualFold(strings.TrimSpace(wisdev.AsOptionalString(entry["status"])), "warning") {
			continue
		}
		provider := strings.TrimSpace(wisdev.AsOptionalString(entry["provider"]))
		message := strings.TrimSpace(wisdev.AsOptionalString(entry["message"]))
		if provider == "" && message == "" {
			continue
		}
		warnings = append(warnings, search.ProviderWarning{Provider: provider, Message: message})
	}
	return warnings
}

type parallelAuthorCoverage struct {
	PapersWithAuthors      int
	PapersWithoutAuthors   int
	AuthorsMissingAll      bool
	MissingAuthorProviders []string
	MissingAuthorSamples   []string
}

func (c parallelAuthorCoverage) resultLabel() string {
	switch {
	case c.PapersWithoutAuthors == 0:
		return "complete"
	case c.AuthorsMissingAll:
		return "missing_all"
	default:
		return "partial"
	}
}

func summarizeParallelAuthorCoverage(papers []search.Paper) parallelAuthorCoverage {
	providerSet := map[string]struct{}{}
	coverage := parallelAuthorCoverage{}
	for _, paper := range papers {
		hasAuthor := false
		for _, author := range paper.Authors {
			if strings.TrimSpace(author) != "" {
				hasAuthor = true
				break
			}
		}
		if hasAuthor {
			coverage.PapersWithAuthors++
			continue
		}
		coverage.PapersWithoutAuthors++
		if provider := strings.TrimSpace(paper.Source); provider != "" {
			providerSet[provider] = struct{}{}
		}
		if len(coverage.MissingAuthorSamples) < 5 {
			label := firstNonEmptyTrimmed(paper.Source, "unknown") + ": " + firstNonEmptyTrimmed(paper.Title, paper.ID, "untitled")
			coverage.MissingAuthorSamples = append(coverage.MissingAuthorSamples, label)
		}
	}
	coverage.AuthorsMissingAll = coverage.PapersWithAuthors == 0 && coverage.PapersWithoutAuthors > 0
	for provider := range providerSet {
		coverage.MissingAuthorProviders = append(coverage.MissingAuthorProviders, provider)
	}
	sort.Strings(coverage.MissingAuthorProviders)
	return coverage
}

func mapHybridParallelResponse(query string, requestedDomain string, result search.SearchResult, latencyMs int64) gatewayHybridResponse {
	providersUsed := make([]string, 0, len(result.Providers))
	for provider := range result.Providers {
		providersUsed = append(providersUsed, provider)
	}
	sort.Strings(providersUsed)

	return gatewayHybridResponse{
		Papers:        result.Papers,
		QueryUsed:     strings.TrimSpace(query),
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

func mapHybridWisdevResponse(query string, requestedDomain string, result *wisdev.MultiSourceResult, latencyMs int64) gatewayHybridResponse {
	parallel := mapWisdevParallelResponse(query, requestedDomain, result)
	return gatewayHybridResponse{
		Papers:            parallel.Papers,
		QueryUsed:         parallel.QueryUsed,
		TraceID:           parallel.TraceID,
		EnhancedQuery:     parallel.EnhancedQuery,
		Providers:         parallel.Providers,
		ProvidersUsed:     parallel.ProvidersUsed,
		Timing:            parallel.Timing,
		TotalFound:        len(parallel.Papers),
		CacheHit:          parallel.CacheHit,
		LatencyMs:         latencyMs,
		BackendUsed:       "go-parallel",
		FallbackTriggered: parallel.Metadata.FallbackTriggered,
		FallbackReason:    parallel.Metadata.FallbackReason,
		Metadata:          parallel.Metadata,
		Warnings:          parallel.Warnings,
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
		QueryUsed:     strings.TrimSpace(query),
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
	venue := firstNonEmptyTrimmed(
		wisdev.AsOptionalString(raw["venue"]),
		wisdev.AsOptionalString(raw["publication"]),
		wisdev.AsOptionalString(raw["journal"]),
	)

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
		Venue:         venue,
		Authors:       parseOpenSearchAuthors(raw["authors"], raw["author"], raw["authorString"], raw["authorsString"]),
		Year:          parseOpenSearchYear(raw["year"]),
		Score:         score,
		CitationCount: citationCount,
	}
}

func parseOpenSearchAuthors(candidates ...any) []string {
	for _, candidate := range candidates {
		values := parseOpenSearchAuthorValue(candidate)
		if len(values) > 0 {
			return values
		}
	}
	return nil
}

func parseOpenSearchAuthorValue(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case []string:
		out := make([]string, 0, len(typed))
		for _, author := range typed {
			if trimmed := strings.TrimSpace(author); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			switch v := item.(type) {
			case string:
				if trimmed := strings.TrimSpace(v); trimmed != "" {
					out = append(out, trimmed)
				}
			case map[string]any:
				if trimmed := firstNonEmptyTrimmed(wisdev.AsOptionalString(v["name"]), wisdev.AsOptionalString(v["display_name"])); trimmed != "" {
					out = append(out, trimmed)
				}
			}
		}
		return out
	case string:
		splitter := func(r rune) bool {
			return r == ';' || r == ','
		}
		parts := strings.FieldsFunc(typed, splitter)
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	default:
		return nil
	}
}

func parseOpenSearchYear(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		return parseIntValue(typed, 0)
	default:
		return 0
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
		sources, err := parseRequestedProviders(values["sources"], values["source"])
		if err != nil {
			return hybridSearchRequest{}, err
		}
		yearFrom, yearTo := normalizeYearRange(parseIntValue(values.Get("yearFrom"), 0), parseIntValue(values.Get("yearTo"), 0))

		return hybridSearchRequest{
			Query:            query,
			Limit:            parseIntValue(values.Get("limit"), 20),
			Sources:          sources,
			YearFrom:         yearFrom,
			YearTo:           yearTo,
			TraceID:          resolveRequestTraceID(r),
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
		if len(req.Sources) > 0 {
			parsed, err := parseRequestedProviders(req.Sources)
			if err != nil {
				return hybridSearchRequest{}, err
			}
			req.Sources = parsed
		} else {
			parsed, err := parseRequestedProviders(values["sources"], values["source"])
			if err != nil {
				return hybridSearchRequest{}, err
			}
			req.Sources = parsed
		}
		if req.YearFrom == 0 {
			req.YearFrom = parseIntValue(values.Get("yearFrom"), 0)
		}
		if req.YearTo == 0 {
			req.YearTo = parseIntValue(values.Get("yearTo"), 0)
		}
		req.YearFrom, req.YearTo = normalizeYearRange(req.YearFrom, req.YearTo)
		req.TraceID = firstNonEmptyTrimmed(req.TraceID, req.LegacyTraceID, resolveRequestTraceID(r))
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
