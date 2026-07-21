package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestBioRxivProvider(t *testing.T) {
	origClient := SharedHTTPClient
	defer func() { SharedHTTPClient = origClient }()

	t.Run("Name and Domains", func(t *testing.T) {
		p := NewBioRxivProvider()
		assert.Equal(t, "biorxiv", p.Name())
		assert.Contains(t, p.Domains(), "biology")

		pm := NewMedRxivProvider()
		assert.Equal(t, "medrxiv", pm.Name())
		assert.Contains(t, pm.Domains(), "medicine")
	})

	t.Run("Search Success", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"collection":[{"doi":"10.1101/1","title":"Bio Paper","abstract":"test","authors":"Ada Lovelace; Grace Hopper","server":"biorxiv"},{"doi":"","title":""}]}`)
				return rec.Result(), nil
			}),
		}

		p := NewBioRxivProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, []string{"Ada Lovelace", "Grace Hopper"}, papers[0].Authors)
		assert.Equal(t, []string{"biorxiv"}, papers[0].SourceApis)
	})

	t.Run("Search Decode Error", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				fmt.Fprint(rec, `invalid json`)
				return rec.Result(), nil
			}),
		}
		p := NewBioRxivProvider()
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})

	t.Run("Search Success MedRxiv", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"collection":[{"doi":"10.1101/med1","title":"Med Paper","abstract":"test","authors":"Katherine Johnson","server":"medrxiv"}]}`)
				return rec.Result(), nil
			}),
		}

		p := NewMedRxivProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Contains(t, papers[0].Link, "medrxiv.org")
		assert.Equal(t, []string{"Katherine Johnson"}, papers[0].Authors)
	})

	t.Run("Search Fallback on Client Error", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "api.biorxiv.org" && !strings.Contains(req.URL.Path, "details") {
					return nil, fmt.Errorf("network error")
				}
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"collection":[{"doi":"10.1101/fallback","title":"Fallback","abstract":"keyword","authors":"Ada Lovelace; Grace Hopper"}]}`)
				return rec.Result(), nil
			}),
		}

		p := NewBioRxivProvider()
		p.baseURL = "https://api.biorxiv.org/details"
		papers, err := p.Search(context.Background(), "keyword", SearchOpts{})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, []string{"biorxiv"}, papers[0].SourceApis)
		assert.Equal(t, []string{"Ada Lovelace", "Grace Hopper"}, papers[0].Authors)
	})

	t.Run("Search Fallback on HTTP 500", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				if !strings.Contains(req.URL.Path, "details") {
					rec.WriteHeader(http.StatusInternalServerError)
					return rec.Result(), nil
				}
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"collection":[{"doi":"10.1101/fallback500","title":"Fallback","abstract":"keyword"}]}`)
				return rec.Result(), nil
			}),
		}

		p := NewBioRxivProvider()
		p.baseURL = "https://api.biorxiv.org/details"
		papers, err := p.Search(context.Background(), "keyword", SearchOpts{})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
	})

	t.Run("Search Build Request Error", func(t *testing.T) {
		origBuilder := buildBioRxivSearchURL
		buildBioRxivSearchURL = func(limit int, query string) string {
			return "http://[::1"
		}
		t.Cleanup(func() { buildBioRxivSearchURL = origBuilder })

		p := NewBioRxivProvider()
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})

	t.Run("searchByDateRange HTTP Error", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.WriteHeader(http.StatusNotFound)
				return rec.Result(), nil
			}),
		}
		p := NewBioRxivProvider()
		_, err := p.searchByDateRange(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})

	t.Run("searchByDateRange Request Error", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("boom")
			}),
		}
		p := NewBioRxivProvider()
		_, err := p.searchByDateRange(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})

	t.Run("searchByDateRange Decode Error", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `not-json`)
				return rec.Result(), nil
			}),
		}
		p := NewBioRxivProvider()
		p.baseURL = "https://api.biorxiv.org/details"
		_, err := p.searchByDateRange(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})

	t.Run("searchByDateRange Build Request Error", func(t *testing.T) {
		p := NewBioRxivProvider()
		p.baseURL = "http://[::1"
		_, err := p.searchByDateRange(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})

	t.Run("searchByDateRange filters by keyword and parses dates", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"collection":[
					{"doi":"10.1101/keep","title":"Keyword Driven Study","abstract":"keyword appears here","date":"2024-03-10","authors":"Ada Lovelace"},
					{"doi":"10.1101/drop","title":"Unrelated Study","abstract":"nothing to see","date":"2023-07-01"}
				]}`)
				return rec.Result(), nil
			}),
		}

		p := NewBioRxivProvider()
		papers, err := p.searchByDateRange(context.Background(), "keyword search", SearchOpts{Limit: 1})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "biorxiv:10.1101/keep", papers[0].ID)
		assert.Equal(t, 2024, papers[0].Year)
		assert.Equal(t, 3, papers[0].Month)
		assert.Equal(t, []string{"biorxiv"}, papers[0].SourceApis)
		assert.Equal(t, []string{"Ada Lovelace"}, papers[0].Authors)
	})

	t.Run("searchByDateRange no keywords includes first result", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"collection":[
					{"doi":"10.1101/keep1","title":"Keep One","abstract":"alpha","date":"2024-03-10"},
					{"doi":"10.1101/keep2","title":"Keep Two","abstract":"beta","date":"2024-04-11"}
				]}`)
				return rec.Result(), nil
			}),
		}

		p := NewBioRxivProvider()
		papers, err := p.searchByDateRange(context.Background(), "", SearchOpts{Limit: 1})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "biorxiv:10.1101/keep1", papers[0].ID)
	})

	t.Run("searchByDateRange stops at limit", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"collection":[
					{"doi":"10.1101/keep1","title":"Keep One","abstract":"keyword","date":"2024-03-10"},
					{"doi":"10.1101/keep2","title":"Keep Two","abstract":"keyword","date":"2024-04-11"}
				]}`)
				return rec.Result(), nil
			}),
		}

		p := NewBioRxivProvider()
		papers, err := p.searchByDateRange(context.Background(), "keyword", SearchOpts{Limit: 1})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "biorxiv:10.1101/keep1", papers[0].ID)
	})

	t.Run("searchByDateRange ignores short keywords but keeps long matches", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"collection":[
					{"doi":"10.1101/keep","title":"Keyword Driven Study","abstract":"keyword appears here","date":"2024-03-10"}
				]}`)
				return rec.Result(), nil
			}),
		}

		p := NewBioRxivProvider()
		papers, err := p.searchByDateRange(context.Background(), "q keyword", SearchOpts{Limit: 1})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "biorxiv:10.1101/keep", papers[0].ID)
	})

	t.Run("Search parses medrxiv year and month", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"collection":[{"doi":"10.1101/med2","title":"Med Study","abstract":"test","date":"2022-11-05","server":"medrxiv"}]}`)
				return rec.Result(), nil
			}),
		}

		p := NewMedRxivProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{Limit: 1})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, 2022, papers[0].Year)
		assert.Equal(t, 11, papers[0].Month)
		assert.Contains(t, papers[0].Link, "medrxiv.org")
		assert.Equal(t, papers[0].Link+".full.pdf", papers[0].PdfUrl)
	})

	t.Run("searchByDateRange defaults limit and skips non-matching keywords", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"collection":[
					{"doi":"10.1101/keep","title":"Keep This Study","abstract":"keyword appears here","date":"2024-03-10"},
					{"doi":"10.1101/drop","title":"Different Study","abstract":"nothing to see","date":"2023-07-01"}
				]}`)
				return rec.Result(), nil
			}),
		}

		p := NewBioRxivProvider()
		papers, err := p.searchByDateRange(context.Background(), "keyword search", SearchOpts{Limit: 0})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "biorxiv:10.1101/keep", papers[0].ID)
	})
}
