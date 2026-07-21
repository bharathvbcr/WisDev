package search

import (
	"testing"
	"time"
)

func TestBuildQuickModeQueryVariants(t *testing.T) {
	t.Run("empty_query", func(t *testing.T) {
		if got := BuildQuickModeQueryVariants("  ", 3); len(got) != 0 {
			t.Fatalf("expected empty tabs, got %+v", got)
		}
	})

	t.Run("max_tabs_1", func(t *testing.T) {
		got := BuildQuickModeQueryVariants("machine learning", 1)
		if len(got) != 1 || got[0].ID != "direct" || got[0].Query != "machine learning" {
			t.Fatalf("unexpected tabs: %+v", got)
		}
	})

	t.Run("max_tabs_2", func(t *testing.T) {
		got := BuildQuickModeQueryVariants("machine learning", 2)
		if len(got) != 2 {
			t.Fatalf("len=%d want 2", len(got))
		}
		if got[1].Query != "machine learning review overview" {
			t.Fatalf("broader query=%q", got[1].Query)
		}
	})

	t.Run("max_tabs_3_default", func(t *testing.T) {
		got := BuildQuickModeQueryVariants("transformers", 0)
		if len(got) != 3 {
			t.Fatalf("len=%d want 3", len(got))
		}
		if got[2].ID != "narrow" || got[2].Query != "transformers recent advances" {
			t.Fatalf("narrow tab=%+v", got[2])
		}
	})
}

func TestMergeAndRankQuickModeResults_FirstTabWinsAndGoPreference(t *testing.T) {
	tabs := BuildQuickModeQueryVariants("transformers", 2)
	results := map[string][]Paper{
		"transformers": {
			{ID: "same", Title: "Paper Same", CitationCount: 10, Link: "https://ex/a", Year: time.Now().Year() - 5},
			{ID: "only-direct", Title: "Only Direct", CitationCount: 5, Link: "https://ex/b", Year: time.Now().Year() - 5},
		},
		"transformers review overview": {
			{ID: "same", Title: "Paper Same", CitationCount: 999, Link: "https://ex/a-dup", Year: time.Now().Year() - 5},
			{ID: "only-broader", Title: "Only Broader", CitationCount: 1, Link: "https://ex/c", Year: time.Now().Year() - 5},
		},
	}

	got := MergeAndRankQuickModeResults("transformers", tabs, NormalizeQuickModeBatchKeys(results))
	if got.DuplicatesRemoved != 1 {
		t.Fatalf("duplicatesRemoved=%d want 1", got.DuplicatesRemoved)
	}
	if got.TotalBefore != 4 {
		t.Fatalf("totalBefore=%d want 4", got.TotalBefore)
	}
	if len(got.Merged) != 3 {
		t.Fatalf("merged len=%d want 3", len(got.Merged))
	}

	// First-tab survivor keeps citation count 10 (not 999).
	var same *Paper
	for i := range got.Merged {
		if got.Merged[i].ID == "same" {
			same = &got.Merged[i]
			break
		}
	}
	if same == nil {
		t.Fatal("expected same paper in merged")
	}
	if same.CitationCount != 10 {
		t.Fatalf("survivor citationCount=%d want 10", same.CitationCount)
	}

	directIDs := paperIDs(got.ByTabID["direct"])
	broaderIDs := paperIDs(got.ByTabID["broader"])
	if !equalStringSlices(directIDs, []string{"same", "only-direct"}) && !containsAll(directIDs, "same", "only-direct") {
		t.Fatalf("direct tab ids=%v", directIDs)
	}
	if !equalStringSlices(broaderIDs, []string{"only-broader"}) {
		t.Fatalf("broader tab ids=%v want [only-broader]", broaderIDs)
	}

	// Merged list is Go-preference ordered (same papers as SortPapersByPreferenceWithQuery).
	wantOrder := paperIDs(SortPapersByPreferenceWithQuery(got.Merged, "transformers"))
	if !equalStringSlices(paperIDs(got.Merged), wantOrder) {
		t.Fatalf("merged order=%v want preference order=%v", paperIDs(got.Merged), wantOrder)
	}
}

func paperIDs(papers []Paper) []string {
	out := make([]string, len(papers))
	for i, p := range papers {
		out[i] = p.ID
	}
	return out
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsAll(haystack []string, needles ...string) bool {
	set := make(map[string]struct{}, len(haystack))
	for _, h := range haystack {
		set[h] = struct{}{}
	}
	for _, n := range needles {
		if _, ok := set[n]; !ok {
			return false
		}
	}
	return true
}
