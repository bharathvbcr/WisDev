package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCrossrefProvider(t *testing.T) {
	origClient := SharedHTTPClient
	defer func() { SharedHTTPClient = origClient }()

	t.Run("Name and Domains", func(t *testing.T) {
		p := NewCrossrefProvider()
		assert.Equal(t, "crossref", p.Name())
		assert.NotEmpty(t, p.Domains())
	})

	t.Run("Search Success", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{
					"message": {
						"items": [
							{
								"DOI":"10.1", 
								"title":["Crossref Paper"], 
								"abstract":"<p>Abstract</p>",
								"author":[{"given":"A","family":"B"}],
								"published":{"date-parts":[[2024,5]]},
								"container-title":["Journal of Testing"]
							},
							{"DOI":"10.2", "title":[]}
						]
					}
				}`)
				return rec.Result(), nil
			}),
		}
		p := NewCrossrefProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{YearFrom: 2020})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "crossref:10.1", papers[0].ID)
		assert.Equal(t, "Abstract", papers[0].Abstract)
		assert.Equal(t, 2024, papers[0].Year)
		assert.Equal(t, 5, papers[0].Month)
		assert.Equal(t, "Journal of Testing", papers[0].Venue)
		assert.Equal(t, []string{"A B"}, papers[0].Authors)
		assert.Equal(t, []string{"crossref"}, papers[0].SourceApis)
	})

	t.Run("HTTP Error", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.WriteHeader(http.StatusTooManyRequests)
				return rec.Result(), nil
			}),
		}
		p := NewCrossrefProvider()
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})

	t.Run("Transport and Decode Errors", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Query().Get("query") == "transport" {
					return nil, fmt.Errorf("boom")
				}
				rec := httptest.NewRecorder()
				fmt.Fprint(rec, `invalid`)
				return rec.Result(), nil
			}),
		}
		p := NewCrossrefProvider()
		_, err := p.Search(context.Background(), "transport", SearchOpts{})
		assert.Error(t, err)
		_, err = p.Search(context.Background(), "decode", SearchOpts{})
		assert.Error(t, err)
	})

	t.Run("Build Request Error", func(t *testing.T) {
		p := NewCrossrefProvider()
		p.baseURL = "http://[::1"
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})
}

func TestStripJATSTags(t *testing.T) {
	assert.Equal(t, "Hello World", stripJATSTags("<p>Hello <b>World</b></p>"))
	assert.Equal(t, "Plain", stripJATSTags("Plain"))
	assert.Equal(t, "   ", stripJATSTags("   "))
}
