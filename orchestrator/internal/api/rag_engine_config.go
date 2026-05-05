package api

import (
	"context"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/rag"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func buildRAGEngine(cfg ServerConfig, agentGateway *wisdev.AgentGateway) *rag.Engine {
	return rag.NewEngineWithConfig(cfg.SearchRegistry, cfg.LLMClient, buildRAGEngineConfig(cfg, agentGateway))
}

func buildRAGEngineConfig(cfg ServerConfig, agentGateway *wisdev.AgentGateway) rag.EngineConfig {
	config := rag.EngineConfig{
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
