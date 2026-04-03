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

// DBLPProvider searches the DBLP Computer Science Bibliography.
// No API key required. Best for CS, systems, and software engineering.
type DBLPProvider struct {
	BaseProvider
	baseURL string
}

var _ SearchProvider = (*DBLPProvider)(nil)

func NewDBLPProvider() *DBLPProvider {
	return &DBLPProvider{baseURL: "https://dblp.org/search/publ/api"}
}

func (d *DBLPProvider) Name() string      { return "dblp" }
func (d *DBLPProvider) Domains() []string { return []string{"cs", "ai", "engineering"} }

func (d *DBLPProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	limit := opts.Limit
	if limit <= 0 {
		limit = 15
	}

	reqURL := fmt.Sprintf("%s?q=%s&format=json&h=%d", d.baseURL, url.QueryEscape(query), limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		d.RecordFailure()
		return nil, providerError("dblp", "build request: %v", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		d.RecordFailure()
		return nil, providerError("dblp", "request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		d.RecordFailure()
		return nil, providerError("dblp", "HTTP %d", resp.StatusCode)
	}

	var result struct {
		Result struct {
			Hits struct {
				Hit []struct {
					Info struct {
						Title   string `json:"title"`
						Authors struct {
							Author any `json:"author"` // can be string or []string
						} `json:"authors"`
						Year  string `json:"year"`
						DOI   string `json:"doi"`
						URL   string `json:"url"`
						EE    string `json:"ee"`
						Pages string `json:"pages"`
					} `json:"info"`
				} `json:"hit"`
			} `json:"hits"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		d.RecordFailure()
		return nil, providerError("dblp", "decode: %v", err)
	}

	papers := make([]Paper, 0, len(result.Result.Hits.Hit))
	for _, hit := range result.Result.Hits.Hit {
		info := hit.Info
		title := strings.TrimSpace(info.Title)
		if title == "" {
			continue
		}
		link := info.URL
		if link == "" {
			link = info.EE
		}
		year := 0
		if info.Year != "" {
			fmt.Sscanf(info.Year, "%d", &year)
		}

		papers = append(papers, Paper{
			ID:     "dblp:" + info.DOI,
			Title:  title,
			Link:   link,
			DOI:    info.DOI,
			Source: "dblp",
			Year:   year,
		})
	}

	d.RecordSuccess()
	return papers, nil
}
