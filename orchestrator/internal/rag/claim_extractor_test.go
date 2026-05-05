package rag

import (
	"context"
	"errors"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
)

func TestClaimExtractor(t *testing.T) {
	t.Run("requires a receiver and client", func(t *testing.T) {
		var extractor *ClaimExtractor
		claims, err := extractor.ExtractClaimsCategorized(context.Background(), "text")
		assert.Error(t, err)
		assert.Nil(t, claims)

		extractor = &ClaimExtractor{}
		claims, err = extractor.ExtractClaimsCategorized(context.Background(), "text")
		assert.Error(t, err)
		assert.Nil(t, claims)
	})

	t.Run("returns nil response errors", func(t *testing.T) {
		stub := &ragLLMStub{}
		extractor := NewClaimExtractor(newRagLLMClient(stub))

		claims, err := extractor.ExtractClaimsCategorized(context.Background(), "text")
		assert.Error(t, err)
		assert.Nil(t, claims)
	})

	t.Run("returns JSON decode errors", func(t *testing.T) {
		stub := &ragLLMStub{
			structuredResp: &llmv1.StructuredResponse{JsonResult: "not-json"},
		}
		extractor := NewClaimExtractor(newRagLLMClient(stub))

		claims, err := extractor.ExtractClaimsCategorized(context.Background(), "text")
		assert.Error(t, err)
		assert.Nil(t, claims)
	})

	t.Run("returns structured output errors", func(t *testing.T) {
		stub := &ragLLMStub{
			structuredErr: errors.New("structured boom"),
		}
		extractor := NewClaimExtractor(newRagLLMClient(stub))

		claims, err := extractor.ExtractClaimsCategorized(context.Background(), "text")
		assert.Error(t, err)
		assert.Nil(t, claims)
	})

	t.Run("extracts categorized claims", func(t *testing.T) {
		stub := &ragLLMStub{
			structuredResp: &llmv1.StructuredResponse{
				JsonResult: `{"claims":[{"text":"Uses a randomized design","category":"methodological","reasoning":"describes method"},{"text":"Reduces error","category":"empirical","reasoning":"observed effect"}]}`,
			},
		}
		extractor := NewClaimExtractor(newRagLLMClient(stub))

		claims, err := extractor.ExtractClaimsCategorized(context.Background(), "academic text")
		assert.NoError(t, err)
		assert.Len(t, claims, 2)
		assert.Equal(t, CategoryMethodological, claims[0].Category)
		assert.Equal(t, CategoryEmpirical, claims[1].Category)
		assert.NotNil(t, stub.structuredReq)
		assert.Contains(t, stub.structuredReq.GetPrompt(), "academic text")
		assert.Contains(t, stub.structuredReq.GetPrompt(), ragStructuredOutputSchemaInstruction)
		assert.NotContains(t, stub.structuredReq.GetPrompt(), "Return a JSON object")
		assert.Contains(t, stub.structuredReq.GetJsonSchema(), "\"claims\"")
		assert.Equal(t, llm.ResolveHeavyModel(), stub.structuredReq.GetModel())
		assert.EqualValues(t, -1, stub.structuredReq.GetThinkingBudget())
		assert.Equal(t, "structured_high_value", stub.structuredReq.GetRequestClass())
		assert.Equal(t, "standard", stub.structuredReq.GetRetryProfile())
		assert.Equal(t, "priority", stub.structuredReq.GetServiceTier())
		assert.Greater(t, stub.structuredReq.GetLatencyBudgetMs(), int32(0))
	})

	t.Run("filters by category", func(t *testing.T) {
		claims := []ExtractedClaim{
			{Text: "a", Category: CategoryMethodological},
			{Text: "b", Category: CategoryEmpirical},
			{Text: "c", Category: CategoryMethodological},
		}

		got := FilterByCategory(claims, CategoryMethodological)
		assert.Len(t, got, 2)
		assert.Equal(t, CategoryMethodological, got[0].Category)
	})
}
