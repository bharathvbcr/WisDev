package wisdev

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestWebFilter_DeriveSearchPolicyHints(t *testing.T) {
	query := "latest machine learning papers"
	hints := &SearchPolicyHints{
		Intent: "academic",
	}
	ctx := &CapabilityExecuteContext{
		DomainHint: "medicine",
	}

	policy := DeriveSearchPolicyHints(query, hints, ctx)
	assert.Equal(t, "academic", policy.Intent)
	assert.Contains(t, policy.AllowedDomains, "arxiv.org")
	assert.Contains(t, policy.AllowedDomains, "pubmed.ncbi.nlm.nih.gov") // From medicine hint
	assert.Equal(t, 8, policy.MaxResults)
}

func TestWebFilter_FilterAndRank(t *testing.T) {
	query := "transformer models"
	results := []WebSearchResultItem{
		{Title: "Attention is All You Need", Link: "https://arxiv.org/abs/1706.03762", Snippet: "The dominant sequence transduction models..."},
		{Title: "Pinterest", Link: "https://pinterest.com/pin/123", Snippet: "Cool pins"},
		{Title: "Invalid", Link: "not-a-url", Snippet: "bad"},
	}
	policy := NormalizedWebSearchPolicy{
		AllowedDomains: []string{"arxiv.org"},
		BlockedDomains: []string{"pinterest.com"},
		FreshnessDays:  3650,
		MinSignalScore: 0.1,
		MaxResults:     5,
		Intent:         "academic",
	}

	ranked, telemetry := FilterAndRankWebSearchResults(query, results, policy)
	assert.Len(t, ranked, 1)
	assert.Equal(t, "Attention is All You Need", ranked[0].Title)
	assert.Equal(t, 2, telemetry.FilteredCount)
	assert.Equal(t, 1, telemetry.FilterReasons["blocked_domain"])
	assert.Equal(t, 1, telemetry.FilterReasons["invalid_url"])
}

func TestExtractPublishedAtMs(t *testing.T) {
	item := WebSearchResultItem{
		Title:   "Test Paper 2023",
		Snippet: "Published on 2023-05-10",
		Pagemap: map[string]any{
			"metatags": []any{
				map[string]any{
					"article:published_time": "2023-05-10T10:00:00Z",
				},
			},
		},
	}

	ts := extractPublishedAtMs(item)
	assert.NotNil(t, ts)

	t2 := time.UnixMilli(*ts)
	assert.Equal(t, 2023, t2.Year())
	assert.Equal(t, time.May, t2.Month())
}
