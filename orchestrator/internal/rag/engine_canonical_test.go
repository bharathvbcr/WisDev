package rag

import (
	"context"
	"strings"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestEngineGenerateAnswerUsesCanonicalRetrievalMetadata(t *testing.T) {
	lc := llm.NewClient()
	msc := &mockLLMService{}
	lc.SetClient(msc)
	msc.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "Sleep significantly improves memory consolidation in adults."}, nil).Once()

	engine := NewEngineWithConfig(nil, lc, EngineConfig{
		CanonicalRetriever: func(context.Context, AnswerRequest) (*CanonicalRetrievalResult, error) {
			return &CanonicalRetrievalResult{
				Papers: []search.Paper{{
					ID:       "p1",
					Title:    "Sleep and Memory Consolidation",
					Abstract: "Sleep improves memory consolidation in controlled studies.",
				}},
				QueryUsed:           "sleep memory consolidation",
				TraceID:             "trace-canonical-1",
				RetrievalStrategies: []string{"query_expansion", "stage2_rerank"},
				RetrievalTrace: []map[string]any{{
					"strategy": "query_expansion",
					"status":   "applied",
				}},
				Backend: "go-wisdev-canonical",
			}, nil
		},
	})

	resp, err := engine.GenerateAnswer(context.Background(), AnswerRequest{Query: "sleep memory"})
	assert.NoError(t, err)
	assert.Equal(t, "trace-canonical-1", resp.TraceID)
	assert.NotEmpty(t, resp.Citations)
	if assert.NotNil(t, resp.Metadata) {
		assert.Equal(t, "go-wisdev-canonical", resp.Metadata.Backend)
		assert.Equal(t, "sleep memory consolidation", resp.Metadata.QueryUsed)
		assert.Equal(t, []string{"query_expansion", "stage2_rerank"}, resp.Metadata.RetrievalStrategies)
		if assert.Len(t, resp.Metadata.RetrievalTrace, 1) {
			assert.Equal(t, "query_expansion", resp.Metadata.RetrievalTrace[0]["strategy"])
		}
	}
}

func TestEngineMultiAgentExecuteUsesCanonicalMetadata(t *testing.T) {
	lc := llm.NewClient()
	msc := &mockLLMService{}
	lc.SetClient(msc)
	msc.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "Sleep significantly improves memory consolidation in adults."}, nil).Once()

	engine := NewEngineWithConfig(nil, lc, EngineConfig{
		CanonicalRetriever: func(context.Context, AnswerRequest) (*CanonicalRetrievalResult, error) {
			return &CanonicalRetrievalResult{
				Papers: []search.Paper{{
					ID:       "p1",
					Title:    "Sleep and Memory Consolidation",
					Abstract: "Sleep improves memory consolidation in controlled studies.",
				}},
				QueryUsed:           "sleep memory consolidation",
				TraceID:             "trace-multi-1",
				RetrievalStrategies: []string{"canonical_wisdev"},
				Backend:             "go-wisdev-canonical",
			}, nil
		},
	})

	resp, err := engine.MultiAgentExecute(context.Background(), AnswerRequest{Query: "sleep memory"})
	assert.NoError(t, err)
	assert.NotNil(t, resp.Dossier)
	if assert.NotNil(t, resp.Metadata) {
		assert.Equal(t, "go-wisdev-canonical-multi-agent", resp.Metadata.Backend)
		assert.Equal(t, "sleep memory consolidation", resp.Metadata.QueryUsed)
		assert.Equal(t, true, resp.Metadata.Policy["multiAgent"])
		assert.Equal(t, true, resp.Metadata.Policy["evidenceConsolidated"])
	}
}

func TestEngineSelectSectionContextUsesFullTextChunks(t *testing.T) {
	engine := NewEngine(nil, nil)

	resp, err := engine.SelectSectionContext(context.Background(), SectionContextRequest{
		SectionName: "Results",
		SectionGoal: "hippocampal replay during sleep",
		Limit:       1,
		ChunkSize:   64,
		Papers: []search.Paper{
			{
				ID:       "p1",
				Title:    "Background Survey",
				Abstract: "A high level overview of sleep science.",
			},
			{
				ID:       "p2",
				Title:    "Replay Study",
				FullText: "Introduction\nSleep affects memory.\nResults\nHippocampal replay during sleep predicted stronger memory consolidation with robust effect sizes.\nDiscussion\nThe finding was specific to replay-rich intervals.\n",
			},
		},
	})
	assert.NoError(t, err)
	if assert.Len(t, resp.SelectedChunks, 1) {
		assert.Equal(t, "p2", resp.SelectedChunks[0].PaperID)
		assert.Contains(t, strings.ToLower(resp.SelectedChunks[0].Text), "hippocampal replay")
	}
}
