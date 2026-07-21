package wisdev

import (
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

func TestAttachedSourcePapersCanonicalShape(t *testing.T) {
	raw := []any{
		map[string]any{
			"id":         "p1",
			"title":      "Meniscus Repair Outcomes",
			"abstract":   "An abstract.",
			"doi":        "10.1/abc",
			"link":       "https://example.org/p1",
			"authors":    []any{"Doe J", "Roe K"},
			"year":       float64(2023), // JSON numbers decode to float64
			"fullText":   "full body text",
			"sourceApis": []any{"pdf_upload"},
		},
	}
	papers := AttachedSourcePapers(raw)
	if len(papers) != 1 {
		t.Fatalf("expected 1 paper, got %d", len(papers))
	}
	p := papers[0]
	if p.ID != "p1" || p.Title != "Meniscus Repair Outcomes" {
		t.Fatalf("unexpected id/title: %+v", p)
	}
	if p.Year != 2023 {
		t.Fatalf("expected year 2023, got %d", p.Year)
	}
	if p.FullText != "full body text" {
		t.Fatalf("expected fullText preserved, got %q", p.FullText)
	}
	if len(p.Authors) != 2 || p.Authors[0] != "Doe J" {
		t.Fatalf("unexpected authors: %v", p.Authors)
	}
	if len(p.SourceApis) != 1 || p.SourceApis[0] != "pdf_upload" {
		t.Fatalf("unexpected sourceApis: %v", p.SourceApis)
	}
}

func TestAttachedSourcePapersFrontendSourceShape(t *testing.T) {
	// Frontend Source shape: author objects, publishDate.year, paperId, summary.
	raw := []any{
		map[string]any{
			"paperId":     "lib-42",
			"title":       "Connected Library Paper",
			"summary":     "Summary used when abstract absent.",
			"url":         "https://lib/42",
			"authors":     []any{map[string]any{"name": "Ada L"}, map[string]any{"name": ""}},
			"publishDate": map[string]any{"year": float64(2019)},
		},
	}
	papers := AttachedSourcePapers(raw)
	if len(papers) != 1 {
		t.Fatalf("expected 1 paper, got %d", len(papers))
	}
	p := papers[0]
	if p.ID != "lib-42" {
		t.Fatalf("expected paperId fallback to id, got %q", p.ID)
	}
	if p.Abstract != "Summary used when abstract absent." {
		t.Fatalf("expected summary fallback for abstract, got %q", p.Abstract)
	}
	if p.Link != "https://lib/42" {
		t.Fatalf("expected url fallback for link, got %q", p.Link)
	}
	if p.Year != 2019 {
		t.Fatalf("expected publishDate.year fallback, got %d", p.Year)
	}
	if len(p.Authors) != 1 || p.Authors[0] != "Ada L" {
		t.Fatalf("expected blank author dropped, got %v", p.Authors)
	}
	if len(p.SourceApis) != 1 || p.SourceApis[0] != "attached" {
		t.Fatalf("expected default 'attached' provenance, got %v", p.SourceApis)
	}
}

func TestAttachedSourcePapersDropsUntitledAndRespectsCap(t *testing.T) {
	items := make([]any, 0, attachedSourceMaxPapers+10)
	items = append(items, map[string]any{"title": "   "}) // dropped: blank title
	items = append(items, "not-a-map")                     // dropped: wrong type
	for i := 0; i < attachedSourceMaxPapers+5; i++ {
		items = append(items, map[string]any{"title": "Paper", "id": "x"})
	}
	papers := AttachedSourcePapers(items)
	if len(papers) != attachedSourceMaxPapers {
		t.Fatalf("expected cap of %d, got %d", attachedSourceMaxPapers, len(papers))
	}
}

func TestAttachedSourcePapersEmpty(t *testing.T) {
	if got := AttachedSourcePapers(nil); got != nil {
		t.Fatalf("expected nil for nil input, got %v", got)
	}
	if got := AttachedSourcePapers([]any{}); got != nil {
		t.Fatalf("expected nil for empty input, got %v", got)
	}
}

func TestAttachedSourceListProducesSources(t *testing.T) {
	raw := []any{
		map[string]any{
			"id":         "p1",
			"title":      "Attached Paper",
			"abstract":   "abs",
			"authors":    []any{"Doe J"},
			"year":       float64(2022),
			"sourceApis": []any{"pdf_upload"},
		},
		map[string]any{"title": "   "}, // dropped
	}
	sources := AttachedSourceList(raw)
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}
	if sources[0].Title != "Attached Paper" || sources[0].ID != "p1" {
		t.Fatalf("unexpected source: %+v", sources[0])
	}
	if sources[0].Year != 2022 {
		t.Fatalf("expected year mapped, got %d", sources[0].Year)
	}
	if len(AttachedSourceList(nil)) != 0 {
		t.Fatal("expected nil input to yield no sources")
	}
}

func TestIsAttachedSourceProvenance(t *testing.T) {
	cases := []struct {
		name       string
		source     string
		sourceApis []string
		want       bool
	}{
		{"pdf upload marker in apis", "", []string{"pdf_upload"}, true},
		{"connected marker in apis", "", []string{"semantic_scholar", "connected"}, true},
		{"default attached marker in source", "attached", nil, true},
		{"case-insensitive", "", []string{"PDF_Upload"}, true},
		{"live provider not attached", "semantic_scholar", []string{"semantic_scholar"}, false},
		{"empty", "", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAttachedSourceProvenance(tc.source, tc.sourceApis); got != tc.want {
				t.Fatalf("IsAttachedSourceProvenance(%q,%v)=%v want %v", tc.source, tc.sourceApis, got, tc.want)
			}
		})
	}
}

func TestCountAttachedSourcesUsed(t *testing.T) {
	papers := []search.Paper{
		{ID: "a", Title: "Uploaded", SourceApis: []string{"pdf_upload"}},
		{ID: "a", Title: "Uploaded dup", SourceApis: []string{"pdf_upload"}}, // dup id, counted once
		{ID: "b", Title: "Connected", SourceApis: []string{"connected"}},
		{ID: "c", Title: "Live", Source: "semantic_scholar", SourceApis: []string{"semantic_scholar"}},
		{Title: "Attached no id", Source: "attached"}, // counted via title key
	}
	if got := CountAttachedSourcesUsed(papers); got != 3 {
		t.Fatalf("expected 3 attached sources used, got %d", got)
	}
	if got := CountAttachedSourcesUsed(nil); got != 0 {
		t.Fatalf("expected 0 for nil, got %d", got)
	}
}

func TestResearchDurableSourceSummariesFlagsAttached(t *testing.T) {
	papers := []search.Paper{
		{ID: "a", Title: "Uploaded Paper", SourceApis: []string{"pdf_upload"}},
		{ID: "b", Title: "Live Paper", Source: "semantic_scholar", SourceApis: []string{"semantic_scholar"}, Year: 2020},
	}
	summaries := researchDurableSourceSummaries(papers, 5)
	if len(summaries) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(summaries))
	}
	var attachedCount int
	for _, s := range summaries {
		if s.Attached {
			attachedCount++
		}
	}
	if attachedCount != 1 {
		t.Fatalf("expected exactly 1 attached summary, got %d (%+v)", attachedCount, summaries)
	}
}

func TestNormalizeAttachedSources(t *testing.T) {
	raw := []any{
		map[string]any{
			"title":      "Stored Paper",
			"id":         "s1",
			"fullText":   "lots of text",
			"sourceApis": []any{"connected"},
			"year":       float64(2021),
		},
	}
	norm := NormalizeAttachedSources(raw)
	if len(norm) != 1 {
		t.Fatalf("expected 1 normalized entry, got %d", len(norm))
	}
	entry := norm[0]
	if entry["title"] != "Stored Paper" || entry["id"] != "s1" {
		t.Fatalf("unexpected normalized entry: %+v", entry)
	}
	if entry["hasFullText"] != true {
		t.Fatalf("expected hasFullText flag, got %+v", entry)
	}
	// Full text body must not be persisted verbatim onto the session document.
	if _, leaked := entry["fullText"]; leaked {
		t.Fatalf("fullText body must not be persisted on session: %+v", entry)
	}
}
