package wisdev

import (
	"context"
	"strings"

	"github.com/redis/go-redis/v9"

	internalsearch "github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

const (
	RetrievalStrategyLexicalBroad       = "lexical_broad"
	RetrievalStrategySemanticFocus      = "semantic_focus"
	RetrievalStrategyCitationSnowball   = "citation_snowball"
	RetrievalStrategyPaperContentLookup = "paper_content_lookup"
	RetrievalStrategyFullTextFollowUp   = "full_text_followup"
)

var retrievalStrategyOrder = []string{
	RetrievalStrategyLexicalBroad,
	RetrievalStrategySemanticFocus,
	RetrievalStrategyCitationSnowball,
	RetrievalStrategyPaperContentLookup,
	RetrievalStrategyFullTextFollowUp,
}

var supportedRetrievalStrategies = map[string]struct{}{
	RetrievalStrategyLexicalBroad:       {},
	RetrievalStrategySemanticFocus:      {},
	RetrievalStrategyCitationSnowball:   {},
	RetrievalStrategyPaperContentLookup: {},
	RetrievalStrategyFullTextFollowUp:   {},
}

var canonicalRetrievalPipeline = []string{
	"provider_routing",
	"query_expansion",
	"parallel_search",
	"rrf_fusion",
	"pageindex_rerank",
	"raptor_context",
	"evidence_gate",
	"citation_trust_gate",
	"synthesis",
	"reviewer_critique",
}

func defaultRetrievalStrategies(mode WisDevMode, tier ServiceTier) []string {
	strategies := []string{
		RetrievalStrategyLexicalBroad,
		RetrievalStrategySemanticFocus,
	}
	if mode == WisDevModeYOLO && tier != ServiceTierStandard {
		strategies = append(strategies, RetrievalStrategyPaperContentLookup)
	}
	return strategies
}

func defaultPageIndexRerank(mode WisDevMode, tier ServiceTier) bool {
	return mode == WisDevModeYOLO || tier == ServiceTierFlex || tier == ServiceTierPriority
}

func defaultStage2Rerank(mode WisDevMode, tier ServiceTier) bool {
	return tier == ServiceTierPriority || (mode == WisDevModeYOLO && tier == ServiceTierFlex)
}

func normalizeRetrievalStrategies(raw any) []string {
	var values []string
	switch typed := raw.(type) {
	case string:
		for _, part := range strings.Split(typed, ",") {
			values = append(values, part)
		}
	case []string:
		values = append(values, typed...)
	case []any:
		for _, item := range typed {
			values = append(values, AsOptionalString(item))
		}
	}

	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if _, ok := supportedRetrievalStrategies[normalized]; !ok {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for _, strategy := range retrievalStrategyOrder {
		if _, ok := seen[strategy]; ok {
			out = append(out, strategy)
		}
	}
	return out
}

func retrievalStrategyEnabled(strategies []string, target string) bool {
	for _, strategy := range strategies {
		if strategy == target {
			return true
		}
	}
	return false
}

func defaultRetrievePapersPlanParams(session *AgentSession) map[string]any {
	mode := WisDevModeGuided
	tier := ServiceTierStandard
	domain := ""
	if session != nil {
		if session.Mode != "" {
			mode = session.Mode
		}
		if session.ServiceTier != "" {
			tier = session.ServiceTier
		}
		domain = strings.TrimSpace(session.DetectedDomain)
	}
	return map[string]any{
		"mode":                mode,
		"serviceTier":         string(tier),
		"domain":              domain,
		"retrievalStrategies": append([]string(nil), defaultRetrievalStrategies(mode, tier)...),
		"stage2Rerank":        defaultStage2Rerank(mode, tier),
		"pageIndexRerank":     defaultPageIndexRerank(mode, tier),
	}
}

func resolveRetrievePapersSearchOptions(payload map[string]any, session *AgentSession, degraded bool) (string, SearchOptions) {
	query := strings.TrimSpace(AsOptionalString(payload["query"]))
	if query == "" && session != nil {
		query = strings.TrimSpace(resolveSessionQuery(session))
	}

	mode := WisDevModeGuided
	if session != nil && session.Mode != "" {
		mode = session.Mode
	} else {
		mode = NormalizeWisDevMode(AsOptionalString(payload["mode"]))
	}

	tier := ServiceTierStandard
	if session != nil && session.ServiceTier != "" {
		tier = session.ServiceTier
	} else if rawTier := strings.TrimSpace(AsOptionalString(payload["serviceTier"])); rawTier != "" {
		tier = ServiceTier(rawTier)
	}

	limit := intFromAny(payload["limit"])
	if limit <= 0 {
		limit = 10
	}
	if degraded && limit > 5 {
		limit = 5
	}

	strategies := normalizeRetrievalStrategies(payload["retrievalStrategies"])
	if len(strategies) == 0 {
		strategies = defaultRetrievalStrategies(mode, tier)
	}

	pageIndexRerank := defaultPageIndexRerank(mode, tier)
	if _, ok := payload["pageIndexRerank"]; ok {
		pageIndexRerank = boolFromAny(payload["pageIndexRerank"])
	}
	stage2Rerank := defaultStage2Rerank(mode, tier)
	if _, ok := payload["stage2Rerank"]; ok {
		stage2Rerank = boolFromAny(payload["stage2Rerank"])
	}

	traceID := strings.TrimSpace(firstNonEmpty(
		AsOptionalString(payload["traceId"]),
		AsOptionalString(payload["traceID"]),
	))
	if traceID == "" {
		traceID = NewTraceID()
	}

	domain := strings.TrimSpace(AsOptionalString(payload["domain"]))
	if domain == "" && session != nil {
		domain = strings.TrimSpace(session.DetectedDomain)
	}

	return query, SearchOptions{
		Limit:               limit,
		ExpandQuery:         retrievalStrategyEnabled(strategies, RetrievalStrategySemanticFocus),
		QualitySort:         true,
		SkipCache:           boolFromAny(payload["skipCache"]),
		Domain:              domain,
		Sources:             normalizeRetrievePaperSources(payload),
		YearFrom:            intFromAny(payload["yearFrom"]),
		YearTo:              intFromAny(payload["yearTo"]),
		TraceID:             traceID,
		Stage2Rerank:        stage2Rerank,
		PageIndexRerank:     pageIndexRerank,
		RetrievalStrategies: strategies,
	}
}

func normalizeRetrievePaperSources(payload map[string]any) []string {
	for _, key := range []string{"sources", "providers", "sourceApis"} {
		if values := dedupeStrings(toStringSlice(payload[key])); len(values) > 0 {
			return values
		}
	}
	return nil
}

func runRetrievePapers(ctx context.Context, rdb redis.UniversalClient, query string, opts SearchOptions) ([]Source, map[string]any, error) {
	if strings.TrimSpace(opts.TraceID) == "" {
		opts.TraceID = NewTraceID()
	}
	if registry := resolveMCPPaperRetrievalRegistry(opts.Registry); registry != nil {
		return runMCPPaperRetrieval(ctx, registry, query, opts)
	}
	result, err := ParallelSearch(ctx, rdb, query, opts)
	if err != nil {
		return nil, nil, err
	}

	payload := map[string]any{
		"papers":    mapsToAny(sourcesToArtifactMaps(result.Papers)),
		"query":     query,
		"queryUsed": result.QueryUsed,
		"count":     len(result.Papers),
		"traceId":   result.TraceID,
	}
	if len(result.RetrievalStrategies) > 0 {
		payload["retrievalStrategies"] = stringSliceToAny(result.RetrievalStrategies)
	}
	if len(result.RetrievalTrace) > 0 {
		payload["retrievalTrace"] = mapsToAny(result.RetrievalTrace)
	}
	return result.Papers, buildCanonicalRetrievalPayload(result.Papers, query, payload, false, ""), nil
}

func resolveMCPPaperRetrievalRegistry(registry *internalsearch.ProviderRegistry) *internalsearch.ProviderRegistry {
	if registry != nil {
		return registry
	}
	return GlobalSearchRegistry
}

func runMCPPaperRetrieval(ctx context.Context, registry *internalsearch.ProviderRegistry, query string, opts SearchOptions) ([]Source, map[string]any, error) {
	params := buildMCPPaperRetrievalParams(query, opts)
	result, err := internalsearch.HandleToolSearch(ctx, registry, internalsearch.ToolSearchPapersName, params)
	if err != nil {
		return nil, nil, err
	}

	papers := make([]Source, 0, len(result.Papers))
	for _, paper := range result.Papers {
		papers = append(papers, mapPaperToSource(paper))
	}
	trace := []map[string]any{{
		"strategy":         "mcp_tool",
		"tool":             internalsearch.ToolSearchPapersName,
		"queryUsed":        strings.TrimSpace(query),
		"domain":           strings.TrimSpace(opts.Domain),
		"retrievalBackend": "provider_registry_mcp",
		"latencyMs":        result.LatencyMs,
	}}
	acquisition := buildResearchSourceAcquisitionPlan(query, result.Papers)
	payload := map[string]any{
		"papers":            mapsToAny(sourcesToArtifactMaps(papers)),
		"query":             strings.TrimSpace(query),
		"queryUsed":         strings.TrimSpace(query),
		"count":             len(papers),
		"traceId":           opts.TraceID,
		"tool":              internalsearch.ToolSearchPapersName,
		"mcpTool":           internalsearch.ToolSearchPapersName,
		"providers":         result.Providers,
		"warnings":          result.Warnings,
		"latencyMs":         result.LatencyMs,
		"retrievalBy":       "wisdev_core_mcp_tool",
		"retrievalTrace":    mapsToAny(trace),
		"sourceAcquisition": acquisition,
	}
	if len(opts.RetrievalStrategies) > 0 {
		payload["retrievalStrategies"] = stringSliceToAny(opts.RetrievalStrategies)
	}
	out := buildCanonicalRetrievalPayload(papers, query, payload, false, "")
	out["contract"] = "wisdev.mcp.paper_retrieval.v1"
	if bundle, ok := out["paperBundle"].(map[string]any); ok {
		bundle["contract"] = "wisdev.mcp.paper_retrieval.v1"
		bundle["tool"] = internalsearch.ToolSearchPapersName
		bundle["mcpTool"] = internalsearch.ToolSearchPapersName
		bundle["providers"] = result.Providers
		bundle["warnings"] = result.Warnings
		bundle["latencyMs"] = result.LatencyMs
		bundle["retrievalBy"] = "wisdev_core_mcp_tool"
		bundle["sourceAcquisition"] = acquisition
	}
	return papers, out, nil
}

func buildMCPPaperRetrievalParams(query string, opts SearchOptions) map[string]any {
	params := map[string]any{
		"query":       strings.TrimSpace(query),
		"limit":       opts.Limit,
		"domain":      strings.TrimSpace(opts.Domain),
		"sources":     stringSliceToAny(opts.Sources),
		"yearFrom":    opts.YearFrom,
		"yearTo":      opts.YearTo,
		"skipCache":   opts.SkipCache,
		"qualitySort": opts.QualitySort,
		"traceId":     opts.TraceID,
	}
	if len(opts.RetrievalStrategies) > 0 {
		params["retrievalStrategies"] = stringSliceToAny(opts.RetrievalStrategies)
	}
	return params
}

func buildCanonicalRetrievalPayload(papers []Source, query string, payload map[string]any, degraded bool, degradedReason string) map[string]any {
	out := cloneAnyMap(payload)
	if out == nil {
		out = map[string]any{}
	}
	if len(papers) > 0 {
		out["papers"] = mapsToAny(sourcesToArtifactMaps(papers))
	}
	bundle := PaperArtifactBundle{
		Papers:              append([]Source(nil), papers...),
		RetrievalStrategies: dedupeStrings(toStringSlice(out["retrievalStrategies"])),
		RetrievalTrace:      firstArtifactMaps(out["retrievalTrace"]),
		QueryUsed:           firstNonEmpty(AsOptionalString(out["queryUsed"]), strings.TrimSpace(query)),
		TraceID:             firstNonEmpty(AsOptionalString(out["traceId"]), NewTraceID()),
	}
	out["query"] = strings.TrimSpace(query)
	out["queryUsed"] = bundle.QueryUsed
	out["traceId"] = bundle.TraceID
	out["count"] = len(papers)
	out["retrievalStrategies"] = stringSliceToAny(bundle.RetrievalStrategies)
	out["retrievalTrace"] = mapsToAny(bundle.RetrievalTrace)
	out["pipeline"] = stringSliceToAny(canonicalRetrievalPipeline)
	out["degraded"] = degraded
	out["contract"] = "paperBundle.v1"

	paperBundle := map[string]any{
		"contract":            "paperBundle.v1",
		"count":               len(bundle.Papers),
		"degraded":            degraded,
		"papers":              mapsToAny(sourcesToArtifactMaps(bundle.Papers)),
		"retrievalStrategies": stringSliceToAny(bundle.RetrievalStrategies),
		"retrievalTrace":      mapsToAny(bundle.RetrievalTrace),
		"pipeline":            stringSliceToAny(canonicalRetrievalPipeline),
		"queryUsed":           bundle.QueryUsed,
		"traceId":             bundle.TraceID,
	}
	if strings.TrimSpace(degradedReason) != "" {
		out["degradedReason"] = degradedReason
		paperBundle["degradedReason"] = degradedReason
	}
	out["paperBundle"] = paperBundle
	return out
}

// RetrieveCanonicalPapers is the exported API-facing retrieval entrypoint.
// It enforces the canonical WisDev retrieval contract even for legacy callers
// that only provide a query and a paper limit.
var RetrieveCanonicalPapers = func(ctx context.Context, rdb redis.UniversalClient, query string, limit int) ([]Source, map[string]any, error) {
	return RetrieveCanonicalPapersWithRegistry(ctx, rdb, nil, query, limit)
}

// RetrieveCanonicalPapersWithRegistry is the API-facing retrieval entrypoint for
// callers that already own the server search registry. Passing the registry here
// keeps paper retrieval on the MCP tool path instead of falling back to the
// older local ParallelSearch registry discovery path.
func RetrieveCanonicalPapersWithRegistry(ctx context.Context, rdb redis.UniversalClient, registry *internalsearch.ProviderRegistry, query string, limit int) ([]Source, map[string]any, error) {
	opts := SearchOptions{Limit: limit}
	return RetrieveCanonicalPapersWithOptions(ctx, rdb, registry, query, opts)
}

// RetrieveCanonicalPapersWithOptions preserves the canonical WisDev retrieval
// contract while allowing API routes to pass request-scoped filters such as
// domain, sources, years, cache policy, and trace id.
func RetrieveCanonicalPapersWithOptions(ctx context.Context, rdb redis.UniversalClient, registry *internalsearch.ProviderRegistry, query string, opts SearchOptions) ([]Source, map[string]any, error) {
	session := &AgentSession{
		OriginalQuery:  strings.TrimSpace(query),
		CorrectedQuery: strings.TrimSpace(query),
		Mode:           WisDevModeGuided,
		ServiceTier:    ServiceTierStandard,
		DetectedDomain: strings.TrimSpace(opts.Domain),
	}
	params := defaultRetrievePapersPlanParams(session)
	params["query"] = query
	if opts.Limit > 0 {
		params["limit"] = opts.Limit
	}
	if strings.TrimSpace(opts.Domain) != "" {
		params["domain"] = strings.TrimSpace(opts.Domain)
	}
	if len(opts.Sources) > 0 {
		params["sources"] = append([]string(nil), opts.Sources...)
	}
	if opts.YearFrom > 0 {
		params["yearFrom"] = opts.YearFrom
	}
	if opts.YearTo > 0 {
		params["yearTo"] = opts.YearTo
	}
	if strings.TrimSpace(opts.TraceID) != "" {
		params["traceId"] = strings.TrimSpace(opts.TraceID)
	}
	if opts.SkipCache {
		params["skipCache"] = true
	}
	if len(opts.RetrievalStrategies) > 0 {
		params["retrievalStrategies"] = append([]string(nil), opts.RetrievalStrategies...)
	}
	if opts.Stage2Rerank {
		params["stage2Rerank"] = true
	}
	if opts.PageIndexRerank {
		params["pageIndexRerank"] = true
	}
	queryUsed, opts := resolveRetrievePapersSearchOptions(params, session, false)
	opts.Registry = registry
	return runRetrievePapers(ctx, rdb, queryUsed, opts)
}
