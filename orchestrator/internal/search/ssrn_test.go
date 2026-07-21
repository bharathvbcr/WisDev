package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSSRNProvider_Mocked(t *testing.T) {
	origClient := SharedHTTPClient
	defer func() { SharedHTTPClient = origClient }()

	t.Run("Name and Domains", func(t *testing.T) {
		p := NewSSRNProvider()
		assert.Equal(t, "ssrn", p.Name())
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
							{"DOI":"10.2139/ssrn.1", "title":["SSRN Paper"], "abstract":"<p>Abs</p>"}
						]
					}
				}`)
				return rec.Result(), nil
			}),
		}
		p := NewSSRNProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "ssrn:10.2139/ssrn.1", papers[0].ID)
		assert.Equal(t, "Abs", papers[0].Abstract)
		assert.Equal(t, []string{"ssrn"}, papers[0].SourceApis)
	})

	t.Run("HTTP Error", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.WriteHeader(http.StatusGatewayTimeout)
				return rec.Result(), nil
			}),
		}
		p := NewSSRNProvider()
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})

	t.Run("Blank Title Skip and Author Parsing", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{
					"message": {
						"items": [
							{"DOI":"10.2139/ssrn.2", "title":[""], "abstract":"ignored"},
							{"DOI":"10.2139/ssrn.3", "title":["Keep"], "abstract":"<p>Abs</p>", "author":[{"given":"Ada","family":"Lovelace"}]}
						]
					}
				}`)
				return rec.Result(), nil
			}),
		}
		p := NewSSRNProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "Keep", papers[0].Title)
		assert.Equal(t, []string{"Ada Lovelace"}, papers[0].Authors)
	})

	t.Run("Build Request Error", func(t *testing.T) {
		p := NewSSRNProvider()
		p.baseURL = "http://[::1"
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})
}

func TestStripHTMLTags(t *testing.T) {
	assert.Equal(t, "Test", stripHTMLTags("<p>Test</p>"))
	assert.Equal(t, "No tags", stripHTMLTags("No tags"))
}
