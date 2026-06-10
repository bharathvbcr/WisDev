package wisdev

import (
	"context"
	"sort"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

var genericTopicTerms = map[string]struct{}{
	"clinical": {}, "engineering": {}, "evidence": {}, "material": {}, "materials": {},
	"repair": {}, "scaffold": {}, "scaffolds": {}, "study": {}, "studies": {},
	"tissue": {}, "trial": {}, "trials": {},
}

var queryRelevanceStopwords = map[string]struct{}{
	"about": {}, "after": {}, "also": {}, "among": {}, "been": {}, "being": {},
	"between": {}, "both": {}, "could": {}, "current": {}, "does": {}, "each": {},
	"evidence": {}, "from": {}, "have": {}, "into": {}, "latest": {}, "more": {},
	"most": {}, "near": {}, "new": {}, "other": {}, "over": {}, "recent": {},
	"research": {}, "review": {}, "should": {}, "some": {}, "such": {}, "than": {},
	"that": {}, "their": {}, "there": {}, "these": {}, "they": {}, "this": {},
	"those": {}, "through": {}, "under": {}, "using": {}, "very": {}, "what": {},
	"when": {}, "where": {}, "which": {}, "while": {}, "with": {}, "within": {},
	"without": {}, "would": {},
}

func queryAnchorTerms(query string) []string {
	tokens := loopEvidenceTokens(query)
	anchors := make([]string, 0, len(tokens))
	seen := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		if _, stop := queryRelevanceStopwords[token]; stop {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		anchors = append(anchors, token)
	}
	sort.Slice(anchors, func(i, j int) bool {
		if len(anchors[i]) == len(anchors[j]) {
			return anchors[i] < anchors[j]
		}
		return len(anchors[i]) > len(anchors[j])
	})
	return anchors
}

func paperQueryRelevanceScore(query string, paper search.Paper) float64 {
	anchors := queryAnchorTerms(query)
	if len(anchors) == 0 {
		return 1
	}
	title := strings.ToLower(strings.TrimSpace(paper.Title))
	abstract := strings.ToLower(strings.TrimSpace(paper.Abstract))
	body := title + " " + abstract
	if strings.TrimSpace(body) == "" {
		return 0
	}

	matched := 0
	weighted := 0.0
	for _, anchor := range anchors {
		if bodyContainsAnchor(body, anchor) {
			matched++
			weight := 1.0
			if bodyContainsAnchor(title, anchor) {
				weight = 2.5
			}
			weighted += weight
		}
	}
	if matched == 0 {
		return 0
	}

	denominator := float64(len(anchors)) * 2.5
	score := weighted / denominator
	if matched == len(anchors) {
		score += 0.15
	}
	if score > 1 {
		score = 1
	}
	return score
}

func splitQueryAnchors(anchors []string) (specific []string, generic []string) {
	for _, anchor := range anchors {
		if _, ok := genericTopicTerms[anchor]; ok {
			generic = append(generic, anchor)
			continue
		}
		specific = append(specific, anchor)
	}
	return specific, generic
}

func queryTopicClauses(query string) []string {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(query)), " and ")
	if len(parts) < 2 {
		return nil
	}
	clauses := make([]string, 0, len(parts))
	for _, part := range parts {
		if clause := strings.TrimSpace(part); clause != "" {
			clauses = append(clauses, clause)
		}
	}
	if len(clauses) < 2 {
		return nil
	}
	return clauses
}

func paperMatchesQueryRelevance(query string, paper search.Paper) bool {
	query = applyResearchQueryCorrections(normalizeResearchQueryText(query))
	if clauses := queryTopicClauses(query); len(clauses) >= 2 {
		for _, clause := range clauses {
			if paperMatchesSingleTopicRelevance(clause, paper) {
				return true
			}
		}
		return false
	}
	return paperMatchesSingleTopicRelevance(query, paper)
}

func paperMatchesSingleTopicRelevance(query string, paper search.Paper) bool {
	anchors := queryAnchorTerms(query)
	if len(anchors) == 0 {
		return true
	}
	body := strings.ToLower(strings.TrimSpace(paper.Title + " " + paper.Abstract))
	if body == "" {
		return false
	}
	specific, generic := splitQueryAnchors(anchors)
	if len(specific) == 0 {
		specific = append(specific, anchors...)
		generic = nil
	}
	for _, anchor := range specific {
		if !bodyContainsAnchor(body, anchor) {
			return false
		}
	}
	if len(generic) > 0 {
		matchedGeneric := false
		for _, anchor := range generic {
			if bodyContainsAnchor(body, anchor) {
				matchedGeneric = true
				break
			}
		}
		if !matchedGeneric {
			return false
		}
	}
	return paperQueryRelevanceScore(query, paper) >= 0.35
}

func shouldSkipQueryRelevanceFilter(papers []search.Paper) bool {
	rich := 0
	for _, paper := range papers {
		if len(strings.TrimSpace(paper.Title+" "+paper.Abstract)) >= 24 {
			rich++
		}
	}
	return rich == 0
}

func filterPapersByQueryRelevance(query string, papers []search.Paper) []search.Paper {
	if len(papers) == 0 {
		return nil
	}
	if shouldSkipQueryRelevanceFilter(papers) {
		return search.SortPapersByPreferenceWithQuery(papers, query)
	}
	filtered := make([]search.Paper, 0, len(papers))
	for _, paper := range papers {
		if paperMatchesQueryRelevance(query, paper) {
			filtered = append(filtered, paper)
		}
	}
	return search.SortPapersByPreferenceWithQuery(filtered, query)
}

func filterEvidenceByQueryRelevance(query string, evidence []EvidenceItem) []EvidenceItem {
	if len(evidence) == 0 {
		return nil
	}
	filtered := make([]EvidenceItem, 0, len(evidence))
	for _, item := range evidence {
		paper := search.Paper{
			ID:       item.PaperID,
			Title:    firstNonEmpty(item.PaperTitle, item.Claim),
			Abstract: firstNonEmpty(item.Snippet, item.Claim),
		}
		if paperMatchesQueryRelevance(query, paper) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// BuildTopicFocusedQueries returns LLM-prepared seed queries when available.
func BuildTopicFocusedQueries(root string) []string {
	if prep, ok := lookupPreparedQuery(root); ok && len(prep.SeedQueries) > 0 {
		return append([]string(nil), prep.SeedQueries...)
	}
	prep := PrepareResearchQueryWithContext(context.Background(), root, ResearchQueryPrepareOptions{
		LLMClient: GlobalLLMClient,
	})
	if len(prep.SeedQueries) > 0 {
		return append([]string(nil), prep.SeedQueries...)
	}
	return offlineSeedQueries(root)
}

func admitSearchPapersForQuery(existing []search.Paper, query string, incoming []search.Paper, maxUniquePapers int) ([]search.Paper, []search.Paper) {
	return appendUniqueSearchPapersWithinBudget(existing, filterPapersByQueryRelevance(query, incoming), maxUniquePapers)
}

func anchorVariants(anchor string) []string {
	anchor = strings.TrimSpace(anchor)
	if anchor == "" {
		return nil
	}
	variants := []string{anchor}
	if strings.HasSuffix(anchor, "s") && len(anchor) > 3 {
		variants = append(variants, strings.TrimSuffix(anchor, "s"))
	} else {
		variants = append(variants, anchor+"s")
	}
	return variants
}

func bodyContainsAnchor(body, anchor string) bool {
	body = strings.ToLower(body)
	anchorLower := strings.ToLower(anchor)

	for _, variant := range anchorVariants(anchorLower) {
		if variant != "" && strings.Contains(body, variant) {
			return true
		}
	}
	return false
}
