package wisdev

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ==========================================
// TYPES
// ==========================================

// WebSearchResultItem loosely maps to the TS `WebSearchResultItem` structure
type WebSearchResultItem struct {
	Title       string         `json:"title,omitempty"`
	Link        string         `json:"link,omitempty"`
	Snippet     string         `json:"snippet,omitempty"`
	DisplayLink string         `json:"displayLink,omitempty"`
	Pagemap     map[string]any `json:"pagemap,omitempty"`
}

type WebSearchFilterTelemetry struct {
	FilteredCount int            `json:"filteredCount"`
	KeptCount     int            `json:"keptCount"`
	FilterReasons map[string]int `json:"filterReasons"`
	DomainMix     map[string]int `json:"domainMix"`
}

type NormalizedWebSearchPolicy struct {
	AllowedDomains []string `json:"allowedDomains"`
	BlockedDomains []string `json:"blockedDomains"`
	FreshnessDays  int      `json:"freshnessDays"`
	MinSignalScore float64  `json:"minSignalScore"`
	MaxResults     int      `json:"maxResults"`
	Intent         string   `json:"intent"` // "news", "academic", "implementation", "policy", "general"
}

type SearchPolicyHints struct {
	Intent         string   `json:"intent,omitempty"`
	AllowedDomains []string `json:"allowedDomains,omitempty"`
	BlockedDomains []string `json:"blockedDomains,omitempty"`
	FreshnessDays  int      `json:"freshnessDays,omitempty"`
	MinSignalScore float64  `json:"minSignalScore,omitempty"`
	MaxResults     int      `json:"maxResults,omitempty"`
}

type CapabilityExecuteContext struct {
	DomainHint string `json:"domainHint,omitempty"`
}

// ==========================================
// RULES
// ==========================================

var DefaultAllowedByIntent = map[string][]string{
	"news":           {"reuters.com", "apnews.com", "nature.com", "science.org"},
	"academic":       {"arxiv.org", "biorxiv.org", "medrxiv.org", "semanticscholar.org", "openalex.org", "pubmed.ncbi.nlm.nih.gov"},
	"implementation": {"github.com", "huggingface.co", "paperswithcode.com", "readthedocs.io"},
	"policy":         {"who.int", "nih.gov", "cdc.gov", "oecd.org"},
	"general":        {},
}

var DefaultBlockedDomains = []string{"pinterest.com", "facebook.com"}

const DayMS = 24 * 60 * 60 * 1000

// Compiled regex patterns for fast evaluation
var (
	rxNews     = regexp.MustCompile(`(?i)\b(latest|recent|today|breaking|current)\b`)
	rxImpl     = regexp.MustCompile(`(?i)\b(github|implementation|code|repo|library|package)\b`)
	rxPolicy   = regexp.MustCompile(`(?i)\b(policy|guideline|regulation|framework)\b`)
	rxAcademic = regexp.MustCompile(`(?i)\b(arxiv|pubmed|paper|systematic review|meta-analysis|clinical)\b`)

	rxYearMatch = regexp.MustCompile(`\b(19|20)\d{2}\b`)
	rxIsoLike   = regexp.MustCompile(`\b(20\d{2}-\d{2}-\d{2})\b`)
	rxMonthLike = regexp.MustCompile(`(?i)\b(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\s+\d{1,2},?\s+(20\d{2})\b`)
	rxYearOnly  = regexp.MustCompile(`\b(19|20)\d{2}\b`)

	rxSpaceSplit = regexp.MustCompile(`\s+`)
)

// ==========================================
// HELPER FUNCTIONS
// ==========================================

func normalizeDomain(val string) string {
	val = strings.ToLower(strings.TrimSpace(val))
	return strings.TrimPrefix(val, "*.")
}

func extractHost(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	return strings.TrimPrefix(host, "www.")
}

func parseTimestamp(raw any) *int64 {
	if raw == nil {
		return nil
	}
	strVal := strings.TrimSpace(fmt.Sprintf("%v", raw))
	if strVal == "" {
		return nil
	}

	// Attempt RFC3339 parsing
	if t, err := time.Parse(time.RFC3339, strVal); err == nil {
		ms := t.UnixMilli()
		return &ms
	}
	// Attempt layout matching common Date string formats
	if t, err := time.Parse("2006-01-02", strVal); err == nil {
		ms := t.UnixMilli()
		return &ms
	}

	match := rxYearMatch.FindString(strVal)
	if match != "" {
		if year, err := strconv.Atoi(match); err == nil {
			ms := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
			return &ms
		}
	}
	return nil
}

func extractPublishedAtMs(item WebSearchResultItem) *int64 {
	var candidates []any

	if metaRaw, ok := item.Pagemap["metatags"]; ok {
		if metaList, ok := metaRaw.([]any); ok {
			for _, m := range metaList {
				if rec, ok := m.(map[string]any); ok {
					keys := []string{
						"article:published_time",
						"article:modified_time",
						"og:updated_time",
						"date",
						"dc.date",
						"citation_publication_date",
						"citation_date",
					}
					for _, k := range keys {
						if v, ok := rec[k]; ok {
							candidates = append(candidates, v)
						}
					}
				}
			}
		}
	}

	textBlob := fmt.Sprintf("%s %s", item.Title, item.Snippet)

	if iso := rxIsoLike.FindStringSubmatch(textBlob); len(iso) > 1 {
		candidates = append(candidates, iso[1])
	}
	if mo := rxMonthLike.FindString(textBlob); mo != "" {
		candidates = append(candidates, mo)
	}
	if yrs := rxYearOnly.FindAllString(textBlob, -1); len(yrs) > 0 {
		candidates = append(candidates, yrs[len(yrs)-1]) // take the last one
	}

	for _, cand := range candidates {
		if ts := parseTimestamp(cand); ts != nil {
			return ts
		}
	}
	return nil
}

func inferIntent(query string, requestedIntent string) string {
	if requestedIntent != "" {
		return requestedIntent
	}
	if rxNews.MatchString(query) {
		return "news"
	}
	if rxImpl.MatchString(query) {
		return "implementation"
	}
	if rxPolicy.MatchString(query) {
		return "policy"
	}
	if rxAcademic.MatchString(query) {
		return "academic"
	}
	return "general"
}

// ==========================================
// CORE EXPORTS
// ==========================================

func DeriveSearchPolicyHints(
	query string,
	provided *SearchPolicyHints,
	context *CapabilityExecuteContext,
) NormalizedWebSearchPolicy {

	if provided == nil {
		provided = &SearchPolicyHints{}
	}

	intent := inferIntent(query, provided.Intent)

	var allowedDomains []string
	if len(provided.AllowedDomains) > 0 {
		for _, d := range provided.AllowedDomains {
			if norm := normalizeDomain(d); norm != "" {
				allowedDomains = append(allowedDomains, norm)
			}
		}
	} else {
		allowedDomains = append([]string{}, DefaultAllowedByIntent[intent]...)
	}

	var blockedDomains []string
	if len(provided.BlockedDomains) > 0 {
		for _, d := range provided.BlockedDomains {
			if norm := normalizeDomain(d); norm != "" {
				blockedDomains = append(blockedDomains, norm)
			}
		}
	} else {
		blockedDomains = append([]string{}, DefaultBlockedDomains...)
	}

	// Dynamic domain hint overrides
	if context != nil && strings.Contains(strings.ToLower(context.DomainHint), "med") {
		hasPubMed := false
		for _, d := range allowedDomains {
			if d == "pubmed.ncbi.nlm.nih.gov" {
				hasPubMed = true
				break
			}
		}
		if !hasPubMed {
			allowedDomains = append(allowedDomains, "pubmed.ncbi.nlm.nih.gov")
		}
	}

	maxResults := provided.MaxResults
	if maxResults == 0 {
		maxResults = 8
	} else if maxResults < 1 {
		maxResults = 1
	} else if maxResults > 10 {
		maxResults = 10
	}

	freshnessDays := provided.FreshnessDays
	if freshnessDays == 0 {
		freshnessDays = 30
	} else if freshnessDays < 1 {
		freshnessDays = 1
	} else if freshnessDays > 3650 {
		freshnessDays = 3650
	}

	minSignalScore := provided.MinSignalScore
	if minSignalScore == 0 {
		minSignalScore = 1.2
	} else if minSignalScore < 0 {
		minSignalScore = 0
	} else if minSignalScore > 5 {
		minSignalScore = 5
	}

	return NormalizedWebSearchPolicy{
		AllowedDomains: allowedDomains, // Unique/Set conversion omitted for brevity
		BlockedDomains: blockedDomains,
		FreshnessDays:  freshnessDays,
		MinSignalScore: minSignalScore,
		MaxResults:     maxResults,
		Intent:         intent,
	}
}

func domainMatchesAllowlist(host string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return true // Empty allowlist means implicitly all allowed
	}
	for _, item := range allowlist {
		if host == item || strings.HasSuffix(host, "."+item) {
			return true
		}
	}
	return false
}

func domainMatchesBlocklist(host string, blocklist []string) bool {
	for _, item := range blocklist {
		if host == item || strings.HasSuffix(host, "."+item) {
			return true
		}
	}
	return false
}

func scoreResult(query string, item WebSearchResultItem, host string, intent string) float64 {
	blob := strings.ToLower(fmt.Sprintf("%s %s", item.Title, item.Snippet))

	tokensRaw := rxSpaceSplit.Split(strings.ToLower(query), -1)
	var queryTokens []string
	for _, t := range tokensRaw {
		if len(t) > 2 {
			queryTokens = append(queryTokens, t)
		}
	}

	tokenHits := 0
	for _, token := range queryTokens {
		if strings.Contains(blob, token) {
			tokenHits++
		}
	}

	tokenCoverage := 0.0
	if len(queryTokens) > 0 {
		tokenCoverage = float64(tokenHits) / float64(len(queryTokens))
	}

	trustBoost := 0.0
	academicRx := regexp.MustCompile(`(?i)\b(arxiv\.org|nature\.com|science\.org|nih\.gov|pubmed)\b`)
	if academicRx.MatchString(host) {
		trustBoost += 0.5
	}

	implRx := regexp.MustCompile(`(?i)\b(github\.com|huggingface\.co|paperswithcode\.com)\b`)
	if intent == "implementation" && implRx.MatchString(host) {
		trustBoost += 0.5
	}

	return tokenCoverage*3.0 + trustBoost
}

type RankedCandidate struct {
	Item  WebSearchResultItem
	Host  string
	Score float64
}

// FilterAndRankWebSearchResults applies strict domain compliance, fast regex scoring, and age staleness filtering
func FilterAndRankWebSearchResults(
	query string,
	results []WebSearchResultItem,
	policy NormalizedWebSearchPolicy,
) ([]WebSearchResultItem, WebSearchFilterTelemetry) {

	filterReasons := make(map[string]int)
	domainMix := make(map[string]int)
	seenHosts := make(map[string]bool)
	var candidates []RankedCandidate

	now := time.Now().UnixMilli()

	for _, item := range results {
		host := extractHost(item.Link)
		if host == "" {
			filterReasons["invalid_url"]++
			continue
		}
		if domainMatchesBlocklist(host, policy.BlockedDomains) {
			filterReasons["blocked_domain"]++
			continue
		}
		if !domainMatchesAllowlist(host, policy.AllowedDomains) {
			filterReasons["not_allowlisted"]++
			continue
		}
		if seenHosts[host] {
			filterReasons["duplicate_host"]++
			continue
		}

		if publishedAtMs := extractPublishedAtMs(item); publishedAtMs != nil {
			ageDays := float64(now-*publishedAtMs) / DayMS
			if ageDays < 0 {
				ageDays = 0
			}
			if ageDays > float64(policy.FreshnessDays) {
				filterReasons["stale_content"]++
				continue
			}
		}

		// Calculate text heuristic score
		score := scoreResult(query, item, host, policy.Intent)
		if score < policy.MinSignalScore {
			filterReasons["low_signal"]++
			continue
		}

		seenHosts[host] = true
		domainMix[host]++
		candidates = append(candidates, RankedCandidate{Item: item, Host: host, Score: score})
	}

	// Sort high scores to the top
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	max := policy.MaxResults
	if len(candidates) < max {
		max = len(candidates)
	}

	var ranked []WebSearchResultItem
	for i := 0; i < max; i++ {
		ranked = append(ranked, candidates[i].Item)
	}

	filteredCount := len(results) - len(ranked)
	if filteredCount < 0 {
		filteredCount = 0
	}

	return ranked, WebSearchFilterTelemetry{
		FilteredCount: filteredCount,
		KeptCount:     len(ranked),
		FilterReasons: filterReasons,
		DomainMix:     domainMix,
	}
}
