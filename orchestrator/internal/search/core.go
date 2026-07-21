package search

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/resilience"
)

type COREProvider struct {
	BaseProvider
	baseURL              string
	mu                   sync.Mutex
	authUnavailableUntil time.Time
}

var _ SearchProvider = (*COREProvider)(nil)

const coreAuthFailureCooldown = 15 * time.Minute

func NewCOREProvider() *COREProvider {
	return &COREProvider{
		baseURL: "https://api.core.ac.uk/v3/search/works",
	}
}

func (c *COREProvider) Name() string { return "core" }

func (c *COREProvider) Domains() []string {
	return []string{} // General domain
}

func (c *COREProvider) authUnavailable() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Before(c.authUnavailableUntil)
}

func (c *COREProvider) markAuthUnavailable() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	shouldLog := !now.Before(c.authUnavailableUntil)
	c.authUnavailableUntil = now.Add(coreAuthFailureCooldown)
	return shouldLog
}

func (c *COREProvider) clearAuthUnavailable() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.authUnavailableUntil = time.Time{}
}

type coreAPIWork struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	Abstract      string `json:"abstract"`
	DOI           string `json:"doi"`
	DownloadURL   string `json:"downloadUrl"`
	YearPublished int    `json:"yearPublished"`
}

type coreAPIResponse struct {
	TotalHits int           `json:"totalHits"`
	Results   []coreAPIWork `json:"results"`
}

func (c *COREProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	apiKey, _ := resilience.GetSecret(context.Background(), "CORE_API_KEY")
	if apiKey == "" {
		// No API key configured -- silently skip this source.
		return []Paper{}, nil
	}
	if c.authUnavailable() {
		return []Paper{}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	searchQuery := query
	if opts.YearFrom > 0 {
		if opts.YearTo > 0 {
			searchQuery += fmt.Sprintf(" AND yearPublished>=%d AND yearPublished<=%d", opts.YearFrom, opts.YearTo)
		} else {
			searchQuery += fmt.Sprintf(" AND yearPublished>=%d", opts.YearFrom)
		}
	}

	reqURL := fmt.Sprintf("%s?q=%s&limit=%d", c.baseURL, url.QueryEscape(searchQuery), limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		c.RecordFailure()
		return nil, providerError("core", "build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		c.RecordFailure()
		return nil, providerError("core", "request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			if c.markAuthUnavailable() {
				slog.Info("optional academic provider skipped due authentication failure",
					"service", "go_orchestrator",
					"runtime", "go",
					"component", "search.provider",
					"operation", "core_search",
					"stage", "provider_auth_failed",
					"provider", "core",
					"status", resp.StatusCode,
					"result", "skipped",
					"error_code", "optional_provider_auth_failed",
					"cooldown_ms", coreAuthFailureCooldown.Milliseconds(),
				)
			}
			c.RecordSuccess()
			return []Paper{}, nil
		}
		c.RecordFailure()
		return nil, providerError("core", "HTTP %d", resp.StatusCode)
	}

	var coreRes coreAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&coreRes); err != nil {
		c.RecordFailure()
		return nil, providerError("core", "decode: %v", err)
	}

	papers := make([]Paper, 0, len(coreRes.Results))
	for _, w := range coreRes.Results {
		link := w.DownloadURL
		if link == "" && w.DOI != "" {
			link = "https://doi.org/" + w.DOI
		}
		papers = append(papers, Paper{
			ID:         fmt.Sprintf("core:%d", w.ID),
			Title:      w.Title,
			Abstract:   w.Abstract,
			Link:       link,
			DOI:        w.DOI,
			Source:     "core",
			SourceApis: []string{"core"},
			Year:       w.YearPublished,
		})
	}

	c.RecordSuccess()
	c.clearAuthUnavailable()
	return papers, nil
}

// LookupCOREFullTextByDOI returns CORE full text for a DOI when available.
// Best-effort: missing API key or empty fullText yields ("", nil).
func LookupCOREFullTextByDOI(ctx context.Context, doi string) (string, error) {
	clean := NormalizeDOI(doi)
	if clean == "" {
		return "", nil
	}
	apiKey, _ := resilience.GetSecret(context.Background(), "CORE_API_KEY")
	if apiKey == "" {
		return "", nil
	}

	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	query := fmt.Sprintf(`doi:"%s"`, strings.ReplaceAll(clean, `"`, `\"`))
	reqURL := fmt.Sprintf("%s?q=%s&limit=1", "https://api.core.ac.uk/v3/search/works", url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("core fulltext: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("core fulltext: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("core fulltext: HTTP %d", resp.StatusCode)
	}

	var payload struct {
		Results []struct {
			FullText string `json:"fullText"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("core fulltext: decode: %w", err)
	}
	if len(payload.Results) == 0 {
		return "", nil
	}
	return strings.TrimSpace(payload.Results[0].FullText), nil
}
