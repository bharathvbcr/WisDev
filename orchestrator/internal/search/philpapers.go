package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// PhilPapersProvider searches PhilPapers for philosophy research.
type PhilPapersProvider struct {
	BaseProvider
}

var _ SearchProvider = (*PhilPapersProvider)(nil)

func NewPhilPapersProvider() *PhilPapersProvider {
	return &PhilPapersProvider{}
}

func (p *PhilPapersProvider) Name() string { return "philpapers" }

func (p *PhilPapersProvider) Domains() []string {
	return []string{"philosophy"}
}

type philPapersEntry struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Authors       []string `json:"authors"`
	Abstract      string   `json:"abstract"`
	Year          int      `json:"year"`
	Pub           string   `json:"pub"`
	DOI           string   `json:"doi"`
	URL           string   `json:"url"`
	OpenAccessURL string   `json:"openAccessUrl"`
	Citations     int      `json:"citations"`
}

type philPapersResponse struct {
	Entries []philPapersEntry `json:"entries"`
}

func (p *PhilPapersProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	u, _ := url.Parse("https://philpapers.org/philpapers/raw/search.json")
	q := u.Query()
	q.Set("searchStr", query)
	q.Set("limit", fmt.Sprintf("%d", opts.Limit))
	q.Set("format", "json")

	if opts.YearFrom > 0 {
		q.Set("start", fmt.Sprintf("%d", opts.YearFrom))
	}
	if opts.YearTo > 0 {
		q.Set("end", fmt.Sprintf("%d", opts.YearTo))
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

	var data philPapersResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		p.RecordFailure()
		return nil, err
	}

	papers := make([]Paper, 0, len(data.Entries))
	for _, entry := range data.Entries {
		authors := entry.Authors

		link := entry.URL
		if link == "" {
			link = "https://philpapers.org/rec/" + entry.ID
		}

		papers = append(papers, Paper{
			ID:            "philpapers-" + entry.ID,
			Title:         entry.Title,
			Abstract:      entry.Abstract,
			Authors:       authors,
			Year:          entry.Year,
			DOI:           entry.DOI,
			Link:          link,
			CitationCount: entry.Citations,
			Source:        "PhilPapers",
		})
	}

	p.RecordSuccess()
	return papers, nil
}
