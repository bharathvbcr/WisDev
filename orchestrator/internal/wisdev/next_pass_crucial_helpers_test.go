package wisdev

import (
	"fmt"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
)

type retrievalPayloadStringer struct {
	value string
}

func (s retrievalPayloadStringer) String() string {
	return s.value
}

func TestNextPassCrucialWisdevHelpers(t *testing.T) {
	t.Run("coverage obligations include only open ledger entries", func(t *testing.T) {
		state := &ResearchSessionState{CoverageLedger: []CoverageLedgerEntry{
			{
				ID:          "open-source",
				Category:    "source_diversity",
				Status:      " open ",
				Title:       "Need independent source triangulation",
				Description: "Need independent source triangulation",
			},
			{
				ID:       "resolved-gap",
				Category: "coverage",
				Status:   coverageLedgerStatusResolved,
				Title:    "Already resolved",
			},
		}}

		obligations := BuildResearchCoverageObligations(state)
		assert.Len(t, obligations, 1)
		payload := obligations[0].(map[string]any)
		assert.Equal(t, "open-source", payload["id"])
		assert.Equal(t, "missing_source_diversity", payload["obligationType"])
		assert.Equal(t, string(ResearchWorkerSourceDiversifier), payload["ownerWorker"])

		assert.Nil(t, BuildResearchCoverageObligations(nil))
		assert.Nil(t, BuildResearchCoverageObligations(&ResearchSessionState{}))
	})

	t.Run("verifier verdict trims and defaults", func(t *testing.T) {
		assert.Equal(t, "", ResearchVerifierVerdict(nil))
		assert.Equal(t, "unknown", ResearchVerifierVerdict(&ResearchVerifierDecision{}))
		assert.Equal(t, "promote", ResearchVerifierVerdict(&ResearchVerifierDecision{Verdict: " promote "}))
	})

	t.Run("retrieval payload helpers normalize provider metadata", func(t *testing.T) {
		typedWarnings := []search.ProviderWarning{{Provider: "openalex", Message: "timeout"}}
		assert.Equal(t, typedWarnings, providerWarningsFromRetrievalPayload(map[string]any{"warnings": typedWarnings}))

		warnings := providerWarningsFromRetrievalPayload(map[string]any{
			"warnings": []any{
				map[string]any{"provider": " semantic_scholar ", "message": retrievalPayloadStringer{"rate limited"}},
				map[string]any{"provider": "", "message": ""},
				"bad-shape",
			},
		})
		assert.Equal(t, []search.ProviderWarning{{Provider: "semantic_scholar", Message: "rate limited"}}, warnings)

		providers := providersFromRetrievalPayload(map[string]any{
			"providers": map[string]any{
				"openalex":         2.0,
				"semantic_scholar": int64(3),
				"bad":              "not-counted",
			},
		})
		assert.Equal(t, map[string]int{"openalex": 2, "semantic_scholar": 3}, providers)

		original := map[string]int{"pubmed": 4}
		cloned := providersFromRetrievalPayload(map[string]any{"providers": original})
		cloned["pubmed"] = 99
		assert.Equal(t, 4, original["pubmed"])
	})

	t.Run("retrieval scalar helpers cover supported types", func(t *testing.T) {
		for _, tc := range []struct {
			value any
			want  int
			ok    bool
		}{
			{1, 1, true},
			{int64(2), 2, true},
			{float64(3.9), 3, true},
			{float32(4.1), 4, true},
			{"5", 0, false},
		} {
			got, ok := intFromRetrievalPayload(tc.value)
			assert.Equal(t, tc.ok, ok, fmt.Sprintf("value=%v", tc.value))
			assert.Equal(t, tc.want, got, fmt.Sprintf("value=%v", tc.value))
		}

		assert.Equal(t, "stringer value", stringFromRetrievalPayload(retrievalPayloadStringer{" stringer value "}))
		assert.Equal(t, "12", stringFromRetrievalPayload(12))
		assert.Equal(t, "", stringFromRetrievalPayload(nil))
	})

	t.Run("safe ids and redis keys handle nils and nested plans", func(t *testing.T) {
		assert.Equal(t, "", safeSessionID(nil))
		assert.Equal(t, "", safePlanID(nil))
		assert.Equal(t, "", safePlanID(&AgentSession{}))
		assert.Equal(t, "session-1", safeSessionID(&AgentSession{SessionID: "session-1"}))
		assert.Equal(t, "plan-1", safePlanID(&AgentSession{Plan: &PlanState{PlanID: "plan-1"}}))

		store := NewRedisSessionStore(nil)
		assert.Equal(t, "wisdev_session:abc", store.key("abc"))
	})
}
