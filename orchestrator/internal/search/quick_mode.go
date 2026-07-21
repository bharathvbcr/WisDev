package search

import (
	"strings"
)

// QuickModeTab is one focused strategy tab in Quick Mode multi-tab search.
type QuickModeTab struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Query string `json:"query"`
}

// QuickModeMergeResult is the deduplicated + ranked output of a Quick Mode fan-out.
type QuickModeMergeResult struct {
	Merged              []Paper
	ByTabID             map[string][]Paper
	TotalBefore         int
	DuplicatesRemoved   int
}

const (
	defaultQuickModeMaxTabs = 3
	maxQuickModeMaxTabs     = 8
)

// BuildQuickModeQueryVariants ports the former FE buildQueryVariants helper.
// Tabs: direct (exact query), broader ("review overview"), narrow ("recent advances").
func BuildQuickModeQueryVariants(query string, maxTabs int) []QuickModeTab {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	if maxTabs <= 0 {
		maxTabs = defaultQuickModeMaxTabs
	}
	if maxTabs > maxQuickModeMaxTabs {
		maxTabs = maxQuickModeMaxTabs
	}

	tabs := []QuickModeTab{
		{ID: "direct", Name: "Direct", Query: q},
	}
	if maxTabs >= 2 {
		tabs = append(tabs, QuickModeTab{
			ID:    "broader",
			Name:  "Broader context",
			Query: q + " review overview",
		})
	}
	if maxTabs >= 3 {
		tabs = append(tabs, QuickModeTab{
			ID:    "narrow",
			Name:  "Recent",
			Query: q + " recent advances",
		})
	}
	return tabs
}

// QuickModePaperIdentity mirrors the former FE getSourceIdentity key used for
// first-tab-wins Quick Mode dedupe: lowercase(trim(id || doi || link || title)).
func QuickModePaperIdentity(p Paper) string {
	id := strings.TrimSpace(p.ID)
	if id == "" {
		id = strings.TrimSpace(p.DOI)
	}
	if id == "" {
		id = strings.TrimSpace(p.Link)
	}
	if id == "" {
		id = strings.TrimSpace(p.Title)
	}
	return strings.ToLower(id)
}

// MergeAndRankQuickModeResults assigns each unique paper to the first matching
// strategy tab (first-tab-wins), then ranks merged and per-tab lists with the
// canonical Go preference scorer (SortPapersByPreferenceWithQuery).
func MergeAndRankQuickModeResults(rootQuery string, tabs []QuickModeTab, resultsByQuery map[string][]Paper) QuickModeMergeResult {
	byTabID := make(map[string][]Paper, len(tabs))
	for _, tab := range tabs {
		byTabID[tab.ID] = nil
	}

	seen := make(map[string]struct{})
	merged := make([]Paper, 0)
	totalBefore := 0
	duplicatesRemoved := 0

	for _, tab := range tabs {
		key := strings.ToLower(strings.TrimSpace(tab.Query))
		papers := resultsByQuery[key]
		if papers == nil {
			// Also try exact (non-normalized) key for callers that already keyed by raw query.
			papers = resultsByQuery[tab.Query]
		}
		totalBefore += len(papers)
		for _, paper := range papers {
			identity := QuickModePaperIdentity(paper)
			if identity == "" {
				continue
			}
			if _, ok := seen[identity]; ok {
				duplicatesRemoved++
				continue
			}
			seen[identity] = struct{}{}
			byTabID[tab.ID] = append(byTabID[tab.ID], paper)
			merged = append(merged, paper)
		}
	}

	rankedMerged := SortPapersByPreferenceWithQuery(merged, rootQuery)
	for tabID, papers := range byTabID {
		byTabID[tabID] = SortPapersByPreferenceWithQuery(papers, rootQuery)
	}

	return QuickModeMergeResult{
		Merged:            rankedMerged,
		ByTabID:           byTabID,
		TotalBefore:       totalBefore,
		DuplicatesRemoved: duplicatesRemoved,
	}
}

// NormalizeQuickModeBatchKeys lowercases+trims map keys so tab query lookup is stable.
func NormalizeQuickModeBatchKeys(results map[string][]Paper) map[string][]Paper {
	if len(results) == 0 {
		return map[string][]Paper{}
	}
	out := make(map[string][]Paper, len(results))
	for k, papers := range results {
		out[strings.ToLower(strings.TrimSpace(k))] = papers
	}
	return out
}
