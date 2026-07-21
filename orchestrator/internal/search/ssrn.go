package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// SSRNProvider searches SSRN papers via the Crossref API.
// SSRN papers in Crossref have the DOI prefix 10.2139/ssrn.
type SSRNProvider struct {
	BaseProvider
	baseURL    string
	politePool string
}

var _ SearchProvider = (*SSRNProvider)(nil)

func NewSSRNProvider() *SSRNProvider {
	email := os.Getenv("CROSSREF_POLITE_EMAIL")
	if email == "" {
		email = "api@wisdev.local"
	}
	return &SSRNProvider{
		baseURL:    "https://api.crossref.org/works",
		politePool: email,
	}
}

func (s *SSRNProvider) Name() string { return "ssrn" }
func (s *SSRNProvider) Domains() []string {
	return []string{"social", "economics", "law"}
}

func (s *SSRNProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	limit := opts.Limit
	if limit <= 0 {
		limit = 15
	}

	params := url.Values{}
	params.Set("query", query)
	params.Set("filter", "prefix:10.2139/ssrn")
	params.Set("rows", fmt.Sprintf("%d", limit))
	params.Set("sort", "relevance")
	params.Set("order", "desc")
	params.Set("mailto", s.politePool)

	reqURL := s.baseURL + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		s.RecordFailure()
		return nil, providerError("ssrn", "build request: %v", err)
	}
	req.Header.Set("User-Agent", "WisDev/1.0 (mailto:"+s.politePool+")")
	req.Header.Set("Accept", "application/json")

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		s.RecordFailure()
		return nil, providerError("ssrn", "request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		s.RecordFailure()
		return nil, providerError("ssrn", "HTTP %d", resp.StatusCode)
	}

	var result struct {
		Message struct {
			Items []struct {
				DOI      string   `json:"DOI"`
				Title    []string `json:"title"`
				Abstract string   `json:"abstract"`
				URL      string   `json:"URL"`
				Author   []struct {
					Given  string `json:"given"`
					Family string `json:"family"`
				} `json:"author"`
				Published struct {
					DateParts [][]int `json:"date-parts"`
				} `json:"published"`
			} `json:"items"`
		} `json:"message"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		s.RecordFailure()
		return nil, providerError("ssrn", "decode: %v", err)
	}

	papers := make([]Paper, 0, len(result.Message.Items))
	for _, item := range result.Message.Items {
		if len(item.Title) == 0 || item.Title[0] == "" {
			continue
		}
		title := strings.TrimSpace(item.Title[0])

		link := item.URL
		if link == "" && item.DOI != "" {
			link = "https://doi.org/" + item.DOI
		}

		authors := make([]string, 0, len(item.Author))
		for _, a := range item.Author {
			name := strings.TrimSpace(a.Given + " " + a.Family)
			if name != " " {
				authors = append(authors, name)
			}
		}

		year := 0
		if len(item.Published.DateParts) > 0 && len(item.Published.DateParts[0]) > 0 {
			year = item.Published.DateParts[0][0]
		}

		// Reuse stripJATSTags from crossref.go if it were exported,
		// but since it's not, we'll implement it or just use a simple version here.
		// For now, I'll just do a simple tag strip.
		abstract := stripHTMLTags(item.Abstract)

		papers = append(papers, Paper{
			ID:         "ssrn:" + item.DOI,
			Title:      title,
			Abstract:   abstract,
			Link:       link,
			DOI:        item.DOI,
			Source:     "ssrn",
			SourceApis: []string{"ssrn"},
			Authors:    authors,
			Year:       year,
		})
	}

	s.RecordSuccess()
	return papers, nil
}

func stripHTMLTags(s string) string {
	var out strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			out.WriteRune(r)
		}
	}
	return strings.TrimSpace(out.String())
}
