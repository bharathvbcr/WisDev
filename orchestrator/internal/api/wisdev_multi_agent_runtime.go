package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/rag"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

var errWisdevSwarmInsufficientGrounding = errors.New("wisdev_swarm_insufficient_grounding")

func executeWisdevMultiAgentSwarm(
	ctx context.Context,
	gateway *wisdev.AgentGateway,
	query string,
	domainHint string,
	maxIterations int,
	includeAnalyst bool,
) (*rag.AnswerResponse, error) {
	normalizedQuery := strings.TrimSpace(query)
	if normalizedQuery == "" {
		return nil, fmt.Errorf("query is required")
	}

	if gateway == nil {
		return nil, fmt.Errorf("wisdev_unified_runtime_unavailable: gateway is nil")
	}
	runtime := resolveUnifiedResearchRuntime(gateway)
	if runtime == nil {
		return nil, fmt.Errorf("wisdev_unified_runtime_unavailable: unified research runtime is not initialized")
	}
	profile := wisdev.BuildResearchExecutionProfile(
		ctx,
		normalizedQuery,
		string(wisdev.WisDevModeYOLO),
		"balanced",
		false,
		maxIterations,
	)
	resp, err := runtime.RunAnswer(ctx, wisdev.UnifiedResearchRequest{
		Query:           normalizedQuery,
		Domain:          domainHint,
		ProjectID:       "multi_" + wisdev.NewTraceID(),
		MaxIterations:   profile.MaxIterations,
		MaxSearchTerms:  profile.SearchBudget.MaxSearchTerms,
		HitsPerSearch:   profile.SearchBudget.HitsPerSearch,
		MaxUniquePapers: profile.SearchBudget.MaxUniquePapers,
		AllocatedTokens: profile.AllocatedTokens,
		Mode:            string(profile.Mode),
		Plane:           wisdev.ResearchExecutionPlaneMultiAgent,
	})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("wisdev_unified_runtime_unavailable: unified research runtime returned no response")
	}
	if len(resp.Papers) == 0 || len(resp.Citations) == 0 {
		return nil, errWisdevSwarmInsufficientGrounding
	}
	if resp.Metadata == nil {
		resp.Metadata = &rag.ResponseMetadata{}
	}
	resp.Metadata.Backend = "go-wisdev-unified-runtime"
	resp.Metadata.FallbackTriggered = false
	if resp.Metadata.Policy == nil {
		resp.Metadata.Policy = map[string]any{}
	}
	resp.Metadata.Policy["includeAnalyst"] = includeAnalyst
	resp.Metadata.Policy["domainHint"] = strings.TrimSpace(domainHint)
	resp.Metadata.Policy["serviceTier"] = string(profile.ServiceTier)
	resp.Metadata.Policy["modelTier"] = string(profile.PrimaryModelTier)
	resp.Metadata.Policy["coverageModel"] = "unified_runtime_blackboard"
	resp.Metadata.Policy["researchPlane"] = string(wisdev.ResearchExecutionPlaneMultiAgent)
	if _, exists := resp.Metadata.Policy["observedEvidenceCount"]; !exists {
		resp.Metadata.Policy["observedEvidenceCount"] = len(resp.Citations)
	}
	{
		var existingFamilies []string
		switch v := resp.Metadata.Policy["observedSourceFamilies"].(type) {
		case []string:
			existingFamilies = v
		case []interface{}:
			for _, item := range v {
				if s, ok := item.(string); ok {
					existingFamilies = append(existingFamilies, s)
				}
			}
		}
		if len(existingFamilies) == 0 {
			seen := make(map[string]bool)
			var families []string
			for _, p := range resp.Papers {
				if src := strings.TrimSpace(p.Source); src != "" && !seen[src] {
					seen[src] = true
					families = append(families, src)
				}
			}
			if len(families) > 0 {
				resp.Metadata.Policy["observedSourceFamilies"] = families
			}
		}
	}
	return resp, nil
}
