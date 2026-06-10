package api

import (
	"context"
	"os"
	"strings"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/rag"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
)

func buildRAGEngine(cfg ServerConfig, agentGateway *wisdev.AgentGateway) *rag.Engine {
	return rag.NewEngineWithConfig(cfg.SearchRegistry, cfg.LLMClient, buildRAGEngineConfig(cfg, agentGateway))
}

func buildRAGEngineConfig(cfg ServerConfig, agentGateway *wisdev.AgentGateway) rag.EngineConfig {
	config := rag.EngineConfig{
		FileSearchGenerator: cfg.VertexClient,
		DefaultFileSearch:   defaultRAGFileSearchConfigFromEnv(),
		CanonicalRetriever: func(ctx context.Context, req rag.AnswerRequest) (*rag.CanonicalRetrievalResult, error) {
			limit := req.Limit
			if limit <= 0 {
				limit = 10
			}

			sources, payload, err := wisdev.RetrieveCanonicalPapersWithRegistry(ctx, cfg.Redis, cfg.SearchRegistry, req.Query, limit)
			if err != nil {
				return nil, err
			}

			paperBundle := mapAny(payload["paperBundle"])
			strategies := sliceStrings(payload["retrievalStrategies"])
			if len(strategies) == 0 {
				strategies = sliceStrings(paperBundle["retrievalStrategies"])
			}
			strategies = uniqueStrings(append([]string{"canonical_wisdev"}, strategies...))

			trace := sliceAnyMap(payload["retrievalTrace"])
			if len(trace) == 0 {
				trace = sliceAnyMap(paperBundle["retrievalTrace"])
			}

			return &rag.CanonicalRetrievalResult{
				Papers:              convertWisdevSourcesToSearchPapers(sources),
				QueryUsed:           firstNonEmptyString(wisdev.AsOptionalString(payload["queryUsed"]), wisdev.AsOptionalString(paperBundle["queryUsed"]), strings.TrimSpace(req.Query)),
				TraceID:             firstNonEmptyString(wisdev.AsOptionalString(payload["traceId"]), wisdev.AsOptionalString(paperBundle["traceId"])),
				RetrievalTrace:      trace,
				RetrievalStrategies: strategies,
				Backend:             "go-wisdev-canonical",
			}, nil
		},
	}

	if agentGateway != nil && agentGateway.ResearchMemory != nil {
		config.ResearchMemoryLookup = func(ctx context.Context, req rag.AnswerRequest) (*rag.ResearchMemoryPrimer, error) {
			response, err := agentGateway.ResearchMemory.Query(ctx, wisdev.ResearchMemoryQueryRequest{
				UserID:    strings.TrimSpace(req.UserID),
				ProjectID: strings.TrimSpace(req.ProjectID),
				Query:     strings.TrimSpace(req.Query),
				Limit:     5,
			})
			if err != nil || response == nil {
				return nil, err
			}

			findings := make([]string, 0, len(response.Findings))
			for _, finding := range response.Findings {
				if claim := strings.TrimSpace(finding.Claim); claim != "" {
					findings = append(findings, claim)
				}
			}

			return &rag.ResearchMemoryPrimer{
				Findings:           findings,
				RecommendedQueries: append([]string(nil), response.RecommendedQueries...),
				RelatedTopics:      append([]string(nil), response.RelatedTopics...),
				RelatedMethods:     append([]string(nil), response.RelatedMethods...),
				QuerySummary:       strings.TrimSpace(response.QuerySummary),
			}, nil
		}
	}

	return config
}

func defaultRAGFileSearchConfigFromEnv() rag.FileSearchConfig {
	storeNames := splitCSVEnv(firstNonEmptyString(
		os.Getenv("RAG_FILE_SEARCH_STORE_NAMES"),
		os.Getenv("GEMINI_FILE_SEARCH_STORE_NAMES"),
	))
	cfg := rag.FileSearchConfig{
		Enabled:        len(storeNames) > 0,
		StoreNames:     storeNames,
		MetadataFilter: firstNonEmptyString(os.Getenv("RAG_FILE_SEARCH_METADATA_FILTER"), os.Getenv("GEMINI_FILE_SEARCH_METADATA_FILTER")),
		TopK:           parseIntValue(firstNonEmptyString(os.Getenv("RAG_FILE_SEARCH_TOP_K"), os.Getenv("GEMINI_FILE_SEARCH_TOP_K")), 0),
		Model:          strings.TrimSpace(firstNonEmptyString(os.Getenv("RAG_FILE_SEARCH_MODEL"), os.Getenv("GEMINI_FILE_SEARCH_MODEL"))),
		Multimodal:     parseBoolValue(firstNonEmptyString(os.Getenv("RAG_FILE_SEARCH_MULTIMODAL"), os.Getenv("GEMINI_FILE_SEARCH_MULTIMODAL")), len(storeNames) > 0),
		EmbeddingModel: firstNonEmptyString(os.Getenv("RAG_FILE_SEARCH_EMBEDDING_MODEL"), os.Getenv("GEMINI_FILE_SEARCH_EMBEDDING_MODEL"), rag.DefaultFileSearchEmbeddingModel()),
	}
	cfg.Required = parseBoolValue(firstNonEmptyString(os.Getenv("RAG_FILE_SEARCH_REQUIRED"), os.Getenv("GEMINI_FILE_SEARCH_REQUIRED")), false)
	return cfg
}

func splitCSVEnv(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
