package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newSearchDecisionsMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	RegisterSearchDecisionRoutes(mux)
	return mux
}

func TestHandleSearchDecisionsSuccess(t *testing.T) {
	mux := newSearchDecisionsMux(t)

	body := `{"query":"recent cancer review","researchField":"medicine"}`
	req := httptest.NewRequest(http.MethodPost, "/api/search/decisions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var envelope struct {
		SearchDecisions struct {
			QualityMode string `json:"qualityMode"`
			Providers   []string
			Filters     struct {
				PublicationType string `json:"publicationType"`
				DateFrom        string `json:"dateFrom"`
			}
			Scope            string `json:"scope"`
			Reasoning        string `json:"reasoning"`
			MultiTabStrategy *struct {
				Tabs []struct {
					Type string `json:"type"`
				} `json:"tabs"`
			} `json:"multiTabStrategy"`
		} `json:"searchDecisions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to parse response: %v\n%s", err, rec.Body.String())
	}
	d := envelope.SearchDecisions
	if d.QualityMode == "" || len(d.Providers) == 0 || d.Scope == "" || d.Reasoning == "" {
		t.Fatalf("expected populated decisions, got %+v", d)
	}
	if d.Filters.PublicationType != "Review" {
		t.Fatalf("expected Review, got %q", d.Filters.PublicationType)
	}
	if d.MultiTabStrategy == nil || len(d.MultiTabStrategy.Tabs) == 0 || d.MultiTabStrategy.Tabs[0].Type != "question" {
		t.Fatalf("expected multi-tab strategy with question tab, got %+v", d.MultiTabStrategy)
	}
}

func TestHandleSearchDecisionsExcludesMultiTabOnRequest(t *testing.T) {
	mux := newSearchDecisionsMux(t)

	body := `{"query":"transformer architectures","includeMultiTab":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/search/decisions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "multiTabStrategy") {
		t.Fatalf("expected multiTabStrategy omitted, got %s", rec.Body.String())
	}
}

func TestHandleSearchDecisionsFailurePaths(t *testing.T) {
	mux := newSearchDecisionsMux(t)

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/search/decisions", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", rec.Code)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/search/decisions", strings.NewReader("{not json"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("empty query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/search/decisions", strings.NewReader(`{"query":"   "}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("oversized query", func(t *testing.T) {
		long := strings.Repeat("q", maxSearchDecisionQueryLen+1)
		req := httptest.NewRequest(http.MethodPost, "/api/search/decisions", strings.NewReader(`{"query":"`+long+`"}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})
}
