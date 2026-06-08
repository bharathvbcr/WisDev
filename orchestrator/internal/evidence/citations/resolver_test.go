package citations

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func makeResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    &http.Request{},
	}
}

type scriptedResolverTransport struct {
	mu     sync.Mutex
	counts map[string]int
}

func (t *scriptedResolverTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.counts == nil {
		t.counts = make(map[string]int)
	}
	key := req.URL.Host + req.URL.Path + "?" + req.URL.RawQuery
	t.counts[key]++

	switch {
	case strings.Contains(req.URL.Host, "crossref.org"):
		return makeResponse(http.StatusOK, `{
			"message": {
				"title": ["Crossref Title"],
				"URL": "https://crossref.example/title",
				"issued": {"date-parts": [[2024]]},
				"author": [{"given": "Ada", "family": "Lovelace"}]
			}
		}`), nil
	case strings.Contains(req.URL.Host, "api.openalex.org") && strings.Contains(req.URL.RawQuery, "filter=doi:"):
		if t.counts[key] == 1 {
			return makeResponse(http.StatusInternalServerError, `{"error":"temporary"}`), nil
		}
		return makeResponse(http.StatusOK, `{
			"results": [{
				"id": "https://openalex.org/W-DOI",
				"title": "OpenAlex DOI Title",
				"doi": "https://doi.org/10.1000/graph",
				"publication_year": 2024,
				"primary_location": {"landing_page_url": "https://openalex.example/doi"}
			}]
		}`), nil
	case strings.Contains(req.URL.Host, "api.openalex.org") && strings.Contains(req.URL.RawQuery, "filter=locations.landing_page_url:"):
		return makeResponse(http.StatusOK, `{
			"results": [{
				"id": "https://openalex.org/W-ARXIV",
				"title": "OpenAlex ArXiv Title",
				"doi": "10.1000/arxiv",
				"publication_year": 2024,
				"primary_location": {"landing_page_url": "https://openalex.example/arxiv"}
			}]
		}`), nil
	case strings.Contains(req.URL.Host, "api.openalex.org") && strings.Contains(req.URL.Path, "/works/"):
		return makeResponse(http.StatusOK, `{
			"id": "https://openalex.org/W-ID",
			"title": "OpenAlex ID Title",
			"doi": "10.1000/id",
			"publication_year": 2024,
			"primary_location": {"landing_page_url": "https://openalex.example/id"}
		}`), nil
	case strings.Contains(req.URL.Host, "semanticscholar.org"):
		return makeResponse(http.StatusOK, `{
			"paperId": "S2-123",
			"title": "Semantic Scholar Title",
			"year": 2024,
			"url": "https://semanticscholar.example/paper",
			"externalIds": {"DOI": "10.1000/graph", "ArXiv": "2301.12345"},
			"authors": [{"name": "Ada Lovelace"}]
		}`), nil
	case strings.Contains(req.URL.Host, "export.arxiv.org"):
		if t.counts[key] == 1 && strings.Contains(req.URL.RawQuery, "id_list=2301.12345") {
			return makeResponse(http.StatusOK, `<?xml version="1.0" encoding="UTF-8"?><feed><entry><title>Broken`), nil
		}
		return makeResponse(http.StatusOK, `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <title>ArXiv Title</title>
    <published>2024-01-01T00:00:00Z</published>
    <summary>ArXiv summary.</summary>
    <author><name>Ada Lovelace</name></author>
    <id>https://arxiv.org/abs/2301.12345</id>
  </entry>
</feed>`), nil
	default:
		return nil, fmt.Errorf("unexpected url: %s", req.URL.String())
	}
}

func TestValidateResolveInput(t *testing.T) {
	currentYear := time.Now().Year()

	cases := []struct {
		name    string
		input   ResolveInput
		wantErr string
	}{
		{name: "missing identifiers", input: ResolveInput{}, wantErr: "input must contain at least one"},
		{name: "title too long", input: ResolveInput{Title: strings.Repeat("a", 513)}, wantErr: "title too long"},
		{name: "year too early", input: ResolveInput{Title: "x", Year: 1899}, wantErr: "invalid year"},
		{name: "year too late", input: ResolveInput{Title: "x", Year: currentYear + 2}, wantErr: "invalid year"},
		{name: "too many authors", input: ResolveInput{Title: "x", Year: currentYear, Authors: make([]string, 101)}, wantErr: "too many authors"},
		{name: "author too long", input: ResolveInput{Title: "x", Year: currentYear, Authors: []string{strings.Repeat("b", 257)}}, wantErr: "author 0 name too long"},
		{name: "valid", input: ResolveInput{Title: "x", Year: currentYear, Authors: []string{"Ada"}}, wantErr: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateResolveInput(tc.input)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestResolverHelpers(t *testing.T) {
	r := NewResolver("research@example.com")
	assert.NotNil(t, r)
	assert.NotNil(t, r.logger)

	same := r.WithLogger(nil)
	assert.Same(t, r, same)

	assert.Equal(t, "10.1000/graph", normalizeDOI(" https://doi.org/10.1000/graph "))
	assert.Equal(t, "", normalizeDOI("not-a-doi"))
	assert.Equal(t, "2301.12345", normalizeArxivID("ArXiv:2301.12345"))
	assert.Equal(t, "", normalizeArxivID("bad id"))
	assert.Equal(t, "graph neural networks", normalizeTitle("Graph, Neural Networks!"))
	assert.Equal(t, "alpha", firstNonEmpty("", " alpha ", "beta"))
	assert.Equal(t, "doi:10.1000/graph", formatID("doi", "10.1000/graph"))
	assert.Equal(t, 0.98, confidenceFor(&ResolvedCitation{DOI: "10.1000/graph"}))
	assert.Equal(t, 0.94, confidenceFor(&ResolvedCitation{ArxivID: "2301.12345"}))
	assert.Equal(t, 0.9, confidenceFor(&ResolvedCitation{OpenAlexID: "W1"}))
	assert.Equal(t, 0.86, confidenceFor(&ResolvedCitation{SemanticScholarID: "S1"}))
	assert.Equal(t, 0.72, confidenceFor(&ResolvedCitation{Title: "Title"}))
	assert.Equal(t, 0.5, confidenceFor(&ResolvedCitation{}))
	assert.True(t, isTransientDecodingError(io.EOF))
	assert.True(t, isTransientDecodingError(fmt.Errorf("unexpected EOF")))
	assert.False(t, isTransientDecodingError(fmt.Errorf("bad json")))
	assert.Equal(t, "abc", endpointPreview("abc", 10))
	assert.Equal(t, "abc", endpointPreview("abcdef", 3))
	assert.Equal(t, 0*time.Millisecond, backoffDuration(0))
	assert.Greater(t, backoffDuration(1), 0*time.Millisecond)
	assert.Greater(t, backoffDuration(2), backoffDuration(1))
	assert.NotEmpty(t, citationFingerprint(ResolvedCitation{Title: "Graph Neural Networks", DOI: "10.1000/graph", Year: 2024}))
	assert.Empty(t, citationFingerprint(ResolvedCitation{}))
}

func TestEvaluatePromotion(t *testing.T) {
	t.Run("no results", func(t *testing.T) {
		verdict := EvaluatePromotion(nil, 2)
		assert.False(t, verdict.Promoted)
		assert.Contains(t, verdict.ConflictNote, "no resolution results")
	})

	t.Run("agreement promotes citation", func(t *testing.T) {
		results := []ResolvedCitation{
			{Title: "Graph Neural Networks", DOI: "10.1000/graph", Year: 2024, ResolutionEngine: "crossref", CanonicalID: "doi:10.1000/graph"},
			{Title: "Graph Neural Networks", DOI: "10.1000/graph", Year: 2024, ResolutionEngine: "openalex", CanonicalID: "doi:10.1000/graph"},
			{Title: "Graph Neural Networks", DOI: "10.1000/graph", Year: 2024, ResolutionEngine: "semantic_scholar", CanonicalID: "doi:10.1000/graph"},
		}
		verdict := EvaluatePromotion(results, 2)
		assert.True(t, verdict.Promoted)
		assert.Equal(t, "doi:10.1000/graph", verdict.Canonical.CanonicalID)
		assert.Len(t, verdict.AgreementSources, 3)
	})
}

func TestResolverResolve(t *testing.T) {
	r := NewResolver("")
	r.httpClient = &http.Client{Transport: &scriptedResolverTransport{}}

	ctx := context.Background()
	res, err := r.Resolve(ctx, ResolveInput{
		DOI:               "https://doi.org/10.1000/graph",
		ArxivID:           "arxiv:2301.12345",
		OpenAlexID:        "openalex:W-ID",
		SemanticScholarID: "s2:S2-123",
		Year:              2024,
		Authors:           []string{"Seed Author"},
	})
	require.NoError(t, err)
	assert.True(t, res.Resolved)
	assert.Equal(t, "doi:10.1000/graph", res.CanonicalID)
	assert.Equal(t, "10.1000/graph", res.DOI)
	assert.Equal(t, "2301.12345", res.ArxivID)
	assert.Equal(t, "openalex:W-ID", res.OpenAlexID)
	assert.Equal(t, "s2:S2-123", res.SemanticScholarID)
	assert.Equal(t, 2024, res.Year)
	assert.NotEmpty(t, res.Title)
	assert.NotEmpty(t, res.LandingURL)
	assert.NotEmpty(t, res.Authors)
}

func TestResolverResolveContextAndValidationErrors(t *testing.T) {
	r := NewResolver("")
	r.httpClient = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return makeResponse(http.StatusOK, `{}`), nil
	})}

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.Resolve(cancelCtx, ResolveInput{Title: "x", Year: time.Now().Year()})
	require.Error(t, err)

	_, err = r.Resolve(context.Background(), ResolveInput{})
	require.Error(t, err)
}

func TestResolverBackoffAndEndpointPreview(t *testing.T) {
	assert.Greater(t, backoffDuration(100), backoffDuration(2))
	assert.Equal(t, "", endpointPreview("abcdef", 0))
}

func TestResolverHTTPFailures(t *testing.T) {
	is := assert.New(t)
	
	t.Run("retry on 429", func(t *testing.T) {
		count := 0
		r := NewResolver("")
		r.httpClient = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			count++
			if count == 1 {
				return makeResponse(http.StatusTooManyRequests, "rate limit"), nil
			}
			return makeResponse(http.StatusOK, `{"id":"W1"}`), nil
		})}
		
		var target struct{ ID string `json:"id"` }
		err := r.getJSON(context.Background(), "http://example.com", &target, nil)
		is.NoError(err)
		is.Equal(2, count)
		is.Equal("W1", target.ID)
	})

	t.Run("retry on 5xx", func(t *testing.T) {
		count := 0
		r := NewResolver("")
		r.httpClient = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			count++
			if count <= 2 {
				return makeResponse(http.StatusInternalServerError, "error"), nil
			}
			return makeResponse(http.StatusOK, `{"id":"W2"}`), nil
		})}
		
		var target struct{ ID string `json:"id"` }
		err := r.getJSON(context.Background(), "http://example.com", &target, nil)
		is.NoError(err)
		is.Equal(3, count)
	})

	t.Run("fail on 4xx", func(t *testing.T) {
		r := NewResolver("")
		r.httpClient = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return makeResponse(http.StatusNotFound, "not found"), nil
		})}
		
		var target any
		err := r.getJSON(context.Background(), "http://example.com", &target, nil)
		is.Error(err)
		is.Contains(err.Error(), "unexpected status 404")
	})
}

func TestResolverOpenAlexByTitle(t *testing.T) {
	is := assert.New(t)
	r := NewResolver("")
	r.httpClient = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		query := strings.ToLower(req.URL.RawQuery)
		if strings.Contains(query, "search=graph+neural") {
			return makeResponse(http.StatusOK, `{"results":[{"id":"https://openalex.org/W-TITLE","title":"Graph Neural Networks"}]}`), nil
		}
		return makeResponse(http.StatusOK, `{"results":[]}`), nil
	})}

	res := &ResolvedCitation{Title: "Graph Neural Networks"}
	r.enrichFromOpenAlexByTitle(context.Background(), res)
	is.Equal("W-TITLE", res.OpenAlexID)
}

func TestResolverXMLFailures(t *testing.T) {
	is := assert.New(t)
	r := NewResolver("")
	
	t.Run("retry on transient decoding error", func(t *testing.T) {
		count := 0
		r.httpClient = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			count++
			if count == 1 {
				return makeResponse(http.StatusOK, `<?xml version="1.0"?><feed><entry><title>Broken`), nil
			}
			return makeResponse(http.StatusOK, `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><entry><title>Fixed</title></entry></feed>`), nil
		})}
		
		var target any
		err := r.getXML(context.Background(), "http://example.com", &target)
		is.NoError(err)
		is.Equal(2, count)
	})
}
