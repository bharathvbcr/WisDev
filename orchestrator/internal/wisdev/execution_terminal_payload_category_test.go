package wisdev

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionTerminalPayloadSplitsRootPromptPaperCategory(t *testing.T) {
	rootPrompt := "What are the new AI architectures? Are there any training advancements, inference advancements, or knowledge retention advancements, like continuous learning or any other architectures?"
	papers := make([]Source, 0, 24)
	seedPapers := []Source{
		{Title: "Survey of transformer architectures for modern AI", Summary: "A broad review and taxonomy of model families."},
		{Title: "Mixture of experts and state space neural architectures", Summary: "Architecture-specific evidence for new model designs."},
		{Title: "Training advances for large language model alignment", Summary: "Optimization, RLHF, and curriculum training methods."},
		{Title: "Speculative decoding improves inference latency", Summary: "Inference serving throughput and deployment evidence."},
		{Title: "Continual learning reduces catastrophic forgetting", Summary: "Knowledge retention and lifelong learning in language models."},
		{Title: "Benchmark protocol for AI system evaluation", Summary: "Methods, datasets, metrics, and experimental protocol."},
	}
	for round := 0; round < 4; round++ {
		for idx, paper := range seedPapers {
			id := fmt.Sprintf("p-%d-%d", round, idx)
			paper.ID = id
			paper.DOI = "10.1000/" + id
			paper.Link = "https://doi.org/" + paper.DOI
			paper.Source = "openalex"
			papers = append(papers, paper)
		}
	}
	session := &AgentSession{
		SessionID: "root-prompt-category-split",
		Status:    SessionComplete,
		Plan: &PlanState{
			PlanID: "plan-root-prompt-category-split",
			Steps: []PlanStep{
				{
					ID:     "step-09",
					Action: ActionResearchRetrievePapers,
					Reason: "Parallel evidence gathering for " + rootPrompt,
					Params: map[string]any{"query": rootPrompt},
				},
			},
			CompletedStepIDs: map[string]bool{"step-09": true},
			FailedStepIDs:    map[string]string{},
			StepArtifacts: map[string]StepArtifactSet{
				"step-09": {
					StepID:      "step-09",
					Action:      ActionResearchRetrievePapers,
					Artifacts:   map[string]any{"query": rootPrompt},
					PaperBundle: &PaperArtifactBundle{Papers: papers, QueryUsed: rootPrompt},
				},
			},
		},
	}

	payload := buildExecutionTerminalPayload(session, "completed", false)

	assert.Equal(t, len(papers), payload["resultCount"])
	categories, ok := payload["categorizedSources"].([]any)
	require.True(t, ok)
	labels := make([]string, 0, len(categories))
	for _, item := range categories {
		category, ok := item.(map[string]any)
		require.True(t, ok)
		label := AsOptionalString(category["category"])
		labels = append(labels, label)
		assert.NotEqual(t, rootPrompt, label)
		sources := firstArtifactMaps(category["sources"])
		assert.NotEmpty(t, sources)
	}
	assert.ElementsMatch(t, []string{
		"Introduction and surveys",
		"Architectures",
		"Training advances",
		"Inference advances",
		"Knowledge retention and continual learning",
		"Methods and evaluation",
	}, labels)
}
