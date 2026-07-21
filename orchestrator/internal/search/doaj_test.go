package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDOAJProvider_Mocked(t *testing.T) {
	origClient := SharedHTTPClient
	defer func() { SharedHTTPClient = origClient }()

	t.Run("Name and Domains", func(t *testing.T) {
		p := NewDOAJProvider()
		assert.Equal(t, "doaj", p.Name())
		assert.NotEmpty(t, p.Domains())
	})

	t.Run("Search Success", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{
					"results": [
						{
							"id":"1",
							"bibjson": {
								"title":"DOAJ Paper",
								"abstract":"Open abstract",
								"year":"2024",
								"journal":{"title":"Open Journal"},
								"identifier":[{"type":"doi","id":"10.1"}],
								"link":[{"type":"fulltext","url":"http://full"},{"content_type":"application/pdf","url":"http://full.pdf"}],
								"author":[{"name":"A1"}]
							}
						},
						{
							"id":"2",
							"bibjson": {
								"title":"Paper 2",
								"link":[{"url":"http://fallback"}]
							}
						}
					]
				}`)
				return rec.Result(), nil
			}),
		}
		p := NewDOAJProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{Limit: 100}) // test limit clamping
		assert.NoError(t, err)
		assert.Len(t, papers, 2)
		assert.Equal(t, "doaj:1", papers[0].ID)
		assert.Equal(t, "10.1", papers[0].DOI)
		assert.Equal(t, "http://full", papers[0].Link)
		assert.Equal(t, []string{"doaj"}, papers[0].SourceApis)
		assert.Equal(t, "Open Journal", papers[0].Venue)
		assert.Equal(t, "http://full", papers[0].OpenAccessUrl)
		assert.Equal(t, "http://full.pdf", papers[0].PdfUrl)
		assert.Equal(t, "http://fallback", papers[1].Link)
	})

	t.Run("HTTP Error", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.WriteHeader(http.StatusNotFound)
				return rec.Result(), nil
			}),
		}
		p := NewDOAJProvider()
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})

	t.Run("Transport and Decode Errors", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/v4/search/articles/transport" {
					return nil, fmt.Errorf("boom")
				}
				rec := httptest.NewRecorder()
				fmt.Fprint(rec, `invalid`)
				return rec.Result(), nil
			}),
		}
		p := NewDOAJProvider()
		_, err := p.Search(context.Background(), "transport", SearchOpts{})
		assert.Error(t, err)
		_, err = p.Search(context.Background(), "decode", SearchOpts{})
		assert.Error(t, err)
	})

	t.Run("Search skips blank titles", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"results":[{"id":"1","bibjson":{"title":"","year":"2024"}},{"id":"2","bibjson":{"title":"Keep","year":"2024"}}]}`)
				return rec.Result(), nil
			}),
		}
		p := NewDOAJProvider()
		papers, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "Keep", papers[0].Title)
	})

	t.Run("Search skips blank links and uses PDF fallback", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"results":[{"id":"3","bibjson":{"title":"PDF Only","link":[{"url":"","type":"fulltext"},{"url":"https://doaj.example/paper.pdf","content_type":"application/pdf"}],"author":[{"name":"Pdf Author"}]}}]}`)
				return rec.Result(), nil
			}),
		}
		p := NewDOAJProvider()
		papers, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "https://doaj.example/paper.pdf", papers[0].Link)
		assert.Equal(t, "https://doaj.example/paper.pdf", papers[0].OpenAccessUrl)
		assert.Equal(t, "https://doaj.example/paper.pdf", papers[0].PdfUrl)
	})

	t.Run("Build Request Error", func(t *testing.T) {
		p := NewDOAJProvider()
		p.baseURL = "http://[::1"
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})
}
