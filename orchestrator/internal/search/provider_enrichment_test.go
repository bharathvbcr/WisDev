package search

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeDOIAndArxiv(t *testing.T) {
	assert.Equal(t, "10.1000/example", NormalizeDOI("https://doi.org/10.1000/example"))
	assert.Equal(t, "10.48550/arXiv.2310.12773", NormalizeDOI("doi:10.48550/arXiv.2310.12773"))
	assert.Equal(t, "2310.12773", ExtractArxivIDFromDOI("10.48550/arXiv.2310.12773"))
	assert.True(t, IsLikelyRetractionProviderMissDOI("10.48550/arXiv.2204.05862"))
	assert.False(t, IsLikelyRetractionProviderMissDOI("10.1038/nature12373"))
}

func TestLookupOpenAccess(t *testing.T) {
	ResetOpenAccessCacheForTests()
	orig := SharedHTTPClient
	t.Cleanup(func() {
		SharedHTTPClient = orig
		ResetOpenAccessCacheForTests()
	})

	t.Run("arxiv fallback without network", func(t *testing.T) {
		info, err := LookupOpenAccess(context.Background(), "10.48550/arXiv.2310.12773")
		require.NoError(t, err)
		require.NotNil(t, info)
		assert.True(t, info.IsOA)
		assert.Equal(t, "arxiv_fallback", info.Source)
		assert.Contains(t, info.BestOALocation.URLForPDF, "arxiv.org/pdf/2310.12773")
	})

	t.Run("success from unpaywall", func(t *testing.T) {
		ResetOpenAccessCacheForTests()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Contains(t, r.URL.Path, "10.1000/example")
			fmt.Fprint(w, `{"doi":"10.1000/example","is_oa":true,"oa_status":"gold","best_oa_location":{"url":"https://oa.example/p.pdf","url_for_pdf":"https://oa.example/p.pdf","is_best":true}}`)
		}))
		t.Cleanup(srv.Close)
		SharedHTTPClient = &http.Client{Transport: rewriteHostTransport(srv.URL)}

		info, err := LookupOpenAccess(context.Background(), "10.1000/example")
		require.NoError(t, err)
		require.NotNil(t, info)
		assert.True(t, info.IsOA)
		assert.Equal(t, "unpaywall", info.Source)
	})

	t.Run("404 -> nil", func(t *testing.T) {
		ResetOpenAccessCacheForTests()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(srv.Close)
		SharedHTTPClient = &http.Client{Transport: rewriteHostTransport(srv.URL)}

		info, err := LookupOpenAccess(context.Background(), "10.1000/missing")
		require.NoError(t, err)
		assert.Nil(t, info)
	})

	t.Run("upstream 500", func(t *testing.T) {
		ResetOpenAccessCacheForTests()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)
		SharedHTTPClient = &http.Client{Transport: rewriteHostTransport(srv.URL)}

		_, err := LookupOpenAccess(context.Background(), "10.1000/fail")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "upstream")
	})

	t.Run("empty doi", func(t *testing.T) {
		info, err := LookupOpenAccess(context.Background(), "   ")
		require.NoError(t, err)
		assert.Nil(t, info)
	})
}

func TestCheckRetractionsBatch(t *testing.T) {
	ResetRetractionCacheForTests()
	orig := SharedHTTPClient
	t.Cleanup(func() {
		SharedHTTPClient = orig
		ResetRetractionCacheForTests()
	})

	t.Run("skip arxiv dois", func(t *testing.T) {
		results, err := CheckRetractionsBatch(context.Background(), []string{"10.48550/arXiv.2204.05862"})
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, "skip", results[0].Source)
		assert.False(t, results[0].IsRetracted)
	})

	t.Run("openretractions hit", func(t *testing.T) {
		ResetRetractionCacheForTests()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"retraction_date":"2024-01-01","retraction_nature":"error","retraction_doi":"10.9/notice"}`)
		}))
		t.Cleanup(srv.Close)
		SharedHTTPClient = &http.Client{Transport: rewriteHostTransport(srv.URL)}

		results, err := CheckRetractionsBatch(context.Background(), []string{"10.1038/nature12373"})
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.True(t, results[0].IsRetracted)
		assert.Equal(t, "openretractions", results[0].Source)
	})

	t.Run("too many dois", func(t *testing.T) {
		dois := make([]string, 30)
		for i := range dois {
			dois[i] = fmt.Sprintf("10.1/%d", i)
		}
		_, err := CheckRetractionsBatch(context.Background(), dois)
		require.Error(t, err)
	})
}

func TestLookupBibliometrics(t *testing.T) {
	orig := SharedHTTPClient
	t.Cleanup(func() { SharedHTTPClient = orig })

	t.Run("author success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"id":"https://openalex.org/A1","display_name":"Ada","works_count":10,"cited_by_count":100,"summary_stats":{"h_index":12,"i10_index":5,"2yr_mean_citedness":3.5},"affiliations":[],"orcid":null}`)
		}))
		t.Cleanup(srv.Close)
		SharedHTTPClient = &http.Client{Transport: rewriteHostTransport(srv.URL)}

		metrics := LookupAuthorMetrics(context.Background(), "A1")
		assert.True(t, metrics.Success)
		assert.Equal(t, 12, metrics.HIndex)
		assert.Equal(t, "Ada", metrics.DisplayName)
	})

	t.Run("author upstream failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		t.Cleanup(srv.Close)
		SharedHTTPClient = &http.Client{Transport: rewriteHostTransport(srv.URL)}

		metrics := LookupAuthorMetrics(context.Background(), "A9")
		assert.False(t, metrics.Success)
		assert.Contains(t, metrics.Error, "502")
	})

	t.Run("source success by issn", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"results":[{"id":"https://openalex.org/S1","display_name":"Nature","works_count":1,"cited_by_count":2,"type":"journal","is_oa":false,"issn":["1234-5678"],"summary_stats":{"h_index":40,"2yr_mean_citedness":9.1}}]}`)
		}))
		t.Cleanup(srv.Close)
		SharedHTTPClient = &http.Client{Transport: rewriteHostTransport(srv.URL)}

		metrics := LookupSourceMetrics(context.Background(), "1234-5678")
		assert.True(t, metrics.Success)
		assert.Equal(t, 40, metrics.HIndex)
		assert.Equal(t, "Nature", metrics.DisplayName)
	})

	t.Run("empty author id", func(t *testing.T) {
		metrics := LookupAuthorMetrics(context.Background(), "  ")
		assert.False(t, metrics.Success)
	})
}

func TestSemanticScholarGetReferences(t *testing.T) {
	orig := SharedHTTPClient
	t.Cleanup(func() { SharedHTTPClient = orig })

	t.Run("success", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				assert.Contains(t, req.URL.Path, "/references")
				assert.Equal(t, semanticScholarReferenceFields, req.URL.Query().Get("fields"))
				body := `{"data":[{"citedPaper":{"paperId":"ref1","title":"Foundational","year":2017,"citationCount":10}}]}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			}),
		}
		p := NewSemanticScholarProvider()
		papers, err := p.GetReferences(context.Background(), "s2:abc", 0)
		require.NoError(t, err)
		require.Len(t, papers, 1)
		assert.Equal(t, "Foundational", papers[0].Title)
	})

	t.Run("upstream failure", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: 502,
					Body:       io.NopCloser(bytes.NewReader(nil)),
					Header:     make(http.Header),
				}, nil
			}),
		}
		p := NewSemanticScholarProvider()
		_, err := p.GetReferences(context.Background(), "abc", 1)
		require.Error(t, err)
	})
}

func TestRegistryGetReferences(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&testCitationProvider{name: "semantic_scholar", healthy: true, papers: []Paper{{ID: "r1"}}})
	res, err := reg.GetReferences(context.Background(), "paper-1", 3)
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Equal(t, "r1", res[0].ID)
}

func rewriteHostTransport(targetBase string) http.RoundTripper {
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		cloned := req.Clone(req.Context())
		target, err := http.NewRequestWithContext(req.Context(), req.Method, targetBase+req.URL.RequestURI(), req.Body)
		if err != nil {
			return nil, err
		}
		target.Header = cloned.Header
		return http.DefaultTransport.RoundTrip(target)
	})
}
