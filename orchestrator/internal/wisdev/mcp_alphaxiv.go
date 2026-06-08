package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// AlphaXivClient implements high-fidelity arxiv lookup via the AlphaXiv MCP server.
type AlphaXivClient struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

func NewAlphaXivClient(baseURL string) *AlphaXivClient {
	return NewAlphaXivClientWithKey(baseURL, "")
}

func NewAlphaXivClientWithKey(baseURL, apiKey string) *AlphaXivClient {
	return &AlphaXivClient{
		BaseURL: baseURL,
		APIKey:  apiKey,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func NewAlphaXivClientFromEnv() *AlphaXivClient {
	base := os.Getenv("ALPHAXIV_BASE_URL")
	if base == "" {
		base = "https://mcp.alphaxiv.org"
	}
	return NewAlphaXivClientWithKey(base, os.Getenv("ALPHAXIV_API_KEY"))
}

func (c *AlphaXivClient) addAuth(req *http.Request) {
	if c.APIKey != "" {
		req.Header.Set("X-API-Key", c.APIKey)
	}
}

// Lookup performs a search query against AlphaXiv.
func (c *AlphaXivClient) Lookup(ctx context.Context, query string) ([]CitationRecord, error) {
	url := fmt.Sprintf("%s/search?q=%s", c.BaseURL, query)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	c.addAuth(req)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("alphaxiv lookup failed with status %d", resp.StatusCode)
	}

	var results []CitationRecord
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, err
	}
	return results, nil
}

// GetPaper retrieves specific metadata for an arxiv ID or DOI.
func (c *AlphaXivClient) GetPaper(ctx context.Context, arxivID string) (CitationRecord, error) {
	url := fmt.Sprintf("%s/paper/%s", c.BaseURL, arxivID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return CitationRecord{}, err
	}

	c.addAuth(req)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return CitationRecord{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return CitationRecord{}, fmt.Errorf("alphaxiv get paper failed with status %d", resp.StatusCode)
	}

	var result CitationRecord
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return CitationRecord{}, err
	}
	return result, nil
}
