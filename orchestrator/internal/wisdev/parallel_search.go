package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"regexp"
	internalsearch "github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
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

type Source struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Summary       string   `json:"summary"`
	Link          string   `json:"link"`
	DOI           string   `json:"doi,omitempty"`
	Source        string   `json:"source,omitempty"`
	SourceApis    []string `json:"sourceApis,omitempty"`
	SiteName      string   `json:"siteName,omitempty"`
	Publication   string   `json:"publication,omitempty"`
	Authors       []string `json:"authors,omitempty"`
	Keywords      []string `json:"keywords,omitempty"`
	Year          int      `json:"year,omitempty"`
	Score         float64  `json:"score,omitempty"`
	CitationCount int      `json:"citationCount,omitempty"`
	SourceCount   int      `json:"-"` // internal: how many sources returned this paper
}

type EnhancedQuery struct {
	Original string   `json:"original"`
	Expanded string   `json:"expanded"`
	Intent   string   `json:"intent"`
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
	Papers        []Source      `json:"papers"`
	EnhancedQuery EnhancedQuery `json:"enhancedQuery"`
	Sources       SourcesStats  `json:"sources"`
	Timing        TimingStats   `json:"timing"`
	Cached        bool          `json:"cached,omitempty"`
	TraceID       string        `json:"traceId,omitempty"`
}

type SearchOptions struct {
	Limit           int
	ExpandQuery     bool
	QualitySort     bool
	SkipCache       bool
	Domain          string
	Sources         []string
	YearFrom        int
	YearTo          int
	TraceID         string
	Stage2Rerank    bool
	PageIndexRerank bool
}

var buildUnifiedSearchRegistry = internalsearch.BuildRegistry

var runUnifiedParallelSearch = internalsearch.ParallelSearch

func mapSearchOpts(opts SearchOptions) internalsearch.SearchOpts {
	return internalsearch.SearchOpts{
		Limit:       opts.Limit,
		Domain:      strings.TrimSpace(opts.Domain),
		YearFrom:    opts.YearFrom,
		YearTo:      opts.YearTo,
		SkipCache:   opts.SkipCache,
		QualitySort: opts.QualitySort,
	}
}

func mapPaperToSource(paper internalsearch.Paper) Source {
	return Source{
		ID:            paper.ID,
		Title:         paper.Title,
		Summary:       paper.Abstract,
		Link:          paper.Link,
		DOI:           paper.DOI,
		Source:        paper.Source,
		Authors:       append([]string(nil), paper.Authors...),
		Year:          paper.Year,
		Score:         paper.Score,
		CitationCount: paper.CitationCount,
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

// ==========================================
// CACHE HELPERS
// ==========================================

func checkCache(rdb redis.UniversalClient, key string) (*MultiSourceResult, bool) {
	if rdb == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	val, err := rdb.Get(ctx, "search_gateway:"+key).Result()
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
			rdb.Set(ctx, "search_gateway:"+key, string(data), 24*time.Hour)
		}
	}()
}

// ==========================================
// QUERY EXPANSION (delegates to query_expansion.go)
// ==========================================

// expandQueryAnalysis uses the local synonym-based expansion from
// query_expansion.go instead of the previous stub.
var expandQueryAnalysis = func(_ context.Context, query string) (EnhancedQuery, error) {
	return ExpandQuery(query), nil
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
	return nil, nil
}
func searchOpenAlex(ctx context.Context, query string, limit int) ([]Source, error) { return nil, nil }
func searchPubMed(ctx context.Context, query string, limit int) ([]Source, error)   { return nil, nil }
func searchCORE(ctx context.Context, query string, limit int) ([]Source, error)     { return nil, nil }
func searchArXiv(ctx context.Context, query string, limit int) ([]Source, error)    { return nil, nil }

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

			// Take higher citation count.
			if p.CitationCount > existing.CitationCount {
				existing.CitationCount = p.CitationCount
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
	normalizedQuery := strings.TrimSpace(query)
	if normalizedQuery == "" {
		return nil, fmt.Errorf("query is required")
	}

	cacheKey := fmt.Sprintf("%s:%v", query, opts)
	if !opts.SkipCache {
		if cached, ok := checkCache(rdb, cacheKey); ok {
			return cached, nil
		}
	}

	enhanced := EnhancedQuery{Original: normalizedQuery, Expanded: normalizedQuery}
	searchQuery := normalizedQuery
	if opts.ExpandQuery {
		expanded, err := expandQueryAnalysis(ctx, normalizedQuery)
		if err != nil {
			return nil, err
		}
		enhanced = expanded
		if strings.TrimSpace(expanded.Expanded) != "" {
			searchQuery = strings.TrimSpace(expanded.Expanded)
		}
	}

	registry := buildUnifiedSearchRegistry()
	registry.SetRedis(rdb)
	result := runUnifiedParallelSearch(ctx, registry, searchQuery, mapSearchOpts(opts))
	finalRes := mapSearchResultToMultiSource(result, enhanced)

	if !opts.SkipCache {
		setCache(rdb, cacheKey, finalRes)
	}

	return finalRes, nil
}

type IterativeResearchResult struct {
	Papers        []Source       `json:"papers"`
	Iterations    []IterationLog `json:"iterations"`
	FinalCoverage float64        `json:"finalCoverage"`
	FinalReward   float64        `json:"finalReward"`
}

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

	return &IterativeResearchResult{
		Papers:        allPapers,
		Iterations:    iterationLogs,
		FinalCoverage: iterationLogs[len(iterationLogs)-1].CoverageScore,
		FinalReward:   iterationLogs[len(iterationLogs)-1].PRMReward,
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
		"a": {}, "an": {}, "and": {}, "for": {}, "from": {}, "the": {}, "with": {},
		"of": {}, "to": {}, "in": {}, "on": {}, "by": {}, "or": {}, "via": {},
		"into": {}, "using": {}, "based": {}, "study": {}, "paper": {}, "review": {},
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
