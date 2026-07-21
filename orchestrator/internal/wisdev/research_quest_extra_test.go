package wisdev

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResearchQuestRuntime_Extra(t *testing.T) {
	t.Run("ApplyHooks nil receiver", func(t *testing.T) {
		var rt *ResearchQuestRuntime
		assert.Nil(t, rt.ApplyHooks(ResearchQuestHooks{}))
	})

	t.Run("ResumeQuest empty ID", func(t *testing.T) {
		runtime := NewResearchQuestRuntime(&AgentGateway{})
		_, err := runtime.ResumeQuest(context.Background(), "", ResearchQuestRequest{})
		assert.Error(t, err)
		assert.NotEmpty(t, err.Error())
	})

	t.Run("LoadQuest empty ID", func(t *testing.T) {
		runtime := NewResearchQuestRuntime(&AgentGateway{})
		_, err := runtime.LoadQuest(context.Background(), "")
		assert.Error(t, err)
		assert.NotEmpty(t, err.Error())
	})

	t.Run("persistQuestMemory - nil memoryStore", func(t *testing.T) {
		runtime := NewResearchQuestRuntime(&AgentGateway{})
		err := runtime.persistQuestMemory(context.Background(), &ResearchQuest{})
		assert.NoError(t, err) // should return nil
	})

	t.Run("escalateQuestModel - low count", func(t *testing.T) {
		quest := &ResearchQuest{RetrievedCount: 5, Artifacts: make(map[string]any)}
		escalateQuestModel(quest, "reason")
		assert.True(t, quest.HeavyModelRequired)
		assert.Equal(t, ModelTierHeavy, quest.DecisionModelTier)
	})

	t.Run("StartQuest persists staged decomposition and dossiers", func(t *testing.T) {
		runtime := newResearchQuestRuntimeForTest(t, stubQuestHooks(testQuestSources(2), CitationVerdict{
			Status:        "promoted",
			Promoted:      true,
			VerifiedCount: 2,
		}))
		runtime.dossierFn = runtime.defaultEvidenceDossiers

		quest, err := runtime.StartQuest(context.Background(), ResearchQuestRequest{
			UserID:      "user-stage",
			Query:       "staged research quest",
			QualityMode: "quality",
		})
		if !assert.NoError(t, err) {
			return
		}

		assert.Equal(t, QuestStatusComplete, quest.Status)
		assert.NotNil(t, asMap(quest.Artifacts["decomposition"]))
		assert.NotNil(t, asMap(quest.Artifacts["hypotheses"]))
		assert.NotEmpty(t, quest.Hypotheses)
		assert.NotEmpty(t, quest.EvidenceDossiers)
		assert.NotEmpty(t, quest.ResearchScratchpad["decomposition"])
		assert.NotEmpty(t, quest.ResearchScratchpad["hypotheses"])
		assert.NotEmpty(t, quest.ResearchScratchpad["reasoning"])
	})
}

func TestStableCitationIdentityKey_Extra(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		assert.Equal(t, "", stableCitationIdentityKey(CanonicalCitation{}))
	})
	t.Run("DOI", func(t *testing.T) {
		assert.Equal(t, "doi:10.1/1", stableCitationIdentityKey(CanonicalCitation{DOI: "10.1/1"}))
	})
	t.Run("Arxiv", func(t *testing.T) {
		assert.Equal(t, "arxiv:123", stableCitationIdentityKey(CanonicalCitation{ArxivID: "123"}))
	})
}

func TestAsMap_Extra(t *testing.T) {
	assert.Nil(t, asMap(nil))
	assert.Nil(t, asMap("not a map"))
	m := map[string]any{"a": 1}
	assert.NotNil(t, asMap(m))
}
