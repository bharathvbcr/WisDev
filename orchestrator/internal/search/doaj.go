package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DOAJProvider searches the Directory of Open Access Journals.
// No API key required. Best for general academic research.
type DOAJProvider struct {
	BaseProvider
	baseURL string
}

var _ SearchProvider = (*DOAJProvider)(nil)

func NewDOAJProvider() *DOAJProvider {
	return &DOAJProvider{baseURL: "https://doaj.org/api/search/articles/"}
}

func (d *DOAJProvider) Name() string { return "doaj" }
func (d *DOAJProvider) Domains() []string {
	return []string{"general", "humanities", "social", "biology"}
}

func (d *DOAJProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	limit := opts.Limit
	if limit <= 0 {
		limit = 15
	}
	if limit > 50 {
		limit = 50
	}

	reqURL := d.baseURL + url.PathEscape(query) + "?pageSize=" + fmt.Sprintf("%d", limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		d.RecordFailure()
		return nil, providerError("doaj", "build request: %v", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		d.RecordFailure()
		return nil, providerError("doaj", "request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		d.RecordFailure()
		return nil, providerError("doaj", "HTTP %d", resp.StatusCode)
	}

	var result struct {
		Results []struct {
			ID      string `json:"id"`
			Bibjson struct {
				Title      string `json:"title"`
				Abstract   string `json:"abstract"`
				Year       string `json:"year"`
				Identifier []struct {
					Type string `json:"type"`
					ID   string `json:"id"`
				} `json:"identifier"`
				Link []struct {
					Type        string `json:"type"`
					ContentType string `json:"content_type"`
					URL         string `json:"url"`
				} `json:"link"`
				Author []struct {
					Name string `json:"name"`
				} `json:"author"`
			} `json:"bibjson"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		d.RecordFailure()
		return nil, providerError("doaj", "decode: %v", err)
	}

	papers := make([]Paper, 0, len(result.Results))
	for _, item := range result.Results {
		bib := item.Bibjson
		title := strings.TrimSpace(bib.Title)
		if title == "" {
			continue
		}

		var doi string
		for _, id := range bib.Identifier {
			if strings.ToLower(id.Type) == "doi" {
				doi = id.ID
				break
			}
		}

		var link string
		for _, l := range bib.Link {
			if l.Type == "fulltext" || strings.Contains(strings.ToLower(l.ContentType), "pdf") {
				link = l.URL
				break
			}
		}
		if link == "" && len(bib.Link) > 0 {
			link = bib.Link[0].URL
		}

		authors := make([]string, 0, len(bib.Author))
		for _, a := range bib.Author {
			if a.Name != "" {
				authors = append(authors, a.Name)
			}
		}

		var year int
		fmt.Sscanf(bib.Year, "%d", &year)

		papers = append(papers, Paper{
			ID:       "doaj:" + item.ID,
			Title:    title,
			Abstract: strings.TrimSpace(bib.Abstract),
			Link:     link,
			DOI:      doi,
			Source:   "doaj",
			Authors:  authors,
			Year:     year,
		})
	}

	d.RecordSuccess()
	return papers, nil
}
