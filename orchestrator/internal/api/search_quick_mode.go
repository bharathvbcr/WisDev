package api

// Quick Mode multi-tab search: Go owns query-variant construction, parallel fan-out,
// first-tab-wins identity dedupe, and preference ranking. The browser keeps progress
// labels and category presentation only (no local re-rank / variant authority).
//
// Route: POST /search/quick-mode

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

const (
	quickModeDefaultProviderLimit = 20
	quickModeDefaultMaxTabs       = 3
	quickModeMaxProviderLimit     = 50
	quickModeMaxTabConcurrency    = 8
)

type quickModeTabSearchFunc func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error)

// quickModeTabSearch is injectable for tests. Production uses ParallelSearch.
var quickModeTabSearch quickModeTabSearchFunc

type quickModeSearchRequest struct {
	Query             string   `json:"query"`
	ProviderLimit     int      `json:"providerLimit,omitempty"`
	MaxTabConcurrency int      `json:"maxTabConcurrency,omitempty"`
	PreferredSources  []string `json:"preferredSources,omitempty"`
	QualityMode       string   `json:"qualityMode,omitempty"`
	ResearchMode      string   `json:"researchMode,omitempty"`
	TraceID           string   `json:"traceId,omitempty"`
}

type quickModeTabResponse struct {
	ID      string           `json:"id"`
	Name    string           `json:"name"`
	Query   string           `json:"query"`
	Results []map[string]any `json:"results"`
}

type quickModeSearchResponse struct {
	Results             []map[string]any       `json:"results"`
	Tabs                []quickModeTabResponse `json:"tabs"`
	Strategy            map[string]any         `json:"strategy"`
	Stats               map[string]int         `json:"stats"`
	CategorizedSources  []map[string]any       `json:"categorizedSources"`
	Diagnostics         map[string]any         `json:"diagnostics,omitempty"`
	TraceID             string                 `json:"traceId,omitempty"`
}

func (h *SearchHandler) HandleQuickModeSearch(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	traceID := resolveRequestTraceID(r)
	logSearchRouteLifecycle(r, "search_quick_mode", "request_received", "", traceID, "result", "accepted")

	if r.Method != http.MethodPost {
		logSearchRouteLifecycle(r, "search_quick_mode", "method_rejected", "", traceID,
			"result", "failure", "error_code", "METHOD_NOT_ALLOWED")
		writeSearchError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}

	var req quickModeSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logSearchRouteLifecycle(r, "search_quick_mode", "body_decode_failed", "", traceID,
			"result", "failure", "error_code", "INVALID_BODY", "error", err.Error())
		writeSearchError(w, http.StatusBadRequest, "INVALID_BODY", "Failed to parse request body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.TraceID) != "" {
		traceID = strings.TrimSpace(req.TraceID)
	}

	query := strings.TrimSpace(req.Query)
	if query == "" {
		logSearchRouteLifecycle(r, "search_quick_mode", "validation_failed", "", traceID,
			"result", "failure", "error_code", "EMPTY_QUERY")
		writeSearchError(w, http.StatusBadRequest, "EMPTY_QUERY", "query is required")
		return
	}

	providerLimit := req.ProviderLimit
	if providerLimit <= 0 {
		providerLimit = quickModeDefaultProviderLimit
	}
	if providerLimit > quickModeMaxProviderLimit {
		providerLimit = quickModeMaxProviderLimit
	}

	maxTabs := req.MaxTabConcurrency
	if maxTabs <= 0 {
		maxTabs = quickModeDefaultMaxTabs
	}
	if maxTabs > quickModeMaxTabConcurrency {
		maxTabs = quickModeMaxTabConcurrency
	}

	tabs := search.BuildQuickModeQueryVariants(query, maxTabs)
	if len(tabs) == 0 {
		writeSearchJSON(w, http.StatusOK, quickModeSearchResponse{
			Results:            []map[string]any{},
			Tabs:               []quickModeTabResponse{},
			Strategy:           map[string]any{"tabs": []any{}},
			Stats:              map[string]int{"totalBefore": 0, "duplicatesRemoved": 0, "totalAfter": 0},
			CategorizedSources: []map[string]any{},
			TraceID:            traceID,
		})
		return
	}

	logSearchRouteLifecycle(r, "search_quick_mode", "variants_built", query, traceID,
		"tab_count", len(tabs), "provider_limit", providerLimit)

	fetchStart := time.Now()
	resultsByQuery := runQuickModeTabSearches(r.Context(), h, tabs, providerLimit, req.PreferredSources, traceID)
	fetchDurationMs := time.Since(fetchStart).Milliseconds()

	dedupStart := time.Now()
	merged := search.MergeAndRankQuickModeResults(query, tabs, search.NormalizeQuickModeBatchKeys(resultsByQuery))
	dedupDurationMs := time.Since(dedupStart).Milliseconds()

	tabResponses := make([]quickModeTabResponse, 0, len(tabs))
	categorized := make([]map[string]any, 0, len(tabs))
	for _, tab := range tabs {
		papers := merged.ByTabID[tab.ID]
		sources := papersToQuickModeSources(papers)
		tabResponses = append(tabResponses, quickModeTabResponse{
			ID:      tab.ID,
			Name:    tab.Name,
			Query:   tab.Query,
			Results: sources,
		})
		if len(sources) > 0 {
			categorized = append(categorized, map[string]any{
				"category": tab.Name,
				"sources":  sources,
			})
		}
	}

	response := quickModeSearchResponse{
		Results:            papersToQuickModeSources(merged.Merged),
		Tabs:               tabResponses,
		Strategy:           map[string]any{"tabs": tabResponses},
		Stats: map[string]int{
			"totalBefore":       merged.TotalBefore,
			"duplicatesRemoved": merged.DuplicatesRemoved,
			"totalAfter":        len(merged.Merged),
		},
		CategorizedSources: categorized,
		Diagnostics: map[string]any{
			"source": "quick",
			"stages": []map[string]any{
				{
					"id":         "batch_gateway_search",
					"label":      formatQuickModeBatchLabel(len(tabs)),
					"durationMs": fetchDurationMs,
				},
				{
					"id":         "deduplication",
					"label":      "Deduplication",
					"durationMs": dedupDurationMs,
				},
			},
			"cache": map[string]any{"used": false, "level": "none"},
		},
		TraceID: traceID,
	}

	logSearchRouteLifecycle(r, "search_quick_mode", "response_ready", query, traceID,
		"result", "success",
		"total_before", merged.TotalBefore,
		"duplicates_removed", merged.DuplicatesRemoved,
		"total_after", len(merged.Merged),
		"latency_ms", time.Since(start).Milliseconds(),
	)
	w.Header().Set("X-Trace-Id", traceID)
	writeSearchJSON(w, http.StatusOK, response)
}

func runQuickModeTabSearches(
	ctx context.Context,
	h *SearchHandler,
	tabs []search.QuickModeTab,
	providerLimit int,
	preferredSources []string,
	traceID string,
) map[string][]search.Paper {
	type queryResult struct {
		Query  string
		Papers []search.Paper
	}

	resultsCh := make(chan queryResult, len(tabs))
	sem := semaphore.NewWeighted(int64(len(tabs)))
	if len(tabs) > batchMaxConcurrent {
		sem = semaphore.NewWeighted(batchMaxConcurrent)
	}
	registry := h.resolveRegistry()
	searchFn := quickModeTabSearch
	if searchFn == nil {
		searchFn = func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
			result := search.ParallelSearch(ctx, registry, query, opts)
			return result.Papers, nil
		}
	}

	var wg sync.WaitGroup
	for _, tab := range tabs {
		tab := tab
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sem.Acquire(ctx, 1); err != nil {
				resultsCh <- queryResult{Query: tab.Query, Papers: []search.Paper{}}
				return
			}
			defer sem.Release(1)

			opts := search.SearchOpts{
				Limit:       providerLimit,
				QualitySort: true,
				TraceID:     traceID,
				Sources:     append([]string(nil), preferredSources...),
			}
			papers, err := searchFn(ctx, tab.Query, opts)
			if err != nil || papers == nil {
				papers = []search.Paper{}
			}
			resultsCh <- queryResult{Query: tab.Query, Papers: papers}
		}()
	}
	wg.Wait()
	close(resultsCh)

	out := make(map[string][]search.Paper, len(tabs))
	for result := range resultsCh {
		out[result.Query] = result.Papers
	}
	return out
}

func papersToQuickModeSources(papers []search.Paper) []map[string]any {
	out := make([]map[string]any, 0, len(papers))
	for _, p := range papers {
		authors := make([]any, 0, len(p.Authors))
		for _, name := range p.Authors {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			authors = append(authors, name)
		}
		id := firstNonEmptyTrimmed(p.ID, p.DOI, p.ArxivID, p.Link)
		out = append(out, map[string]any{
			"id":            id,
			"paperId":       id,
			"title":         p.Title,
			"abstract":      p.Abstract,
			"summary":       p.Abstract,
			"link":          p.Link,
			"doi":           p.DOI,
			"arxivId":       p.ArxivID,
			"source":        p.Source,
			"sourceApis":    p.SourceApis,
			"authors":       authors,
			"year":          p.Year,
			"venue":         p.Venue,
			"citationCount": p.CitationCount,
			"openAccessUrl": p.OpenAccessUrl,
			"pdfUrl":        p.PdfUrl,
			"score":         p.Score,
			"keywords":      p.Keywords,
		})
	}
	return out
}

func formatQuickModeBatchLabel(tabCount int) string {
	if tabCount == 1 {
		return "Batch search (1 strategy)"
	}
	return "Batch search (" + strconv.Itoa(tabCount) + " strategies)"
}

func writeSearchJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
