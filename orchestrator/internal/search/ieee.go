package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
)

// IEEEProvider searches IEEE Xplore for engineering and CS papers.
// Requires IEEE_API_KEY.
type IEEEProvider struct {
	BaseProvider
	apiKey string
}

var _ SearchProvider = (*IEEEProvider)(nil)

func NewIEEEProvider() *IEEEProvider {
	return &IEEEProvider{
		apiKey: os.Getenv("IEEE_API_KEY"),
	}
}

func (p *IEEEProvider) Name() string { return "ieee" }

func (p *IEEEProvider) Domains() []string {
	return []string{"engineering", "cs", "physics"}
}

type ieeeArticle struct {
	ArticleNumber    string `json:"article_number"`
	Title            string `json:"title"`
	Abstract         string `json:"abstract"`
	PublicationTitle string `json:"publication_title"`
	PublicationYear  int    `json:"publication_year,string"`
	PublicationDate  string `json:"publication_date"`
	DOI              string `json:"doi"`
	CitingPaperCount int    `json:"citing_paper_count,string"`
	ContentType      string `json:"content_type"`
	Publisher        string `json:"publisher"`
	PDFURL           string `json:"pdf_url"`
	HTMLURL          string `json:"html_url"`
	Authors          struct {
		Authors []struct {
			FullName    string `json:"full_name"`
			Affiliation string `json:"affiliation"`
			AuthorOrder int    `json:"author_order"`
		} `json:"authors"`
	} `json:"authors"`
	IndexTerms struct {
		IEEETerms   struct{ Terms []string } `json:"ieee_terms"`
		AuthorTerms struct{ Terms []string } `json:"author_terms"`
	} `json:"index_terms"`
}

type ieeeResponse struct {
	TotalRecords int           `json:"total_records"`
	Articles     []ieeeArticle `json:"articles"`
}

func (p *IEEEProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	if p.apiKey == "" {
		return nil, providerError(p.Name(), "API key not configured")
	}

	u, _ := url.Parse("https://ieeexploreapi.ieee.org/api/v1/search/articles")
	q := u.Query()
	q.Set("apikey", p.apiKey)
	q.Set("querytext", query)
	q.Set("max_records", fmt.Sprintf("%d", opts.Limit))
	q.Set("format", "json")
	q.Set("sort_field", "relevance")
	q.Set("sort_order", "desc")

	if opts.YearFrom > 0 {
		q.Set("start_year", fmt.Sprintf("%d", opts.YearFrom))
	}
	if opts.YearTo > 0 {
		q.Set("end_year", fmt.Sprintf("%d", opts.YearTo))
	}

	u.RawQuery = q.Encode()

	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		p.RecordFailure()
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		p.RecordFailure()
		return nil, providerError(p.Name(), "HTTP %d", resp.StatusCode)
	}

	var data ieeeResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		p.RecordFailure()
		return nil, err
	}

	papers := make([]Paper, 0, len(data.Articles))
	for _, art := range data.Articles {
		// Sort authors by order
		sort.Slice(art.Authors.Authors, func(i, j int) bool {
			return art.Authors.Authors[i].AuthorOrder < art.Authors.Authors[j].AuthorOrder
		})

		authors := make([]string, len(art.Authors.Authors))
		for i, a := range art.Authors.Authors {
			authors[i] = a.FullName
		}

		link := art.HTMLURL
		if art.DOI != "" {
			link = "https://doi.org/" + art.DOI
		}

		papers = append(papers, Paper{
			ID:            "ieee-" + art.ArticleNumber,
			Title:         art.Title,
			Abstract:      art.Abstract,
			Authors:       authors,
			Year:          art.PublicationYear,
			DOI:           art.DOI,
			Link:          link,
			CitationCount: art.CitingPaperCount,
			Source:        "ieee",
			SourceApis:    []string{"ieee"},
			Venue:         art.PublicationTitle,
			PdfUrl:        art.PDFURL,
		})
	}

	p.RecordSuccess()
	return papers, nil
}
