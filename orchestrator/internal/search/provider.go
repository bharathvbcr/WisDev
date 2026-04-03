// Package search provides the unified academic search infrastructure for ScholarLM.
//
// Architecture:
//   - SearchProvider interface — every academic source implements this
//   - ProviderRegistry — registers providers and routes by domain
//   - ParallelOrchestrator — fan-out search across selected providers
//   - RRF Fusion — merges ranked lists into a single deduped result
//   - QualityScorer — citation-aware quality ranking
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/telemetry"
	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/semaphore"
)

// ============================================================
// Core types
// ============================================================

// Paper is the canonical paper type shared across all providers.
type Paper struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Abstract      string   `json:"abstract"`
	Link          string   `json:"link"`
	DOI           string   `json:"doi,omitempty"`
	Source        string   `json:"source"`
	SourceApis    []string `json:"sourceApis,omitempty"`
	Authors       []string `json:"authors,omitempty"`
	Year          int      `json:"year,omitempty"`
	Venue         string   `json:"venue,omitempty"`
	Keywords      []string `json:"keywords,omitempty"`
	CitationCount int      `json:"citationCount,omitempty"`
	Score         float64  `json:"score,omitempty"`
	EvidenceLevel string   `json:"evidenceLevel,omitempty"`
	// internal: how many providers returned this paper (used for dedup)
	providerCount int
}

// SearchOpts controls provider behaviour per-request.
type SearchOpts struct {
	Limit            int
	Domain           string
	YearFrom         int
	YearTo           int
	SkipCache        bool
	QualitySort      bool
	DynamicProviders bool
	LLMClient        *llm.Client // Required if DynamicProviders is true
}

// ProviderResult wraps a provider's results with latency metadata.
type ProviderResult struct {
	Provider  string
	Papers    []Paper
	LatencyMs int64
	Err       error
}

// SearchResult is the unified response from the parallel orchestrator.
type SearchResult struct {
	Papers    []Paper           `json:"papers"`
	Providers map[string]int    `json:"providers"` // provider → count
	LatencyMs int64             `json:"latencyMs"`
	Cached    bool              `json:"cached,omitempty"`
	Warnings  []ProviderWarning `json:"warnings,omitempty"`
}

// ProviderWarning carries degraded-mode info about a failing provider.
type ProviderWarning struct {
	Provider string `json:"provider"`
	Message  string `json:"message"`
}

// ============================================================
// SearchProvider interface
// ============================================================

// SearchProvider is the contract every academic source must implement.
type SearchProvider interface {
	// Name returns the canonical provider identifier (e.g. "semantic_scholar").
	Name() string

	// Search performs a search and returns papers. Implementations must
	// respect ctx cancellation and apply the supplied opts.
	Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error)

	// Domains returns the academic domains this provider specialises in.
	// Empty slice means the provider is used for all domains.
	Domains() []string

	// Healthy returns false if the provider is currently experiencing errors
	// and should be skipped to avoid adding latency.
	Healthy() bool

	// Tools returns any specialised capabilities of this provider (e.g. "author_lookup").
	Tools() []string
}

// CitationGraphProvider extends SearchProvider with citation-specific retrieval.
type CitationGraphProvider interface {
	SearchProvider
	GetCitations(ctx context.Context, paperID string, limit int) ([]Paper, error)
}

// ============================================================
// ProviderRegistry — domain-based routing
// ============================================================

// ProviderRegistry maps domain labels to ordered provider lists.
type ProviderRegistry struct {
	mu         sync.RWMutex
	providers  map[string]SearchProvider // keyed by Name()
	routes     map[string][]string       // domain → []providerName
	defaults   []string                  // used when domain is empty / unrecognised
	breakers   map[string]*CircuitBreaker
	semaphores map[string]*semaphore.Weighted
	redis      redis.UniversalClient

	// Adaptive concurrency
	adaptiveCaps map[string]int64
	globalSem    *semaphore.Weighted
}

// NewProviderRegistry creates an empty registry.
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		providers:    make(map[string]SearchProvider),
		routes:       make(map[string][]string),
		breakers:     make(map[string]*CircuitBreaker),
		semaphores:   make(map[string]*semaphore.Weighted),
		adaptiveCaps: make(map[string]int64),
		// Global backpressure limit: 50 concurrent provider requests across all users
		globalSem: semaphore.NewWeighted(50),
	}
}

func (r *ProviderRegistry) SetRedis(client redis.UniversalClient) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.redis = client
}

// Register adds a provider. Safe for concurrent use.
func (r *ProviderRegistry) Register(p SearchProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := p.Name()
	r.providers[name] = p
	r.breakers[name] = NewCircuitBreaker(name, 5, 30*time.Second)

	// Default to 10 concurrent requests per provider
	cap := int64(10)
	r.adaptiveCaps[name] = cap
	r.semaphores[name] = semaphore.NewWeighted(cap)

	for _, domain := range p.Domains() {
		r.routes[domain] = append(r.routes[domain], name)
	}
	if len(p.Domains()) == 0 {
		r.defaults = append(r.defaults, name)
	}
}

// AdjustConcurrency dynamically scales provider limits based on success/failure.
// Called by the orchestrator after each request.
func (r *ProviderRegistry) AdjustConcurrency(name string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cap, ok := r.adaptiveCaps[name]
	if !ok {
		return
	}

	if err != nil {
		// Reduce capacity on error (min 2)
		if cap > 2 {
			cap--
		}
	} else {
		// Increase capacity on success (max 20)
		if cap < 20 {
			cap++
		}
	}

	if cap != r.adaptiveCaps[name] {
		r.adaptiveCaps[name] = cap
		// Replace semaphore with new capacity.
		// Note: active requests on the old semaphore will still finish normally.
		r.semaphores[name] = semaphore.NewWeighted(cap)
	}
}

// SetConcurrencyLimit sets the maximum number of concurrent requests for a provider.
func (r *ProviderRegistry) SetConcurrencyLimit(name string, limit int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.semaphores[name] = semaphore.NewWeighted(limit)
}

// SetDefaultOrder overrides the default provider order (used when domain is unknown).
func (r *ProviderRegistry) SetDefaultOrder(names []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaults = names
}

// ProvidersFor returns the healthy providers for the given domain.
func (r *ProviderRegistry) ProvidersFor(domain string) []SearchProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var names []string
	if domain != "" {
		names = r.routes[strings.ToLower(domain)]
	}
	if len(names) == 0 {
		names = r.defaults
	}

	out := make([]SearchProvider, 0, len(names))
	for _, name := range names {
		p, ok := r.providers[name]
		if ok && p.Healthy() {
			out = append(out, p)
		}
	}
	return out
}

// All returns every registered provider regardless of domain.
func (r *ProviderRegistry) All() []SearchProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]SearchProvider, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p)
	}
	return out
}

// GetCitations fetches papers that cited the given paper ID.
func (r *ProviderRegistry) GetCitations(ctx context.Context, paperID string, limit int) ([]Paper, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 1. Try Semantic Scholar first (preferred)
	if p, ok := r.providers["semantic_scholar"]; ok {
		if cp, ok := p.(CitationGraphProvider); ok && cp.Healthy() {
			return cp.GetCitations(ctx, paperID, limit)
		}
	}

	// 2. Fallback to any other healthy provider that implements the interface
	for _, p := range r.providers {
		if cp, ok := p.(CitationGraphProvider); ok && cp.Healthy() {
			return cp.GetCitations(ctx, paperID, limit)
		}
	}

	return nil, fmt.Errorf("no healthy citation graph providers found")
}

// SelectProvidersDynamic uses the LLM to choose the best providers for a query.
func (r *ProviderRegistry) SelectProvidersDynamic(ctx context.Context, llmClient *llm.Client, query string) []SearchProvider {
	if llmClient == nil {
		return r.ProvidersFor("general")
	}

	r.mu.RLock()
	available := make([]string, 0, len(r.providers))
	tools := make(map[string][]string)
	for name, p := range r.providers {
		available = append(available, name)
		if t := p.Tools(); len(t) > 0 {
			tools[name] = t
		}
	}
	r.mu.RUnlock()

	prompt := fmt.Sprintf(`Select the top 3-4 academic search providers from the list below that are most relevant to this research query.
Query: %s
Available Providers: %v
Specialised Tools: %v

Return a JSON array of strings containing only the provider names.`, query, available, tools)

	resp, err := llmClient.Generate(ctx, &llmv1.GenerateRequest{
		Prompt: prompt,
		Model:  llm.ResolveStandardModel(),
	})
	if err != nil {
		return r.ProvidersFor("general")
	}

	var selectedNames []string
	if err := json.Unmarshal([]byte(resp.Text), &selectedNames); err != nil {
		// Try to extract from text if not pure JSON
		for _, name := range available {
			if strings.Contains(strings.ToLower(resp.Text), strings.ToLower(name)) {
				selectedNames = append(selectedNames, name)
			}
		}
	}

	if len(selectedNames) == 0 {
		return r.ProvidersFor("general")
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]SearchProvider, 0, len(selectedNames))
	for _, name := range selectedNames {
		if p, ok := r.providers[name]; ok && p.Healthy() {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return r.ProvidersFor("general")
	}
	return out
}

// ============================================================
// Parallel orchestrator
// ============================================================

// SharedHTTPClient is the connection-pooled client used by all providers.
var SharedHTTPClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        500,
		MaxIdleConnsPerHost: 50,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second,
	},
	Timeout: 12 * time.Second,
}

// ParallelSearch fans out to all providers appropriate for the domain,
// collects results concurrently, fuses them with RRF, and returns a
// single deduplicated ranked list.
func ParallelSearch(ctx context.Context, reg *ProviderRegistry, query string, opts SearchOpts) SearchResult {
	started := time.Now()
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}

	// 0. Cache check
	cacheKey := getCacheKey(query, opts)
	if !opts.SkipCache {
		if cached, ok := checkCache(ctx, reg.redis, cacheKey); ok {
			return *cached
		}
	}

	var providers []SearchProvider
	if opts.DynamicProviders && opts.LLMClient != nil {
		providers = reg.SelectProvidersDynamic(ctx, opts.LLMClient, query)
	} else {
		providers = reg.ProvidersFor(opts.Domain)
		if len(providers) == 0 {
			providers = reg.All()
		}
	}

	// Global backpressure: acquire 1 slot from the global semaphore
	if reg.globalSem != nil {
		gCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if err := reg.globalSem.Acquire(gCtx, 1); err != nil {
			cancel()
			return SearchResult{
				LatencyMs: time.Since(started).Milliseconds(),
				Warnings: []ProviderWarning{{
					Provider: "system",
					Message:  "System too busy, please try again in a few seconds",
				}},
			}
		}
		cancel()
		defer reg.globalSem.Release(1)
	}

	// Fan-out
	results := make(chan ProviderResult, len(providers))
	for _, p := range providers {
		breaker := reg.breakers[p.Name()]

		reg.mu.RLock()
		sem := reg.semaphores[p.Name()]
		reg.mu.RUnlock()

		if breaker != nil && !breaker.Allow() {
			results <- ProviderResult{
				Provider: p.Name(),
				Err:      fmt.Errorf("circuit breaker open"),
			}
			continue
		}

		go func(prov SearchProvider, cb *CircuitBreaker, s *semaphore.Weighted) {
			// Acquire provider-specific semaphore with timeout
			acquireCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			if s != nil {
				if err := s.Acquire(acquireCtx, 1); err != nil {
					results <- ProviderResult{
						Provider: prov.Name(),
						Err:      fmt.Errorf("concurrency limit reached: %w", err),
					}
					return
				}
				defer s.Release(1)
			}

			t0 := time.Now()
			papers, err := prov.Search(ctx, query, opts)

			// Update adaptive concurrency
			reg.AdjustConcurrency(prov.Name(), err)

			telemetry.RecordSearchProviderRequest(prov.Name(), err)
			if cb != nil {
				cb.RecordResult(err)
			}
			results <- ProviderResult{
				Provider:  prov.Name(),
				Papers:    papers,
				LatencyMs: time.Since(t0).Milliseconds(),
				Err:       err,
			}
		}(p, breaker, sem)
	}

	// Collect
	var ranked [][]Paper
	providerCounts := make(map[string]int)
	var warnings []ProviderWarning

	for range providers {
		r := <-results
		if r.Err != nil {
			warnings = append(warnings, ProviderWarning{
				Provider: r.Provider,
				Message:  r.Err.Error(),
			})
			continue
		}
		providerCounts[r.Provider] = len(r.Papers)
		if len(r.Papers) > 0 {
			ranked = append(ranked, r.Papers)
		}
	}

	// Fuse + deduplicate
	fused := RRFFuse(ranked, 60)
	deduped := Deduplicate(fused)
	if len(deduped) > limit {
		deduped = deduped[:limit]
	}

	if opts.QualitySort {
		ScoreQuality(deduped)
	}

	finalResult := SearchResult{
		Papers:    deduped,
		Providers: providerCounts,
		LatencyMs: time.Since(started).Milliseconds(),
		Warnings:  warnings,
	}

	// Async set cache
	if !opts.SkipCache {
		r := reg.redis
		go setCache(context.Background(), r, cacheKey, finalResult)
	}

	return finalResult
}

// ============================================================
// RRF Fusion — Reciprocal Rank Fusion (k=60)
// ============================================================

// RRFFuse merges multiple ranked lists using Reciprocal Rank Fusion.
// k is the RRF constant (default 60 as per the original paper).
func RRFFuse(lists [][]Paper, k int) []Paper {
	if k <= 0 {
		k = 60
	}

	scores := make(map[string]float64)
	papers := make(map[string]Paper)

	for _, list := range lists {
		for rank, paper := range list {
			key := paperKey(paper)
			scores[key] += 1.0 / float64(k+rank+1)
			if _, exists := papers[key]; !exists {
				if len(paper.SourceApis) == 0 && strings.TrimSpace(paper.Source) != "" {
					paper.SourceApis = []string{paper.Source}
				}
				papers[key] = paper
			} else {
				// Merge: preserve the best metadata across providers.
				existing := papers[key]
				if paper.CitationCount > existing.CitationCount {
					existing.CitationCount = paper.CitationCount
				}
				if strings.TrimSpace(existing.Abstract) == "" && strings.TrimSpace(paper.Abstract) != "" {
					existing.Abstract = paper.Abstract
				}
				if strings.TrimSpace(existing.Link) == "" && strings.TrimSpace(paper.Link) != "" {
					existing.Link = paper.Link
				}
				if strings.TrimSpace(existing.DOI) == "" && strings.TrimSpace(paper.DOI) != "" {
					existing.DOI = paper.DOI
				}
				if existing.Year == 0 && paper.Year > 0 {
					existing.Year = paper.Year
				}
				if strings.TrimSpace(existing.Venue) == "" && strings.TrimSpace(paper.Venue) != "" {
					existing.Venue = paper.Venue
				}
				existing.SourceApis = mergeProviderList(existing.SourceApis, paper.SourceApis, existing.Source, paper.Source)
				if len(existing.SourceApis) == 0 && strings.TrimSpace(existing.Source) != "" {
					existing.SourceApis = []string{existing.Source}
				}
				papers[key] = existing
			}
		}
	}

	type scoredPaper struct {
		paper Paper
		score float64
	}
	result := make([]scoredPaper, 0, len(papers))
	for key, paper := range papers {
		paper.Score = scores[key]
		result = append(result, scoredPaper{paper, scores[key]})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].score > result[j].score
	})

	out := make([]Paper, len(result))
	for i, sp := range result {
		out[i] = sp.paper
	}
	return out
}

// ============================================================
// Deduplication
// ============================================================

// Deduplicate removes near-duplicate papers by DOI and normalised title.
func Deduplicate(papers []Paper) []Paper {
	seen := make(map[string]int, len(papers))
	out := make([]Paper, 0, len(papers))
	for _, p := range papers {
		key := paperKey(p)
		index, exists := seen[key]
		if exists {
			out[index].SourceApis = mergeProviderList(out[index].SourceApis, p.SourceApis, out[index].Source, p.Source)
			continue
		}
		if len(p.SourceApis) == 0 && strings.TrimSpace(p.Source) != "" {
			p.SourceApis = []string{p.Source}
		}
		seen[key] = len(out)
		out = append(out, p)
	}
	return out
}

func mergeProviderList(existing []string, incoming []string, fallback ...string) []string {
	merged := make([]string, 0, len(existing)+len(incoming)+len(fallback))
	seen := map[string]struct{}{}
	appendUnique := func(values []string) {
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			merged = append(merged, value)
		}
	}
	appendUnique(existing)
	appendUnique(incoming)
	appendUnique(fallback)
	return merged
}

// paperKey returns a canonical deduplication key for a paper.
// Prefers DOI, falls back to normalised title.
func paperKey(p Paper) string {
	if p.DOI != "" {
		return "doi:" + strings.ToLower(strings.TrimSpace(p.DOI))
	}
	return "title:" + normaliseTitle(p.Title)
}

// normaliseTitle lowercases, trims punctuation and collapses whitespace.
func normaliseTitle(title string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(title) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == ' ' {
			sb.WriteRune(r)
		}
	}
	// Collapse whitespace
	fields := strings.Fields(sb.String())
	return strings.Join(fields, " ")
}

// ============================================================
// Quality Scoring — citation-aware relevance boost
// ============================================================

// ScoreQuality modifies Paper.Score in-place by blending RRF score
// with a log-damped citation signal. Papers are re-sorted descending.
func ScoreQuality(papers []Paper) {
	const citationWeight = 0.15
	const maxCitations = 10_000.0

	for i := range papers {
		// Infer evidence level if missing
		if papers[i].EvidenceLevel == "" {
			papers[i].EvidenceLevel = InferEvidenceLevel(papers[i])
		}

		cit := math.Min(float64(papers[i].CitationCount), maxCitations)
		citNorm := math.Log1p(cit) / math.Log1p(maxCitations) // 0–1
		papers[i].Score = papers[i].Score*(1-citationWeight) + citNorm*citationWeight
	}

	sort.Slice(papers, func(i, j int) bool {
		return papers[i].Score > papers[j].Score
	})
}

// InferEvidenceLevel uses keyword matching to estimate the level of evidence.
func InferEvidenceLevel(p Paper) string {
	text := strings.ToLower(p.Title + " " + p.Abstract)

	// Tier 1: Secondary/Synthesized Evidence
	if containsAny(text, "systematic review", "meta-analysis", "meta analysis", "cochrane") {
		return "systematic-review"
	}
	if strings.Contains(text, "review") || strings.Contains(text, "survey") {
		return "review"
	}

	// Tier 2: Primary Experimental
	if containsAny(text, "randomized controlled trial", "randomized trial", " rct ", "clinical trial") {
		return "rct"
	}
	if strings.Contains(text, "cohort study") || strings.Contains(text, "longitudinal") {
		return "cohort"
	}
	if strings.Contains(text, "case-control") || strings.Contains(text, "case control") {
		return "case-control"
	}

	// Tier 3: Observational/Descriptive
	if strings.Contains(text, "case report") || strings.Contains(text, "case series") {
		return "case-report"
	}
	if strings.Contains(text, "cross-sectional") || strings.Contains(text, "cross sectional") {
		return "cross-sectional"
	}

	// Specific Source Indicators
	if strings.Contains(strings.ToLower(p.Source), "arxiv") ||
		strings.Contains(strings.ToLower(p.Source), "biorxiv") ||
		strings.Contains(strings.ToLower(p.Source), "medrxiv") {
		return "preprint"
	}

	return "unknown"
}

func containsAny(s string, keywords ...string) bool {
	for _, k := range keywords {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// ============================================================
// Domain router — maps query domain to provider priority order
// ============================================================

// DomainRoutes is the canonical domain → provider priority mapping.
// Providers earlier in the list are preferred.
var DomainRoutes = map[string][]string{
	"medicine":              {"pubmed", "europe_pmc", "medrxiv", "semantic_scholar", "openalex", "biorxiv", "clinical_trials", "doaj"},
	"biomedical":            {"pubmed", "semantic_scholar", "europe_pmc", "biorxiv", "medrxiv"},
	"cs":                    {"dblp", "arxiv", "semantic_scholar", "papers_with_code", "openalex"},
	"ml":                    {"arxiv", "semantic_scholar", "papers_with_code", "openalex"},
	"social":                {"openalex", "semantic_scholar", "core", "crossref", "ssrn", "doaj"},
	"climate":               {"openalex", "semantic_scholar", "core", "crossref"},
	"physics":               {"arxiv", "nasa_ads", "semantic_scholar", "openalex", "core"},
	"biology":               {"pubmed", "biorxiv", "medrxiv", "europe_pmc", "semantic_scholar", "openalex", "doaj"},
	"neuro":                 {"biorxiv", "medrxiv", "europe_pmc", "semantic_scholar", "openalex", "pubmed"},
	"humanities":            {"openalex", "core", "crossref", "semantic_scholar", "doaj"},
	"mathematics":           {"arxiv", "openalex", "semantic_scholar", "repec"},
	"math":                  {"arxiv", "semantic_scholar", "openalex", "repec"},
	"chemistry":             {"openalex", "semantic_scholar", "europe_pmc", "pubmed"},
	"economics":             {"repec", "ssrn", "semantic_scholar", "openalex", "core", "crossref"},
	"law":                   {"openalex", "semantic_scholar", "crossref", "ssrn"},
	"education":             {"openalex", "semantic_scholar", "core"},
	"environmental_science": {"openalex", "semantic_scholar", "core", "crossref", "biorxiv"},
	"materials_science":     {"openalex", "semantic_scholar", "arxiv", "crossref"},
	"agriculture":           {"openalex", "semantic_scholar", "europe_pmc", "pubmed", "biorxiv"},
	"linguistics":           {"openalex", "semantic_scholar", "arxiv"},
	"philosophy":            {"philpapers", "semantic_scholar", "openalex", "doaj"},
	"engineering":           {"ieee", "semantic_scholar", "openalex", "arxiv", "crossref"},
	"astronomy":             {"nasa_ads", "arxiv", "semantic_scholar", "openalex"},
	"general":               {"semantic_scholar", "openalex", "core", "crossref", "arxiv", "doaj"},
}

// DefaultProviderOrder is used when no domain is specified or recognised.
var DefaultProviderOrder = []string{
	"semantic_scholar",
	"openalex",
	"arxiv",
	"pubmed",
	"core",
	"google_scholar",
}

// ApplyDomainRoutes configures a registry with the canonical routing table.
func ApplyDomainRoutes(reg *ProviderRegistry) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	for domain, names := range DomainRoutes {
		// Only add routes for providers that are actually registered
		var valid []string
		for _, name := range names {
			if _, ok := reg.providers[name]; ok {
				valid = append(valid, name)
			}
		}
		reg.routes[domain] = valid
	}
	// Set defaults from registered subset of DefaultProviderOrder
	var defaults []string
	for _, name := range DefaultProviderOrder {
		if _, ok := reg.providers[name]; ok {
			defaults = append(defaults, name)
		}
	}
	// Add any registered providers not in the default list
	for name := range reg.providers {
		found := false
		for _, d := range defaults {
			if d == name {
				found = true
				break
			}
		}
		if !found {
			defaults = append(defaults, name)
		}
	}
	reg.defaults = defaults
}

// ============================================================
// BaseProvider — embed in every provider for Healthy() tracking
// ============================================================

// BaseProvider provides a simple exponential-backoff health tracker.
// Embed this in concrete providers to get Healthy() for free.
type BaseProvider struct {
	mu           sync.Mutex
	failCount    int
	backoffUntil time.Time
}

// RecordSuccess resets the failure counter.
func (b *BaseProvider) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failCount = 0
	b.backoffUntil = time.Time{}
}

// RecordFailure increments the failure counter and applies exponential backoff.
func (b *BaseProvider) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failCount++
	backoff := time.Duration(math.Min(float64(b.failCount*b.failCount), 60)) * time.Second
	b.backoffUntil = time.Now().Add(backoff)
}

// Healthy returns true if the provider is not in backoff.
func (b *BaseProvider) Healthy() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return time.Now().After(b.backoffUntil)
}

func (b *BaseProvider) Tools() []string {
	return nil
}

// ============================================================
// Error helpers
// ============================================================

func providerError(provider, msg string, args ...any) error {
	return fmt.Errorf("%s: %s", provider, fmt.Sprintf(msg, args...))
}
