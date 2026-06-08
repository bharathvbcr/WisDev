package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDBLPProviderBranches(t *testing.T) {
	origClient := SharedHTTPClient
	defer func() { SharedHTTPClient = origClient }()

	t.Run("Search Success and Fallbacks", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				assert.Contains(t, req.URL.RawQuery, "h=15")
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"result":{"hits":{"hit":[
					{"info":{"key":"journals/corr/abs-2601-15509","title":"DBLP Paper","authors":{"author":{"@pid":"145/2060","text":"Ada"}},"venue":"CoRR","year":"2024","doi":"","url":"https://dblp.org/rec/journals/corr/abs-2601-15509","ee":"https://example.com"}},
					{"info":{"title":"DBLP Paper 2","authors":{"author":["Grace","Ada"]},"year":"2023","doi":"10.1","url":"","ee":"https://example.com/doi"}},
					{"info":{"title":"","authors":{"author":["Skip"]},"year":"2023"}}
				]}}}`)
				return rec.Result(), nil
			}),
		}
		p := NewDBLPProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{})
		assert.NoError(t, err)
		assert.Len(t, papers, 2)
		assert.Equal(t, "dblp:journals/corr/abs-2601-15509", papers[0].ID)
		assert.Equal(t, []string{"Ada"}, papers[0].Authors)
		assert.Equal(t, "CoRR", papers[0].Venue)
		assert.Equal(t, []string{"dblp"}, papers[0].SourceApis)
		assert.Equal(t, "https://dblp.org/rec/journals/corr/abs-2601-15509", papers[0].Link)
		assert.Equal(t, "dblp:10.1", papers[1].ID)
		assert.Equal(t, []string{"Grace", "Ada"}, papers[1].Authors)
		assert.Equal(t, "https://example.com/doi", papers[1].Link)
	})

	t.Run("Link and title ID fallbacks", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"result":{"hits":{"hit":[
					{"info":{"title":"Link Paper","authors":{"author":"Ada"},"year":"2024","doi":"","url":"https://dblp.example/link","ee":"https://dblp.example/ee"}},
					{"info":{"title":"Title Only","authors":{"author":"Grace"},"year":"2023","doi":"","url":"","ee":""}}
				]}}}`)
				return rec.Result(), nil
			}),
		}
		p := NewDBLPProvider()
		papers, err := p.Search(context.Background(), "fallbacks", SearchOpts{})
		assert.NoError(t, err)
		assert.Len(t, papers, 2)
		assert.Equal(t, "dblp:https://dblp.example/link", papers[0].ID)
		assert.Equal(t, "dblp:Title Only", papers[1].ID)
	})

	t.Run("Request Status Decode and Build Errors", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch {
				case req.URL.Query().Get("q") == "request":
					return nil, fmt.Errorf("boom")
				case req.URL.Query().Get("q") == "status":
					rec := httptest.NewRecorder()
					rec.WriteHeader(http.StatusBadGateway)
					return rec.Result(), nil
				case req.URL.Query().Get("q") == "decode":
					rec := httptest.NewRecorder()
					fmt.Fprint(rec, `invalid`)
					return rec.Result(), nil
				default:
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, `{"result":{"hits":{"hit":[]}}}`)
					return rec.Result(), nil
				}
			}),
		}
		p := NewDBLPProvider()
		_, err := p.Search(context.Background(), "request", SearchOpts{})
		assert.Error(t, err)
		_, err = p.Search(context.Background(), "status", SearchOpts{})
		assert.Error(t, err)
		_, err = p.Search(context.Background(), "decode", SearchOpts{})
		assert.Error(t, err)

		bad := NewDBLPProvider()
		bad.baseURL = "://bad"
		_, err = bad.Search(context.Background(), "query", SearchOpts{})
		assert.Error(t, err)
	})
}
