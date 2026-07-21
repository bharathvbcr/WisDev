package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEuropePMCProvider(t *testing.T) {
	origClient := SharedHTTPClient
	defer func() { SharedHTTPClient = origClient }()

	t.Run("Name and Domains", func(t *testing.T) {
		p := NewEuropePMCProvider()
		assert.Equal(t, "europe_pmc", p.Name())
		assert.NotEmpty(t, p.Domains())
	})

	t.Run("Search Success", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{
					"resultList": {
						"result": [
							{"id":"1", "title":"T1", "pmcid":"PMC123", "pubYear":"2024", "authorString":"Ada Lovelace; Grace Hopper"},
							{"id":"2", "title":"T2", "pmid":"456", "authorString":"Katherine Johnson"},
							{"id":"3", "title":"T3", "doi":"10.1"},
							{"id":"4", "title":""}
						]
					}
				}`)
				return rec.Result(), nil
			}),
		}
		p := NewEuropePMCProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{})
		assert.NoError(t, err)
		assert.Len(t, papers, 3)
		assert.Equal(t, "https://europepmc.org/article/pmc/123", papers[0].Link)
		assert.Equal(t, "https://europepmc.org/article/med/456", papers[1].Link)
		assert.Equal(t, "https://doi.org/10.1", papers[2].Link)
		assert.Equal(t, 2024, papers[0].Year)
		assert.Equal(t, []string{"Ada Lovelace", "Grace Hopper"}, papers[0].Authors)
		assert.Equal(t, []string{"Katherine Johnson"}, papers[1].Authors)
		assert.Equal(t, []string{"europe_pmc"}, papers[0].SourceApis)
	})

	t.Run("HTTP Error", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.WriteHeader(http.StatusServiceUnavailable)
				return rec.Result(), nil
			}),
		}
		p := NewEuropePMCProvider()
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
		p := NewEuropePMCProvider()
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})

	t.Run("Request Error", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("boom")
			}),
		}
		p := NewEuropePMCProvider()
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})

	t.Run("Build Request Error", func(t *testing.T) {
		p := NewEuropePMCProvider()
		p.baseURL = "http://[::1"
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})
}
