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
					fmt.Fprint(rec, `{"result":{"123":{"title":"P1","source":"Methods Mol Biol","fulljournalname":"Methods in molecular biology","authors":[{"name":"Ada Lovelace"},{"name":"Grace Hopper"}],"pubdate":"2019 Aug 28","articleids":[{"idtype":"doi","value":"10.1/1"}]},"456":{"title":"P2","pubdate":"2020","authors":[{"name":"Ada Lovelace"}]}}}`)
				} else if strings.Contains(req.URL.Path, "efetch") {
					fmt.Fprint(rec, `<PubmedArticleSet>
						<PubmedArticle>
							<MedlineCitation>
								<PMID>123</PMID>
								<Article><Abstract>
									<AbstractText Label="BACKGROUND">PubMed abstract one.</AbstractText>
									<AbstractText>Second sentence.</AbstractText>
								</Abstract></Article>
							</MedlineCitation>
						</PubmedArticle>
						<PubmedArticle>
							<MedlineCitation>
								<PMID>456</PMID>
								<Article><Abstract><AbstractText>PubMed abstract two.</AbstractText></Abstract></Article>
							</MedlineCitation>
						</PubmedArticle>
					</PubmedArticleSet>`)
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
		assert.Equal(t, []string{"Ada Lovelace", "Grace Hopper"}, papers[0].Authors)
		assert.Equal(t, "Methods in molecular biology", papers[0].Venue)
		assert.Equal(t, 2019, papers[0].Year)
		assert.Equal(t, []string{"pubmed"}, papers[0].SourceApis)
		assert.Equal(t, "BACKGROUND: PubMed abstract one. Second sentence.", papers[0].Abstract)
		assert.Equal(t, []string{"Ada Lovelace"}, papers[1].Authors)
		assert.Equal(t, 2020, papers[1].Year)
		assert.Equal(t, "PubMed abstract two.", papers[1].Abstract)
	})

	t.Run("Search continues when abstract fetch fails", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				rec.Header().Set("Content-Type", "application/json")
				switch {
				case strings.Contains(req.URL.Path, "esearch"):
					fmt.Fprint(rec, `{"esearchresult":{"idlist":["123"]}}`)
				case strings.Contains(req.URL.Path, "esummary"):
					fmt.Fprint(rec, `{"result":{"123":{"title":"P1","pubdate":"2020"}}}`)
				case strings.Contains(req.URL.Path, "efetch"):
					rec.WriteHeader(http.StatusGatewayTimeout)
				}
				return rec.Result(), nil
			}),
		}
		p := NewPubMedProvider()
		papers, err := p.Search(context.Background(), "test", SearchOpts{})
		assert.NoError(t, err)
		assert.Len(t, papers, 1)
		assert.Equal(t, "P1", papers[0].Title)
		assert.Empty(t, papers[0].Abstract)
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

	t.Run("Request and Decode Errors", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch {
				case strings.Contains(req.URL.RawQuery, "term=err%3Drequest"):
					return nil, fmt.Errorf("boom")
				case strings.Contains(req.URL.RawQuery, "term=err%3Ddecode-search"):
					rec := httptest.NewRecorder()
					fmt.Fprint(rec, `invalid`)
					return rec.Result(), nil
				case strings.Contains(req.URL.RawQuery, "term=err%3Ddecode-summary") && strings.Contains(req.URL.Path, "esearch"):
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, `{"esearchresult":{"idlist":["123"]}}`)
					return rec.Result(), nil
				case strings.Contains(req.URL.RawQuery, "id=123") && strings.Contains(req.URL.Path, "esummary"):
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, `invalid`)
					return rec.Result(), nil
				default:
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, `{"esearchresult":{"idlist":["123"]}}`)
					return rec.Result(), nil
				}
			}),
		}
		p := NewPubMedProvider()
		_, err := p.Search(context.Background(), "err=request", SearchOpts{})
		assert.Error(t, err)
		_, err = p.Search(context.Background(), "err=decode-search", SearchOpts{})
		assert.Error(t, err)

		_, err = p.Search(context.Background(), "err=decode-summary", SearchOpts{})
		assert.Error(t, err)
	})

	t.Run("Summary Request Error", func(t *testing.T) {
		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if strings.Contains(req.URL.Path, "esearch") {
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, `{"esearchresult":{"idlist":["123"]}}`)
					return rec.Result(), nil
				}
				return nil, fmt.Errorf("summary boom")
			}),
		}
		p := NewPubMedProvider()
		_, err := p.Search(context.Background(), "summary-request", SearchOpts{})
		assert.Error(t, err)
	})

	t.Run("Build Search and Summary Request Errors", func(t *testing.T) {
		p := NewPubMedProvider()
		p.searchURL = "http://[::1"
		_, err := p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)

		SharedHTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if strings.Contains(req.URL.Path, "esearch") {
					rec := httptest.NewRecorder()
					rec.Header().Set("Content-Type", "application/json")
					fmt.Fprint(rec, `{"esearchresult":{"idlist":["123"]}}`)
					return rec.Result(), nil
				}
				return nil, fmt.Errorf("summary boom")
			}),
		}
		p = NewPubMedProvider()
		p.summaryURL = "http://[::1"
		_, err = p.Search(context.Background(), "q", SearchOpts{})
		assert.Error(t, err)
	})
}

func TestPubMedFetchAbstracts(t *testing.T) {
	origClient := SharedHTTPClient
	defer func() { SharedHTTPClient = origClient }()

	SharedHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			rec.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(rec, `<PubmedArticleSet>
				<PubmedArticle>
					<MedlineCitation>
						<PMID>1</PMID>
						<Article><Abstract>
							<AbstractText Label="OBJECTIVE">  First   abstract. </AbstractText>
							<AbstractText> Follow-up text. </AbstractText>
						</Abstract></Article>
					</MedlineCitation>
				</PubmedArticle>
			</PubmedArticleSet>`)
			return rec.Result(), nil
		}),
	}

	p := NewPubMedProvider()
	abstracts, err := p.fetchAbstracts(context.Background(), []string{"1"})
	assert.NoError(t, err)
	assert.Equal(t, "OBJECTIVE: First abstract. Follow-up text.", abstracts["1"])
}
