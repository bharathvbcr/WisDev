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
				fmt.Fprint(rec, `{"collection":[{"doi":"10.1101/1","title":"Bio Paper","abstract":"test","server":"biorxiv"},{"doi":"","title":""}]}`)
				return rec.Result(), nil
			}),
		}

		p := NewBioRxivProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
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
				fmt.Fprint(rec, `{"collection":[{"doi":"10.1101/med1","title":"Med Paper","abstract":"test","server":"medrxiv"}]}`)
				return rec.Result(), nil
			}),
		}

		p := NewMedRxivProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Contains(t, papers[0].Link, "medrxiv.org")
	})

	t.Run("Search Fallback on Client Error", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "api.biorxiv.org" && !strings.Contains(req.URL.Path, "details") {
					return nil, fmt.Errorf("network error")
				}
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"collection":[{"doi":"10.1101/fallback","title":"Fallback","abstract":"keyword"}]}`)
				return rec.Result(), nil
			}),
		}

		p := NewBioRxivProvider()
		p.baseURL = "https://api.biorxiv.org/details"
		papers, err := p.Search(context.Background(), "keyword", SearchOpts{})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
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
}
