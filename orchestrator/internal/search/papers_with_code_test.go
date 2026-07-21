package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPapersWithCodeProvider(t *testing.T) {
	origClient := SharedHTTPClient
	defer func() { SharedHTTPClient = origClient }()

	t.Run("Search Success", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{
					"results": [
						{
							"id":"paper-1",
							"arxiv_id":"2401.00001",
							"title":"Paper With Code",
							"abstract":"Test abstract",
							"url_abs":"https://paperswithcode.com/paper/paper-1",
							"url_pdf":"https://paperswithcode.com/paper/paper-1.pdf",
							"published":"2024-01-01",
							"authors":["Author One"],
							"stars":42
						}
					]
				}`)
				res := rec.Result()
				res.Request = req
				return res, nil
			}),
		}

		p := NewPapersWithCodeProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{Limit: 5})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "arxiv:2401.00001", papers[0].ID)
		assert.Equal(t, "papers_with_code", papers[0].Source)
		assert.Equal(t, []string{"papers_with_code"}, papers[0].SourceApis)
		assert.Equal(t, "https://paperswithcode.com/paper/paper-1.pdf", papers[0].OpenAccessUrl)
		assert.Equal(t, "https://paperswithcode.com/paper/paper-1.pdf", papers[0].PdfUrl)
	})

	t.Run("Redirected Endpoint Returns Explicit Error", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "text/html; charset=utf-8")
				fmt.Fprint(rec, "<html>redirected</html>")
				res := rec.Result()
				res.Request = &http.Request{URL: &url.URL{Scheme: "https", Host: "huggingface.co", Path: "/papers/trending"}}
				return res, nil
			}),
		}

		p := NewPapersWithCodeProvider()
		_, err := p.Search(context.Background(), "test", SearchOpts{Limit: 5})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), papersWithCodeRedirectError)
	})

	t.Run("Search HTTP Error and Blank Title Skip", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.RawQuery == "items_per_page=5&q=test" {
					rec := httptest.NewRecorder()
					rec.WriteHeader(http.StatusBadGateway)
					return rec.Result(), nil
				}
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"results":[{"id":"skip","title":""},{"id":"keep","title":"Keep","url_abs":"https://example.com"}]}`)
				res := rec.Result()
				res.Request = req
				return res, nil
			}),
		}

		p := NewPapersWithCodeProvider()
		_, err := p.Search(context.Background(), "test", SearchOpts{Limit: 5})
		assert.Error(t, err)

		papers, err := p.Search(context.Background(), "keep", SearchOpts{Limit: 5})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "Keep", papers[0].Title)
	})

	t.Run("Build Request Error", func(t *testing.T) {
		p := NewPapersWithCodeProvider()
		p.baseURL = "http://[::1"
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})
}
