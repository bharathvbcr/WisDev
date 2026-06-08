package wisdev

import (
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/evidence"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextPass10CrucialWisdevHelpers(t *testing.T) {
	t.Run("manuscript scalar helpers and pipeline packet IDs", func(t *testing.T) {
		assert.Equal(t, 1.0, minFloat(1, 2))
		assert.Equal(t, 1.0, minFloat(2, 1))
		assert.Equal(t, "short-id", hashIDForPipeline("short-id"))
		assert.Equal(t, "abcdefghijklmnop", hashIDForPipeline("abcdefghijklmnopqrstuvwxyz"))

		result := ManuscriptPipelineResult{RawMaterials: evidence.ManuscriptRawMaterialSet{ClaimPackets: []evidence.EvidencePacket{
			{PacketID: "p2"},
			{PacketID: "p1"},
			{PacketID: "p2"},
		}}}
		assert.Equal(t, []string{"p1", "p2"}, result.ClaimPacketIDs())
	})

	t.Run("model session conversion and belief insertion handle nil maps", func(t *testing.T) {
		assert.Nil(t, (*AgentSession)(nil).ToSession())

		beliefs := NewBeliefState()
		session := (&AgentSession{
			SessionID:            "session-1",
			UserID:               "user-1",
			Query:                "root query",
			OriginalQuery:        "original",
			CorrectedQuery:       "corrected",
			DetectedDomain:       "medicine",
			SecondaryDomains:     []string{"biology"},
			Answers:              map[string]Answer{"q1": {QuestionID: "q1", Values: []string{"a1"}}},
			CurrentQuestionIndex: 1,
			QuestionSequence:     []string{"q1", "q2"},
			Status:               SessionExecutingPlan,
			BeliefState:          beliefs,
			CreatedAt:            10,
			UpdatedAt:            20,
		}).ToSession()
		require.NotNil(t, session)
		assert.Equal(t, "session-1", session.ID)
		assert.Equal(t, "corrected", session.CorrectedQuery)
		assert.Equal(t, beliefs, session.BeliefState)

		state := &BeliefState{}
		belief := &Belief{ID: "b1", Claim: "claim"}
		state.AddBelief(belief)
		assert.Same(t, belief, state.Beliefs["b1"])
	})

	t.Run("durable task timeout and citation graph counters cover nil and populated states", func(t *testing.T) {
		assert.Equal(t, 25_000, researchDurableTaskTimeoutMs(ResearchWorkerCitationGraph, researchDurableTaskSearchBatch))
		assert.Equal(t, 8_000, researchDurableTaskTimeoutMs(ResearchWorkerScout, researchDurableTaskCitationGraph))
		assert.Equal(t, 10_000, researchDurableTaskTimeoutMs(ResearchWorkerScout, researchDurableTaskVerifier))
		assert.Equal(t, 5_000, researchDurableTaskTimeoutMs(ResearchWorkerScout, researchDurableTaskBranch))
		assert.Equal(t, 18_000, researchDurableTaskTimeoutMs(ResearchWorkerSynthesizer, "other"))

		assert.Equal(t, 0, citationGraphNodeCount(nil))
		assert.Equal(t, 0, citationGraphEdgeCount(nil))
		assert.Equal(t, 0, citationGraphIdentityConflictCount(nil))
		graph := &ResearchCitationGraph{
			Nodes:              []ResearchCitationGraphNode{{ID: "n1"}, {ID: "n2"}},
			Edges:              []ResearchCitationGraphEdge{{SourceID: "n1", TargetID: "n2"}},
			IdentityConflicts:  []string{"conflict"},
			DuplicateSourceIDs: []string{"dup-1", "dup-2"},
		}
		assert.Equal(t, 2, citationGraphNodeCount(graph))
		assert.Equal(t, 1, citationGraphEdgeCount(graph))
		assert.Equal(t, 3, citationGraphIdentityConflictCount(graph))
	})

	t.Run("research memory max and dossier findings parse canonical source metadata", func(t *testing.T) {
		assert.Equal(t, int64(9), maxResearchMemoryInt64(9, 3))
		assert.Equal(t, int64(9), maxResearchMemoryInt64(3, 9))

		findings := findingsFromDossierPayload(map[string]any{
			"canonicalSources": []any{
				map[string]any{"canonicalId": "s1", "title": "Canonical source title"},
			},
			"verifiedClaims": []any{
				map[string]any{
					"packetId":       "p1",
					"claimText":      "Grounded treatment claim",
					"confidence":     1.3,
					"verifierStatus": "supported",
					"evidenceSpans": []any{
						map[string]any{"sourceCanonicalId": "s1", "snippet": "source snippet"},
					},
				},
				map[string]any{"claimText": " "},
			},
		})
		require.Len(t, findings, 1)
		assert.Equal(t, "p1", findings[0].ID)
		assert.Equal(t, "Grounded treatment claim", findings[0].Claim)
		assert.Equal(t, "s1", findings[0].SourceID)
		assert.Equal(t, "Canonical source title", findings[0].PaperTitle)
		assert.Equal(t, "source snippet", findings[0].Snippet)
		assert.Equal(t, 1.0, findings[0].Confidence)
		assert.Equal(t, "supported", findings[0].Status)
		assert.NotEmpty(t, findings[0].Keywords)
	})

	t.Run("quest payload conversion and summaries handle success and fallback branches", func(t *testing.T) {
		assert.Nil(t, valueToAny(nil))
		assert.Nil(t, valueToAny(make(chan int)))
		assert.Equal(t, map[string]any{"count": float64(2), "label": "ok"}, valueToAny(map[string]any{"count": 2, "label": "ok"}))

		assert.Equal(t, "", summarizeQuestDecomposition(nil))
		assert.Equal(t, "2 planned research tasks for immune response", summarizeQuestDecomposition(map[string]any{
			"query": " immune response ",
			"tasks": []any{map[string]any{"query": "a"}, map[string]any{"query": "b"}},
		}))
		assert.Equal(t, "1 planned research tasks", summarizeQuestDecomposition(map[string]any{
			"tasks": []any{map[string]any{"query": "a"}},
		}))
		assert.Equal(t, "immune response", summarizeQuestDecomposition(map[string]any{"query": " immune response "}))
		assert.Equal(t, "research decomposition captured", summarizeQuestDecomposition(map[string]any{"other": "value"}))
	})

	t.Run("steering journal helpers parse payload and fallback timestamps", func(t *testing.T) {
		signal, ok := steeringSignalFromJournalEntry(RuntimeJournalEntry{})
		assert.False(t, ok)
		assert.Equal(t, SteeringSignal{}, signal)

		signal, ok = steeringSignalFromJournalEntry(RuntimeJournalEntry{CreatedAt: 123, Payload: map[string]any{
			"type":    " focus ",
			"payload": " platelet ",
			"queries": []any{" query one ", "query two", "query one"},
		}})
		require.True(t, ok)
		assert.Equal(t, "focus", signal.Type)
		assert.Equal(t, "platelet", signal.Payload)
		assert.Equal(t, []string{"query one", "query two"}, signal.Queries)
		assert.Equal(t, int64(123), signal.Timestamp)

		signal, ok = steeringSignalFromJournalEntry(RuntimeJournalEntry{CreatedAt: 123, Payload: map[string]any{
			"type":      "exclude",
			"timestamp": "456",
			"queries":   []string{"a", "a", "b"},
		}})
		require.True(t, ok)
		assert.Equal(t, int64(456), signal.Timestamp)
		assert.Equal(t, []string{"a", "b"}, signal.Queries)
		assert.Equal(t, int64(789), int64FromAny(float64(789)))
		assert.Equal(t, int64(0), int64FromAny("bad"))
	})
}
