package api

import (
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeSessionMode(t *testing.T) {
	assert.Equal(t, wisdev.WisDevModeGuided, normalizeSessionMode(""))
	assert.Equal(t, wisdev.WisDevModeGuided, normalizeSessionMode("guided"))
	assert.Equal(t, wisdev.WisDevModeYOLO, normalizeSessionMode("yolo"))
	assert.Equal(t, wisdev.WisDevModeYOLO, normalizeSessionMode("YOLO_BOUNDED"))
	assert.Equal(t, wisdev.WisDevModeYOLO, normalizeSessionMode("yolo_full"))
}

func TestEnsureSessionQuestState(t *testing.T) {
	assert.Nil(t, ensureSessionQuestState(nil))

	session := map[string]any{"sessionId": "sess_abc"}
	assert.Equal(t, "quest_sess_abc", ensureSessionQuestState(session)["questId"])

	withQuest := map[string]any{"sessionId": "sess_def", "questId": "quest_explicit"}
	assert.Equal(t, "quest_explicit", ensureSessionQuestState(withQuest)["questId"])
}

func TestPersistSessionQuestState(t *testing.T) {
	t.Setenv("WISDEV_STATE_DIR", t.TempDir())
	store := wisdev.NewRuntimeStateStore(nil, nil)

	t.Run("stores session quest state", func(t *testing.T) {
		session := map[string]any{
			"questId":          "quest_abc",
			"userId":           "u1",
			"originalQuery":    "original query",
			"correctedQuery":   "corrected query",
			"detectedDomain":   "biology",
			"sessionId":        "sess_abc",
			"status":           "active",
			"secondaryDomains": []string{"biology", "medicine"},
		}
		persistSessionQuestState(store, session, "active")

		loaded, err := store.LoadQuestState("quest_abc")
		require.NoError(t, err)
		assert.Equal(t, "u1", wisdev.AsOptionalString(loaded["userId"]))
		assert.Equal(t, "biology", wisdev.AsOptionalString(loaded["detectedDomain"]))
		assert.Equal(t, "active", wisdev.AsOptionalString(loaded["status"]))
		assert.Equal(t, []any{"biology", "medicine"}, loaded["secondaryDomains"])
	})

	t.Run("supports quest state from any array type", func(t *testing.T) {
		session := map[string]any{
			"questId":          "quest_123",
			"sessionId":        "sess_123",
			"status":           "active",
			"secondaryDomains": []any{"immunology", "transcriptomics"},
		}
		persistSessionQuestState(store, session, "active")
		loaded, err := store.LoadQuestState("quest_123")
		require.NoError(t, err)
		assert.Equal(t, []any{"immunology", "transcriptomics"}, loaded["secondaryDomains"])
	})

	t.Run("no-ops when questId is missing", func(t *testing.T) {
		persistSessionQuestState(store, map[string]any{
			"sessionId":      "sess_noquest",
			"userId":         "u1",
			"originalQuery":  "query",
			"correctedQuery": "query",
		}, "active")
		_, err := store.LoadQuestState("quest_sess_noquest")
		assert.Error(t, err)
	})

	t.Run("no-op when store is nil", func(t *testing.T) {
		assert.NotPanics(t, func() {
			persistSessionQuestState(nil, map[string]any{"questId": "ignored"}, "active")
		})
	})

	t.Run("no-op when session is nil", func(t *testing.T) {
		assert.NotPanics(t, func() {
			persistSessionQuestState(store, nil, "active")
		})
	})
}

func TestHydrateSessionQuestState(t *testing.T) {
	t.Setenv("WISDEV_STATE_DIR", t.TempDir())
	store := wisdev.NewRuntimeStateStore(nil, nil)
	sessionWithoutQuest := map[string]any{"sessionId": "sess_1"}
	assert.Equal(t, sessionWithoutQuest, hydrateSessionQuestState(store, sessionWithoutQuest))

	t.Run("hydrates when quest exists", func(t *testing.T) {
		require.NoError(t, store.SaveQuestState("quest_missing", map[string]any{
			"questId": "quest_missing",
			"status":  "cached",
		}))

		session := map[string]any{
			"sessionId": "sess_missing",
			"questId":   "quest_missing",
		}
		hydrated := hydrateSessionQuestState(store, session)
		assert.Equal(t, "quest_missing", wisdev.AsOptionalString(hydrated["questId"]))

		questState, ok := hydrated["questState"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "cached", wisdev.AsOptionalString(questState["status"]))
	})

	t.Run("returns original when load fails", func(t *testing.T) {
		session := map[string]any{"sessionId": "sess_missing", "questId": "quest_not_found"}
		hydrated := hydrateSessionQuestState(store, session)
		assert.Equal(t, session, hydrated)
		_, hasQuestState := hydrated["questState"]
		assert.False(t, hasQuestState)
	})
}

func TestEnsureSessionArchitectureState(t *testing.T) {
	base := map[string]any{
		"sessionId":      "sess_arch",
		"query":          "query text",
		"correctedQuery": "corrected query text",
		"originalQuery":  "original query text",
	}
	ensured := ensureSessionArchitectureState(base)
	assert.Equal(t, "guided", wisdev.AsOptionalString(ensured["mode"]))
	assert.Equal(t, "priority", wisdev.AsOptionalString(ensured["serviceTier"]))
	reasoningGraph, ok := ensured["reasoningGraph"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "query text", wisdev.AsOptionalString(reasoningGraph["query"]))
	memoryTiers, ok := ensured["memoryTiers"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, memoryTiers, "shortTermWorking")
	assert.Contains(t, memoryTiers, "longTermVector")
	assert.Contains(t, memoryTiers, "userPersonalized")
	assert.Contains(t, memoryTiers, "artifactMemory")
	modeManifest, ok := ensured["modeManifest"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "guided", wisdev.AsOptionalString(modeManifest["mode"]))
	assert.Equal(t, "priority", wisdev.AsOptionalString(modeManifest["serviceTier"]))

	preserved := map[string]any{
		"mode":             "yolo",
		"serviceTier":      "standard",
		"reasoningGraph":   map[string]any{"query": "preset"},
		"memoryTiers":      map[string]any{},
		"modeManifest":     map[string]any{"mode": "yolo"},
		"sessionId":        "sess_arch",
		"secondaryDomains": []string{},
		"originalQuery":    "keep",
	}
	ensured = ensureSessionArchitectureState(preserved)
	assert.Equal(t, "yolo", wisdev.AsOptionalString(ensured["mode"]))
	assert.Equal(t, "standard", wisdev.AsOptionalString(ensured["serviceTier"]))
	graph, ok := ensured["reasoningGraph"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "preset", wisdev.AsOptionalString(graph["query"]))
	manifest, ok := ensured["modeManifest"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "yolo", wisdev.AsOptionalString(manifest["mode"]))
}

func TestSessionPayloadForResponse(t *testing.T) {
	t.Setenv("WISDEV_STATE_DIR", t.TempDir())
	store := wisdev.NewRuntimeStateStore(nil, nil)
	require.NoError(t, store.PersistAgentSessionMutation("loaded-session", "u2", map[string]any{
		"sessionId":      "loaded-session",
		"userId":         "u2",
		"query":          "persisted query",
		"originalQuery":  "persisted original",
		"correctedQuery": "persisted corrected",
		"mode":           "guided",
		"serviceTier":    "priority",
	}, wisdev.RuntimeJournalEntry{}))
	require.NoError(t, store.SaveQuestState("quest_loaded-session", map[string]any{
		"questId": "quest_loaded-session",
		"status":  "cached",
	}))

	gateway := &wisdev.AgentGateway{StateStore: store}

	t.Run("errors when session is nil", func(t *testing.T) {
		payload, err := sessionPayloadForResponse(gateway, nil)
		require.Error(t, err)
		assert.Nil(t, payload)
	})

	t.Run("errors when session id is missing", func(t *testing.T) {
		payload, err := sessionPayloadForResponse(gateway, &wisdev.AgentSession{})
		require.Error(t, err)
		assert.Nil(t, payload)
	})

	t.Run("hydrates from persisted state store", func(t *testing.T) {
		sessionPayload, err := sessionPayloadForResponse(gateway, &wisdev.AgentSession{
			SessionID:      "loaded-session",
			UserID:         "u2",
			OriginalQuery:  "fallback original",
			CorrectedQuery: "fallback corrected",
		})
		require.NoError(t, err)
		assert.Equal(t, "persisted query", wisdev.AsOptionalString(sessionPayload["query"]))
		assert.Equal(t, "quest_loaded-session", wisdev.AsOptionalString(sessionPayload["questId"]))
		questState, ok := sessionPayload["questState"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "cached", wisdev.AsOptionalString(questState["status"]))
	})

	t.Run("falls back to in-memory session when persisted session is missing", func(t *testing.T) {
		fallback, err := sessionPayloadForResponse(&wisdev.AgentGateway{}, &wisdev.AgentSession{
			SessionID:      "not-found",
			UserID:         "u2",
			OriginalQuery:  "fallback original",
			CorrectedQuery: "fallback corrected",
		})
		require.NoError(t, err)
		assert.Equal(t, "not-found", wisdev.AsOptionalString(fallback["sessionId"]))
		assert.Equal(t, "quest_not-found", wisdev.AsOptionalString(fallback["questId"]))
		assert.Equal(t, "priority", wisdev.AsOptionalString(fallback["serviceTier"]))
	})

	t.Run("falls back to in-memory session when persisted load fails", func(t *testing.T) {
		emptyStore := wisdev.NewRuntimeStateStore(nil, nil)
		fallback, err := sessionPayloadForResponse(&wisdev.AgentGateway{StateStore: emptyStore}, &wisdev.AgentSession{
			SessionID:      "missing-persisted",
			UserID:         "u2",
			OriginalQuery:  "fallback original",
			CorrectedQuery: "fallback corrected",
		})
		require.NoError(t, err)
		assert.Equal(t, "missing-persisted", wisdev.AsOptionalString(fallback["sessionId"]))
		assert.Equal(t, "quest_missing-persisted", wisdev.AsOptionalString(fallback["questId"]))
		assert.Equal(t, "priority", wisdev.AsOptionalString(fallback["serviceTier"]))
	})
}
