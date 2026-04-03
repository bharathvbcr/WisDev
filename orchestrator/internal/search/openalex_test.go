package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOpenAlexProvider(t *testing.T) {
	origClient := SharedHTTPClient
	defer func() { SharedHTTPClient = origClient }()

	t.Run("Name and Domains", func(t *testing.T) {
		p := NewOpenAlexProvider()
		assert.Equal(t, "openalex", p.Name())
		assert.Empty(t, p.Domains())
	})

	t.Run("Search Success", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"results":[{"id":"https://openalex.org/W123","title":"OpenAlex Paper","doi":"https://doi.org/10.1","publication_year":2024}]}`)
				return rec.Result(), nil
			}),
		}
		p := NewOpenAlexProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{Limit: 5})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "openalex:W123", papers[0].ID)
		assert.Equal(t, "10.1", papers[0].DOI)
	})

	t.Run("Search Filters and Mailto", func(t *testing.T) {
		os.Setenv("OPENALEX_EMAIL", "test@example.com")
		defer os.Unsetenv("OPENALEX_EMAIL")

		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				assert.Contains(t, req.URL.RawQuery, "mailto=test%40example.com")
				assert.Contains(t, req.URL.RawQuery, "filter=publication_year:2020-2022")
				rec := httptest.NewRecorder()
				fmt.Fprint(rec, `{"results":[]}`)
				return rec.Result(), nil
			}),
		}
		p := NewOpenAlexProvider()
		_, _ = p.Search(context.Background(), "test", SearchOpts{YearFrom: 2020, YearTo: 2022})
	})

	t.Run("Search Filters YearFrom Only", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				assert.Contains(t, req.URL.RawQuery, "filter=publication_year:>2019")
				rec := httptest.NewRecorder()
				fmt.Fprint(rec, `{"results":[]}`)
				return rec.Result(), nil
			}),
		}
		p := NewOpenAlexProvider()
		_, _ = p.Search(context.Background(), "test", SearchOpts{YearFrom: 2020})
	})

	t.Run("HTTP Error", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.WriteHeader(http.StatusForbidden)
				return rec.Result(), nil
			}),
		}
		p := NewOpenAlexProvider()
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})

	t.Run("Decode Error", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				fmt.Fprint(rec, `invalid`)
				return rec.Result(), nil
			}),
		}
		p := NewOpenAlexProvider()
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})
}
