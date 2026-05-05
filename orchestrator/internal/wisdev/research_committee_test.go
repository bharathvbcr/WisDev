package wisdev

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestResearchCommittee_DeliberateUsesRecoverableStructuredPolicy(t *testing.T) {
	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)

	msc.On("StructuredOutput",
		mock.MatchedBy(func(ctx context.Context) bool {
			_, ok := ctx.Deadline()
			return ok
		}),
		mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return req != nil &&
				req.GetThinkingBudget() == 1024 &&
				req.RequestClass == "standard" &&
				req.ServiceTier == "standard" &&
				req.RetryProfile == "standard" &&
				req.LatencyBudgetMs > 0 &&
				!strings.Contains(req.Prompt, "Return JSON") &&
				(strings.Contains(req.Prompt, "Role: FactChecker") ||
					strings.Contains(req.Prompt, "Role: Synthesizer") ||
					strings.Contains(req.Prompt, "Role: ContradictionAnalyst") ||
					strings.Contains(req.Prompt, "Role: Supervisor"))
		}),
	).Return(&llmv1.StructuredResponse{JsonResult: `{"verdict":"approve","reason":"grounded enough"}`}, nil)

	committee := NewResearchCommittee(lc)
	verdict, err := committee.Deliberate(context.Background(), &Hypothesis{Claim: "RLHF improves alignment"}, []EvidenceFinding{
		{Claim: "RLHF improves helpfulness", Snippet: "benchmark evidence", SourceID: "paper-1", Confidence: 0.8},
		{Claim: "RLHF can over-optimize rewards", Snippet: "limitation evidence", SourceID: "paper-2", Confidence: 0.7},
	})

	require.NoError(t, err)
	require.NotNil(t, verdict)
	require.Equal(t, "approve", verdict.Verdict)
	msc.AssertNumberOfCalls(t, "StructuredOutput", 4)
}

func TestResearchCommittee_DeliberateStopsOptionalCallsAfterRateLimit(t *testing.T) {
	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)

	msc.On("StructuredOutput",
		mock.Anything,
		mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return req != nil && strings.Contains(req.Prompt, "Role: FactChecker")
		}),
	).Return(nil, errors.New("resource exhausted 429")).Once()

	committee := NewResearchCommittee(lc)
	verdict, err := committee.Deliberate(context.Background(), &Hypothesis{Claim: "RLHF improves alignment"}, []EvidenceFinding{
		{Claim: "RLHF improves helpfulness", Snippet: "benchmark evidence", SourceID: "paper-1", Confidence: 0.8},
	})

	require.NoError(t, err)
	require.NotNil(t, verdict)
	require.Equal(t, "approve", verdict.Verdict)
	require.Contains(t, verdict.Reason, "Provider cooldown active")
	msc.AssertNumberOfCalls(t, "StructuredOutput", 1)
}

func TestResearchCommittee_DeliberateMalformedStructuredMemberOutputFailsClosed(t *testing.T) {
	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)

	msc.On("StructuredOutput",
		mock.Anything,
		mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return req != nil && strings.Contains(req.Prompt, "Role:")
		}),
	).Return(&llmv1.StructuredResponse{JsonResult: "member prose {\"verdict\":\"approve\",\"reason\":\"wrapped\"}"}, nil)

	committee := NewResearchCommittee(lc)
	verdict, err := committee.Deliberate(context.Background(), &Hypothesis{Claim: "Structured output must be exact"}, []EvidenceFinding{
		{Claim: "wrapped JSON is not exact structured output", Snippet: "schema violation", SourceID: "paper-1", Confidence: 0.8},
	})

	require.NoError(t, err)
	require.NotNil(t, verdict)
	require.Equal(t, "revise", verdict.Verdict)
	require.Contains(t, verdict.Reason, "structured output")
	msc.AssertNumberOfCalls(t, "StructuredOutput", 3)
}
