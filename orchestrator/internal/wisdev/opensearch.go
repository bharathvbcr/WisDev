package wisdev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ── Data Contracts ────────────────────────────────────────────────────────────

type OpenSearchHybridRequest struct {
	Query           string         `json:"query"`
	Limit           int            `json:"limit"`
	Filters         map[string]any `json:"filters,omitempty"`
	LatencyBudgetMs int            `json:"latencyBudgetMs,omitempty"`
	RetrievalMode   string         `json:"retrievalMode,omitempty"`
	FusionMode      string         `json:"fusionMode,omitempty"`
}

type OpenSearchHybridResponse struct {
	Papers            []map[string]any   `json:"papers"`
	TotalFound        int                `json:"totalFound"`
	LatencyMs         int                `json:"latencyMs"`
	BackendUsed       string             `json:"backendUsed"`
	FallbackTriggered bool               `json:"fallbackTriggered"`
	FallbackReason    string             `json:"fallbackReason,omitempty"`
	QualitySignals    map[string]float64 `json:"qualitySignals,omitempty"`
}

// ── Transport ─────────────────────────────────────────────────────────────────

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// osClient is a package-level HTTP client for OpenSearch; reusing it across
// requests takes advantage of keep-alive TCP connections.
var osClient HTTPClient = &http.Client{Timeout: 12 * time.Second}

// resolveOpenSearchConfig reads connection parameters from environment
// variables. Returns ok=false when OPENSEARCH_URL is not set, signalling that
// the cluster is not configured and calls should degrade gracefully.
func resolveOpenSearchConfig() (baseURL, index, user, password string, ok bool) {
	baseURL = strings.TrimRight(os.Getenv("OPENSEARCH_URL"), "/")
	if baseURL == "" {
		return "", "", "", "", false
	}
	index = os.Getenv("OPENSEARCH_INDEX")
	if index == "" {
		index = "wisdev-papers"
	}
	user = os.Getenv("OPENSEARCH_USER")
	password = os.Getenv("OPENSEARCH_PASSWORD")
	return baseURL, index, user, password, true
}

func applyOSAuth(req *http.Request, user, password string) {
	if user != "" {
		req.SetBasicAuth(user, password)
	}
}

func buildOSFilterClauses(filters map[string]any) []map[string]any {
	if len(filters) == 0 {
		return nil
	}

	clauses := make([]map[string]any, 0, len(filters))
	for field, value := range filters {
		switch typed := value.(type) {
		case map[string]any:
			clauses = append(clauses, map[string]any{
				"range": map[string]any{field: typed},
			})
		case []string:
			clauses = append(clauses, map[string]any{
				"terms": map[string]any{field: typed},
			})
		case []any:
			clauses = append(clauses, map[string]any{
				"terms": map[string]any{field: typed},
			})
		default:
			clauses = append(clauses, map[string]any{
				"term": map[string]any{field: value},
			})
		}
	}
	return clauses
}

// ── Main entry point ──────────────────────────────────────────────────────────

// OpenSearchHybridSearch executes a BM25 hybrid search against an OpenSearch
// cluster. When OPENSEARCH_URL is not set it returns an empty result set with
// FallbackTriggered=true so callers can route to alternative providers without
// treating the absence of OpenSearch as a hard error.
func OpenSearchHybridSearch(ctx context.Context, req OpenSearchHybridRequest) (OpenSearchHybridResponse, error) {
	start := time.Now()
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}

	baseURL, index, user, password, ok := resolveOpenSearchConfig()
	if !ok {
		return OpenSearchHybridResponse{
			Papers:            []map[string]any{},
			TotalFound:        0,
			LatencyMs:         int(time.Since(start).Milliseconds()),
			BackendUsed:       "opensearch_hybrid",
			FallbackTriggered: true,
			FallbackReason:    "OPENSEARCH_URL not configured",
		}, nil
	}

	// Health-check probe: just confirm the index is reachable.
	if req.Query == "health-check" {
		return osHealthCheck(ctx, baseURL, index, user, password, start)
	}

	return osHybridSearch(ctx, baseURL, index, user, password, req, limit, start)
}

// ── Health check ──────────────────────────────────────────────────────────────

func osHealthCheck(ctx context.Context, baseURL, index, user, password string, start time.Time) (OpenSearchHybridResponse, error) {
	url := fmt.Sprintf("%s/%s/_count", baseURL, index)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return osFallback(start, fmt.Sprintf("health_check_request_failed: %v", err)), nil
	}
	applyOSAuth(httpReq, user, password)

	resp, err := osClient.Do(httpReq)
	if err != nil {
		return osFallback(start, fmt.Sprintf("opensearch_unreachable: %v", err)), nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return osFallback(start, fmt.Sprintf("opensearch_status_%d", resp.StatusCode)), nil
	}
	return OpenSearchHybridResponse{
		Papers:      []map[string]any{},
		TotalFound:  0,
		LatencyMs:   int(time.Since(start).Milliseconds()),
		BackendUsed: "opensearch_hybrid",
		QualitySignals: map[string]float64{
			"health": 1.0,
		},
	}, nil
}

// ── Hybrid search ─────────────────────────────────────────────────────────────

func osHybridSearch(
	ctx context.Context,
	baseURL, index, user, password string,
	req OpenSearchHybridRequest,
	limit int,
	start time.Time,
) (OpenSearchHybridResponse, error) {
	// Build the BM25 multi_match query. The "hybrid" retrieval mode is
	// handled here as field-boosted lexical search; a kNN clause can be
	// appended later once an ML model is configured in the cluster.
	queryBody := map[string]any{
		"size": limit,
		"query": map[string]any{
			"multi_match": map[string]any{
				"query":     req.Query,
				"fields":    []string{"title^3", "abstract^2", "authors", "venue", "keywords"},
				"type":      "best_fields",
				"fuzziness": "AUTO",
			},
		},
		"_source": []string{
			"id", "title", "abstract", "authors",
			"year", "venue", "doi", "url", "citationCount",
		},
	}

	// Apply user-supplied filters as term queries wrapping the main query.
	if len(req.Filters) > 0 {
		filters := buildOSFilterClauses(req.Filters)
		queryBody["query"] = map[string]any{
			"bool": map[string]any{
				"must":   queryBody["query"],
				"filter": filters,
			},
		}
	}

	body, err := json.Marshal(queryBody)
	if err != nil {
		return osFallback(start, fmt.Sprintf("query_marshal_failed: %v", err)), nil
	}

	url := fmt.Sprintf("%s/%s/_search", baseURL, index)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return osFallback(start, fmt.Sprintf("request_build_failed: %v", err)), nil
	}
	httpReq.Header.Set("Content-Type", "application/json")
	applyOSAuth(httpReq, user, password)

	resp, err := osClient.Do(httpReq)
	if err != nil {
		return osFallback(start, fmt.Sprintf("opensearch_unreachable: %v", err)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return osFallback(start, fmt.Sprintf("opensearch_status_%d: %s", resp.StatusCode, string(raw))), nil
	}

	return parseOSResponse(resp.Body, start, limit)
}

// ── Response parsing ──────────────────────────────────────────────────────────

type osSearchResponse struct {
	Hits struct {
		Total struct {
			Value int `json:"value"`
		} `json:"total"`
		Hits []struct {
			ID     string         `json:"_id"`
			Score  float64        `json:"_score"`
			Source map[string]any `json:"_source"`
		} `json:"hits"`
	} `json:"hits"`
}

func parseOSResponse(body io.Reader, start time.Time, limit int) (OpenSearchHybridResponse, error) {
	var osResp osSearchResponse
	if err := json.NewDecoder(body).Decode(&osResp); err != nil {
		return osFallback(start, fmt.Sprintf("response_parse_failed: %v", err)), nil
	}

	papers := make([]map[string]any, 0, len(osResp.Hits.Hits))
	var totalScore float64
	for _, hit := range osResp.Hits.Hits {
		doc := hit.Source
		if doc == nil {
			doc = make(map[string]any)
		}
		// Normalise: prefer the _source id field, fall back to _id.
		if _, hasID := doc["id"]; !hasID {
			doc["id"] = hit.ID
		}
		doc["_score"] = hit.Score
		papers = append(papers, doc)
		totalScore += hit.Score
	}

	if len(papers) > limit {
		papers = papers[:limit]
	}

	avgScore := 0.0
	if len(papers) > 0 {
		avgScore = totalScore / float64(len(papers))
	}

	return OpenSearchHybridResponse{
		Papers:      papers,
		TotalFound:  osResp.Hits.Total.Value,
		LatencyMs:   int(time.Since(start).Milliseconds()),
		BackendUsed: "opensearch_hybrid",
		QualitySignals: map[string]float64{
			"avg_bm25_score": avgScore,
			"result_count":   float64(len(papers)),
		},
	}, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func osFallback(start time.Time, reason string) OpenSearchHybridResponse {
	return OpenSearchHybridResponse{
		Papers:            []map[string]any{},
		TotalFound:        0,
		LatencyMs:         int(time.Since(start).Milliseconds()),
		BackendUsed:       "opensearch_hybrid",
		FallbackTriggered: true,
		FallbackReason:    reason,
	}
}
