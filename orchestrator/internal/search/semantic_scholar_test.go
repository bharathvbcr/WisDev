package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSemanticScholarProvider(t *testing.T) {
	origClient := SharedHTTPClient
	defer func() { SharedHTTPClient = origClient }()

	t.Run("Name and Tools", func(t *testing.T) {
		p := NewSemanticScholarProvider()
		assert.Equal(t, "semantic_scholar", p.Name())
		assert.ElementsMatch(t, []string{"author_lookup", "paper_lookup"}, p.Tools())
		assert.NotEmpty(t, p.Domains())
	})

	t.Run("Search Success", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"data":[{"paperId":"1","title":"T1","authors":[{"name":"A1"}]}]}`)
				return rec.Result(), nil
			}),
		}
		p := NewSemanticScholarProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{YearFrom: 2020})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "s2:1", papers[0].ID)
	})

	t.Run("Search Success YearRange", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				assert.Contains(t, req.URL.RawQuery, "year=2020-2022")
				rec := httptest.NewRecorder()
				fmt.Fprint(rec, `{"data":[]}`)
				return rec.Result(), nil
			}),
		}
		p := NewSemanticScholarProvider()
		_, _ = p.Search(context.Background(), "test", SearchOpts{YearFrom: 2020, YearTo: 2022})
	})

	t.Run("SearchByAuthor Success", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"data":[{"paperId":"auth1","title":"Author Paper"}]}`)
				return rec.Result(), nil
			}),
		}
		p := NewSemanticScholarProvider()
		papers, err := p.SearchByAuthor(context.Background(), "123", 5)
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
	})

	t.Run("SearchByPaperID Success", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"paperId":"p1","title":"Paper One"}`)
				return rec.Result(), nil
			}),
		}
		p := NewSemanticScholarProvider()
		paper, err := p.SearchByPaperID(context.Background(), "s2:p1")
		assert.NoError(t, err)
		assert.Equal(t, "s2:p1", paper.ID)
	})

	t.Run("Errors", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.WriteHeader(http.StatusUnauthorized)
				return rec.Result(), nil
			}),
		}
		p := NewSemanticScholarProvider()
		
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
		
		_, err = p.SearchByAuthor(context.Background(), "a", 0)
		assert.Error(t, err)
		
		_, err = p.SearchByPaperID(context.Background(), "p")
		assert.Error(t, err)
	})
	
	t.Run("Decode Errors", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				fmt.Fprint(rec, `invalid`)
				return rec.Result(), nil
			}),
		}
		p := NewSemanticScholarProvider()
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
		
		_, err = p.SearchByPaperID(context.Background(), "p")
		assert.Error(t, err)
	})
}
