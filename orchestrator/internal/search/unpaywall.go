package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	unpaywallAPIBase   = "https://api.unpaywall.org/v2"
	unpaywallTimeout   = 6 * time.Second
	unpaywallCacheTTL  = 6 * time.Hour
	unpaywallNullTTL   = 6 * time.Hour
)

// OpenAccessLocation is a single Unpaywall OA location.
type OpenAccessLocation struct {
	URL               string `json:"url"`
	URLForPDF         string `json:"url_for_pdf,omitempty"`
	URLForLandingPage string `json:"url_for_landing_page,omitempty"`
	Evidence          string `json:"evidence,omitempty"`
	License           string `json:"license,omitempty"`
	Version           string `json:"version,omitempty"`
	HostType          string `json:"host_type,omitempty"`
	IsBest            bool   `json:"is_best,omitempty"`
	Updated           string `json:"updated,omitempty"`
}

// OpenAccessInfo is the Go-owned open-access lookup result (Unpaywall shape).
type OpenAccessInfo struct {
	DOI            string               `json:"doi"`
	IsOA           bool                 `json:"is_oa"`
	Title          string               `json:"title,omitempty"`
	BestOALocation *OpenAccessLocation  `json:"best_oa_location,omitempty"`
	OALocations    []OpenAccessLocation `json:"oa_locations,omitempty"`
	OAStatus       string               `json:"oa_status,omitempty"`
	Publisher      string               `json:"publisher,omitempty"`
	JournalName    string               `json:"journal_name,omitempty"`
	PublishedDate  string               `json:"published_date,omitempty"`
	Source         string               `json:"source,omitempty"` // unpaywall | arxiv_fallback
}

type unpaywallCacheEntry struct {
	value     *OpenAccessInfo
	expiresAt time.Time
}

var (
	unpaywallCacheMu sync.Mutex
	unpaywallCache   = map[string]unpaywallCacheEntry{}
)

// LookupOpenAccess resolves open-access metadata for a DOI via Unpaywall,
// with arXiv DOI fallback and in-process caching.
func LookupOpenAccess(ctx context.Context, doi string) (*OpenAccessInfo, error) {
	clean := NormalizeDOI(doi)
	if clean == "" {
		return nil, nil
	}

	unpaywallCacheMu.Lock()
	if entry, ok := unpaywallCache[clean]; ok && entry.expiresAt.After(time.Now()) {
		unpaywallCacheMu.Unlock()
		return entry.value, nil
	}
	unpaywallCacheMu.Unlock()

	if fallback := buildArxivOpenAccessFallback(clean); fallback != nil {
		cacheOpenAccess(clean, fallback, unpaywallCacheTTL)
		return fallback, nil
	}

	ctx, cancel := context.WithTimeout(ctx, unpaywallTimeout)
	defer cancel()

	email := academicPoliteEmail("UNPAYWALL_EMAIL")
	reqURL := fmt.Sprintf("%s/%s?email=%s", unpaywallAPIBase, url.PathEscape(clean), url.QueryEscape(email))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("unpaywall: build request: %w", err)
	}

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("unpaywall: request failed: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var info OpenAccessInfo
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
			return nil, fmt.Errorf("unpaywall: decode failed: %w", err)
		}
		info.DOI = clean
		info.Source = "unpaywall"
		cacheOpenAccess(clean, &info, unpaywallCacheTTL)
		return &info, nil
	case http.StatusNotFound:
		cacheOpenAccess(clean, nil, unpaywallNullTTL)
		return nil, nil
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("unpaywall: rate limit exceeded (429)")
	default:
		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("unpaywall: upstream error (%d)", resp.StatusCode)
		}
		return nil, fmt.Errorf("unpaywall: unexpected status %d", resp.StatusCode)
	}
}

func cacheOpenAccess(doi string, value *OpenAccessInfo, ttl time.Duration) {
	unpaywallCacheMu.Lock()
	defer unpaywallCacheMu.Unlock()
	unpaywallCache[doi] = unpaywallCacheEntry{value: value, expiresAt: time.Now().Add(ttl)}
}

func buildArxivOpenAccessFallback(doi string) *OpenAccessInfo {
	arxivID := ExtractArxivIDFromDOI(doi)
	if arxivID == "" {
		return nil
	}
	pdfURL := "https://arxiv.org/pdf/" + arxivID + ".pdf"
	absURL := "https://arxiv.org/abs/" + arxivID
	updated := time.Now().UTC().Format(time.RFC3339)
	loc := OpenAccessLocation{
		URL:               pdfURL,
		URLForPDF:         pdfURL,
		URLForLandingPage: absURL,
		Evidence:          "arxiv_doi_fallback",
		Version:           "submittedVersion",
		HostType:          "repository",
		IsBest:            true,
		Updated:           updated,
	}
	return &OpenAccessInfo{
		DOI:            doi,
		IsOA:           true,
		OAStatus:       "green",
		BestOALocation: &loc,
		OALocations:    []OpenAccessLocation{loc},
		Source:         "arxiv_fallback",
	}
}

// ResetOpenAccessCacheForTests clears the in-process Unpaywall cache.
func ResetOpenAccessCacheForTests() {
	unpaywallCacheMu.Lock()
	defer unpaywallCacheMu.Unlock()
	unpaywallCache = map[string]unpaywallCacheEntry{}
}
