package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// NASAADSProvider searches NASA ADS for astronomy and physics papers.
// Requires NASA_ADS_API_KEY.
type NASAADSProvider struct {
	BaseProvider
	apiKey  string
	baseURL string
}

var _ SearchProvider = (*NASAADSProvider)(nil)

func NewNASAADSProvider() *NASAADSProvider {
	return &NASAADSProvider{
		apiKey:  os.Getenv("NASA_ADS_API_KEY"),
		baseURL: "https://api.adsabs.harvard.edu/v1/search/query",
	}
}

func (p *NASAADSProvider) Name() string { return "nasa_ads" }

func (p *NASAADSProvider) Domains() []string {
	return []string{"astronomy", "physics"}
}

type adsDoc struct {
	Bibcode       string   `json:"bibcode"`
	Title         []string `json:"title"`
	Abstract      string   `json:"abstract"`
	Author        []string `json:"author"`
	Aff           []string `json:"aff"`
	Year          string   `json:"year"`
	PubDate       string   `json:"pubdate"`
	Pub           string   `json:"pub"`
	DOI           []string `json:"doi"`
	CitationCount int      `json:"citation_count"`
	ReadCount     int      `json:"read_count"`
	Identifier    []string `json:"identifier"`
}

type adsResponse struct {
	Response struct {
		NumFound int      `json:"numFound"`
		Docs     []adsDoc `json:"docs"`
	} `json:"response"`
}

func (p *NASAADSProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	if p.apiKey == "" {
		return nil, providerError(p.Name(), "API key not configured")
	}

	u, _ := url.Parse(p.baseURL)
	q := u.Query()

	adsQuery := query
	if opts.YearFrom > 0 || opts.YearTo > 0 {
		filters := []string{}
		if opts.YearFrom > 0 {
			filters = append(filters, fmt.Sprintf("year:%d-", opts.YearFrom))
		}
		if opts.YearTo > 0 {
			filters = append(filters, fmt.Sprintf("year:-%d", opts.YearTo))
		}
		adsQuery = fmt.Sprintf("(%s) %s", query, strings.Join(filters, " "))
	}

	q.Set("q", adsQuery)
	q.Set("rows", fmt.Sprintf("%d", opts.Limit))
	q.Set("sort", "score desc")
	q.Set("fl", "bibcode,title,abstract,author,aff,year,pubdate,pub,doi,citation_count,read_count,identifier")

	u.RawQuery = q.Encode()

	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

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

	var data adsResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		p.RecordFailure()
		return nil, err
	}

	papers := make([]Paper, 0, len(data.Response.Docs))
	for _, doc := range data.Response.Docs {
		authors := doc.Author

		year, _ := strconv.Atoi(doc.Year)
		doi := ""
		if len(doc.DOI) > 0 {
			doi = doc.DOI[0]
		}

		link := "https://ui.adsabs.harvard.edu/abs/" + doc.Bibcode
		if doi != "" {
			link = "https://doi.org/" + doi
		}

		title := "Untitled"
		if len(doc.Title) > 0 {
			title = doc.Title[0]
		}

		papers = append(papers, Paper{
			ID:            "ads-" + doc.Bibcode,
			Title:         title,
			Abstract:      doc.Abstract,
			Authors:       authors,
			Year:          year,
			DOI:           doi,
			Link:          link,
			CitationCount: doc.CitationCount,
			Source:        "nasa_ads",
			SourceApis:    []string{"nasa_ads"},
			Venue:         strings.TrimSpace(doc.Pub),
		})
	}

	p.RecordSuccess()
	return papers, nil
}
