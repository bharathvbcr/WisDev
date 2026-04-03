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

func TestPubMedProvider(t *testing.T) {
	origClient := SharedHTTPClient
	defer func() { SharedHTTPClient = origClient }()

	t.Run("Name and Domains", func(t *testing.T) {
		p := NewPubMedProvider()
		assert.Equal(t, "pubmed", p.Name())
		assert.Contains(t, p.Domains(), "medicine")
	})

	t.Run("Search Success", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				if strings.Contains(req.URL.Path, "esearch") {
					fmt.Fprint(rec, `{"esearchresult":{"idlist":["123","456"]}}`)
				} else if strings.Contains(req.URL.Path, "esummary") {
					fmt.Fprint(rec, `{"result":{"123":{"title":"P1","articleids":[{"idtype":"doi","value":"10.1/1"}]},"456":{"title":"P2"}}}`)
				}
				return rec.Result(), nil
			}),
		}

		p := NewPubMedProvider()
		// Also test year filtering
		papers, err := p.Search(context.Background(), "test", SearchOpts{YearFrom: 2020, YearTo: 2022})
		assert.NoError(t, err)
		assert.Len(t, papers, 2)
		assert.Equal(t, "pubmed:123", papers[0].ID)
		assert.Equal(t, "10.1/1", papers[0].DOI)
	})

	t.Run("Search No Results", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"esearchresult":{"idlist":[]}}`)
				return rec.Result(), nil
			}),
		}
		p := NewPubMedProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{YearFrom: 2020})
		assert.NoError(t, err)
		assert.Empty(t, papers)
	})

	t.Run("Search HTTP Error", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.WriteHeader(http.StatusForbidden)
				return rec.Result(), nil
			}),
		}
		p := NewPubMedProvider()
		_, err := p.Search(context.Background(), "test", SearchOpts{})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "search HTTP 403")
	})

	t.Run("Summary HTTP Error", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				if strings.Contains(req.URL.Path, "esearch") {
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, `{"esearchresult":{"idlist":["123"]}}`)
				} else {
					rec.WriteHeader(http.StatusGatewayTimeout)
				}
				return rec.Result(), nil
			}),
		}
		p := NewPubMedProvider()
		_, err := p.Search(context.Background(), "test", SearchOpts{})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "summary HTTP 504")
	})
}
