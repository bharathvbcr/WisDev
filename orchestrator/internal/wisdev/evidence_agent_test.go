package wisdev

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

func TestEvidenceAgent_Gather_NoRegistry(t *testing.T) {
	agent := NewEvidenceAgent(nil)
	items, err := agent.Gather(context.Background(), "sleep and memory", "", 10)
	assert.NoError(t, err)
	assert.Len(t, items, 0)
}

func TestEvidenceAgent_Gather_RequiresSignal(t *testing.T) {
	agent := NewEvidenceAgent(nil)
	_, err := agent.Gather(context.Background(), "", "", 10)
	assert.Error(t, err)
}

func TestEvidenceAgent_Gather_ExtractsSnippetLevelClaims(t *testing.T) {
	reg := search.NewProviderRegistry()
	reg.Register(&mockSearchProvider{
		name: "snippet-provider",
		papers: []search.Paper{{
			ID:       "paper-1",
			Title:    "Sleep Research",
			Abstract: "Sleep improves declarative memory recall in healthy adults.",
			FullText: "Independent replication confirms hippocampal replay during overnight consolidation.",
			Source:   "crossref",
			Score:    0.89,
		}},
	})
	reg.SetDefaultOrder([]string{"snippet-provider"})

	agent := NewEvidenceAgent(reg)
	items, err := agent.Gather(context.Background(), "sleep memory", "", 10)
	assert.NoError(t, err)
	if assert.NotEmpty(t, items) {
		assert.NotEqual(t, "Sleep Research", items[0].Claim)
		assert.Contains(t, items[0].Snippet, "Sleep improves declarative memory recall")
	}
}
