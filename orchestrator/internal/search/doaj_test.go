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
								"year":"2024",
								"identifier":[{"type":"doi","id":"10.1"}],
								"link":[{"type":"fulltext","url":"http://full"}],
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
}
