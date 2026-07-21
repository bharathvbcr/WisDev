package wisdev

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestWebFilterHelpers(t *testing.T) {
	t.Run("domain and timestamp helpers", func(t *testing.T) {
		assert.Equal(t, "example.com", normalizeDomain(" *.Example.com "))
		assert.Equal(t, "example.com", extractHost("https://www.example.com/path"))
		assert.Equal(t, "", extractHost("not-a-url"))

		now := time.Now().UTC()
		year := now.Year()
		assert.NotNil(t, parseTimestamp("2024-01-02T03:04:05Z"))
		assert.NotNil(t, parseTimestamp("2024-01-02"))
		assert.NotNil(t, parseTimestamp(year))
		assert.Nil(t, parseTimestamp(""))

		item := WebSearchResultItem{
			Title:   "Paper published 2024-01-02",
			Snippet: "Updated in March 2024",
			Pagemap: map[string]any{
				"metatags": []any{
					map[string]any{"article:published_time": "2024-01-02T03:04:05Z"},
				},
			},
		}
		assert.NotNil(t, extractPublishedAtMs(item))
	})

	t.Run("intent and policy derivation", func(t *testing.T) {
		assert.Equal(t, "news", inferIntent("latest model release", ""))
		assert.Equal(t, "implementation", inferIntent("github repo for code", ""))
		assert.Equal(t, "policy", inferIntent("policy guideline framework", ""))
		assert.Equal(t, "academic", inferIntent("systematic review paper", ""))
		assert.Equal(t, "general", inferIntent("general topic", ""))
		assert.Equal(t, "custom", inferIntent("whatever", "custom"))

		policy := DeriveSearchPolicyHints("latest clinical treatment", nil, &CapabilityExecuteContext{DomainHint: "medicine"})
		assert.Equal(t, "news", policy.Intent)
		assert.Contains(t, policy.AllowedDomains, "pubmed.ncbi.nlm.nih.gov")
		assert.Equal(t, 8, policy.MaxResults)
		assert.Equal(t, 30, policy.FreshnessDays)
		assert.Equal(t, 1.2, policy.MinSignalScore)

		assert.True(t, domainMatchesAllowlist("sub.example.com", []string{"example.com"}))
		assert.False(t, domainMatchesAllowlist("bad.com", []string{"example.com"}))
		assert.True(t, domainMatchesBlocklist("sub.example.com", []string{"example.com"}))
		assert.False(t, domainMatchesBlocklist("good.com", []string{"example.com"}))
	})

	t.Run("scoring and ranking", func(t *testing.T) {
		score := scoreResult(
			"transformer github",
			WebSearchResultItem{Title: "Transformer code", Snippet: "github repo"},
			"github.com",
			"implementation",
		)
		assert.Greater(t, score, 1.0)

		results := []WebSearchResultItem{
			{
				Title:   "Fresh paper",
				Link:    "https://arxiv.org/abs/1",
				Snippet: "transformer code",
				Pagemap: map[string]any{
					"metatags": []any{map[string]any{"article:published_time": time.Now().UTC().Format(time.RFC3339)}},
				},
			},
			{
				Title:   "Duplicate host",
				Link:    "https://arxiv.org/abs/2",
				Snippet: "transformer code",
			},
			{
				Title:   "Old paper",
				Link:    "https://arxiv.org/abs/3",
				Snippet: "transformer code 1999",
			},
			{
				Title:   "Low signal",
				Link:    "https://example.com/4",
				Snippet: "short",
			},
		}
		policy := NormalizedWebSearchPolicy{
			AllowedDomains: []string{"arxiv.org", "example.com"},
			BlockedDomains: []string{"blocked.com"},
			FreshnessDays:  3650,
			MinSignalScore: 0.1,
			MaxResults:     3,
			Intent:         "academic",
		}
		ranked, telemetry := FilterAndRankWebSearchResults("transformer code", results, policy)
		assert.Len(t, ranked, 1)
		assert.Equal(t, "Fresh paper", ranked[0].Title)
		assert.Equal(t, 3, telemetry.FilteredCount)
		assert.Equal(t, 1, telemetry.DomainMix["arxiv.org"])
	})
}
