package wisdev

import (
	"context"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

func executeFullPaperRetrieveAction(
	ctx context.Context,
	rdb redis.UniversalClient,
	session *AgentSession,
	payload map[string]any,
	degraded bool,
) (map[string]any, []Source, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	query, opts := resolveRetrievePapersSearchOptions(payload, session, degraded)
	queries := boundedFullPaperQueries(query, payload)
	if len(queries) == 0 {
		return nil, nil, fmt.Errorf("query is required for research.fullPaperRetrieve")
	}
	if opts.Limit <= 0 {
		opts.Limit = 10
	}
	perQueryLimit := MaxInt(1, opts.Limit/len(queries))
	if perQueryLimit > 5 {
		perQueryLimit = 5
	}

	allPapers := make([]Source, 0, opts.Limit)
	seen := map[string]struct{}{}
	trajectory := make([]map[string]any, 0, len(queries))
	for _, q := range queries {
		localOpts := opts
		localOpts.Limit = perQueryLimit
		papers, result, err := runRetrievePapers(ctx, rdb, q, localOpts)
		if err != nil {
			return nil, nil, err
		}
		trajectory = append(trajectory, map[string]any{
			"query":     q,
			"queryUsed": result["queryUsed"],
			"count":     len(papers),
			"traceId":   result["traceId"],
		})
		for _, paper := range papers {
			key := strings.TrimSpace(firstNonEmpty(paper.ID, paper.DOI, paper.ArxivID, paper.Title))
			if key == "" {
				key = fmt.Sprintf("%s:%d", q, len(allPapers))
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			allPapers = append(allPapers, paper)
			if opts.Limit > 0 && len(allPapers) >= opts.Limit {
				break
			}
		}
		if opts.Limit > 0 && len(allPapers) >= opts.Limit {
			break
		}
	}

	result := buildCanonicalRetrievalPayload(allPapers, query, map[string]any{
		"query":               query,
		"queryUsed":           query,
		"traceId":             opts.TraceID,
		"retrievalStrategies": stringSliceToAny(opts.RetrievalStrategies),
		"retrievalTrace":      mapsToAny(trajectory),
	}, degraded, "")
	result["mode"] = "full_paper"
	result["queryTrajectory"] = mapsToAny(trajectory)
	result["fullPaperBundle"] = result["paperBundle"]
	return result, allPapers, nil
}

func executeFullPaperGatewayDispatchAction(
	ctx context.Context,
	rdb redis.UniversalClient,
	session *AgentSession,
	payload map[string]any,
	degraded bool,
) (map[string]any, []Source, error) {
	action := strings.ToLower(strings.TrimSpace(AsOptionalString(payload["action"])))
	switch action {
	case "academic_search", "search", "retrieve", "retrieve_papers", "source_bundle":
		return executeFullPaperRetrieveAction(ctx, rdb, session, payload, degraded)
	case "source_bundle_preview", "preview_sources", "preview":
		input := map[string]any{}
		if typed, ok := payload["input"].(map[string]any); ok {
			input = typed
		}
		papers := sourcesFromAnyList(firstNonEmptyValue(payload["papers"], payload["sources"], input["papers"], input["sources"]))
		query := strings.TrimSpace(AsOptionalString(firstNonEmptyValue(payload["query"], input["query"])))
		result := buildCanonicalRetrievalPayload(papers, query, map[string]any{
			"query":     query,
			"queryUsed": query,
			"traceId":   firstNonEmpty(AsOptionalString(payload["traceId"]), NewTraceID()),
		}, degraded, "")
		result["mode"] = "full_paper"
		result["stageId"] = AsOptionalString(payload["stageId"])
		result["preview"] = true
		return result, papers, nil
	default:
		if action == "" {
			return nil, nil, fmt.Errorf("action is required for research.fullPaperGatewayDispatch")
		}
		return nil, nil, fmt.Errorf("unsupported research.fullPaperGatewayDispatch action: %s", action)
	}
}

func boundedFullPaperQueries(query string, payload map[string]any) []string {
	candidates := []string{query}
	candidates = append(candidates, toStringSlice(firstNonEmptyValue(payload["planQueries"], payload["queries"]))...)
	categories := toStringSlice(payload["categories"])
	for _, category := range categories {
		category = strings.TrimSpace(category)
		if category != "" && strings.TrimSpace(query) != "" {
			candidates = append(candidates, strings.TrimSpace(query)+" "+category)
		}
	}
	out := make([]string, 0, MinInt(len(candidates), 6))
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		key := strings.ToLower(candidate)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, candidate)
		if len(out) >= 6 {
			break
		}
	}
	return out
}
