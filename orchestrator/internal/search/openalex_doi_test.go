package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenAlexGetByDOI covers the provider DOI-resolution added for frontend-thinning
// Phase 2 (the Go replacement for openAlexService.getOpenAlexByDoi).
func TestOpenAlexGetByDOI(t *testing.T) {
	t.Run("resolves a work by DOI and maps to Paper", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"https://openalex.org/W42","title":"Graphene Review","doi":"https://doi.org/10.1/xyz","publication_year":2021,"cited_by_count":7,"open_access":{"is_oa":true,"oa_url":"https://example.org/paper.pdf"}}`))
		}))
		defer srv.Close()
		orig := SharedHTTPClient
		t.Cleanup(func() { SharedHTTPClient = orig })
		SharedHTTPClient = srv.Client()

		p := &OpenAlexProvider{baseURL: srv.URL}
		paper, err := p.GetByDOI(context.Background(), "https://doi.org/10.1/xyz")
		require.NoError(t, err)
		require.NotNil(t, paper)
		assert.Equal(t, "Graphene Review", paper.Title)
		assert.Equal(t, "10.1/xyz", paper.DOI) // doi.org prefix stripped
		assert.Equal(t, "openalex", paper.Source)
		assert.Equal(t, "https://example.org/paper.pdf", paper.OpenAccessUrl)
	})

	t.Run("returns nil (not an error) for an unknown DOI (404)", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()
		orig := SharedHTTPClient
		t.Cleanup(func() { SharedHTTPClient = orig })
		SharedHTTPClient = srv.Client()

		p := &OpenAlexProvider{baseURL: srv.URL}
		paper, err := p.GetByDOI(context.Background(), "10.9/missing")
		require.NoError(t, err)
		assert.Nil(t, paper)
	})

	t.Run("rejects an empty DOI", func(t *testing.T) {
		p := &OpenAlexProvider{baseURL: "https://api.openalex.org/works"}
		_, err := p.GetByDOI(context.Background(), "   ")
		assert.Error(t, err)
	})
}
