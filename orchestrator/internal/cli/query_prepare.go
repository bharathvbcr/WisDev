package cli

import (
	"context"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	internal "github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
)

type preparedResearchQuery = internal.PreparedResearchQuery

func prepareResearchQuery(raw string) preparedResearchQuery {
	return prepareResearchQueryWithLLM(context.Background(), raw, nil, false)
}

func prepareResearchQueryWithLLM(ctx context.Context, raw string, client *llm.Client, disableAI bool) preparedResearchQuery {
	return internal.EarlyPrepareResearchQuery(ctx, raw, client, disableAI)
}

func inferResearchDomain(query string) string {
	return internal.InferResearchDomain(query)
}
