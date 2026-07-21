package policy

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// These tests mirror frontend/services/__tests__/aiDecisionEngine.test.ts so the
// Go port stays behaviorally in parity with the browser engine it replaced.

func withFixedNow(t *testing.T, fixed time.Time) {
	t.Helper()
	prev := nowFunc
	nowFunc = func() time.Time { return fixed }
	t.Cleanup(func() { nowFunc = prev })
}

func TestDecideQualityModeForQuery(t *testing.T) {
	if got := DecideQualityModeForQuery("what is A"); got != QualityFast {
		t.Fatalf("simple query: expected fast, got %s", got)
	}
	// Comparative/synthesis phrasing with enough words should escalate.
	q := "compare the overall trend across multiple transformer architectures and their trade-offs in production systems today"
	if got := DecideQualityModeForQuery(q); got != QualityQuality {
		t.Fatalf("complex query: expected quality, got %s", got)
	}
}

func TestDecideProvidersForQuery(t *testing.T) {
	t.Run("uses valid user preferences", func(t *testing.T) {
		got := DecideProvidersForQuery("query", SearchDecisionPreferences{
			PreferredSources: []string{"pubmed", "not-a-real-provider"},
			ResearchField:    "Medicine",
		})
		if len(got) != 1 || got[0] != "pubmed" {
			t.Fatalf("expected [pubmed], got %v", got)
		}
	})

	t.Run("defaults to general providers", func(t *testing.T) {
		got := DecideProvidersForQuery("query", SearchDecisionPreferences{})
		if !containsString(got, "semanticscholar") {
			t.Fatalf("expected general set to include semanticscholar, got %v", got)
		}
	})

	t.Run("selects domain-specific providers", func(t *testing.T) {
		got := DecideProvidersForQuery("cancer treatment efficacy in patients", SearchDecisionPreferences{})
		if !containsString(got, "pubmed") {
			t.Fatalf("medical query: expected pubmed, got %v", got)
		}
	})

	t.Run("field recommendations override domain and cap at 4", func(t *testing.T) {
		got := DecideProvidersForQuery("cancer", SearchDecisionPreferences{ResearchField: "machine-learning"})
		if len(got) > 4 {
			t.Fatalf("expected at most 4 providers, got %d: %v", len(got), got)
		}
		if !containsString(got, "arxiv") {
			t.Fatalf("ml field preset: expected arxiv, got %v", got)
		}
	})

	t.Run("research mode providers when preferred sources absent", func(t *testing.T) {
		got := DecideProvidersForQuery("query", SearchDecisionPreferences{
			ResearchMode:     "methodology",
			SubscriptionTier: "pro",
		})
		if !containsString(got, "paperswithcode") {
			t.Fatalf("methodology mode: expected paperswithcode, got %v", got)
		}
		if len(got) > 4 {
			t.Fatalf("expected at most 4 providers, got %d", len(got))
		}
	})

	t.Run("research mode falls back for free tier on pro modes", func(t *testing.T) {
		got := DecideProvidersForQuery("query", SearchDecisionPreferences{
			ResearchMode:     "review",
			SubscriptionTier: "free",
		})
		if !containsString(got, "base") {
			t.Fatalf("free+review fallback to exploration: expected base, got %v", got)
		}
	})
}

func TestExtractFiltersFromQuery(t *testing.T) {
	withFixedNow(t, time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC))

	t.Run("year range", func(t *testing.T) {
		f := ExtractFiltersFromQuery("research from 2018 to 2022")
		if f.DateFrom != "2018" || f.DateTo != "2022" {
			t.Fatalf("expected 2018..2022, got %q..%q", f.DateFrom, f.DateTo)
		}
	})

	t.Run("distant years keep most recent as minimum", func(t *testing.T) {
		f := ExtractFiltersFromQuery("studies between 1995 and 2020")
		if f.DateFrom != "2020" || f.DateTo != "" {
			t.Fatalf("expected dateFrom=2020 only, got %q..%q", f.DateFrom, f.DateTo)
		}
	})

	t.Run("recent maps to last 5 years", func(t *testing.T) {
		f := ExtractFiltersFromQuery("recent research")
		want := strconv.Itoa(2026 - 5)
		if f.DateFrom != want {
			t.Fatalf("expected dateFrom=%s, got %q", want, f.DateFrom)
		}
	})

	t.Run("publication types", func(t *testing.T) {
		if f := ExtractFiltersFromQuery("systematic review of X"); f.PublicationType != "Review" {
			t.Fatalf("expected Review, got %q", f.PublicationType)
		}
		if f := ExtractFiltersFromQuery("randomized clinical trial outcomes"); f.PublicationType != "Clinical Trial" {
			t.Fatalf("expected Clinical Trial, got %q", f.PublicationType)
		}
	})

	t.Run("open access", func(t *testing.T) {
		if f := ExtractFiltersFromQuery("open access paper"); !f.OpenAccessOnly {
			t.Fatal("expected openAccessOnly=true")
		}
	})

	t.Run("field of study + semantic keywords from detection", func(t *testing.T) {
		f := ExtractFiltersFromQuery("cardiac disease diagnosis in patients")
		if f.FieldOfStudy != "Medicine" {
			t.Fatalf("expected Medicine, got %q", f.FieldOfStudy)
		}
		if len(f.SemanticKeywords) == 0 {
			t.Fatal("expected semantic keywords from matched domain terms")
		}
		if len(f.SemanticKeywords) > 10 {
			t.Fatalf("expected at most 10 semantic keywords, got %d", len(f.SemanticKeywords))
		}
	})
}

func TestDecideTimeframeForQuery(t *testing.T) {
	cases := map[string]string{
		"recent advances in CRISPR": "1year",
		"historical origins of AI":  "alltime",
		"machine learning methods":  "5years",
	}
	for query, want := range cases {
		if got := DecideTimeframeForQuery(query); got != want {
			t.Fatalf("DecideTimeframeForQuery(%q)=%q want %q", query, got, want)
		}
	}
}

func TestAllFieldProviderPresetsIncludesGeneral(t *testing.T) {
	got := AllFieldProviderPresets()
	if len(got["general"]) == 0 {
		t.Fatal("expected general field preset")
	}
	got["general"] = nil
	if len(AllFieldProviderPresets()["general"]) == 0 {
		t.Fatal("AllFieldProviderPresets must return a defensive copy")
	}
}

func TestDecideScopeForQuery(t *testing.T) {
	cases := map[string]string{
		"give me everything about X":  "exhaustive",
		"a comprehensive survey of Y": "exhaustive",
		"the specific mechanism of Z": "focused",
		"transformer architectures":   "comprehensive",
	}
	for query, want := range cases {
		if got := DecideScopeForQuery(query); got != want {
			t.Fatalf("%q: expected %s, got %s", query, want, got)
		}
	}
}

func TestAnalyzeQueryComplexity(t *testing.T) {
	t.Run("simple", func(t *testing.T) {
		got := AnalyzeQueryComplexity("what is CRISPR")
		if got.IsComplex || got.ComplexityType != "simple" {
			t.Fatalf("expected simple/not-complex, got %+v", got)
		}
	})
	t.Run("comparative", func(t *testing.T) {
		got := AnalyzeQueryComplexity("compare BERT versus GPT for classification")
		if !got.IsComplex || got.ComplexityType != "comparative" {
			t.Fatalf("expected comparative/complex, got %+v", got)
		}
	})
	t.Run("indicators deduped", func(t *testing.T) {
		got := AnalyzeQueryComplexity("compare and contrast, compare again")
		seen := map[string]bool{}
		for _, ind := range got.Indicators {
			if seen[ind] {
				t.Fatalf("duplicate indicator %q in %v", ind, got.Indicators)
			}
			seen[ind] = true
		}
	})
}

func TestDecideMultiTabStrategyForQuery(t *testing.T) {
	t.Run("always creates question tab first", func(t *testing.T) {
		got := DecideMultiTabStrategyForQuery("simple query", SearchDecisionPreferences{})
		if len(got.Tabs) == 0 || got.Tabs[0].Type != "question" || got.Tabs[0].Priority != 10 {
			t.Fatalf("expected leading question tab, got %+v", got.Tabs)
		}
	})

	t.Run("adds clinical trials tab for medical queries", func(t *testing.T) {
		got := DecideMultiTabStrategyForQuery("clinical trial for diabetes", SearchDecisionPreferences{})
		found := false
		for _, tab := range got.Tabs {
			if tab.Type == "clinical-trials" {
				found = true
				if tab.Filters == nil || len(tab.Filters.TrialStatus) != 3 {
					t.Fatalf("expected trial status filters, got %+v", tab.Filters)
				}
			}
		}
		if !found {
			t.Fatalf("expected clinical-trials tab, got %+v", got.Tabs)
		}
	})

	t.Run("adds keyword tab for boolean operator queries", func(t *testing.T) {
		got := DecideMultiTabStrategyForQuery("A AND B", SearchDecisionPreferences{})
		found := false
		for _, tab := range got.Tabs {
			if tab.Type == "keyword" {
				found = true
				if tab.BooleanQuery != "A AND B" {
					t.Fatalf("expected booleanQuery preserved, got %+v", tab)
				}
			}
		}
		if !found {
			t.Fatalf("expected keyword tab, got %+v", got.Tabs)
		}
	})
}

func TestDecideSearchParameters(t *testing.T) {
	withFixedNow(t, time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC))

	got := DecideSearchParameters("recent cancer review", SearchDecisionPreferences{})
	if got.QualityMode == "" || len(got.Providers) == 0 || got.Scope == "" {
		t.Fatalf("expected populated decisions, got %+v", got)
	}
	if got.Timeframe != "1year" {
		t.Fatalf("expected timeframe 1year for 'recent', got %q", got.Timeframe)
	}
	if got.Filters.PublicationType != "Review" {
		t.Fatalf("expected Review publication type, got %q", got.Filters.PublicationType)
	}
	if got.Filters.DateFrom == "" {
		t.Fatal("expected dateFrom for 'recent'")
	}
	if got.Reasoning == "" || got.MultiTabStrategy == nil || len(got.MultiTabStrategy.Tabs) == 0 {
		t.Fatalf("expected reasoning and multi-tab strategy, got %+v", got)
	}
	if !strings.Contains(got.Reasoning, "sources:") {
		t.Fatalf("expected provider names in reasoning, got %q", got.Reasoning)
	}
}

func TestFieldRecommendationsUnknownFallsBackToGeneral(t *testing.T) {
	got := FieldRecommendations("definitely-not-a-field")
	want := fieldProviderPresets["general"]
	if len(got) != len(want) {
		t.Fatalf("expected general preset, got %v", got)
	}
}

func TestDetectDomainFromKeywordsDeterministicOnEmptyMatch(t *testing.T) {
	got := DetectDomainFromKeywords("zzz qqq xxx")
	if got.PrimaryDomain != "general" || got.Confidence != 0.5 {
		t.Fatalf("expected general/0.5, got %+v", got)
	}
}
