package wisdev

import (
	"context"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	internalsearch "github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveRetrievePapersSearchOptionsDefaultsToFullResearchLimit(t *testing.T) {
	_, opts := resolveRetrievePapersSearchOptions(map[string]any{
		"query": "adaptive graph of thoughts",
	}, &AgentSession{
		Mode:        WisDevModeYOLO,
		ServiceTier: ServiceTierFlex,
	}, false)

	assert.Equal(t, 50, opts.Limit)
}

func TestDefaultRetrievePapersOptionsYoloMatchesGuidedStableDefaults(t *testing.T) {
	for _, tier := range []ServiceTier{ServiceTierStandard, ServiceTierFlex, ServiceTierPriority} {
		assert.Equal(t, defaultRetrievalStrategies(WisDevModeGuided, tier), defaultRetrievalStrategies(WisDevModeYOLO, tier), "tier %s strategies", tier)
		assert.Equal(t, defaultPageIndexRerank(WisDevModeGuided, tier), defaultPageIndexRerank(WisDevModeYOLO, tier), "tier %s page index rerank", tier)
		assert.Equal(t, defaultStage2Rerank(WisDevModeGuided, tier), defaultStage2Rerank(WisDevModeYOLO, tier), "tier %s stage2 rerank", tier)
	}
}

func TestResolveRetrievePapersSearchOptionsYoloFallbackUsesGuidedStableRetrieval(t *testing.T) {
	payload := map[string]any{"query": "adaptive graph of thoughts"}
	_, guided := resolveRetrievePapersSearchOptions(payload, &AgentSession{
		Mode:        WisDevModeGuided,
		ServiceTier: ServiceTierFlex,
	}, false)
	_, yolo := resolveRetrievePapersSearchOptions(payload, &AgentSession{
		Mode:        WisDevModeYOLO,
		ServiceTier: ServiceTierFlex,
	}, false)

	assert.Equal(t, guided.RetrievalStrategies, yolo.RetrievalStrategies)
	assert.NotContains(t, yolo.RetrievalStrategies, RetrievalStrategyPaperContentLookup)
	assert.Equal(t, guided.PageIndexRerank, yolo.PageIndexRerank)
	assert.Equal(t, guided.Stage2Rerank, yolo.Stage2Rerank)
}

func TestResolveRetrievePapersSearchOptionsKeepsDegradedLimitBounded(t *testing.T) {
	_, opts := resolveRetrievePapersSearchOptions(map[string]any{
		"query": "adaptive graph of thoughts",
	}, &AgentSession{
		Mode:        WisDevModeYOLO,
		ServiceTier: ServiceTierFlex,
	}, true)

	assert.Equal(t, 5, opts.Limit)
}

func TestPlanExecutorRetrievePapersStepDefaultsToFullResearchLimit(t *testing.T) {
	disableExecutorGuardrailDeps(t)

	var captured internalsearch.SearchOpts
	registry := internalsearch.NewProviderRegistry()
	registry.Register(&mockSearchProvider{
		name: "openalex",
		SearchFunc: func(ctx context.Context, query string, opts internalsearch.SearchOpts) ([]internalsearch.Paper, error) {
			captured = opts
			return []internalsearch.Paper{{
				ID:     "paper-full-1",
				Title:  "Full Research Paper",
				Source: "openalex",
			}}, nil
		},
	})

	executor := NewPlanExecutor(NewToolRegistry(), policy.PolicyConfig{
		AllowLowRiskAutoRun:    true,
		MaxToolCallsPerSession: 10,
		MaxCostPerSessionCents: 1000,
	}, nil, nil, nil, nil, nil, registry)
	session := &AgentSession{
		SessionID:      "full-research-default-limit",
		OriginalQuery:  "adaptive graph of thoughts",
		CorrectedQuery: "adaptive graph of thoughts",
		Mode:           WisDevModeYOLO,
		ServiceTier:    ServiceTierFlex,
		Budget:         policy.BudgetState{MaxToolCalls: 10, MaxCostCents: 1000},
		Plan: &PlanState{
			PlanID:          "plan-full-research",
			ApprovedStepIDs: map[string]bool{},
		},
	}
	step := PlanStep{
		ID:              "step-03",
		Action:          ActionResearchRetrievePapers,
		ExecutionTarget: ExecutionTargetGoNative,
		Risk:            RiskLevelLow,
	}

	_, papers, _, err := executor.executeStepOnce(context.Background(), session, step, false)

	require.NoError(t, err)
	require.Len(t, papers, 1)
	assert.Equal(t, 50, captured.Limit)
}
