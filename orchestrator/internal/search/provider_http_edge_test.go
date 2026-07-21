package search

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestNASAADSProviderBranches(t *testing.T) {
	t.Run("missing api key", func(t *testing.T) {
		t.Setenv("NASA_ADS_API_KEY", "")
		provider := NewNASAADSProvider()
		if _, err := provider.Search(context.Background(), "query", SearchOpts{Limit: 1}); err == nil {
			t.Fatal("expected missing key error")
		}
	})

	t.Run("success with filters and fallbacks", func(t *testing.T) {
		t.Setenv("NASA_ADS_API_KEY", "test-key")
		provider := &NASAADSProvider{apiKey: "test-key", baseURL: "https://api.adsabs.harvard.edu/v1/search/query"}

		orig := SharedHTTPClient
		t.Cleanup(func() { SharedHTTPClient = orig })
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				q := req.URL.Query().Get("q")
				if !strings.Contains(q, "year:2020-") || !strings.Contains(q, "year:-2024") {
					t.Fatalf("expected year filters in query, got %q", q)
				}

				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"response":{"docs":[{"bibcode":"B1","title":["Paper One"],"abstract":"A","author":["Ada"],"year":"2024","doi":["10.1/one"],"citation_count":7},{"bibcode":"B2","title":[],"abstract":"B","author":["Bob"],"year":"2023","citation_count":2}]}}`)
				res := rec.Result()
				res.Request = req
				return res, nil
			}),
		}

		papers, err := provider.Search(context.Background(), "astro", SearchOpts{Limit: 2, YearFrom: 2020, YearTo: 2024})
		if err != nil {
			t.Fatalf("unexpected search error: %v", err)
		}
		if len(papers) != 2 {
			t.Fatalf("expected 2 papers, got %+v", papers)
		}
		if papers[0].Link != "https://doi.org/10.1/one" {
			t.Fatalf("expected DOI link for first paper, got %+v", papers[0])
		}
		if papers[1].Title != "Untitled" || papers[1].Link != "https://ui.adsabs.harvard.edu/abs/B2" {
			t.Fatalf("expected fallback title/link, got %+v", papers[1])
		}
	})

	t.Run("request and decode errors", func(t *testing.T) {
		t.Setenv("NASA_ADS_API_KEY", "test-key")
		provider := &NASAADSProvider{apiKey: "test-key", baseURL: "https://api.adsabs.harvard.edu/v1/search/query"}

		orig := SharedHTTPClient
		t.Cleanup(func() { SharedHTTPClient = orig })
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch q := req.URL.Query().Get("q"); q {
				case "err=request":
					return nil, fmt.Errorf("boom")
				case "err=status":
					rec := httptest.NewRecorder()
					rec.WriteHeader(http.StatusBadGateway)
					return rec.Result(), nil
				default:
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, "{not-json}")
					res := rec.Result()
					res.Request = req
					return res, nil
				}
			}),
		}

		if _, err := provider.Search(context.Background(), "err=request", SearchOpts{Limit: 1}); err == nil {
			t.Fatal("expected transport error")
		}
		if _, err := provider.Search(context.Background(), "err=status", SearchOpts{Limit: 1}); err == nil {
			t.Fatal("expected status error")
		}
		if _, err := provider.Search(context.Background(), "err=decode", SearchOpts{Limit: 1}); err == nil {
			t.Fatal("expected decode error")
		}
	})
}

func TestCoreAcademicProviderFailureContracts(t *testing.T) {
	orig := SharedHTTPClient
	t.Cleanup(func() { SharedHTTPClient = orig })

	t.Run("openalex rejects truncated JSON", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"results":[{"id":"https://openalex.org/W1"}`)
				return rec.Result(), nil
			}),
		}

		_, err := NewOpenAlexProvider().Search(context.Background(), "partial-json", SearchOpts{Limit: 1})
		if err == nil || !strings.Contains(err.Error(), "failed to parse response") {
			t.Fatalf("expected parse failure for partial OpenAlex JSON, got %v", err)
		}
	})

	t.Run("semantic scholar rejects truncated JSON", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"data":[{"paperId":"p1"}`)
				return rec.Result(), nil
			}),
		}

		_, err := NewSemanticScholarProvider().Search(context.Background(), "partial-json", SearchOpts{Limit: 1})
		if err == nil || !strings.Contains(err.Error(), "failed to parse response") {
			t.Fatalf("expected parse failure for partial Semantic Scholar JSON, got %v", err)
		}
	})

	t.Run("arxiv rejects truncated XML", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/xml")
				fmt.Fprint(rec, `<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>broken`)
				return rec.Result(), nil
			}),
		}

		_, err := NewArXivProvider().Search(context.Background(), "partial-xml", SearchOpts{Limit: 1})
		if err == nil || !strings.Contains(err.Error(), "failed to parse response") {
			t.Fatalf("expected parse failure for partial arXiv XML, got %v", err)
		}
	})

	t.Run("dns failures are surfaced as provider request failures", func(t *testing.T) {
		dnsErr := &net.DNSError{Err: "no such host", Name: "provider.invalid"}
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return nil, dnsErr
			}),
		}

		providers := map[string]SearchProvider{
			"openalex":         NewOpenAlexProvider(),
			"semantic_scholar": NewSemanticScholarProvider(),
			"arxiv":            NewArXivProvider(),
		}
		for name, provider := range providers {
			_, err := provider.Search(context.Background(), "dns", SearchOpts{Limit: 1})
			if err == nil || !strings.Contains(err.Error(), "request failed") {
				t.Fatalf("%s: expected request failure for DNS error, got %v", name, err)
			}
		}
	})
}

func TestPapersWithCodeProviderBranches(t *testing.T) {
	t.Run("request content type and decode errors", func(t *testing.T) {
		provider := &PapersWithCodeProvider{baseURL: "https://paperswithcode.com/api/v1/papers/"}

		orig := SharedHTTPClient
		t.Cleanup(func() { SharedHTTPClient = orig })
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch q := req.URL.Query().Get("q"); q {
				case "err=request":
					return nil, fmt.Errorf("boom")
				case "err=content":
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "text/html")
					fmt.Fprint(rec, "<html>nope</html>")
					res := rec.Result()
					res.Request = &http.Request{URL: &url.URL{Scheme: "https", Host: "paperswithcode.com"}}
					return res, nil
				default:
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, "{not-json}")
					res := rec.Result()
					res.Request = req
					return res, nil
				}
			}),
		}

		if _, err := provider.Search(context.Background(), "err=request", SearchOpts{Limit: 1}); err == nil {
			t.Fatal("expected transport error")
		}
		if _, err := provider.Search(context.Background(), "err=content", SearchOpts{Limit: 1}); err == nil {
			t.Fatal("expected content type error")
		}
		if _, err := provider.Search(context.Background(), "err=decode", SearchOpts{Limit: 1}); err == nil {
			t.Fatal("expected decode error")
		}
	})

	t.Run("success skips blank titles and uses arxiv fallback", func(t *testing.T) {
		provider := NewPapersWithCodeProvider()

		orig := SharedHTTPClient
		t.Cleanup(func() { SharedHTTPClient = orig })
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				fmt.Fprint(rec, `{"results":[{"id":"skip","arxiv_id":"2401.00001","title":"","abstract":"skip"},{"id":"keep","arxiv_id":"2401.00002","title":"Paper With Code","abstract":"Test","published":"2024-01-01","authors":["A"],"stars":42}]}`)
				res := rec.Result()
				res.Request = req
				return res, nil
			}),
		}

		papers, err := provider.Search(context.Background(), "test", SearchOpts{Limit: 5})
		if err != nil {
			t.Fatalf("unexpected search error: %v", err)
		}
		if len(papers) != 1 {
			t.Fatalf("expected one parsed paper, got %+v", papers)
		}
		if papers[0].ID != "arxiv:2401.00002" || papers[0].Link != "https://arxiv.org/abs/2401.00002" {
			t.Fatalf("unexpected parsed paper: %+v", papers[0])
		}
	})

	t.Run("transport error", func(t *testing.T) {
		provider := NewPapersWithCodeProvider()

		orig := SharedHTTPClient
		t.Cleanup(func() { SharedHTTPClient = orig })
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("boom")
			}),
		}

		if _, err := provider.Search(context.Background(), "test", SearchOpts{Limit: 1}); err == nil {
			t.Fatal("expected transport error")
		}
	})
}

func TestSSRNAndRePECProviderBranches(t *testing.T) {
	t.Run("ssrn request, status, decode and success branches", func(t *testing.T) {
		provider := NewSSRNProvider()

		orig := SharedHTTPClient
		t.Cleanup(func() { SharedHTTPClient = orig })
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch q := req.URL.Query().Get("query"); q {
				case "err=request":
					return nil, fmt.Errorf("boom")
				case "err=status":
					rec := httptest.NewRecorder()
					rec.WriteHeader(http.StatusGatewayTimeout)
					return rec.Result(), nil
				case "err=decode":
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, "{not-json}")
					return rec.Result(), nil
				default:
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, `{"message":{"items":[{"DOI":"10.2139/ssrn.1","title":[""],"abstract":"<p>skip</p>"},{"DOI":"10.2139/ssrn.2","title":["SSRN Paper"],"abstract":"<p>Abs</p>","URL":"","published":{"date-parts":[[2024]]}}]}}`)
					return rec.Result(), nil
				}
			}),
		}

		if _, err := provider.Search(context.Background(), "err=request", SearchOpts{}); err == nil {
			t.Fatal("expected request error")
		}
		if _, err := provider.Search(context.Background(), "err=status", SearchOpts{}); err == nil {
			t.Fatal("expected status error")
		}
		if _, err := provider.Search(context.Background(), "err=decode", SearchOpts{}); err == nil {
			t.Fatal("expected decode error")
		}

		papers, err := provider.Search(context.Background(), "ok", SearchOpts{})
		if err != nil {
			t.Fatalf("unexpected search error: %v", err)
		}
		if len(papers) != 1 || papers[0].Abstract != "Abs" || papers[0].Link != "https://doi.org/10.2139/ssrn.2" {
			t.Fatalf("unexpected SSRN result: %+v", papers)
		}
	})

	t.Run("repec request status decode and success branches", func(t *testing.T) {
		provider := NewRePECProvider()

		orig := SharedHTTPClient
		t.Cleanup(func() { SharedHTTPClient = orig })
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch q := req.URL.Query().Get("q"); q {
				case "err=request":
					return nil, fmt.Errorf("boom")
				case "err=status":
					rec := httptest.NewRecorder()
					rec.WriteHeader(http.StatusBadGateway)
					return rec.Result(), nil
				case "err=decode":
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, "{not-json}")
					return rec.Result(), nil
				default:
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, `{"items":[{"handle":"h1","title":"RePEc Paper","abstract":"A","author":["A"],"year":"2023","url":"https://example.com"}]}`)
					return rec.Result(), nil
				}
			}),
		}

		if _, err := provider.Search(context.Background(), "err=request", SearchOpts{Limit: 1}); err == nil {
			t.Fatal("expected request error")
		}
		if _, err := provider.Search(context.Background(), "err=status", SearchOpts{Limit: 1}); err == nil {
			t.Fatal("expected status error")
		}
		if _, err := provider.Search(context.Background(), "err=decode", SearchOpts{Limit: 1}); err == nil {
			t.Fatal("expected decode error")
		}

		papers, err := provider.Search(context.Background(), "ok", SearchOpts{Limit: 1})
		if err != nil {
			t.Fatalf("unexpected search error: %v", err)
		}
		if len(papers) != 1 || papers[0].ID != "repec-h1" || papers[0].Source != "repec" || len(papers[0].SourceApis) != 1 || papers[0].SourceApis[0] != "repec" {
			t.Fatalf("unexpected RePEC result: %+v", papers)
		}
	})
}

func TestGoogleScholarSearchByAuthorRequestError(t *testing.T) {
	t.Setenv("SERPAPI_API_KEY", "test-key")
	provider := &GoogleScholarProvider{apiKey: "test-key"}

	orig := SharedHTTPClient
	t.Cleanup(func() { SharedHTTPClient = orig })
	SharedHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("boom")
		}),
	}

	if _, err := provider.SearchByAuthor(context.Background(), "author-1", 1); err == nil {
		t.Fatal("expected author search transport error")
	}
}
