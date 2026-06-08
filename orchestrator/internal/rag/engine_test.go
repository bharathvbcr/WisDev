package rag

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

func TestEngine_GenerateAnswer_NoPapers(t *testing.T) {
	is := assert.New(t)
	reg := search.NewProviderRegistry()
	llmClient := llm.NewClient()
	engine := NewEngine(reg, llmClient)

	resp, err := engine.GenerateAnswer(context.Background(), AnswerRequest{
		Query: "what is the impact of GNNs on protein folding?",
	})

	if err != nil {
		is.Contains(err.Error(), "synthesis failed")
		return
	}

	is.NoError(err)
	is.Contains(resp.Answer, "I couldn't find any relevant academic papers")
}

func TestDedupPapers(t *testing.T) {
	is := assert.New(t)
	papers := []search.Paper{
		{ID: "1", Title: "Paper 1"},
		{ID: "1", Title: "Paper 1 Duplicate"},
		{DOI: "10.123", Title: "Paper 2"},
		{DOI: "10.123", Title: "Paper 2 Duplicate"},
		{Title: "Paper 3"},
		{Title: "Paper 3"},
	}

	unique := dedupPapers(papers)
	is.Len(unique, 3)
}

func TestTruncateContextRune(t *testing.T) {
	is := assert.New(t)
	is.Equal("abc", truncateContextRune("abc", 10))
	is.Equal("ab…", truncateContextRune("abc", 2))
	is.Equal("😊…", truncateContextRune("😊😊", 1))
}

func TestEngine_SelectSectionContext(t *testing.T) {
	is := assert.New(t)
	engine := NewEngine(nil, nil)

	req := SectionContextRequest{
		SectionName: "Introduction",
		SectionGoal: "Explain GNNs",
		Papers: []search.Paper{
			{ID: "p1", Title: "Title 1", Abstract: "GNNs are powerful for graph data."},
			{ID: "p2", Title: "Title 2", Abstract: "Transformers are used in NLP."},
		},
		Limit: 1,
	}

	resp, err := engine.SelectSectionContext(context.Background(), req)
	is.NoError(err)
	is.Equal("Introduction", resp.SectionName)
	is.Len(resp.SelectedChunks, 1)
	is.Equal("p1", resp.SelectedChunks[0].PaperID)
}
