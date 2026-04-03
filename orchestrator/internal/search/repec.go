package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// RePECProvider searches RePEc/IDEAS for economics papers.
type RePECProvider struct {
	BaseProvider
}

var _ SearchProvider = (*RePECProvider)(nil)

func NewRePECProvider() *RePECProvider {
	return &RePECProvider{}
}

func (p *RePECProvider) Name() string { return "repec" }

func (p *RePECProvider) Domains() []string {
	return []string{"economics"}
}

type repecDoc struct {
	Handle   string   `json:"handle"`
	Title    string   `json:"title"`
	Abstract string   `json:"abstract"`
	Author   []string `json:"author"`
	Year     string   `json:"year"`
	URL      string   `json:"url"`
	Series   string   `json:"series"`
}

func (p *RePECProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	u, _ := url.Parse("https://ideas.repec.org/cgi-bin/htsearch")
	q := u.Query()
	q.Set("q", query)
	q.Set("fmt", "json")
	q.Set("num", strconv.Itoa(opts.Limit))

	u.RawQuery = q.Encode()

	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	req.Header.Set("User-Agent", "ScholarLM/1.0 (mailto:scholar.focus.app@gmail.com)")

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

	var data struct {
		Items []repecDoc `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		p.RecordFailure()
		return nil, err
	}

	papers := make([]Paper, 0, len(data.Items))
	for _, doc := range data.Items {
		authors := doc.Author

		year, _ := strconv.Atoi(doc.Year)

		link := doc.URL
		if link == "" && doc.Handle != "" {
			// Convert handle to IDEAS URL
			// RePEc:nbr:nberwo:12345 -> https://ideas.repec.org/p/nbr/nberwo/12345.html
		}

		papers = append(papers, Paper{
			ID:       "repec-" + doc.Handle,
			Title:    doc.Title,
			Abstract: doc.Abstract,
			Authors:  authors,
			Year:     year,
			Link:     link,
			Source:   "IDEAS/RePEc",
		})
	}

	p.RecordSuccess()
	return papers, nil
}
