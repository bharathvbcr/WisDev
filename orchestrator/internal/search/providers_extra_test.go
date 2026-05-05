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
)

type mockTransport struct {
	roundTripFn func(req *http.Request) (*http.Response, error)
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.roundTripFn(req)
}

func TestExtraProviders(t *testing.T) {
	is := assert.New(t)
	origClient := SharedHTTPClient
	defer func() { SharedHTTPClient = origClient }()

	t.Run("DBLPProvider", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: &mockTransport{
				roundTripFn: func(req *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewReader([]byte(`{"result":{"hits":{"hit":[{"info":{"title":"T1","doi":"d1","url":"u1","year":"2024"}}]}}}`))),
					}, nil
				},
			},
		}
		p := NewDBLPProvider()
		res, err := p.Search(context.Background(), "query", SearchOpts{})
		is.NoError(err)
		is.Len(res, 1)
		is.Equal("T1", res[0].Title)
	})

	t.Run("DBLPProvider richer branches", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: &mockTransport{
				roundTripFn: func(req *http.Request) (*http.Response, error) {
					is.Contains(req.URL.RawQuery, "h=15")
					is.Contains(req.URL.RawQuery, "q=graph+neural+networks")
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, `{"result":{"hits":{"hit":[
						{"info":{"title":"  First Paper  ","year":"2024","doi":"10.1/a","ee":"https://ee.example/a"}},
						{"info":{"title":"","year":"2023","doi":"10.1/b","url":"https://url.example/b"}}
					]}}}`)
					return rec.Result(), nil
				},
			},
		}
		p := NewDBLPProvider()
		res, err := p.Search(context.Background(), "graph neural networks", SearchOpts{})
		is.NoError(err)
		is.Len(res, 1)
		is.Equal("dblp:10.1/a", res[0].ID)
		is.Equal("https://ee.example/a", res[0].Link)
		is.Equal(2024, res[0].Year)
	})

	t.Run("DBLPProvider errors", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: &mockTransport{
				roundTripFn: func(req *http.Request) (*http.Response, error) {
					if strings.Contains(req.URL.RawQuery, "h=2") {
						rec := httptest.NewRecorder()
						rec.WriteHeader(http.StatusInternalServerError)
						return rec.Result(), nil
					}
					return nil, fmt.Errorf("transport failed")
				},
			},
		}
		p := NewDBLPProvider()
		_, err := p.Search(context.Background(), "query", SearchOpts{Limit: 2})
		is.Error(err)

		p.baseURL = "http://bad host"
		_, err = p.Search(context.Background(), "query", SearchOpts{Limit: 2})
		is.Error(err)

		SharedHTTPClient = &http.Client{
			Transport: &mockTransport{
				roundTripFn: func(req *http.Request) (*http.Response, error) {
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, `invalid`)
					return rec.Result(), nil
				},
			},
		}
		_, err = p.Search(context.Background(), "query", SearchOpts{Limit: 2})
		is.Error(err)
	})

	t.Run("BioRxivProvider richer branches", func(t *testing.T) {
		call := 0
		SharedHTTPClient = &http.Client{
			Transport: &mockTransport{
				roundTripFn: func(req *http.Request) (*http.Response, error) {
					call++
					if call == 1 {
						rec := httptest.NewRecorder()
						rec.Header().Set("Content-Type", "application/json")
						fmt.Fprint(rec, `{"collection":[{"doi":"10.1101/one","title":"Keyword Match","abstract":"keyword appears","date":"2024-03-10","server":"biorxiv"},{"doi":"10.1101/two","title":"Other Study","abstract":"no match","date":"2024-01-01","server":"biorxiv"}]}`)
						return rec.Result(), nil
					}
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, `{"collection":[{"doi":"10.1101/all","title":"All Results","abstract":"no specific keywords","date":"2023-02-01"}]}`)
					return rec.Result(), nil
				},
			},
		}

		p := NewBioRxivProvider()
		papers, err := p.searchByDateRange(context.Background(), "keyword search", SearchOpts{Limit: 1})
		is.NoError(err)
		is.Len(papers, 1)
		is.Equal("biorxiv:10.1101/one", papers[0].ID)

		papers, err = p.searchByDateRange(context.Background(), "", SearchOpts{Limit: 1})
		is.NoError(err)
		is.Len(papers, 1)
		is.Equal("biorxiv:10.1101/all", papers[0].ID)

		p.baseURL = "http://bad host"
		_, err = p.searchByDateRange(context.Background(), "keyword", SearchOpts{Limit: 1})
		is.Error(err)
	})

	t.Run("IEEEProvider", func(t *testing.T) {
		t.Setenv("IEEE_API_KEY", "test-key")
		SharedHTTPClient = &http.Client{
			Transport: &mockTransport{
				roundTripFn: func(req *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewReader([]byte(`{"total_records":1,"articles":[{"article_number":"1","title":"T1","doi":"d1","publication_year":"2024"}]}`))),
					}, nil
				},
			},
		}
		p := NewIEEEProvider()
		res, err := p.Search(context.Background(), "query", SearchOpts{})
		is.NoError(err)
		is.Len(res, 1)
		is.Equal("T1", res[0].Title)
	})

	t.Run("GoogleScholarProvider", func(t *testing.T) {
		t.Setenv("SERPAPI_API_KEY", "test-key")
		SharedHTTPClient = &http.Client{
			Transport: &mockTransport{
				roundTripFn: func(req *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewReader([]byte(`{"organic_results":[{"title":"T1","link":"l1","snippet":"s1","publication_info":{"summary":"2024"}}]}`))),
					}, nil
				},
			},
		}
		p := NewGoogleScholarProvider()
		res, err := p.Search(context.Background(), "query", SearchOpts{})
		is.NoError(err)
		is.Len(res, 1)
		is.Equal("T1", res[0].Title)
		is.Equal("2024", res[0].Venue)
		is.Equal([]string{"google_scholar"}, res[0].SourceApis)
	})

	t.Run("ClinicalTrialsProvider", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: &mockTransport{
				roundTripFn: func(req *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewReader([]byte(`{"studies":[{"protocolSection":{"identificationModule":{"nctId":"NCT1","briefTitle":"T1"}}}]}`))),
					}, nil
				},
			},
		}
		p := NewClinicalTrialsProvider()
		res, err := p.Search(context.Background(), "query", SearchOpts{})
		is.NoError(err)
		is.Len(res, 1)
		is.Equal("T1", res[0].Title)
	})

	t.Run("IEEEProvider no key and html fallback", func(t *testing.T) {
		p := &IEEEProvider{}
		res, err := p.Search(context.Background(), "query", SearchOpts{})
		is.Error(err)
		is.Nil(res)

		t.Setenv("IEEE_API_KEY", "test-key")
		SharedHTTPClient = &http.Client{
			Transport: &mockTransport{
				roundTripFn: func(req *http.Request) (*http.Response, error) {
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, `{"total_records":1,"articles":[{"article_number":"3","title":"HTML Only","html_url":"https://html.example/p3","publication_year":"2021","authors":{"authors":[{"full_name":"Solo","author_order":1}]}}]}`)
					return rec.Result(), nil
				},
			},
		}
		p = NewIEEEProvider()
		res, err = p.Search(context.Background(), "query", SearchOpts{Limit: 1})
		is.NoError(err)
		is.Len(res, 1)
		is.Equal("https://html.example/p3", res[0].Link)
	})

	t.Run("IEEEProvider richer branches", func(t *testing.T) {
		t.Setenv("IEEE_API_KEY", "test-key")
		SharedHTTPClient = &http.Client{
			Transport: &mockTransport{
				roundTripFn: func(req *http.Request) (*http.Response, error) {
					is.Contains(req.URL.RawQuery, "apikey=test-key")
					is.Contains(req.URL.RawQuery, "querytext=quantum")
					is.Contains(req.URL.RawQuery, "start_year=2020")
					is.Contains(req.URL.RawQuery, "end_year=2022")
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, `{"total_records":1,"articles":[{"article_number":"2","title":"T2","doi":"10.1/ieee","publication_year":"2024","citing_paper_count":"8","html_url":"https://html.example","authors":{"authors":[{"full_name":"B","author_order":2},{"full_name":"A","author_order":1}]}}]}`)
					return rec.Result(), nil
				},
			},
		}
		p := NewIEEEProvider()
		res, err := p.Search(context.Background(), "quantum", SearchOpts{Limit: 5, YearFrom: 2020, YearTo: 2022})
		is.NoError(err)
		is.Len(res, 1)
		is.Equal("ieee-2", res[0].ID)
		is.Equal("https://doi.org/10.1/ieee", res[0].Link)
		is.Equal([]string{"A", "B"}, res[0].Authors)
		is.Equal(2024, res[0].Year)
		is.Equal("ieee", res[0].Source)
		is.Equal([]string{"ieee"}, res[0].SourceApis)
	})

	t.Run("IEEEProvider errors", func(t *testing.T) {
		t.Setenv("IEEE_API_KEY", "test-key")
		SharedHTTPClient = &http.Client{
			Transport: &mockTransport{
				roundTripFn: func(req *http.Request) (*http.Response, error) {
					return nil, fmt.Errorf("transport failed")
				},
			},
		}
		p := NewIEEEProvider()
		_, err := p.Search(context.Background(), "query", SearchOpts{Limit: 1})
		is.Error(err)

		SharedHTTPClient = &http.Client{
			Transport: &mockTransport{
				roundTripFn: func(req *http.Request) (*http.Response, error) {
					rec := httptest.NewRecorder()
					rec.WriteHeader(http.StatusBadGateway)
					return rec.Result(), nil
				},
			},
		}
		_, err = p.Search(context.Background(), "query", SearchOpts{Limit: 1})
		is.Error(err)

		SharedHTTPClient = &http.Client{
			Transport: &mockTransport{
				roundTripFn: func(req *http.Request) (*http.Response, error) {
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, `invalid`)
					return rec.Result(), nil
				},
			},
		}
		_, err = p.Search(context.Background(), "query", SearchOpts{Limit: 1})
		is.Error(err)
	})

	t.Run("ClinicalTrialsProvider rich parse and defaults", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: &mockTransport{
				roundTripFn: func(req *http.Request) (*http.Response, error) {
					is.Contains(req.URL.RawQuery, "query.term=heart+failure")
					is.Contains(req.URL.RawQuery, "pageSize=10")
					is.Contains(req.URL.RawQuery, "fields=NCTId%2CBriefTitle%2CBriefSummary%2CCondition%2CPhase%2COverallStatus%2CStartDate%2CCompletionDate%2CLeadSponsorName")
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, `{"studies":[
						{"protocolSection":{"identificationModule":{"nctId":"NCT2","briefTitle":" Trial Two "},"descriptionModule":{"briefSummary":"Summary text"},"conditionsModule":{"conditions":["Heart Failure","Cardiology"]},"designModule":{"phases":["Phase 2"]},"statusModule":{"overallStatus":"Recruiting","startDateStruct":{"date":"2021-02-10"}},"sponsorCollaboratorsModule":{"leadSponsor":{"name":"ACME"}}}},
						{"protocolSection":{"identificationModule":{"nctId":"NCT3","briefTitle":""}}}
					]}`)
					return rec.Result(), nil
				},
			},
		}
		p := NewClinicalTrialsProvider()
		papers, err := p.Search(context.Background(), "heart failure", SearchOpts{})
		is.NoError(err)
		is.Len(papers, 1)
		is.Equal("ct:NCT2", papers[0].ID)
		is.Equal(2021, papers[0].Year)
		is.Contains(papers[0].Abstract, "Conditions: Heart Failure, Cardiology")
		is.Contains(papers[0].Abstract, "Phase: Phase 2")
		is.Contains(papers[0].Abstract, "Status: Recruiting")
		is.Contains(papers[0].Abstract, "Summary text")
	})

	t.Run("ClinicalTrialsProvider errors", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: &mockTransport{
				roundTripFn: func(req *http.Request) (*http.Response, error) {
					if strings.Contains(req.URL.RawQuery, "pageSize=3") {
						rec := httptest.NewRecorder()
						rec.WriteHeader(http.StatusServiceUnavailable)
						return rec.Result(), nil
					}
					return nil, fmt.Errorf("transport failed")
				},
			},
		}
		p := NewClinicalTrialsProvider()
		_, err := p.Search(context.Background(), "query", SearchOpts{Limit: 3})
		is.Error(err)

		p.baseURL = "http://bad host"
		_, err = p.Search(context.Background(), "query", SearchOpts{Limit: 3})
		is.Error(err)

		SharedHTTPClient = &http.Client{
			Transport: &mockTransport{
				roundTripFn: func(req *http.Request) (*http.Response, error) {
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, `invalid`)
					return rec.Result(), nil
				},
			},
		}
		_, err = p.Search(context.Background(), "query", SearchOpts{Limit: 3})
		is.Error(err)
	})

	t.Run("ClinicalTrialsProvider request build failure", func(t *testing.T) {
		p := NewClinicalTrialsProvider()
		p.baseURL = "http://bad host"
		_, err := p.Search(context.Background(), "query", SearchOpts{Limit: 1})
		is.Error(err)
	})
}
