package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const openAlexAPIBase = "https://api.openalex.org"

// AuthorMetrics is OpenAlex author-level bibliometrics.
type AuthorMetrics struct {
	AuthorID              string   `json:"authorId"`
	DisplayName           string   `json:"displayName"`
	HIndex                int      `json:"hIndex"`
	I10Index              int      `json:"i10Index"`
	WorksCount            int      `json:"worksCount"`
	CitedByCount          int      `json:"citedByCount"`
	TwoYearMeanCitedness  float64  `json:"twoYearMeanCitedness"`
	ORCID                 string   `json:"orcid,omitempty"`
	Affiliations          []string `json:"affiliations"`
	LastKnownInstitution  string   `json:"lastKnownInstitution,omitempty"`
	Success               bool     `json:"success"`
	Error                 string   `json:"error,omitempty"`
}

// SourceMetrics is OpenAlex source/venue-level bibliometrics.
type SourceMetrics struct {
	SourceID             string   `json:"sourceId"`
	DisplayName          string   `json:"displayName"`
	TwoYearMeanCitedness float64  `json:"twoYearMeanCitedness"`
	HIndex               int      `json:"hIndex"`
	WorksCount           int      `json:"worksCount"`
	CitedByCount         int      `json:"citedByCount"`
	Type                 string   `json:"type"`
	ISSN                 []string `json:"issn,omitempty"`
	IsOA                 bool     `json:"isOa"`
	Success              bool     `json:"success"`
	Error                string   `json:"error,omitempty"`
}

var issnPattern = regexp.MustCompile(`(?i)^\d{4}-?\d{3}[\dX]$`)

// LookupAuthorMetrics fetches author bibliometrics from OpenAlex.
func LookupAuthorMetrics(ctx context.Context, authorID string) AuthorMetrics {
	authorID = strings.TrimSpace(authorID)
	fail := func(msg string) AuthorMetrics {
		return AuthorMetrics{
			AuthorID:     authorID,
			DisplayName:  authorID,
			Affiliations: []string{},
			Success:      false,
			Error:        msg,
		}
	}
	if authorID == "" {
		return fail("authorId is required")
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	isOpenAlexID := strings.HasPrefix(authorID, "A") || strings.HasPrefix(authorID, "https://openalex.org/")
	cleanID := strings.TrimPrefix(authorID, "https://openalex.org/")
	email := academicPoliteEmail("OPENALEX_EMAIL")

	var reqURL string
	if isOpenAlexID {
		reqURL = fmt.Sprintf("%s/authors/%s?mailto=%s", openAlexAPIBase, url.PathEscape(cleanID), url.QueryEscape(email))
	} else {
		reqURL = fmt.Sprintf("%s/authors?filter=display_name.search:%s&mailto=%s",
			openAlexAPIBase, url.QueryEscape(authorID), url.QueryEscape(email))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fail(err.Error())
	}
	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		return fail(err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fail(fmt.Sprintf("OpenAlex returned %d", resp.StatusCode))
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fail(err.Error())
	}

	author := payload
	if !isOpenAlexID {
		results, _ := payload["results"].([]any)
		if len(results) == 0 {
			return fail("Author not found")
		}
		author, _ = results[0].(map[string]any)
		if author == nil {
			return fail("Author not found")
		}
	}

	stats, _ := author["summary_stats"].(map[string]any)
	affiliations := []string{}
	if rawAff, ok := author["affiliations"].([]any); ok {
		for _, item := range rawAff {
			m, _ := item.(map[string]any)
			inst, _ := m["institution"].(map[string]any)
			if name, _ := inst["display_name"].(string); strings.TrimSpace(name) != "" {
				affiliations = append(affiliations, strings.TrimSpace(name))
			}
		}
	}
	lastInst := ""
	if inst, ok := author["last_known_institution"].(map[string]any); ok {
		lastInst, _ = inst["display_name"].(string)
	}

	return AuthorMetrics{
		AuthorID:             firstNonEmptyString(asString(author["id"]), authorID),
		DisplayName:          firstNonEmptyString(asString(author["display_name"]), authorID),
		HIndex:               asInt(stats["h_index"]),
		I10Index:             asInt(stats["i10_index"]),
		WorksCount:           asInt(author["works_count"]),
		CitedByCount:         asInt(author["cited_by_count"]),
		TwoYearMeanCitedness: asFloat(stats["2yr_mean_citedness"]),
		ORCID:                asString(author["orcid"]),
		Affiliations:         affiliations,
		LastKnownInstitution: lastInst,
		Success:              true,
	}
}

// LookupSourceMetrics fetches source/venue bibliometrics from OpenAlex.
func LookupSourceMetrics(ctx context.Context, sourceID string) SourceMetrics {
	sourceID = strings.TrimSpace(sourceID)
	fail := func(msg string) SourceMetrics {
		return SourceMetrics{
			SourceID:    sourceID,
			DisplayName: sourceID,
			Type:        "unknown",
			Success:     false,
			Error:       msg,
		}
	}
	if sourceID == "" {
		return fail("sourceId is required")
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	isOpenAlexID := strings.HasPrefix(sourceID, "S") || strings.HasPrefix(sourceID, "https://openalex.org/")
	isISSN := issnPattern.MatchString(sourceID)
	cleanID := strings.TrimPrefix(sourceID, "https://openalex.org/")
	email := academicPoliteEmail("OPENALEX_EMAIL")

	var reqURL string
	switch {
	case isOpenAlexID:
		reqURL = fmt.Sprintf("%s/sources/%s?mailto=%s", openAlexAPIBase, url.PathEscape(cleanID), url.QueryEscape(email))
	case isISSN:
		reqURL = fmt.Sprintf("%s/sources?filter=issn:%s&mailto=%s", openAlexAPIBase, url.QueryEscape(sourceID), url.QueryEscape(email))
	default:
		reqURL = fmt.Sprintf("%s/sources?filter=display_name.search:%s&mailto=%s",
			openAlexAPIBase, url.QueryEscape(sourceID), url.QueryEscape(email))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fail(err.Error())
	}
	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		return fail(err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fail(fmt.Sprintf("OpenAlex returned %d", resp.StatusCode))
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fail(err.Error())
	}

	source := payload
	if !isOpenAlexID {
		results, _ := payload["results"].([]any)
		if len(results) == 0 {
			return fail("Source not found")
		}
		source, _ = results[0].(map[string]any)
		if source == nil {
			return fail("Source not found")
		}
	}

	stats, _ := source["summary_stats"].(map[string]any)
	var issn []string
	if raw, ok := source["issn"].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				issn = append(issn, strings.TrimSpace(s))
			}
		}
	}

	return SourceMetrics{
		SourceID:             firstNonEmptyString(asString(source["id"]), sourceID),
		DisplayName:          firstNonEmptyString(asString(source["display_name"]), sourceID),
		TwoYearMeanCitedness: asFloat(stats["2yr_mean_citedness"]),
		HIndex:               asInt(stats["h_index"]),
		WorksCount:           asInt(source["works_count"]),
		CitedByCount:         asInt(source["cited_by_count"]),
		Type:                 firstNonEmptyString(asString(source["type"]), "unknown"),
		ISSN:                 issn,
		IsOA:                 asBool(source["is_oa"]),
		Success:              true,
	}
}

func asInt(v any) int {
	switch typed := v.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case json.Number:
		n, _ := typed.Int64()
		return int(n)
	default:
		return 0
	}
}

func asFloat(v any) float64 {
	switch typed := v.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case json.Number:
		n, _ := typed.Float64()
		return n
	default:
		return 0
	}
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
