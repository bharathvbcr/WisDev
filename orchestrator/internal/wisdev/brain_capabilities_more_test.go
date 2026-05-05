package wisdev

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

func TestBrainCapabilities_Errors(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)
	ctx := context.Background()
	llmErr := errors.New("llm down")

	t.Run("DecomposeTask Error", func(t *testing.T) {
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, llmErr).Once()
		_, err := caps.DecomposeTask(ctx, "q", "d", "")
		assert.Error(t, err)
	})

	t.Run("ProposeHypotheses Error", func(t *testing.T) {
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, llmErr).Once()
		_, err := caps.ProposeHypotheses(ctx, "q", "i", "")
		assert.Error(t, err)
	})

	t.Run("CoordinateReplan Error", func(t *testing.T) {
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, llmErr).Once()
		_, err := caps.CoordinateReplan(ctx, "s", "r", nil, "")
		assert.Error(t, err)
	})

	t.Run("AssessResearchComplexity Error", func(t *testing.T) {
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, llmErr).Once()
		_, err := caps.AssessResearchComplexity(ctx, "q")
		assert.Error(t, err)
	})

	t.Run("GenerateSnowballQueries Error", func(t *testing.T) {
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, llmErr).Once()
		_, err := caps.GenerateSnowballQueries(ctx, []Source{{Title: "P1"}}, "")
		assert.Error(t, err)
	})

	t.Run("VerifyCitations TrustBundle", func(t *testing.T) {
		mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return req != nil && strings.Contains(req.Prompt, "Verify citations")
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"validCount":1,"issues":["duplicate DOI for P2"]}`}, nil).Once()

		result, err := caps.VerifyCitations(ctx, []Source{
			{ID: "p1", Title: "P1", DOI: "10.1000/test-1"},
			{ID: "p2", Title: "P2", DOI: "10.1000/test-1"},
		}, "")
		assert.NoError(t, err)
		assert.Equal(t, float64(1), result["validCount"])
		assert.NotEmpty(t, result["issues"])
	})

	t.Run("BuildClaimEvidenceTable Error", func(t *testing.T) {
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, llmErr).Once()
		_, err := caps.BuildClaimEvidenceTable(ctx, "q", nil, "")
		assert.Error(t, err)
	})

	t.Run("GenerateThoughts Error", func(t *testing.T) {
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, llmErr).Once()
		_, err := caps.GenerateThoughts(ctx, nil, "")
		assert.Error(t, err)
	})

	t.Run("DetectContradictions Error", func(t *testing.T) {
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, llmErr).Once()
		_, err := caps.DetectContradictions(ctx, nil, "")
		assert.Error(t, err)
	})

	t.Run("VerifyClaims Error", func(t *testing.T) {
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, llmErr).Once()
		_, err := caps.VerifyClaims(ctx, "t", nil, "")
		assert.Error(t, err)
	})

	t.Run("SystematicReviewPrisma Error", func(t *testing.T) {
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, llmErr).Once()
		_, err := caps.SystematicReviewPrisma(ctx, "q", nil, "")
		assert.Error(t, err)
	})

	t.Run("EnhanceAcademicQuery Error", func(t *testing.T) {
		mockLLM.On("Generate", mock.Anything, mock.Anything).Return(nil, llmErr).Once()
		_, err := caps.EnhanceAcademicQuery(ctx, "q", "")
		assert.Error(t, err)
	})

	t.Run("EnhanceAcademicQuery Empty", func(t *testing.T) {
		mockLLM.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "   "}, nil).Once()
		_, err := caps.EnhanceAcademicQuery(ctx, "q", "")
		assert.Error(t, err)
	})

	t.Run("SelectPrimarySource Error", func(t *testing.T) {
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, llmErr).Once()
		_, err := caps.SelectPrimarySource(ctx, "q", nil, "")
		assert.Error(t, err)
	})

	t.Run("AskFollowUpIfAmbiguous Error", func(t *testing.T) {
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, llmErr).Once()
		_, err := caps.AskFollowUpIfAmbiguous(ctx, "q", "")
		assert.Error(t, err)
	})

	t.Run("SynthesizeAnswer Error", func(t *testing.T) {
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, llmErr).Once()
		_, err := caps.SynthesizeAnswer(ctx, "q", nil, "")
		assert.Error(t, err)
	})

	t.Run("SynthesizeAnswer Empty", func(t *testing.T) {
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(&llmv1.StructuredResponse{JsonResult: ""}, nil).Once()
		_, err := caps.SynthesizeAnswer(ctx, "q", nil, "")
		assert.Error(t, err)
	})
}

func TestBrainCapabilities_DecomposeTaskRejectsLegacyTopLevelArray(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assertWisdevStructuredPromptHygiene(t, req.Prompt)
		return strings.Contains(req.GetJsonSchema(), `"tasks"`)
	})).Return(&llmv1.StructuredResponse{JsonResult: `[{"id":"t1","name":"legacy top-level task","action":"search"}]`}, nil).Once()

	_, err := caps.DecomposeTask(context.Background(), "sleep memory", "neuroscience", "")

	assert.Error(t, err)
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_ProposeHypothesesRejectsLegacyTopLevelArray(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assertWisdevStructuredPromptHygiene(t, req.Prompt)
		return strings.Contains(req.GetJsonSchema(), `"hypotheses"`)
	})).Return(&llmv1.StructuredResponse{JsonResult: `[{"claim":"legacy top-level hypothesis","falsifiabilityCondition":"replication fails"}]`}, nil).Once()

	_, err := caps.ProposeHypotheses(context.Background(), "sleep memory", "understand", "")

	assert.Error(t, err)
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_CoordinateReplanRejectsLegacyTopLevelArray(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assertWisdevStructuredPromptHygiene(t, req.Prompt)
		return strings.Contains(req.GetPrompt(), "Propose a recovery plan") &&
			strings.Contains(req.GetJsonSchema(), `"tasks"`)
	})).Return(&llmv1.StructuredResponse{JsonResult: `[{"id":"r1","name":"legacy top-level recovery","action":"retry_search"}]`}, nil).Once()

	_, err := caps.CoordinateReplan(context.Background(), "step-1", "provider failed", map[string]any{"query": "sleep"}, "")

	assert.Error(t, err)
	mockLLM.AssertExpectations(t)
}
