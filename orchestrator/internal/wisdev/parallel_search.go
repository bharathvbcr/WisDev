package wisdev

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	internalsearch "github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/telemetry"
	"log"
	"math/rand"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/semaphore"
)

var byteBufferPool = sync.Pool{
	New: func() any {
		return new(strings.Builder)
	},
}

var quotedPhrasePattern = regexp.MustCompile(`"([^"]+)"`)

// Helper to quickly release buffer
func getBuffer() *strings.Builder {
	return byteBufferPool.Get().(*strings.Builder)
}

func putBuffer(b *strings.Builder) {
	b.Reset()
	byteBufferPool.Put(b)
}

// ==========================================
// Connection-pooled HTTP client
// ==========================================

var httpTransport = &http.Transport{
	MaxIdleConns:        100,
	MaxIdleConnsPerHost: 10,
	IdleConnTimeout:     90 * time.Second,
	TLSHandshakeTimeout: 5 * time.Second,
}

var httpClient = &http.Client{
	Transport: httpTransport,
	Timeout:   10 * time.Second,
}

var (
	semanticScholarSearchURL = "https://api.semanticscholar.org/graph/v1/paper/search"
	openAlexWorksURL         = "https://api.openalex.org/works"
	pubMedESearchURL         = "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/esearch.fcgi"
	pubMedESummaryURL        = "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/esummary.fcgi"
)

const maxConcurrentPerProvider = 50

var (
	s2Sem       = semaphore.NewWeighted(maxConcurrentPerProvider)
	openAlexSem = semaphore.NewWeighted(maxConcurrentPerProvider)
	pubmedSem   = semaphore.NewWeighted(maxConcurrentPerProvider)
	coreSem     = semaphore.NewWeighted(maxConcurrentPerProvider)
	arxivSem    = semaphore.NewWeighted(maxConcurrentPerProvider)
)

// ==========================================
// TYPES
// ==========================================

type EnhancedQuery struct {
	Original string   `json:"original"`
	Expanded string   `json:"expanded"`
	Intent   string   `json:"intent"`
	Strategy string   `json:"strategy,omitempty"`
	Keywords []string `json:"keywords"`
	Synonyms []string `json:"synonyms"`
}

type SourcesStats struct {
	SemanticScholar int `json:"semanticScholar"`
	OpenAlex        int `json:"openAlex"`
	PubMed          int `json:"pubmed,omitempty"`
	CORE            int `json:"core,omitempty"`
	ArXiv           int `json:"arxiv,omitempty"`
	BioRxiv         int `json:"biorxiv,omitempty"`
	EuropePMC       int `json:"europePmc,omitempty"`
	CrossRef        int `json:"crossRef,omitempty"`
	DBLP            int `json:"dblp,omitempty"`
	IEEE            int `json:"ieee,omitempty"`
	NASAADS         int `json:"nasaAds,omitempty"`
}

type TimingStats struct {
	Total     int64 `json:"total"`
	Expansion int64 `json:"expansion"`
	Search    int64 `json:"search"`
}

type MultiSourceResult struct {
	Papers              []Source         `json:"papers"`
	EnhancedQuery       EnhancedQuery    `json:"enhancedQuery"`
	Sources             SourcesStats     `json:"sources"`
	Timing              TimingStats      `json:"timing"`
	Cached              bool             `json:"cached,omitempty"`
	TraceID             string           `json:"traceId,omitempty"`
	QueryUsed           string           `json:"queryUsed,omitempty"`
	RetrievalStrategies []string         `json:"retrievalStrategies,omitempty"`
	RetrievalTrace      []map[string]any `json:"retrievalTrace,omitempty"`
}

type SearchOptions struct {
	Limit               int
	ExpandQuery         bool
	QualitySort         bool
	SkipCache           bool
	Domain              string
	Sources             []string
	YearFrom            int
	YearTo              int
	TraceID             string
	Stage2Rerank        bool
	PageIndexRerank     bool
	RetrievalStrategies []string
	Registry            *internalsearch.ProviderRegistry
}

const searchGatewayCachePrefix = "search_gateway:"

var buildUnifiedSearchRegistry = internalsearch.BuildRegistry

// GlobalSearchRegistry allows the server to inject a shared registry with
// learned provider intelligence and shared circuit-breaker state.
var GlobalSearchRegistry *internalsearch.ProviderRegistry

var runUnifiedParallelSearch = internalsearch.ParallelSearch

var shouldApplyStage2Rerank = func(requested bool) bool { return requested }

var applyStage2Rerank = func(_ context.Context, _ string, papers []Source, _ string, topK int) []Source {
	if topK > 0 && len(papers) > topK {
		return append([]Source(nil), papers[:topK]...)
	}
	return append([]Source(nil), papers...)
}

var shouldRunPageIndexRerank = func(requested bool) bool { return requested }

func mapSearchOpts(opts SearchOptions) internalsearch.SearchOpts {
	return internalsearch.SearchOpts{
		Limit:       opts.Limit,
		Domain:      strings.TrimSpace(opts.Domain),
		Sources:     append([]string(nil), opts.Sources...),
		YearFrom:    opts.YearFrom,
		YearTo:      opts.YearTo,
		SkipCache:   opts.SkipCache,
		QualitySort: opts.QualitySort,
	}
}

func resolvePaperArxivID(paper internalsearch.Paper) string {
	if trimmed := strings.TrimSpace(paper.ArxivID); trimmed != "" {
		return trimmed
	}
	for _, candidate := range []string{
		strings.TrimSpace(paper.ID),
		strings.TrimSpace(paper.Link),
		strings.TrimSpace(paper.Title),
	} {
		if candidate == "" {
			continue
		}
		if matches := rxArxivID.FindStringSubmatch(candidate); len(matches) > 1 {
			return matches[1]
		}
		if strings.HasPrefix(strings.ToLower(candidate), "arxiv:") {
			return strings.TrimSpace(candidate[len("arxiv:"):])
		}
	}
	return ""
}

func mapPaperToSource(paper internalsearch.Paper) Source {
	return Source{
		ID:                       paper.ID,
		Title:                    paper.Title,
		Summary:                  paper.Abstract,
		Abstract:                 paper.Abstract,
		Link:                     paper.Link,
		DOI:                      paper.DOI,
		ArxivID:                  resolvePaperArxivID(paper),
		Source:                   paper.Source,
		SourceApis:               append([]string(nil), paper.SourceApis...),
		SiteName:                 paper.Source,
		Publication:              paper.Venue,
		Authors:                  append([]string(nil), paper.Authors...),
		Keywords:                 append([]string(nil), paper.Keywords...),
		Year:                     paper.Year,
		Month:                    paper.Month,
		Score:                    paper.Score,
		CitationCount:            paper.CitationCount,
		ReferenceCount:           paper.ReferenceCount,
		InfluentialCitationCount: paper.InfluentialCitationCount,
		OpenAccessUrl:            paper.OpenAccessUrl,
		PdfUrl:                   paper.PdfUrl,
		FullText:                 paper.FullText,
		StructureMap:             append([]any(nil), paper.StructureMap...),
	}
}

func mapSearchResultToMultiSource(result internalsearch.SearchResult, enhanced EnhancedQuery) *MultiSourceResult {
	papers := make([]Source, 0, len(result.Papers))
	sources := SourcesStats{}
	for _, paper := range result.Papers {
		papers = append(papers, mapPaperToSource(paper))
		switch strings.ToLower(strings.TrimSpace(paper.Source)) {
		case "semantic_scholar":
			sources.SemanticScholar++
		case "openalex":
			sources.OpenAlex++
		case "pubmed":
			sources.PubMed++
		case "core":
			sources.CORE++
		case "arxiv":
			sources.ArXiv++
		case "crossref":
			sources.CrossRef++
		}
	}

	return &MultiSourceResult{
		Papers:        papers,
		EnhancedQuery: enhanced,
		Sources:       sources,
		Timing: TimingStats{
			Total:  result.LatencyMs,
			Search: result.LatencyMs,
		},
		Cached: result.Cached,
	}
}

func buildRetrievalTrace(result internalsearch.SearchResult) []map[string]any {
	if len(result.Providers) == 0 && len(result.Warnings) == 0 {
		return nil
	}

	trace := make([]map[string]any, 0, len(result.Providers)+len(result.Warnings))
	providers := make([]string, 0, len(result.Providers))
	for provider := range result.Providers {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	for _, provider := range providers {
		trace = append(trace, map[string]any{
			"provider":    provider,
			"resultCount": result.Providers[provider],
			"status":      "ok",
		})
	}
	for _, warning := range result.Warnings {
		trace = append(trace, map[string]any{
			"provider": warning.Provider,
			"status":   "warning",
			"message":  warning.Message,
		})
	}
	return trace
}

func normalizeSearchQuery(query string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(query)), " ")
}

func normalizeStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	sort.Strings(out)
	return out
}

func normalizedSearchOptions(opts SearchOptions) SearchOptions {
	normalized := opts
	normalized.Domain = strings.ToLower(strings.TrimSpace(opts.Domain))
	normalized.Sources = normalizeStringList(opts.Sources)
	normalized.RetrievalStrategies = normalizeStringList(opts.RetrievalStrategies)
	normalized.TraceID = ""
	normalized.SkipCache = false
	normalized.Registry = nil
	if normalized.YearFrom > 0 && normalized.YearTo > 0 && normalized.YearFrom > normalized.YearTo {
		normalized.YearFrom, normalized.YearTo = normalized.YearTo, normalized.YearFrom
	}
	return normalized
}

func logParallelSearchStage(ctx context.Context, stage string, query string, attrs ...any) {
	base := []any{
		"service", "go_orchestrator",
		"runtime", "go",
		"component", "wisdev.parallel_search",
		"operation", "parallel_search",
		"stage", stage,
		"query_preview", QueryPreview(query),
		"query_length", len(normalizeSearchQuery(query)),
	}
	telemetry.FromCtx(ctx).InfoContext(ctx, "wisdev parallel search lifecycle", append(base, attrs...)...)
}

func buildSearchCacheKey(query string, opts SearchOptions) string {
	payload := struct {
		Query string        `json:"query"`
		Opts  SearchOptions `json:"opts"`
	}{
		Query: normalizeSearchQuery(query),
		Opts:  normalizedSearchOptions(opts),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		sum := sha256.Sum256([]byte(payload.Query))
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func cloneRetrievalTrace(trace []map[string]any) []map[string]any {
	if len(trace) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(trace))
	for _, item := range trace {
		out = append(out, cloneAnyMap(item))
	}
	return out
}

func cloneMultiSourceResult(result *MultiSourceResult) *MultiSourceResult {
	if result == nil {
		return nil
	}
	cloned := *result
	cloned.Papers = append([]Source(nil), result.Papers...)
	cloned.RetrievalStrategies = append([]string(nil), result.RetrievalStrategies...)
	cloned.RetrievalTrace = cloneRetrievalTrace(result.RetrievalTrace)
	return &cloned
}

// ==========================================
// CACHE HELPERS
// ==========================================

func checkCache(rdb redis.UniversalClient, key string) (*MultiSourceResult, bool) {
	if rdb == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	val, err := rdb.Get(ctx, searchGatewayCachePrefix+key).Result()
	if err != nil {
		return nil, false
	}

	var res MultiSourceResult
	if err := json.Unmarshal([]byte(val), &res); err != nil {
		return nil, false
	}
	return &res, true
}

func setCache(rdb redis.UniversalClient, key string, result *MultiSourceResult) {
	if rdb == nil {
		return
	}
	// Async set cache
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		data, err := json.Marshal(result)
		if err == nil {
			rdb.Set(ctx, searchGatewayCachePrefix+key, string(data), 24*time.Hour)
		}
	}()
}

// ==========================================
// QUERY EXPANSION (delegates to query_expansion.go)
// ==========================================

func adaptiveExpansionTargetAPIs(query string) []string {
	lower := strings.ToLower(query)
	switch {
	case isMedicalQuery(lower):
		return []string{"pubmed", "europe_pmc", "semantic_scholar", "openalex"}
	case containsAny(lower, "quantum", "gravity", "particle", "cosmology", "galaxy", "astrophysics"):
		return []string{"arxiv", "nasa_ads", "semantic_scholar", "openalex"}
	case containsAny(lower, "algorithm", "software", "neural", "network", "database", "security"):
		return []string{"dblp", "arxiv", "semantic_scholar", "openalex"}
	default:
		return []string{"semantic_scholar", "openalex", "arxiv", "crossref"}
	}
}

func selectAdaptiveExpansion(ctx context.Context, query string) (EnhancedQuery, error) {
	base := ExpandQuery(query)
	if strings.TrimSpace(base.Original) == "" {
		base.Original = strings.TrimSpace(query)
	}
	if strings.TrimSpace(base.Expanded) == "" {
		base.Expanded = strings.TrimSpace(query)
	}

	var intelligence *internalsearch.SearchIntelligence
	if GlobalSearchRegistry != nil {
		intelligence = GlobalSearchRegistry.GetIntelligence()
	}

	resp := GenerateAdaptiveExpansion(
		ctx,
		intelligence,
		nil,
		query,
		6,
		isMedicalQuery(query),
		true,
		true,
		adaptiveExpansionTargetAPIs(query),
	)

	normalizedOriginal := normalizeAdaptiveQuery(query)
	for _, variation := range resp.Variations {
		normalizedVariation := normalizeAdaptiveQuery(variation.Query)
		if normalizedVariation == "" || normalizedVariation == normalizedOriginal {
			continue
		}
		base.Expanded = strings.TrimSpace(variation.Query)
		base.Strategy = strings.TrimSpace(variation.Strategy)
		if base.Intent == "" || base.Intent == "general" {
			base.Intent = "adaptive_expansion"
		}
		return base, nil
	}

	if base.Strategy == "" {
		base.Strategy = "heuristic"
	}
	return base, nil
}

// expandQueryAnalysis chooses the best available expansion path, preferring the
// adaptive aggressive expander while preserving the structured heuristic output.
var expandQueryAnalysis = func(ctx context.Context, query string) (EnhancedQuery, error) {
	return selectAdaptiveExpansion(ctx, query)
}

func isMedicalQuery(query string) bool {
	lower := strings.ToLower(query)
	medicalTerms := []string{"clinical", "patient", "disease", "treatment", "therapy", "medical"}
	for _, term := range medicalTerms {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

// ==========================================
// RESILIENCE HELPER
// ==========================================

func executeWithResilience[T any](ctx context.Context, name string, cb *CircuitBreaker, sem *semaphore.Weighted, op func() (T, error)) (T, error) {
	var empty T
	if !sem.TryAcquire(1) {
		log.Printf("%s semaphore full -- shedding load", name)
		return empty, fmt.Errorf("concurrency limit reached for %s", name)
	}
	defer sem.Release(1)

	var result T
	var err error
	maxRetries := 2
	baseDelay := 100 * time.Millisecond

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if cb != nil && !cb.Allow() {
			log.Printf("%s circuit breaker OPEN -- skipping", name)
			return empty, fmt.Errorf("circuit breaker open for %s", name)
		}

		result, err = op()
		if err == nil {
			if cb != nil {
				cb.RecordSuccess()
			}
			return result, nil
		}
		if cb != nil {
			cb.RecordFailure()
		}

		if attempt == maxRetries || ctx.Err() != nil {
			break
		}

		sleep := baseDelay * time.Duration(1<<attempt)
		jitter := time.Duration(rand.Int63n(int64(sleep) / 2))
		sleep = sleep/2 + jitter

		select {
		case <-ctx.Done():
			return empty, ctx.Err()
		case <-time.After(sleep):
		}
	}
	return empty, err
}

// ==========================================
// INDIVIDUAL SOURCE SEARCH FUNCTIONS
// ==========================================

// --- Semantic Scholar Types ---
type S2Paper struct {
	PaperID     string `json:"paperId"`
	Title       string `json:"title"`
	Abstract    string `json:"abstract"`
	URL         string `json:"url"`
	ExternalIds struct {
		DOI string `json:"DOI"`
	} `json:"externalIds"`
}

type S2Response struct {
	Data []S2Paper `json:"data"`
}

func searchSemanticScholar(ctx context.Context, query string, limit int) ([]Source, error) {
	return legacyDirectProviderSearchDisabled(ctx, "Semantic Scholar", query, limit)
}
func searchOpenAlex(ctx context.Context, query string, limit int) ([]Source, error) {
	return legacyDirectProviderSearchDisabled(ctx, "OpenAlex", query, limit)
}
func searchPubMed(ctx context.Context, query string, limit int) ([]Source, error) {
	return legacyDirectProviderSearchDisabled(ctx, "PubMed", query, limit)
}
func searchCORE(ctx context.Context, query string, limit int) ([]Source, error) {
	return legacyDirectProviderSearchDisabled(ctx, "CORE", query, limit)
}
func searchArXiv(ctx context.Context, query string, limit int) ([]Source, error) {
	return legacyDirectProviderSearchDisabled(ctx, "arXiv", query, limit)
}

func legacyDirectProviderSearchDisabled(ctx context.Context, provider string, _ string, _ int) ([]Source, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%s direct search is disabled; use ParallelSearch with the unified provider registry", provider)
}

// ==========================================
// DEDUPLICATION (with metadata merging)
// ==========================================

// punctuationRe strips punctuation for normalized title comparison.
var punctuationRe = regexp.MustCompile(`[^\w\s]`)

// normalizeTitle produces a canonical form of a paper title for dedup
// comparison: lowercased, trimmed, punctuation removed.
func NormalizeTitle(title string) string {
	t := strings.ToLower(strings.TrimSpace(title))
	t = punctuationRe.ReplaceAllString(t, "")
	// Collapse multiple spaces into one.
	t = strings.Join(strings.Fields(t), " ")
	return t
}

// deduplicatePapers removes duplicates by DOI or very similar title and
// merges metadata: prefers entries with a non-empty Summary and takes
// the higher CitationCount.
func deduplicatePapers(papers []Source) []Source {
	// Map from canonical key -> index into unique slice.
	doiIndex := make(map[string]int)
	titleIndex := make(map[string]int)
	var unique []Source

	for _, p := range papers {
		doiKey := strings.ToLower(strings.TrimSpace(p.DOI))
		titleKey := NormalizeTitle(p.Title)

		existingIdx := -1

		// Check DOI match first (strongest signal).
		if doiKey != "" {
			if idx, ok := doiIndex[doiKey]; ok {
				existingIdx = idx
			}
		}

		// Check title match if no DOI match found.
		if existingIdx == -1 && titleKey != "" {
			if idx, ok := titleIndex[titleKey]; ok {
				existingIdx = idx
			}
		}

		if existingIdx >= 0 {
			// Merge metadata into the existing entry.
			existing := &unique[existingIdx]
			existing.SourceCount++

			// Prefer non-empty summary.
			if strings.TrimSpace(existing.Summary) == "" && strings.TrimSpace(p.Summary) != "" {
				existing.Summary = p.Summary
			}
			if strings.TrimSpace(existing.Abstract) == "" && strings.TrimSpace(p.Abstract) != "" {
				existing.Abstract = p.Abstract
			}

			// Take higher citation count.
			if p.CitationCount > existing.CitationCount {
				existing.CitationCount = p.CitationCount
			}
			if p.ReferenceCount > existing.ReferenceCount {
				existing.ReferenceCount = p.ReferenceCount
			}
			if p.InfluentialCitationCount > existing.InfluentialCitationCount {
				existing.InfluentialCitationCount = p.InfluentialCitationCount
			}

			// Fill in missing DOI.
			if existing.DOI == "" && p.DOI != "" {
				existing.DOI = p.DOI
				doiIndex[strings.ToLower(strings.TrimSpace(p.DOI))] = existingIdx
			}

			// Fill in missing link.
			if existing.Link == "" && p.Link != "" {
				existing.Link = p.Link
			}
			if existing.URL == "" && p.URL != "" {
				existing.URL = p.URL
			}
			if existing.ArxivID == "" && p.ArxivID != "" {
				existing.ArxivID = p.ArxivID
			}
			if existing.Source == "" && p.Source != "" {
				existing.Source = p.Source
			}
			if existing.SiteName == "" && p.SiteName != "" {
				existing.SiteName = p.SiteName
			}
			if existing.Publication == "" && p.Publication != "" {
				existing.Publication = p.Publication
			}
			if countNonEmptyStrings(p.Authors) > countNonEmptyStrings(existing.Authors) {
				existing.Authors = append([]string(nil), p.Authors...)
			}
			if existing.Year == 0 && p.Year != 0 {
				existing.Year = p.Year
			}
			if existing.Month == 0 && p.Month != 0 {
				existing.Month = p.Month
			}
			if p.Score > existing.Score {
				existing.Score = p.Score
			}
			if existing.OpenAccessUrl == "" && p.OpenAccessUrl != "" {
				existing.OpenAccessUrl = p.OpenAccessUrl
			}
			if existing.PdfUrl == "" && p.PdfUrl != "" {
				existing.PdfUrl = p.PdfUrl
			}
			if existing.FullText == "" && p.FullText != "" {
				existing.FullText = p.FullText
			}
			if len(existing.StructureMap) == 0 && len(p.StructureMap) > 0 {
				existing.StructureMap = append([]any(nil), p.StructureMap...)
			}
			existing.SourceApis = mergeUniqueStringValues(existing.SourceApis, p.SourceApis)
			existing.Keywords = mergeUniqueStringValues(existing.Keywords, p.Keywords)
		} else {
			// New unique paper.
			p.SourceCount = 1
			idx := len(unique)
			unique = append(unique, p)

			if doiKey != "" {
				doiIndex[doiKey] = idx
			}
			if titleKey != "" {
				titleIndex[titleKey] = idx
			}
		}
	}
	return unique
}

func countNonEmptyStrings(values []string) int {
	count := 0
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			count++
		}
	}
	return count
}

func mergeUniqueStringValues(primary []string, secondary []string) []string {
	if len(primary) == 0 && len(secondary) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(primary)+len(secondary))
	merged := make([]string, 0, len(primary)+len(secondary))
	appendValue := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		merged = append(merged, trimmed)
	}

	for _, value := range primary {
		appendValue(value)
	}
	for _, value := range secondary {
		appendValue(value)
	}

	if len(merged) == 0 {
		return nil
	}
	return merged
}

// ==========================================
// QUALITY SORTING (multi-signal)
// ==========================================

// sortByQuality ranks papers using multiple quality signals:
//   - Primary: papers with abstracts/summaries first
//   - Secondary: papers that appeared in multiple sources get a boost
//   - Tertiary: papers with DOIs preferred over those without
func sortByQuality(papers []Source, _ string) []Source {
	sorted := make([]Source, len(papers))
	copy(sorted, papers)

	sort.SliceStable(sorted, func(i, j int) bool {
		iScore := qualityScore(sorted[i])
		jScore := qualityScore(sorted[j])
		return iScore > jScore
	})

	return sorted
}

// qualityScore computes a numeric quality signal for sorting.
func qualityScore(p Source) int {
	score := 0

	// Primary: has summary/abstract (+10)
	if len(strings.TrimSpace(p.Summary)) > 0 {
		score += 10
	}

	// Secondary: multi-source boost (+3 per extra Source, max +9)
	if p.SourceCount > 1 {
		bonus := (p.SourceCount - 1) * 3
		if bonus > 9 {
			bonus = 9
		}
		score += bonus
	}

	// Tertiary: has DOI (+2)
	if p.DOI != "" {
		score += 2
	}

	return score
}

// ==========================================
// CORE SEARCH LOGIC
// ==========================================

// ParallelSearch delegates to the maintained unified search package and adapts
// its result into the legacy WisDev search surface.
var ParallelSearch = func(ctx context.Context, rdb redis.UniversalClient, query string, opts SearchOptions) (*MultiSourceResult, error) {
	normalizedQuery := normalizeSearchQuery(query)

	// Surface empty-query failures at the very first boundary so Cloud Logging
	// can identify exactly which caller sent an empty query before any
	// downstream processing begins.
	traceIDEarly := strings.TrimSpace(opts.TraceID)
	if normalizedQuery == "" {
		telemetry.FromCtx(ctx).ErrorContext(ctx, "wisdev parallel search rejected: empty query",
			"service", "go_orchestrator",
			"runtime", "go",
			"component", "wisdev.parallel_search",
			"operation", "parallel_search",
			"stage", "query_received_empty",
			"trace_id", traceIDEarly,
			"raw_query_length", len(strings.TrimSpace(query)),
		)
		return nil, fmt.Errorf("query is required")
	}

	traceID := traceIDEarly
	logParallelSearchStage(ctx, "query_received", normalizedQuery,
		"trace_id", traceID,
		"raw_query_length", len(strings.TrimSpace(query)),
		"normalized_query_length", len(normalizedQuery),
	)
	logParallelSearchStage(ctx, "entry", normalizedQuery,
		"trace_id", traceID,
		"expand_query", opts.ExpandQuery,
		"quality_sort", opts.QualitySort,
		"skip_cache", opts.SkipCache,
		"domain", strings.TrimSpace(opts.Domain),
		"source_count", len(opts.Sources),
		"limit", opts.Limit,
	)
	cacheKey := buildSearchCacheKey(normalizedQuery, opts)
	if !opts.SkipCache {
		if cached, ok := checkCache(rdb, cacheKey); ok {
			rehydrated := cloneMultiSourceResult(cached)
			rehydrated.Cached = true
			rehydrated.TraceID = traceID
			rehydrated.RetrievalTrace = append(rehydrated.RetrievalTrace, map[string]any{
				"strategy": "cache_hit",
				"status":   "hit",
			})
			logParallelSearchStage(ctx, "cache_hit", normalizedQuery,
				"trace_id", traceID,
				"result_count", len(rehydrated.Papers),
				"query_used", QueryPreview(rehydrated.QueryUsed),
			)
			return rehydrated, nil
		}
	}

	enhanced := EnhancedQuery{Original: normalizedQuery, Expanded: normalizedQuery}
	searchQuery := normalizedQuery
	extraTrace := make([]map[string]any, 0, 4)
	if opts.ExpandQuery {
		expanded, err := expandQueryAnalysis(ctx, normalizedQuery)
		if err != nil {
			extraTrace = append(extraTrace, map[string]any{
				"strategy": "query_expansion",
				"status":   "degraded_to_original_query",
				"error":    err.Error(),
			})
			telemetry.FromCtx(ctx).WarnContext(ctx, "wisdev query expansion degraded",
				"service", "go_orchestrator",
				"runtime", "go",
				"component", "wisdev.parallel_search",
				"operation", "parallel_search",
				"stage", "query_expansion_degraded",
				"trace_id", traceID,
				"query_preview", QueryPreview(normalizedQuery),
				"error", err.Error(),
			)
		} else {
			enhanced = expanded
			if strings.TrimSpace(expanded.Expanded) != "" && expanded.Expanded != normalizedQuery {
				searchQuery = strings.TrimSpace(expanded.Expanded)
				logParallelSearchStage(ctx, "query_expansion_applied", normalizedQuery,
					"trace_id", traceID,
					"expanded_query_preview", QueryPreview(searchQuery),
					"strategy", strings.TrimSpace(expanded.Strategy),
					"intent", strings.TrimSpace(expanded.Intent),
				)
			} else {
				// Expansion returned the same text as the input — log a distinct
				// stage so operators can tell "we tried and found no variation"
				// apart from "expansion was never attempted".
				logParallelSearchStage(ctx, "query_expansion_noop", normalizedQuery,
					"trace_id", traceID,
					"strategy", strings.TrimSpace(expanded.Strategy),
					"reason", "expanded_equals_original",
				)
			}
		}
	}

	registry := opts.Registry
	if registry == nil {
		registry = GlobalSearchRegistry
	}
	if registry == nil {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					extraTrace = append(extraTrace, map[string]any{
						"strategy": "provider_registry",
						"status":   "registry_init_error",
						"error":    fmt.Sprintf("%v", recovered),
					})
					registry = nil
				}
			}()
			registry = buildUnifiedSearchRegistry()
		}()
	}
	if registry == nil {
		extraTrace = append(extraTrace, map[string]any{
			"strategy": "provider_registry",
			"status":   "skipped_no_registry",
		})
		return &MultiSourceResult{
			Papers:              []Source{},
			EnhancedQuery:       enhanced,
			TraceID:             traceID,
			QueryUsed:           searchQuery,
			RetrievalStrategies: append([]string(nil), opts.RetrievalStrategies...),
			RetrievalTrace:      extraTrace,
		}, nil
	}
	registry.SetRedis(rdb)
	result := runUnifiedParallelSearch(ctx, registry, searchQuery, mapSearchOpts(opts))
	if opts.ExpandQuery && searchQuery != normalizedQuery && len(result.Papers) == 0 {
		fallbackResult := runUnifiedParallelSearch(ctx, registry, normalizedQuery, mapSearchOpts(opts))
		if len(fallbackResult.Papers) > 0 {
			result = fallbackResult
			searchQuery = normalizedQuery
			extraTrace = append(extraTrace, map[string]any{
				"strategy": "query_expansion",
				"status":   "fallback_to_original_query_succeeded",
			})
		}
	}
	if opts.ExpandQuery && enhanced.Strategy != "" && registry != nil {
		if intelligence := registry.GetIntelligence(); intelligence != nil {
			_ = intelligence.RecordExpansionPerformance(
				ctx,
				normalizedQuery,
				searchQuery,
				enhanced.Strategy,
				len(result.Papers),
				estimateExpansionConfidence(result, opts.Limit),
			)
		}
	}
	finalRes := mapSearchResultToMultiSource(result, enhanced)
	finalRes.QueryUsed = searchQuery
	finalRes.TraceID = traceID
	finalRes.RetrievalStrategies = append([]string(nil), opts.RetrievalStrategies...)
	finalRes.RetrievalTrace = buildRetrievalTrace(result)
	if finalRes.RetrievalTrace == nil {
		finalRes.RetrievalTrace = make([]map[string]any, 0)
	}

	// Add high-level orchestration trace
	finalRes.RetrievalTrace = append(finalRes.RetrievalTrace, map[string]any{
		"strategy":         "provider_registry",
		"queryUsed":        searchQuery,
		"domain":           strings.TrimSpace(opts.Domain),
		"pageIndexRerank":  opts.PageIndexRerank,
		"latencyMs":        result.LatencyMs,
		"retrievalBackend": "provider_registry",
	})
	finalRes.RetrievalTrace = append(finalRes.RetrievalTrace, extraTrace...)

	if opts.Stage2Rerank {
		if shouldApplyStage2Rerank(opts.Stage2Rerank) {
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						finalRes.RetrievalTrace = append(finalRes.RetrievalTrace, map[string]any{
							"strategy": "stage2_rerank",
							"status":   "preserved_existing_order",
							"error":    fmt.Sprintf("%v", recovered),
						})
					}
				}()
				finalRes.Papers = applyStage2Rerank(ctx, searchQuery, finalRes.Papers, strings.TrimSpace(opts.Domain), opts.Limit)
			}()
		} else {
			finalRes.RetrievalTrace = append(finalRes.RetrievalTrace, map[string]any{
				"strategy": "stage2_rerank",
				"status":   "skipped_gate_disabled",
			})
		}
	}
	if opts.PageIndexRerank {
		if shouldRunPageIndexRerank(opts.PageIndexRerank) {
			finalRes.RetrievalTrace = append(finalRes.RetrievalTrace, map[string]any{
				"strategy": "pageindex_rerank",
				"status":   "applied",
			})
		} else {
			finalRes.RetrievalTrace = append(finalRes.RetrievalTrace, map[string]any{
				"strategy": "pageindex_rerank",
				"status":   "skipped_gate_disabled",
			})
		}
	}
	if opts.Limit > 0 && len(finalRes.Papers) > opts.Limit {
		finalRes.Papers = append([]Source(nil), finalRes.Papers[:opts.Limit]...)
	}

	if !opts.SkipCache {
		setCache(rdb, cacheKey, finalRes)
	}

	warnings := mapWarningsFromRetrievalTrace(finalRes.RetrievalTrace)

	// Emit a dedicated zero-results warning so Cloud Logging surfaces the
	// per-provider failure reasons without requiring a trace-log drill-down.
	// This closes the "why did search return nothing?" diagnosability gap.
	if len(finalRes.Papers) == 0 {
		warningMessages := make([]string, 0, len(warnings))
		for _, w := range warnings {
			if w.Provider != "" || w.Message != "" {
				warningMessages = append(warningMessages, w.Provider+": "+w.Message)
			}
		}
		logParallelSearchStage(ctx, "zero_results", normalizedQuery,
			"trace_id", traceID,
			"query_used", QueryPreview(finalRes.QueryUsed),
			"expand_query", opts.ExpandQuery,
			"fallback_attempted", opts.ExpandQuery && finalRes.QueryUsed != normalizedQuery,
			"warning_count", len(warnings),
			"provider_warnings", strings.Join(warningMessages, "; "),
			"skip_cache", opts.SkipCache,
		)
	}

	logParallelSearchStage(ctx, "exit", normalizedQuery,
		"trace_id", traceID,
		"query_used", QueryPreview(finalRes.QueryUsed),
		"result_count", len(finalRes.Papers),
		"cached", finalRes.Cached,
		"warning_count", len(warnings),
	)

	return finalRes, nil
}

func mapWarningsFromRetrievalTrace(trace []map[string]any) []internalsearch.ProviderWarning {
	if len(trace) == 0 {
		return nil
	}
	warnings := make([]internalsearch.ProviderWarning, 0, len(trace))
	for _, item := range trace {
		status := strings.ToLower(strings.TrimSpace(AsOptionalString(item["status"])))
		if status != "warning" {
			continue
		}
		provider := strings.TrimSpace(AsOptionalString(item["provider"]))
		message := strings.TrimSpace(AsOptionalString(item["message"]))
		if provider == "" && message == "" {
			continue
		}
		warnings = append(warnings, internalsearch.ProviderWarning{
			Provider: provider,
			Message:  message,
		})
	}
	return warnings
}

func estimateExpansionConfidence(result internalsearch.SearchResult, limit int) float64 {
	if len(result.Papers) == 0 {
		return 0
	}
	if limit <= 0 {
		limit = 10
	}

	coverage := clampFloat64(float64(len(result.Papers))/float64(limit), 0, 1)
	abstractCoverage := 0.0
	citationSignal := 0.0
	for _, paper := range result.Papers {
		if strings.TrimSpace(paper.Abstract) != "" {
			abstractCoverage += 1
		}
		if paper.CitationCount > 0 {
			citationSignal += 1
		}
	}
	abstractCoverage = abstractCoverage / float64(len(result.Papers))
	citationSignal = citationSignal / float64(len(result.Papers))

	return clampFloat64((coverage*0.55)+(abstractCoverage*0.25)+(citationSignal*0.20), 0, 1)
}

// IterativeResearchResult is the domain-layer result of IterativeResearch.
// Use float64 throughout for full computation precision; narrowing to float32
// happens only at the proto transport boundary in server_grpc.go.
type IterativeResearchResult struct {
	Papers        []Source       `json:"papers"`
	Iterations    []IterationLog `json:"iterations"`
	FinalCoverage float64        `json:"finalCoverage"`
	FinalReward   float64        `json:"finalReward"`
}

// IterationLog is the domain-layer record of a single research iteration.
// It intentionally mirrors the proto IterationLog message but uses float64 / int
// for internal precision. Conversion to proto is handled by toProtoIterationLogs
// in server_grpc.go; do not replace this struct with the generated proto type.
type IterationLog struct {
	Iteration     int      `json:"iteration"`
	QueriesAdded  []string `json:"queriesAdded"`
	CoverageScore float64  `json:"coverageScore"`
	PRMReward     float64  `json:"prmReward"`
}

func callPRM(ctx context.Context, sessionID string, output map[string]any) (float64, error) {
	_ = ctx
	_ = sessionID

	paperCount := intFromAny(output["paperCount"])
	searchSuccess := clampFloat64(floatFromAny(output["searchSuccess"]), 0, 1)
	citationVerifiedRatio := clampFloat64(floatFromAny(output["citationVerifiedRatio"]), 0, 1)
	coverageScore := clampFloat64(floatFromAny(output["coverageScore"]), 0, 1)
	success := boolFromAny(output["success"])

	if paperCount == 0 {
		return 0, nil
	}

	reward := 0.15 + (0.35 * searchSuccess) + (0.35 * citationVerifiedRatio) + (0.15 * coverageScore)
	if success {
		reward += 0.05
	}
	if reward > 1 {
		reward = 1
	}
	if reward < 0 {
		reward = 0
	}
	return reward, nil
}

var IterativeResearch = func(
	ctx context.Context,
	queries []string,
	sessionID string,
	maxIterations int,
	coverageThreshold float64,
) (*IterativeResearchResult, error) {
	if maxIterations <= 0 {
		maxIterations = 3
	}
	if coverageThreshold <= 0 {
		coverageThreshold = 0.8
	}

	var allPapers []Source
	var iterationLogs []IterationLog
	workingQueries := append([]string{}, queries...)

	for i := 1; i <= maxIterations; i++ {
		// Run ParallelSearch for each new query
		var newPapers []Source
		for _, q := range workingQueries {
			res, err := ParallelSearch(ctx, nil, q, SearchOptions{Limit: 10, QualitySort: true})
			if err == nil {
				newPapers = append(newPapers, res.Papers...)
			}
		}

		allPapers = deduplicatePapers(append(allPapers, newPapers...))

		verifiedCount := 0
		for _, paper := range allPapers {
			if strings.TrimSpace(paper.DOI) != "" || strings.TrimSpace(paper.Link) != "" {
				verifiedCount++
			}
		}
		searchSuccess := 0.0
		if len(workingQueries) > 0 {
			searchSuccess = float64(len(newPapers)) / float64(len(workingQueries)*2)
		}
		coverageScore := 0.0
		if len(queries) > 0 {
			coverageScore = float64(len(allPapers)) / float64(len(queries)*5)
		}
		prmOutput := map[string]any{
			"paperCount":            len(allPapers),
			"searchSuccess":         clampFloat64(searchSuccess, 0, 1),
			"citationVerifiedRatio": clampFloat64(float64(verifiedCount)/float64(MaxInt(len(allPapers), 1)), 0, 1),
			"coverageScore":         clampFloat64(coverageScore, 0, 1),
			"success":               len(allPapers) >= MaxInt(4, len(queries)*2),
		}

		reward, _ := callPRM(ctx, sessionID, prmOutput)

		log := IterationLog{
			Iteration:     i,
			QueriesAdded:  workingQueries,
			CoverageScore: float64(len(allPapers)) / 30.0, // simple coverage proxy
			PRMReward:     reward,
		}
		iterationLogs = append(iterationLogs, log)

		if reward >= coverageThreshold {
			break
		}

		// Prepare for next iteration using deterministic query expansion and
		// the strongest signals from the papers we already found.
		if i < maxIterations {
			workingQueries = buildDeterministicFollowUpQueries(ctx, workingQueries, allPapers)
		}
	}

	var finalCoverage, finalReward float64
	if n := len(iterationLogs); n > 0 {
		last := iterationLogs[n-1]
		finalCoverage = last.CoverageScore
		finalReward = last.PRMReward
	}
	return &IterativeResearchResult{
		Papers:        allPapers,
		Iterations:    iterationLogs,
		FinalCoverage: finalCoverage,
		FinalReward:   finalReward,
	}, nil
}

// FastParallelSearch provides maximum speed without query expansion over the
// maintained unified search providers.
var FastParallelSearch = func(ctx context.Context, rdb redis.UniversalClient, query string, limit int) ([]Source, error) {
	result, err := ParallelSearch(ctx, rdb, query, SearchOptions{
		Limit:       limit,
		ExpandQuery: false,
		QualitySort: true,
	})
	if err != nil {
		return nil, err
	}
	return result.Papers, nil
}

func buildDeterministicFollowUpQueries(ctx context.Context, currentQueries []string, papers []Source) []string {
	seen := make(map[string]struct{})
	queries := make([]string, 0, 6)

	addQuery := func(value string) {
		normalized := strings.TrimSpace(value)
		if normalized == "" {
			return
		}
		key := strings.ToLower(normalized)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		queries = append(queries, normalized)
	}

	for _, query := range currentQueries {
		addQuery(query)
		expanded, err := expandQueryAnalysis(ctx, query)
		if err != nil {
			continue
		}
		addQuery(expanded.Expanded)
		if len(expanded.Keywords) > 0 {
			addQuery(query + " " + strings.Join(limitStrings(expanded.Keywords, 2), " "))
		}
	}

	informativeTerms := collectInformativeTerms(papers, 6)
	baseQuery := firstNonEmptyString(currentQueries)
	if baseQuery == "" && len(papers) > 0 {
		baseQuery = strings.TrimSpace(papers[0].Title)
	}
	if baseQuery != "" {
		for _, term := range informativeTerms {
			addQuery(baseQuery + " " + term)
		}
	}

	if len(queries) == 0 {
		addQuery("literature review")
	}
	if len(queries) > 6 {
		queries = queries[:6]
	}
	return queries
}

func collectInformativeTerms(papers []Source, limit int) []string {
	stopwords := map[string]struct{}{
		// Common English function words
		"a": {}, "an": {}, "and": {}, "for": {}, "from": {}, "the": {}, "with": {},
		"of": {}, "to": {}, "in": {}, "on": {}, "by": {}, "or": {}, "via": {},
		"into": {}, "using": {}, "based": {}, "study": {}, "paper": {}, "review": {},
		// Structural section-header terms — these produce near-zero results
		// when appended to a base query (same set blocked in topic_tree.go).
		"background": {}, "methods": {}, "applications": {}, "findings": {},
		"outcomes": {}, "safety": {}, "algorithms": {}, "benchmarks": {},
		"systems": {}, "mechanisms": {}, "theory": {}, "experiments": {},
		"overview": {}, "introduction": {}, "discussion": {}, "results": {},
		"conclusion": {},
	}
	seen := make(map[string]struct{})
	terms := make([]string, 0, limit)

	add := func(text string) {
		for _, raw := range strings.Fields(strings.ToLower(text)) {
			term := strings.Trim(raw, ".,:;()[]{}\"'`")
			if len(term) < 4 {
				continue
			}
			if _, blocked := stopwords[term]; blocked {
				continue
			}
			if _, ok := seen[term]; ok {
				continue
			}
			seen[term] = struct{}{}
			terms = append(terms, term)
			if len(terms) >= limit {
				return
			}
		}
	}

	for _, paper := range papers {
		add(paper.Title)
		if len(terms) >= limit {
			break
		}
		add(paper.Summary)
		if len(terms) >= limit {
			break
		}
	}

	return terms
}

func firstNonEmptyString(values []string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func limitStrings(values []string, limit int) []string {
	if limit <= 0 || len(values) <= limit {
		return values
	}
	return values[:limit]
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed
		}
	}
	return 0
}

func floatFromAny(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int32:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		if parsed, err := typed.Float64(); err == nil {
			return parsed
		}
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
			return parsed
		}
	}
	return 0
}

func boolFromAny(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed
	default:
		return false
	}
}

func clampFloat64(value float64, min float64, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
