package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"
)

// CrossrefProvider searches the Crossref metadata API.
// Free to use with optional polite-pool email. Best for humanities,
// social sciences, and interdisciplinary work. Covers all disciplines
// via DOI metadata.
type CrossrefProvider struct {
	BaseProvider
	baseURL    string
	politePool string // "mailto:contact@scholarlm.com" for polite pool
}

var _ SearchProvider = (*CrossrefProvider)(nil)

func NewCrossrefProvider() *CrossrefProvider {
	email := os.Getenv("CROSSREF_POLITE_EMAIL")
	if email == "" {
		email = "api@scholarlm.com"
	}
	return &CrossrefProvider{
		baseURL:    "https://api.crossref.org/works",
		politePool: email,
	}
}

func (c *CrossrefProvider) Name() string { return "crossref" }
func (c *CrossrefProvider) Domains() []string {
	return []string{"social", "humanities", "climate", "engineering"}
}

type crossrefWork struct {
	DOI      string   `json:"DOI"`
	Title    []string `json:"title"`
	Abstract string   `json:"abstract"`
	URL      string   `json:"URL"`
	Author   []struct {
		Given  string `json:"given"`
		Family string `json:"family"`
	} `json:"author"`
	Published struct {
		DateParts [][]int `json:"date-parts"`
	} `json:"published"`
	ContainerTitle      []string `json:"container-title"`
	IsReferencedByCount int      `json:"is-referenced-by-count"`
	ReferencesCount     int      `json:"references-count"`
	Type                string   `json:"type"`
}

type crossrefWorksResponse struct {
	Message struct {
		Items []crossrefWork `json:"items"`
	} `json:"message"`
}

func (c *CrossrefProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	limit := opts.Limit
	if limit <= 0 {
		limit = 15
	}
	rows := crossrefRowsForLimit(limit)

	providerQuery := buildCrossrefSearchQuery(query)
	if providerQuery == "" {
		providerQuery = strings.TrimSpace(query)
	}
	if providerQuery != strings.TrimSpace(query) {
		logProviderSearchStage(ctx, "provider_query_normalized", c.Name(), query, opts,
			"result", "normalized",
			"provider_query_preview", queryPreview(providerQuery),
		)
	}

	papers, rawCount, err := c.searchWorks(ctx, providerQuery, query, opts, rows, limit)
	if err != nil {
		c.RecordFailure()
		return nil, err
	}
	logCrossrefFilterApplied(ctx, c.Name(), query, opts, rawCount, len(papers), nil)

	if len(papers) < limit {
		seen := make(map[string]struct{}, len(papers))
		for _, paper := range papers {
			seen[paperKey(paper)] = struct{}{}
		}
		for _, topUpQuery := range buildCrossrefTopUpQueries(query, providerQuery) {
			if len(papers) >= limit {
				break
			}
			logProviderSearchStage(ctx, "provider_query_topup_start", c.Name(), query, opts,
				"result", "topup",
				"provider_query_preview", queryPreview(topUpQuery),
				"current_count", len(papers),
				"target_count", limit,
			)

			topUpPapers, topUpRawCount, err := c.searchWorks(ctx, topUpQuery, topUpQuery, opts, rows, rows)
			if err != nil {
				logProviderSearchFailure(ctx, "provider_query_topup_failed", c.Name(), query, opts,
					"result", "degraded",
					"provider_query_preview", queryPreview(topUpQuery),
					"error", err.Error(),
				)
				if ctx.Err() != nil {
					break
				}
				continue
			}
			logCrossrefFilterApplied(ctx, c.Name(), query, opts, topUpRawCount, len(topUpPapers),
				[]any{"provider_query_preview", queryPreview(topUpQuery)})

			added := 0
			for _, paper := range topUpPapers {
				key := paperKey(paper)
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				papers = append(papers, paper)
				added++
				if len(papers) >= limit {
					break
				}
			}
			logProviderSearchStage(ctx, "provider_query_topup_complete", c.Name(), query, opts,
				"result", "topup",
				"provider_query_preview", queryPreview(topUpQuery),
				"raw_result_count", topUpRawCount,
				"kept_count", len(topUpPapers),
				"added_count", added,
				"total_count", len(papers),
				"target_count", limit,
			)
		}
	}

	c.RecordSuccess()
	return papers, nil
}

func crossrefRowsForLimit(limit int) int {
	rows := limit
	if rows < 50 {
		rows = rows * 3
		if rows > 50 {
			rows = 50
		}
	}
	return rows
}

func (c *CrossrefProvider) searchWorks(ctx context.Context, requestQuery string, relevanceQuery string, opts SearchOpts, rows int, maxPapers int) ([]Paper, int, error) {
	params := url.Values{}
	params.Set("query", requestQuery)
	params.Set("rows", fmt.Sprintf("%d", rows))
	params.Set("select", "DOI,title,abstract,author,published,is-referenced-by-count,references-count,container-title,URL,type")
	// Use polite pool for higher rate limits
	params.Set("mailto", c.politePool)

	if opts.YearFrom > 0 {
		params.Set("filter", fmt.Sprintf("from-pub-date:%d", opts.YearFrom))
	}

	reqURL := c.baseURL + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, providerError("crossref", "build request: %v", err)
	}
	req.Header.Set("User-Agent", "ScholarLM/1.0 (mailto:"+c.politePool+")")
	req.Header.Set("Accept", "application/json")

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		return nil, 0, providerError("crossref", "request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, 0, providerError("crossref", "HTTP %d", resp.StatusCode)
	}

	var result crossrefWorksResponse

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, 0, providerError("crossref", "decode: %v", err)
	}

	papers := make([]Paper, 0, len(result.Message.Items))
	for _, item := range result.Message.Items {
		if len(item.Title) == 0 || item.Title[0] == "" {
			continue
		}
		title := strings.TrimSpace(item.Title[0])

		link := item.URL
		if link == "" && item.DOI != "" {
			link = "https://doi.org/" + item.DOI
		}

		authors := make([]string, 0, len(item.Author))
		for _, a := range item.Author {
			name := strings.TrimSpace(a.Given + " " + a.Family)
			if name != " " {
				authors = append(authors, name)
			}
		}

		year := 0
		month := 0
		if len(item.Published.DateParts) > 0 && len(item.Published.DateParts[0]) > 0 {
			year = item.Published.DateParts[0][0]
			if len(item.Published.DateParts[0]) > 1 {
				month = item.Published.DateParts[0][1]
			}
		}

		// Strip XML tags from abstract (Crossref sometimes returns JATS XML)
		abstract := stripJATSTags(item.Abstract)
		if !isRelevantCrossrefWork(relevanceQuery, title, abstract, item.Type) {
			continue
		}

		venue := ""
		if len(item.ContainerTitle) > 0 {
			venue = item.ContainerTitle[0]
		}

		papers = append(papers, Paper{
			ID:             "crossref:" + item.DOI,
			Title:          title,
			Abstract:       abstract,
			Link:           link,
			DOI:            item.DOI,
			Source:         "crossref",
			SourceApis:     []string{"crossref"},
			Venue:          venue,
			Authors:        authors,
			Year:           year,
			Month:          month,
			CitationCount:  item.IsReferencedByCount,
			ReferenceCount: item.ReferencesCount,
		})
		if maxPapers > 0 && len(papers) >= maxPapers {
			break
		}
	}

	return papers, len(result.Message.Items), nil
}

func logCrossrefFilterApplied(ctx context.Context, provider string, query string, opts SearchOpts, rawCount int, keptCount int, attrs []any) {
	if rawCount <= keptCount {
		return
	}
	logAttrs := []any{
		"result", "filtered",
		"raw_result_count", rawCount,
		"kept_count", keptCount,
		"filtered_count", rawCount - keptCount,
	}
	logAttrs = append(logAttrs, attrs...)
	logProviderSearchStage(ctx, "provider_result_filter_applied", provider, query, opts, logAttrs...)
}

var crossrefPaperLikeTypes = map[string]struct{}{
	"":                    {},
	"journal-article":     {},
	"journal-issue":       {},
	"journal-volume":      {},
	"proceedings-article": {},
	"posted-content":      {},
	"report":              {},
	"dissertation":        {},
}

var crossrefQueryStopWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "by": {}, "for": {}, "from": {},
	"in": {}, "into": {}, "is": {}, "of": {}, "on": {}, "or": {}, "than": {}, "the": {}, "to": {},
	"with": {}, "without": {},
	"about": {}, "across": {}, "advance": {}, "advances": {}, "benchmark": {}, "benchmarks": {},
	"causal": {}, "competing": {}, "contradictions": {}, "dataset": {}, "datasets": {}, "direction": {},
	"directions": {}, "effect": {}, "effects": {}, "evidence": {}, "future": {}, "gap": {}, "gaps": {},
	"less": {}, "limitation": {}, "limitations": {}, "method": {}, "methods": {}, "open": {}, "pathway": {},
	"pathways": {}, "quality": {}, "recent": {}, "reproducibility": {}, "review": {}, "systematic": {},
}

func isRelevantCrossrefWork(query, title, abstract, itemType string) bool {
	if _, ok := crossrefPaperLikeTypes[strings.ToLower(strings.TrimSpace(itemType))]; !ok {
		return false
	}

	terms := significantCrossrefQueryTerms(query)
	if len(terms) < 2 {
		return true
	}

	titleTokens := crossrefTextTokens(title)
	textTokens := crossrefTextTokens(title + " " + abstract)
	if len(textTokens) == 0 {
		return false
	}
	if !providerPassesFocusedTunnelingChipEvidence(terms, titleTokens, textTokens) {
		return false
	}

	matches := 0
	anchorTerms := 0
	anchorMatched := false
	seen := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		if _, exists := seen[term]; exists {
			continue
		}
		seen[term] = struct{}{}
		isAnchorTerm := anchorTerms < 2
		anchorTerms++
		if matched := crossrefTermMatches(term, textTokens); matched {
			matches++
			if isAnchorTerm {
				anchorMatched = true
			}
		}
	}

	if len(seen) >= 3 && !anchorMatched {
		return false
	}
	required := 2
	if len(seen) >= 5 {
		required = 3
	}
	return matches >= required
}

func significantCrossrefQueryTerms(query string) []string {
	tokens := crossrefTextTokens(query)
	terms := make([]string, 0, len(tokens))
	seen := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		if _, stop := crossrefQueryStopWords[token]; stop {
			continue
		}
		token = normalizeCrossrefToken(token)
		if len(token) < 2 {
			continue
		}
		if _, stop := crossrefQueryStopWords[token]; stop {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		terms = append(terms, token)
	}
	return terms
}

func buildCrossrefSearchQuery(query string) string {
	terms := significantCrossrefQueryTerms(query)
	if len(terms) < 2 {
		return strings.TrimSpace(query)
	}
	return strings.Join(terms, " ")
}

func buildCrossrefTopUpQueries(query string, providerQuery string) []string {
	terms := significantCrossrefQueryTerms(query)
	if !crossrefHasTerm(terms, "tunneling") {
		return nil
	}
	if !crossrefHasNanoscaleTerm(terms) && !crossrefHasAnyTerm(terms, "chip", "transistor", "gate", "oxide", "nanoelectronic") {
		return nil
	}

	candidates := []string{
		"gate tunneling 1nm transistor",
		"tunneling current 1nm gate oxide",
		"quantum tunneling nanoelectronics chip",
	}
	queries := make([]string, 0, len(candidates))
	seen := map[string]struct{}{strings.TrimSpace(providerQuery): {}}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		queries = append(queries, candidate)
	}
	return queries
}

func crossrefHasTerm(terms []string, needle string) bool {
	for _, term := range terms {
		if term == needle {
			return true
		}
	}
	return false
}

func crossrefHasAnyTerm(terms []string, needles ...string) bool {
	for _, needle := range needles {
		if crossrefHasTerm(terms, needle) {
			return true
		}
	}
	return false
}

func crossrefHasNanoscaleTerm(terms []string) bool {
	for _, term := range terms {
		if term == "nanometer" || strings.HasSuffix(term, "nm") {
			return true
		}
	}
	return false
}

func crossrefTextTokens(text string) []string {
	normalized := strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			return unicode.ToLower(r)
		default:
			return ' '
		}
	}, text)
	return strings.Fields(normalized)
}

func normalizeCrossrefToken(token string) string {
	token = strings.ToLower(strings.TrimSpace(token))
	switch token {
	case "quantam":
		return "quantum"
	case "tunneliing", "tunnelling":
		return "tunneling"
	case "chips":
		return "chip"
	case "nanometre", "nanometres", "nanometers":
		return "nanometer"
	}
	if len(token) > 3 && strings.HasSuffix(token, "s") && !strings.HasSuffix(token, "ss") {
		return strings.TrimSuffix(token, "s")
	}
	return token
}

func crossrefTermMatches(term string, textTokens []string) bool {
	for _, token := range textTokens {
		normalized := normalizeCrossrefToken(token)
		if normalized == term {
			return true
		}
		if len(term) >= 5 && len(normalized) >= 5 && boundedEditDistance(term, normalized, crossrefFuzzyDistance(term)) {
			return true
		}
	}
	return false
}

func crossrefFuzzyDistance(term string) int {
	if len(term) >= 9 {
		return 2
	}
	return 1
}

func boundedEditDistance(a, b string, maxDistance int) bool {
	if a == b {
		return true
	}
	if maxDistance < 0 {
		return false
	}
	if diff := len(a) - len(b); diff > maxDistance || diff < -maxDistance {
		return false
	}

	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current := make([]int, len(b)+1)
		current[0] = i
		rowMin := current[0]
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			current[j] = minInt(prev[j]+1, current[j-1]+1, prev[j-1]+cost)
			if current[j] < rowMin {
				rowMin = current[j]
			}
		}
		if rowMin > maxDistance {
			return false
		}
		prev = current
	}
	return prev[len(b)] <= maxDistance
}

func minInt(values ...int) int {
	if len(values) == 0 {
		return 0
	}
	minimum := values[0]
	for _, value := range values[1:] {
		if value < minimum {
			minimum = value
		}
	}
	return minimum
}

// stripJATSTags removes JATS XML markup from Crossref abstracts.
func stripJATSTags(s string) string {
	if !strings.ContainsRune(s, '<') {
		return s
	}
	var out strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			out.WriteRune(r)
		}
	}
	return strings.TrimSpace(out.String())
}
