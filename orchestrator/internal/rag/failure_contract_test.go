package rag

import (
	"context"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type contract_mockSearchProvider struct {
	mock.Mock
}

func (m *contract_mockSearchProvider) Search(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
	args := m.Called(ctx, query, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]search.Paper), args.Error(1)
}

func (m *contract_mockSearchProvider) Name() string { return "contract_mock" }
func (m *contract_mockSearchProvider) Domains() []string { return []string{"general"} }
func (m *contract_mockSearchProvider) Healthy() bool { return true }
func (m *contract_mockSearchProvider) Tools() []string { return nil }

func TestEngine_GenerateAnswer_FailureModes_Contract(t *testing.T) {
	msc := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(msc)
	
	msp := new(contract_mockSearchProvider)
	reg := search.NewProviderRegistry()
	reg.Register(msp)
	
	engine := NewEngine(reg, client)

	ctx := context.Background()

	t.Run("Synthesis Provider Timeout", func(t *testing.T) {
		papers := []search.Paper{{ID: "p1", Title: "Paper 1", Abstract: "Content"}}
		// Allow multiple Search calls due to potential legacy query expansion
		msp.On("Search", mock.Anything, mock.Anything, mock.Anything).Return(papers, nil)
		msc.On("Generate", mock.Anything, mock.Anything, mock.Anything).Return(nil, context.DeadlineExceeded).Once()

		_, err := engine.GenerateAnswer(ctx, AnswerRequest{
			Query: "test query",
		})
		
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "synthesis failed")
	})
}

func TestEvidenceGate_FailureModes_Contract(t *testing.T) {
	msc := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(msc)
	gate := NewEvidenceGate(client)

	ctx := context.Background()

	t.Run("AI Extraction Malformed JSON - Fallback to Heuristic", func(t *testing.T) {
		// Trigger AI extraction with long text
		text := "The study found significant results. "
		for len(text) < 300 {
			text += "This is a long sentence to trigger AI extraction threshold. "
		}

		msc.On("StructuredOutput", mock.Anything, mock.Anything, mock.Anything).Return(&llmv1.StructuredResponse{
			JsonResult: "{ invalid json",
		}, nil).Once()

		res, err := gate.Run(ctx, text, nil)
		
		assert.NoError(t, err)
		assert.NotNil(t, res)
		// Should have fallen back to heuristic and found the "found" claim
		assert.Contains(t, res.Claims, "The study found significant results.")
	})

	t.Run("Evidence Grounding - Low Confidence/Absent Citations", func(t *testing.T) {
		text := "The study found significant results."
		papers := []search.Paper{
			{ID: "p1", Title: "Irrelevant", Abstract: "Something else entirely different words."},
		}

		res, err := gate.Run(ctx, text, papers)
		assert.NoError(t, err)
		assert.Equal(t, "failed", res.Verdict)
		assert.Empty(t, res.LinkedClaims)
	})
	
	t.Run("Evidence Grounding - Duplicated Citations", func(t *testing.T) {
		text := "The study found significant results. The study found significant results again."
		papers := []search.Paper{
			{ID: "p1", Title: "Study title", Abstract: "significant results were found in the study."},
		}
		
		res, err := gate.Run(ctx, text, papers)
		assert.NoError(t, err)
		// Heuristic extraction might dedupe or find both depending on normalization
		assert.NotEmpty(t, res.LinkedClaims)
	})
}
