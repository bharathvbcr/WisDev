package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

func TestHandleQuickModeSearch_EmptyQuery(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/search/quick-mode", bytes.NewBufferString(`{"query":"  "}`))
	rec := httptest.NewRecorder()
	h.HandleQuickModeSearch(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestHandleQuickModeSearch_MethodNotAllowed(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/search/quick-mode", nil)
	rec := httptest.NewRecorder()
	h.HandleQuickModeSearch(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
}

func TestHandleQuickModeSearch_BuildsVariantsRanksAndDedupes(t *testing.T) {
	prev := quickModeTabSearch
	t.Cleanup(func() { quickModeTabSearch = prev })

	quickModeTabSearch = func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
		year := time.Now().Year() - 3
		switch {
		case query == "machine learning":
			return []search.Paper{
				{ID: "1", Title: "Paper A", CitationCount: 10, Year: year, Score: 0.5, Link: "https://ex/a"},
				{ID: "2", Title: "Paper B", CitationCount: 5, Year: year, Score: 0.4, Link: "https://ex/b"},
			}, nil
		case strings.Contains(query, "review overview"):
			return []search.Paper{
				{ID: "1", Title: "Paper A", CitationCount: 99, Year: year, Score: 0.9, Link: "https://ex/a-dup"},
				{ID: "3", Title: "Paper C", CitationCount: 1, Year: year, Score: 0.3, Link: "https://ex/c"},
			}, nil
		default:
			return []search.Paper{}, nil
		}
	}

	h := NewSearchHandler(nil, nil, nil)
	body := `{"query":"machine learning","maxTabConcurrency":2,"providerLimit":10}`
	req := httptest.NewRequest(http.MethodPost, "/search/quick-mode", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.HandleQuickModeSearch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp quickModeSearchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Stats["duplicatesRemoved"] != 1 {
		t.Fatalf("duplicatesRemoved=%d want 1", resp.Stats["duplicatesRemoved"])
	}
	if resp.Stats["totalAfter"] != 3 {
		t.Fatalf("totalAfter=%d want 3", resp.Stats["totalAfter"])
	}
	if len(resp.Tabs) != 2 {
		t.Fatalf("expected 2 tabs, got %d", len(resp.Tabs))
	}
	if len(resp.Results) != 3 {
		t.Fatalf("results len=%d want 3", len(resp.Results))
	}
	if len(resp.CategorizedSources) != 2 {
		t.Fatalf("categorizedSources len=%d want 2", len(resp.CategorizedSources))
	}
	// First-tab survivor for id=1 keeps citationCount 10.
	foundSurvivor := false
	for _, raw := range resp.Results {
		if raw["id"] == "1" {
			foundSurvivor = true
			if int(raw["citationCount"].(float64)) != 10 {
				t.Fatalf("survivor citationCount=%v want 10", raw["citationCount"])
			}
		}
	}
	if !foundSurvivor {
		t.Fatal("expected paper id=1 in results")
	}
}
