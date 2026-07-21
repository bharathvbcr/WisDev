package wisdev

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
)

func TestBrainCapabilities_SynthesizeAnswerStyledLongFormAddsIntroBackgroundInstructions(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	papers := []Source{{ID: "p1", Title: "P1", Summary: "S1"}}
	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		prompt := req.GetPrompt()
		return req != nil &&
			strings.Contains(prompt, `extended "Introduction" section`) &&
			strings.Contains(prompt, `"Background" section`)
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"sections":[{"heading":"Introduction","sentences":[{"text":"intro","evidenceIds":["p1"]}]}]}`}, nil)

	answer, err := caps.SynthesizeAnswerStyled(context.Background(), "query", papers, "", true)
	require.NoError(t, err)
	require.NotNil(t, answer)
	require.Len(t, answer.Sections, 1)
	assert.Equal(t, "Introduction", answer.Sections[0].Heading)
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_SynthesizeAnswerDefaultOmitsLongFormInstructions(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	papers := []Source{{ID: "p1", Title: "P1", Summary: "S1"}}
	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil && !strings.Contains(req.GetPrompt(), `extended "Introduction" section`)
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"sections":[{"heading":"Findings","sentences":[{"text":"t","evidenceIds":[]}]}]}`}, nil)

	answer, err := caps.SynthesizeAnswer(context.Background(), "query", papers, "")
	require.NoError(t, err)
	require.NotNil(t, answer)
	mockLLM.AssertExpectations(t)
}

func TestSynthesizePlainTextFallbackLongFormAddsIntroBackground(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	loop := NewAutonomousLoop(search.NewProviderRegistry(), client)
	loop.longFormReport = true

	mockLLM.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		prompt := req.GetPrompt()
		return strings.Contains(prompt, `extended "Introduction" heading`) &&
			strings.Contains(prompt, `"Background" heading`)
	})).Return(&llmv1.GenerateResponse{Text: "long-form report"}, nil)

	out, err := loop.synthesizePlainTextFallback(context.Background(), "q", []search.Paper{{ID: "p1", Title: "Paper title"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "long-form report", out)
	mockLLM.AssertExpectations(t)
}

func TestSynthesizePlainTextFallbackDefaultOmitsLongFormRequirement(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	loop := NewAutonomousLoop(search.NewProviderRegistry(), client)

	mockLLM.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return !strings.Contains(req.GetPrompt(), `extended "Introduction" heading`)
	})).Return(&llmv1.GenerateResponse{Text: "report"}, nil)

	out, err := loop.synthesizePlainTextFallback(context.Background(), "q", []search.Paper{{ID: "p1", Title: "Paper title"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "report", out)
	mockLLM.AssertExpectations(t)
}

func TestRunPropagatesLongFormReportToLoop(t *testing.T) {
	loop := NewAutonomousLoop(search.NewProviderRegistry(), nil)
	_, err := loop.Run(context.Background(), LoopRequest{
		Query:           "test query",
		MaxIterations:   1,
		MaxSearchTerms:  1,
		HitsPerSearch:   1,
		MaxUniquePapers: 1,
		LongFormReport:  true,
	})
	require.NoError(t, err)
	assert.True(t, loop.longFormReport)
}
