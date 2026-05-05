package rag

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
)

func TestEvidenceGate_RunBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("no claims is provisional", func(t *testing.T) {
		gate := NewEvidenceGate(nil)
		result, err := gate.Run(ctx, "A neutral statement without a verifiable claim.", nil)
		assert.NoError(t, err)
		assert.Equal(t, "provisional", result.Verdict)
		assert.Zero(t, result.Checked)
		assert.Contains(t, result.Message, "No verifiable claims")
	})

	t.Run("failed when claims stay ungrounded", func(t *testing.T) {
		gate := NewEvidenceGate(nil)
		result, err := gate.Run(ctx, "The study found significant results. The treatment showed improvement.", []search.Paper{
			{ID: "p1", Title: "Unrelated paper", Abstract: "Completely different topic."},
		})
		assert.NoError(t, err)
		assert.Equal(t, "failed", result.Verdict)
		assert.Equal(t, 2, result.Checked)
		assert.Equal(t, 2, result.UnlinkedCount)
	})

	t.Run("ai fallback uses heuristic claims when structured output fails", func(t *testing.T) {
		stub := &synthesisLLMStub{structuredErr: errors.New("structured extraction failed")}
		client := llm.NewClient()
		client.SetClient(stub)
		gate := NewEvidenceGate(client)

		text := strings.Repeat("The study found significant results. ", 8)
		result, err := gate.Run(ctx, text, nil)
		assert.NoError(t, err)
		assert.NotEmpty(t, result.Claims)
		assert.Equal(t, "failed", result.Verdict)
	})
}

func TestEvidenceGate_HelperBranches(t *testing.T) {
	gate := NewEvidenceGate(nil)
	ctx := context.Background()

	t.Run("extractHeuristicClaims deduplicates and caps output", func(t *testing.T) {
		var builder strings.Builder
		builder.WriteString("Too short. ")
		for i := 0; i < 16; i++ {
			builder.WriteString(strings.TrimSpace(strings.Join([]string{
				"The study found significant results number",
				strings.TrimSpace(string(rune('A' + i))),
				".",
			}, " ")))
			builder.WriteString(" ")
		}
		claims := gate.extractHeuristicClaims(builder.String())
		assert.Len(t, claims, 15)
		assert.Contains(t, claims[0], "significant results")
	})

	t.Run("extractClaimsWithAI rejects invalid JSON", func(t *testing.T) {
		stub := &synthesisLLMStub{
			structuredResp: &llmv1.StructuredResponse{JsonResult: "not json"},
		}
		client := llm.NewClient()
		client.SetClient(stub)
		gate := NewEvidenceGate(client)

		claims, err := gate.extractClaimsWithAI(ctx, "Text")
		assert.Error(t, err)
		assert.Nil(t, claims)
		assert.NotNil(t, stub.structuredReq)
		assert.Contains(t, stub.structuredReq.GetPrompt(), ragStructuredOutputSchemaInstruction)
		assert.NotContains(t, stub.structuredReq.GetPrompt(), "Return a JSON object")
		assert.Equal(t, llm.ResolveLightModel(), stub.structuredReq.GetModel())
		assert.EqualValues(t, 0, stub.structuredReq.GetThinkingBudget())
		assert.Equal(t, "light", stub.structuredReq.GetRequestClass())
		assert.Equal(t, "conservative", stub.structuredReq.GetRetryProfile())
		assert.Equal(t, "standard", stub.structuredReq.GetServiceTier())
		assert.Greater(t, stub.structuredReq.GetLatencyBudgetMs(), int32(0))
	})

	t.Run("findBestSourceForClaim matches, ignores empty tokens, and respects threshold", func(t *testing.T) {
		claim := "Graph neural networks improve protein folding"
		papers := []search.Paper{
			{ID: "empty", Title: "the and or", Abstract: "is are to"},
			{ID: "match", Title: "Graph neural networks improve protein folding", Abstract: "Graph neural networks improve protein folding."},
			{ID: "low", Title: "Alpha beta gamma delta epsilon zeta eta theta iota kappa", Abstract: "alpha unrelated words"},
		}

		best, ratio := gate.findBestSourceForClaim(claim, papers)
		assert.NotNil(t, best)
		assert.Equal(t, "match", best.ID)
		assert.Greater(t, ratio, 0.1)

		none, ratio := gate.findBestSourceForClaim("alpha beta gamma delta epsilon zeta eta theta iota kappa", []search.Paper{
			{ID: "low", Title: "alpha unrelated words", Abstract: "more unrelated content"},
		})
		assert.Nil(t, none)
		assert.Zero(t, ratio)

		none, ratio = gate.findBestSourceForClaim("claim words", []search.Paper{
			{ID: "empty", Title: "the and or", Abstract: "is are to"},
		})
		assert.Nil(t, none)
		assert.Zero(t, ratio)
	})

	t.Run("detectContradiction handles empty, low-overlap, and contradictory cases", func(t *testing.T) {
		assert.False(t, gate.detectContradiction("", search.Paper{Abstract: "positive findings"}))
		assert.False(t, gate.detectContradiction("The treatment failed.", search.Paper{Abstract: "positive findings"}))
		assert.False(t, gate.detectContradiction("The treatment failed.", search.Paper{Abstract: "The treatment was not related to the study area."}))
		assert.True(t, gate.detectContradiction("The treatment failed to show effect.", search.Paper{Abstract: "The treatment showed significant positive effect."}))
	})
}
