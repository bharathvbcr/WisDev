package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	openRetractionsAPI = "https://api.openretractions.com/doi"
	crossrefWorksAPI   = "https://api.crossref.org/works"
	retractionTimeout  = 4 * time.Second
	retractionCacheTTL = 7 * 24 * time.Hour
	retractionUnknownTTL = time.Hour
	maxRetractionBatch = 25
)

// RetractionInfo is the Go-owned retraction check result.
type RetractionInfo struct {
	DOI                 string `json:"doi"`
	IsRetracted         bool   `json:"isRetracted"`
	RetractionDate      string `json:"retractionDate,omitempty"`
	RetractionReason    string `json:"retractionReason,omitempty"`
	RetractionNoticeDOI string `json:"retractionNoticeDOI,omitempty"`
	Source              string `json:"source"` // openretractions | crossref | unknown | skip
	FetchedAt           string `json:"fetchedAt"`
}

type retractionCacheEntry struct {
	value     RetractionInfo
	expiresAt time.Time
}

var (
	retractionCacheMu sync.Mutex
	retractionCache   = map[string]retractionCacheEntry{}
)

// CheckRetractionsBatch looks up retraction status for DOIs via OpenRetractions
// with Crossref fallback. Skips DOI families that routinely miss.
func CheckRetractionsBatch(ctx context.Context, dois []string) ([]RetractionInfo, error) {
	if len(dois) == 0 {
		return []RetractionInfo{}, nil
	}
	if len(dois) > maxRetractionBatch {
		return nil, fmt.Errorf("too many dois (max %d)", maxRetractionBatch)
	}

	out := make([]RetractionInfo, 0, len(dois))
	seen := map[string]struct{}{}
	for _, raw := range dois {
		doi := NormalizeDOI(raw)
		if doi == "" {
			continue
		}
		key := strings.ToLower(doi)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, checkSingleRetraction(ctx, doi))
	}
	return out, nil
}

func checkSingleRetraction(ctx context.Context, doi string) RetractionInfo {
	now := time.Now().UTC().Format(time.RFC3339)
	if cached, ok := getCachedRetraction(doi); ok {
		return cached
	}
	if IsLikelyRetractionProviderMissDOI(doi) {
		info := RetractionInfo{DOI: doi, IsRetracted: false, Source: "skip", FetchedAt: now}
		cacheRetraction(doi, info, retractionUnknownTTL)
		return info
	}

	if info, err := checkOpenRetractions(ctx, doi); err == nil && info != nil {
		cacheRetraction(doi, *info, retractionCacheTTL)
		return *info
	}
	if info, err := checkCrossrefRetraction(ctx, doi); err == nil && info != nil {
		cacheRetraction(doi, *info, retractionCacheTTL)
		return *info
	}

	info := RetractionInfo{DOI: doi, IsRetracted: false, Source: "unknown", FetchedAt: now}
	cacheRetraction(doi, info, retractionUnknownTTL)
	return info
}

func checkOpenRetractions(ctx context.Context, doi string) (*RetractionInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, retractionTimeout)
	defer cancel()

	reqURL := fmt.Sprintf("%s/%s", openRetractionsAPI, url.PathEscape(doi))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var raw map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
			return nil, err
		}
		info := &RetractionInfo{
			DOI:                 doi,
			IsRetracted:         true,
			RetractionDate:      firstString(raw, "retraction_date", "timestamp"),
			RetractionReason:    firstString(raw, "retraction_nature", "reason"),
			RetractionNoticeDOI: firstString(raw, "retraction_doi"),
			Source:              "openretractions",
			FetchedAt:           time.Now().UTC().Format(time.RFC3339),
		}
		return info, nil
	case http.StatusNotFound:
		return &RetractionInfo{
			DOI:         doi,
			IsRetracted: false,
			Source:      "openretractions",
			FetchedAt:   time.Now().UTC().Format(time.RFC3339),
		}, nil
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("openretractions: rate limit (429)")
	default:
		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("openretractions: upstream %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("openretractions: status %d", resp.StatusCode)
	}
}

func checkCrossrefRetraction(ctx context.Context, doi string) (*RetractionInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, retractionTimeout)
	defer cancel()

	email := academicPoliteEmail("CROSSREF_MAILTO")
	reqURL := fmt.Sprintf("%s/%s?mailto=%s", crossrefWorksAPI, url.PathEscape(doi), url.QueryEscape(email))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			return nil, fmt.Errorf("crossref: rate limit (%d)", resp.StatusCode)
		}
		return nil, fmt.Errorf("crossref: status %d", resp.StatusCode)
	}

	var payload struct {
		Message map[string]any `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	work := payload.Message
	isRetracted := crossrefWorkIsRetracted(work)
	noticeDOI := ""
	if rel, ok := work["relation"].(map[string]any); ok {
		if retractedBy, ok := rel["is-retracted-by"].([]any); ok && len(retractedBy) > 0 {
			if first, ok := retractedBy[0].(map[string]any); ok {
				noticeDOI, _ = first["id"].(string)
			}
		}
	}
	return &RetractionInfo{
		DOI:                 doi,
		IsRetracted:         isRetracted,
		RetractionNoticeDOI: noticeDOI,
		Source:              "crossref",
		FetchedAt:           time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func crossrefWorkIsRetracted(work map[string]any) bool {
	if work == nil {
		return false
	}
	if typ, _ := work["type"].(string); typ == "retracted-article" {
		return true
	}
	if updates, ok := work["update-to"].([]any); ok {
		for _, raw := range updates {
			update, _ := raw.(map[string]any)
			typ, _ := update["type"].(string)
			if typ == "retraction" || typ == "withdrawal" {
				return true
			}
		}
	}
	if rel, ok := work["relation"].(map[string]any); ok {
		if retractedBy, ok := rel["is-retracted-by"].([]any); ok && len(retractedBy) > 0 {
			return true
		}
	}
	return false
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			switch typed := v.(type) {
			case string:
				if strings.TrimSpace(typed) != "" {
					return strings.TrimSpace(typed)
				}
			}
		}
	}
	return ""
}

func getCachedRetraction(doi string) (RetractionInfo, bool) {
	retractionCacheMu.Lock()
	defer retractionCacheMu.Unlock()
	entry, ok := retractionCache[strings.ToLower(doi)]
	if !ok || entry.expiresAt.Before(time.Now()) {
		return RetractionInfo{}, false
	}
	return entry.value, true
}

func cacheRetraction(doi string, info RetractionInfo, ttl time.Duration) {
	retractionCacheMu.Lock()
	defer retractionCacheMu.Unlock()
	retractionCache[strings.ToLower(doi)] = retractionCacheEntry{value: info, expiresAt: time.Now().Add(ttl)}
}

// ResetRetractionCacheForTests clears the in-process retraction cache.
func ResetRetractionCacheForTests() {
	retractionCacheMu.Lock()
	defer retractionCacheMu.Unlock()
	retractionCache = map[string]retractionCacheEntry{}
}
