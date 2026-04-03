package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// GoogleScholarProvider queries SerpAPI's Google Scholar engine.
// Requires SERPAPI_API_KEY. If the key is not configured, the provider
// returns no results so search can continue without failing the full query.
type GoogleScholarProvider struct {
	BaseProvider
	apiKey string
}

var _ SearchProvider = (*GoogleScholarProvider)(nil)

// NewGoogleScholarProvider returns the Google Scholar provider using SerpAPI.
func NewGoogleScholarProvider() *GoogleScholarProvider {
	return &GoogleScholarProvider{
		apiKey: os.Getenv("SERPAPI_API_KEY"),
	}
}

func (g *GoogleScholarProvider) Name() string { return "google_scholar" }

func (g *GoogleScholarProvider) Tools() []string {
	return []string{"author_lookup"}
}

func (g *GoogleScholarProvider) Domains() []string {
	return []string{"cs", "ai", "biology", "medicine", "social", "engineering", "humanities", "climate"}
}

func (g *GoogleScholarProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	if strings.TrimSpace(g.apiKey) == "" {
		return []Paper{}, nil
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	endpoint := fmt.Sprintf(
		"https://serpapi.com/search.json?engine=google_scholar&hl=en&num=%d&q=%s&api_key=%s",
		limit,
		url.QueryEscape(query),
		url.QueryEscape(g.apiKey),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		g.RecordFailure()
		return nil, providerError("google_scholar", "build request: %v", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		g.RecordFailure()
		return nil, providerError("google_scholar", "request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		g.RecordFailure()
		return nil, providerError("google_scholar", "HTTP %d", resp.StatusCode)
	}

	var payload struct {
		Results []map[string]any `json:"organic_results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		g.RecordFailure()
		return nil, providerError("google_scholar", "decode: %v", err)
	}

	papers := make([]Paper, 0, len(payload.Results))
	for _, raw := range payload.Results {
		title := asString(raw["title"])
		if title == "" {
			continue
		}

		link := asString(raw["link"])
		summary := asString(raw["snippet"])
		doi := parseDOIFromString(asString(raw["snippet"]))
		if doi == "" {
			doi = parseDOIFromString(link)
		}

		paper := Paper{
			ID:       g.Name() + ":" + sanitizePaperID(title),
			Title:    title,
			Abstract: summary,
			Link:     link,
			DOI:      doi,
			Source:   g.Name(),
		}
		if doi != "" {
			paper.ID = g.Name() + ":" + doi
		}

		if year := parseYearFromString(asString(raw["year"])); year > 0 {
			paper.Year = year
		} else if publicationInfo, ok := raw["publication_info"].(map[string]any); ok {
			paper.Year = parseYearFromString(asString(publicationInfo["summary"]))
		}

		if publicationInfo, ok := raw["publication_info"].(map[string]any); ok {
			paper.Authors = parseAuthorList(publicationInfo["authors"])
		} else if authors, ok := raw["authors"].([]any); ok {
			paper.Authors = parseAuthorList(authors)
		}

		paper.CitationCount = parseCitationCount(raw)
		papers = append(papers, paper)
	}

	g.RecordSuccess()
	return papers, nil
}

func (g *GoogleScholarProvider) SearchByAuthor(ctx context.Context, authorID string, limit int) ([]Paper, error) {
	if strings.TrimSpace(g.apiKey) == "" {
		return []Paper{}, nil
	}

	if limit <= 0 {
		limit = 20
	}

	endpoint := fmt.Sprintf(
		"https://serpapi.com/search.json?engine=google_scholar_author&hl=en&num=%d&author_id=%s&api_key=%s",
		limit,
		url.QueryEscape(authorID),
		url.QueryEscape(g.apiKey),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build author request: %w", err)
	}

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("author request failed: %w", err)
	}
	defer resp.Body.Close()

	var payload struct {
		Articles []map[string]any `json:"articles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode author results: %w", err)
	}

	papers := make([]Paper, 0, len(payload.Articles))
	for _, raw := range payload.Articles {
		title := asString(raw["title"])
		if title == "" {
			continue
		}

		papers = append(papers, Paper{
			ID:            g.Name() + ":" + sanitizePaperID(title),
			Title:         title,
			Link:          asString(raw["link"]),
			Source:        g.Name(),
			Authors:       parseAuthorList(raw["authors"]),
			Year:          parseYearFromString(asString(raw["year"])),
			CitationCount: parseCitationCount(raw),
		})
	}

	return papers, nil
}

func parseYearFromString(v string) int {
	var year int
	if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &year); err == nil {
		return year
	}
	return 0
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func parseDOIFromString(v string) string {
	const marker = "10."
	idx := strings.Index(v, marker)
	if idx < 0 {
		return ""
	}
	raw := v[idx:]
	raw = strings.TrimPrefix(raw, "https://doi.org/")
	raw = strings.TrimSpace(strings.Trim(raw, ". ,;:()[]{}\"'"))
	return raw
}

func parseAuthorList(value any) []string {
	var authors []string
	list, ok := value.([]any)
	if !ok {
		return authors
	}
	for _, item := range list {
		if name, ok := item.(string); ok {
			name = strings.TrimSpace(name)
			if name != "" {
				authors = append(authors, name)
			}
			continue
		}
		if itemMap, ok := item.(map[string]any); ok {
			if name, ok := itemMap["name"].(string); ok {
				name = strings.TrimSpace(name)
				if name != "" {
					authors = append(authors, name)
				}
			}
		}
	}
	return authors
}

func parseCitationCount(v map[string]any) int {
	pub, ok := v["inline_links"].(map[string]any)
	if !ok {
		return 0
	}
	citedBy, ok := pub["cited_by"].(map[string]any)
	if !ok {
		return 0
	}
	switch typed := citedBy["total"].(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		if c, err := typed.Int64(); err == nil {
			return int(c)
		}
	default:
		return 0
	}
	return 0
}

func sanitizePaperID(value string) string {
	out := strings.ToLower(strings.TrimSpace(value))
	out = strings.ReplaceAll(out, " ", "_")
	out = strings.ReplaceAll(out, "/", "_")
	out = strings.ReplaceAll(out, "\\", "_")
	out = strings.ReplaceAll(out, "\n", "_")
	return out
}
