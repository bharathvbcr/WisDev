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
				fmt.Fprint(rec, `{"results":[{"id":"https://openalex.org/W123","title":"OpenAlex Paper","doi":"https://doi.org/10.1","abstract_inverted_index":{"This":[0],"paper":[1],"has":[2],"an":[3],"abstract":[4]},"publication_year":2024,"primary_location":{"source":{"display_name":"Nature"}},"authorships":[{"author":{"display_name":"Ada Lovelace"}},{"author":{"display_name":"Grace Hopper"}}]}]}`)
				return rec.Result(), nil
			}),
		}
		p := NewOpenAlexProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{Limit: 5})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "openalex:W123", papers[0].ID)
		assert.Equal(t, "10.1", papers[0].DOI)
		assert.Equal(t, "openalex", papers[0].Source)
		assert.Equal(t, "Nature", papers[0].Venue)
		assert.Equal(t, []string{"openalex"}, papers[0].SourceApis)
		assert.Equal(t, []string{"Ada Lovelace", "Grace Hopper"}, papers[0].Authors)
		assert.Equal(t, "This paper has an abstract", papers[0].Abstract)
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

	t.Run("Classifies nonstandard throttle headers", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("X-RateLimit-Remaining", "0")
				rec.WriteHeader(http.StatusForbidden)
				return rec.Result(), nil
			}),
		}
		p := NewOpenAlexProvider()
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "rate limit exceeded")
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

	t.Run("Search Default Limit and OA Fallback", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				assert.Contains(t, req.URL.RawQuery, "per_page=10")
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"results":[{"id":"https://openalex.org/W999","title":"OpenAlex Paper","doi":"","publication_year":0,"open_access":{"is_oa":false,"oa_url":""},"primary_location":{"source":{"display_name":""}}}]}`)
				return rec.Result(), nil
			}),
		}
		p := NewOpenAlexProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "openalex", papers[0].Source)
		assert.Equal(t, "OpenAlex", papers[0].Venue)
		assert.Empty(t, papers[0].OpenAccessUrl)
	})

	t.Run("Search uses open access URL when available", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"results":[{"id":"https://openalex.org/W321","title":"OA Paper","doi":"","publication_year":2024,"open_access":{"is_oa":true,"oa_url":"https://oa.example.com/paper.pdf"},"primary_location":{"source":{"display_name":"Journal of Testing"}}}]}`)
				return rec.Result(), nil
			}),
		}
		p := NewOpenAlexProvider()
		papers, err := p.Search(context.Background(), "oa", SearchOpts{Limit: 1})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "https://oa.example.com/paper.pdf", papers[0].OpenAccessUrl)
		assert.Equal(t, "openalex", papers[0].Source)
		assert.Equal(t, "Journal of Testing", papers[0].Venue)
	})

	t.Run("Search falls back to raw author name and landing page link", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"results":[{"id":"https://openalex.org/W654","title":"Fallback Author Paper","doi":"https://doi.org/10.9/fallback","publication_year":2023,"primary_location":{"landing_page_url":"https://paper.example.com/fallback","pdf_url":"https://paper.example.com/fallback.pdf","source":{"display_name":"Journal of Fallbacks"}},"open_access":{"is_oa":true,"oa_url":"https://oa.example.com/fallback.pdf"},"authorships":[{"author":{"display_name":""},"raw_author_name":"Fallback Author"}]}]}`)
				return rec.Result(), nil
			}),
		}
		p := NewOpenAlexProvider()
		papers, err := p.Search(context.Background(), "fallback", SearchOpts{Limit: 1})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, []string{"Fallback Author"}, papers[0].Authors)
		assert.Equal(t, "https://paper.example.com/fallback", papers[0].Link)
		assert.Equal(t, "https://paper.example.com/fallback.pdf", papers[0].PdfUrl)
		assert.Equal(t, "https://oa.example.com/fallback.pdf", papers[0].OpenAccessUrl)
	})

	t.Run("Search skips blank author entries", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"results":[{"id":"https://openalex.org/W777","title":"Blank Author Paper","publication_year":2024,"primary_location":{"source":{"display_name":"Journal of Blank Authors"}},"authorships":[{"author":{"display_name":""},"raw_author_name":""},{"author":{"display_name":"Ada Lovelace"},"raw_author_name":""}]}]}`)
				return rec.Result(), nil
			}),
		}
		p := NewOpenAlexProvider()
		papers, err := p.Search(context.Background(), "blank", SearchOpts{Limit: 1})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, []string{"Ada Lovelace"}, papers[0].Authors)
	})

	t.Run("Transport Error", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("boom")
			}),
		}
		p := NewOpenAlexProvider()
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})

	t.Run("Build Request Error", func(t *testing.T) {
		p := NewOpenAlexProvider()
		p.baseURL = "http://[::1"
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})
}

func TestReconstructOpenAlexAbstract(t *testing.T) {
	assert.Equal(t, "", reconstructOpenAlexAbstract(nil))
	assert.Equal(t, "", reconstructOpenAlexAbstract(map[string][]int{"ignored": {-1}}))
	assert.Equal(t, "Repeated word repeated", reconstructOpenAlexAbstract(map[string][]int{
		"Repeated": {0},
		"word":     {1},
		"repeated": {2},
	}))
	assert.Equal(t, "handles gaps", reconstructOpenAlexAbstract(map[string][]int{
		"handles": {0},
		"gaps":    {3},
	}))
}
