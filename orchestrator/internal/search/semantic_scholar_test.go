package search

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSemanticScholarProvider(t *testing.T) {
	origClient := SharedHTTPClient
	origNewRequestWithContext := newRequestWithContext
	defer func() { SharedHTTPClient = origClient }()
	defer func() { newRequestWithContext = origNewRequestWithContext }()

	t.Run("Name and Tools", func(t *testing.T) {
		p := NewSemanticScholarProvider()
		assert.Equal(t, "semantic_scholar", p.Name())
		assert.ElementsMatch(t, []string{"author_lookup", "paper_lookup"}, p.Tools())
		assert.NotEmpty(t, p.Domains())
	})

	t.Run("Search Success", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				assert.Equal(t, semanticScholarPaperFields, req.URL.Query().Get("fields"))
				fmt.Fprint(rec, `{"data":[{"paperId":"1","title":"T1","authors":[{"name":"A1"}],"venue":"Venue One","openAccessPdf":{"url":"https://example.com/p1.pdf"}}]}`)
				return rec.Result(), nil
			}),
		}
		p := NewSemanticScholarProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{YearFrom: 2020})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "s2:1", papers[0].ID)
		assert.Equal(t, "https://example.com/p1.pdf", papers[0].PdfUrl)
		assert.Equal(t, "Venue One", papers[0].Venue)
		assert.Equal(t, []string{"semantic_scholar"}, papers[0].SourceApis)
	})

	t.Run("Search defaults limit and adds API key", func(t *testing.T) {
		t.Setenv("SEMANTIC_SCHOLAR_API_KEY", "api-key")
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				assert.Equal(t, "api-key", req.Header.Get("x-api-key"))
				assert.Contains(t, req.URL.RawQuery, "limit=10")
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"data":[]}`)
				return rec.Result(), nil
			}),
		}
		p := NewSemanticScholarProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{})
		assert.NoError(t, err)
		assert.Empty(t, papers)
	})

	t.Run("Search Success YearRange", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				assert.Contains(t, req.URL.RawQuery, "year=2020-2022")
				rec := httptest.NewRecorder()
				fmt.Fprint(rec, `{"data":[]}`)
				return rec.Result(), nil
			}),
		}
		p := NewSemanticScholarProvider()
		_, _ = p.Search(context.Background(), "test", SearchOpts{YearFrom: 2020, YearTo: 2022})
	})

	t.Run("SearchByAuthor Success", func(t *testing.T) {
		t.Setenv("SEMANTIC_SCHOLAR_API_KEY", "api-key")
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				assert.Equal(t, "api-key", req.Header.Get("x-api-key"))
				assert.Equal(t, semanticScholarPaperFields, req.URL.Query().Get("fields"))
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"data":[{"paperId":"auth1","title":"Author Paper","authors":[{"name":" Author One "}],"venue":"Author Venue","openAccessPdf":{"url":"https://example.com/auth1.pdf"}}]}`)
				return rec.Result(), nil
			}),
		}
		p := NewSemanticScholarProvider()
		papers, err := p.SearchByAuthor(context.Background(), "123", 5)
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, []string{"Author One"}, papers[0].Authors)
		assert.Equal(t, "https://example.com/auth1.pdf", papers[0].PdfUrl)
		assert.Equal(t, "Author Venue", papers[0].Venue)
		assert.Equal(t, []string{"semantic_scholar"}, papers[0].SourceApis)
	})

	t.Run("SearchByAuthor DefaultLimit", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				assert.Contains(t, req.URL.RawQuery, "limit=20")
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"data":[]}`)
				return rec.Result(), nil
			}),
		}
		p := NewSemanticScholarProvider()
		papers, err := p.SearchByAuthor(context.Background(), "123", 0)
		assert.NoError(t, err)
		assert.Empty(t, papers)
	})

	t.Run("SearchByPaperID Success", func(t *testing.T) {
		t.Setenv("SEMANTIC_SCHOLAR_API_KEY", "api-key")
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				assert.Equal(t, "api-key", req.Header.Get("x-api-key"))
				assert.Equal(t, semanticScholarPaperFields, req.URL.Query().Get("fields"))
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"paperId":"p1","title":"Paper One","authors":[{"name":" A1 "},{"name":"A2"}],"venue":"Lookup Venue","referenceCount":7,"openAccessPdf":{"url":"https://example.com/p1.pdf"}}`)
				return rec.Result(), nil
			}),
		}
		p := NewSemanticScholarProvider()
		paper, err := p.SearchByPaperID(context.Background(), "s2:p1")
		assert.NoError(t, err)
		assert.Equal(t, "s2:p1", paper.ID)
		assert.Equal(t, []string{"A1", "A2"}, paper.Authors)
		assert.Equal(t, "Lookup Venue", paper.Venue)
		assert.Equal(t, 7, paper.ReferenceCount)
		assert.Equal(t, "https://example.com/p1.pdf", paper.PdfUrl)
		assert.Equal(t, []string{"semantic_scholar"}, paper.SourceApis)
	})

	t.Run("GetCitations Success", func(t *testing.T) {
		t.Setenv("SEMANTIC_SCHOLAR_API_KEY", "api-key")
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				assert.Equal(t, "api-key", req.Header.Get("x-api-key"))
				assert.Contains(t, req.URL.Path, "/paper/abc/citations")
				assert.Equal(t, semanticScholarCitationFields, req.URL.Query().Get("fields"))
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"data":[{"citingPaper":{"paperId":"c1","title":"Citation One","authors":[{"name":" Author C "}],"venue":"Citation Venue","referenceCount":4,"openAccessPdf":{"url":"https://example.com/c1.pdf"}}}]}`)
				return rec.Result(), nil
			}),
		}
		p := NewSemanticScholarProvider()
		papers, err := p.GetCitations(context.Background(), "s2:abc", 0)
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, []string{"Author C"}, papers[0].Authors)
		assert.Equal(t, "Citation Venue", papers[0].Venue)
		assert.Equal(t, 4, papers[0].ReferenceCount)
		assert.Equal(t, "https://example.com/c1.pdf", papers[0].PdfUrl)
		assert.Equal(t, []string{"semantic_scholar"}, papers[0].SourceApis)
	})

	t.Run("GetCitations Errors", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.WriteHeader(http.StatusUnauthorized)
				return rec.Result(), nil
			}),
		}
		p := NewSemanticScholarProvider()
		_, err := p.GetCitations(context.Background(), "abc", 1)
		assert.Error(t, err)

		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				fmt.Fprint(rec, `invalid`)
				return rec.Result(), nil
			}),
		}
		_, err = p.GetCitations(context.Background(), "abc", 1)
		assert.Error(t, err)
	})

	t.Run("Request Errors", func(t *testing.T) {
		orig := newRequestWithContext
		defer func() { newRequestWithContext = orig }()
		newRequestWithContext = func(context.Context, string, string, io.Reader) (*http.Request, error) {
			return nil, fmt.Errorf("request build failed")
		}

		p := NewSemanticScholarProvider()

		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)

		_, err = p.SearchByAuthor(context.Background(), "a", 0)
		assert.Error(t, err)

		_, err = p.SearchByPaperID(context.Background(), "p")
		assert.Error(t, err)

		_, err = p.GetCitations(context.Background(), "p", 0)
		assert.Error(t, err)
	})

	t.Run("Transport Errors", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("transport failed")
			}),
		}
		p := NewSemanticScholarProvider()
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)

		_, err = p.SearchByAuthor(context.Background(), "a", 1)
		assert.Error(t, err)

		_, err = p.SearchByPaperID(context.Background(), "p")
		assert.Error(t, err)

		_, err = p.GetCitations(context.Background(), "abc", 1)
		assert.Error(t, err)
	})

	t.Run("Errors", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.WriteHeader(http.StatusUnauthorized)
				return rec.Result(), nil
			}),
		}
		p := NewSemanticScholarProvider()

		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)

		_, err = p.SearchByAuthor(context.Background(), "a", 0)
		assert.Error(t, err)

		_, err = p.SearchByPaperID(context.Background(), "p")
		assert.Error(t, err)
	})

	t.Run("Classifies retry-after throttles on forbidden responses", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Retry-After", "15")
				rec.WriteHeader(http.StatusForbidden)
				return rec.Result(), nil
			}),
		}
		p := NewSemanticScholarProvider()
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "rate limit exceeded")
	})

	t.Run("Decode Errors", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				fmt.Fprint(rec, `invalid`)
				return rec.Result(), nil
			}),
		}
		p := NewSemanticScholarProvider()
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)

		_, err = p.SearchByPaperID(context.Background(), "p")
		assert.Error(t, err)
	})

	t.Run("SearchByAuthor Decode Errors", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `invalid`)
				return rec.Result(), nil
			}),
		}
		p := NewSemanticScholarProvider()
		_, err := p.SearchByAuthor(context.Background(), "author", 1)
		assert.Error(t, err)
	})
}
