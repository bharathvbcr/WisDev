package wisdev

import (
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/evidence"

	"github.com/stretchr/testify/assert"
)

func TestNextPass3CrucialWisdevHelpers(t *testing.T) {
	t.Run("branch plan scalar helpers cover supported shapes", func(t *testing.T) {
		assert.Equal(t, []string{"alpha", "beta"}, branchPlanStringSlice([]string{"alpha", "beta"}))
		assert.Equal(t, []string{"alpha", "3"}, branchPlanStringSlice([]any{" alpha ", "", 3}))
		assert.Equal(t, []string{"solo"}, branchPlanStringSlice(" solo "))
		assert.Nil(t, branchPlanStringSlice(" "))
		assert.Nil(t, branchPlanStringSlice(map[string]any{"bad": true}))

		assert.Equal(t, 1.5, branchPlanFloat(float64(1.5)))
		assert.Equal(t, 2.5, branchPlanFloat(float32(2.5)))
		assert.Equal(t, 3.0, branchPlanFloat(3))
		assert.Equal(t, 4.0, branchPlanFloat(int64(4)))
		assert.Equal(t, 5.25, branchPlanFloat(" 5.25 "))
		assert.Equal(t, 0.0, branchPlanFloat("not-a-number"))
		assert.Equal(t, 0.0, branchPlanFloat(nil))

		assert.Equal(t, 4.0, firstPositive(-1, 0, 4, 5))
		assert.Equal(t, 0.0, firstPositive(-1, 0))
		assert.Equal(t, 8.0, maxFloat(8, 2))
		assert.Equal(t, 8.0, maxFloat(2, 8))
	})

	t.Run("retrieval integer helper handles supported numeric types", func(t *testing.T) {
		for _, tc := range []struct {
			value any
			want  int64
			ok    bool
		}{
			{int64(9), 9, true},
			{int(8), 8, true},
			{float64(7.9), 7, true},
			{float32(6.9), 6, true},
			{"5", 0, false},
		} {
			got, ok := int64FromRetrievalPayload(tc.value)
			assert.Equal(t, tc.ok, ok, "value=%v", tc.value)
			assert.Equal(t, tc.want, got, "value=%v", tc.value)
		}
	})

	t.Run("source acquisition requirement thresholds are explicit", func(t *testing.T) {
		assert.Equal(t, 0, requiredFullTextSourceCount(0))
		assert.Equal(t, 0, requiredFullTextSourceCount(-2))
		assert.Equal(t, 1, requiredFullTextSourceCount(1))
		assert.Equal(t, 2, requiredFullTextSourceCount(3))
		assert.Equal(t, 3, requiredFullTextSourceCount(8))
	})

	t.Run("manuscript packet helpers parse explicit and inferred evidence IDs", func(t *testing.T) {
		packets := []evidence.EvidencePacket{
			{
				PacketID:  "p1",
				ClaimText: "neural reranking improves retrieval precision",
				EvidenceSpans: []evidence.EvidenceSpan{
					{SourceCanonicalID: "s1"},
					{SourceCanonicalID: "s1"},
				},
			},
			{
				PacketID:  "p2",
				ClaimText: "citation integrity checks reduce hallucinated references",
				EvidenceSpans: []evidence.EvidenceSpan{
					{SourceCanonicalID: "s2"},
				},
			},
		}

		assert.Nil(t, extractExplicitPacketIDs("", packets))
		assert.Nil(t, extractExplicitPacketIDs("[p1]", nil))
		assert.Equal(t, []string{"p1", "p2"}, extractExplicitPacketIDs("Uses [p1, p2; missing | p1].", packets))

		assert.Nil(t, inferPacketIDsFromText("and the with", packets))
		assert.Equal(t, []string{"p1"}, inferPacketIDsFromText("reranking precision improves retrieval", packets))

		index := packetIndexByID(packets)
		assert.Equal(t, []string{"s1", "s2"}, sourceIDsFromPacketsByIDs(index, []string{"missing", "p1", "p2", "p1"}))
	})

	t.Run("manuscript numeric and contradiction helpers cover edge cases", func(t *testing.T) {
		assert.Nil(t, extractFirstNumericValue("no numeric token"))
		value := extractFirstNumericValue("effect size (1.25), p value")
		if assert.NotNil(t, value) {
			assert.Equal(t, 1.25, *value)
		}

		raw := evidence.ManuscriptRawMaterialSet{ClaimPackets: []evidence.EvidencePacket{
			{PacketID: "p1", ContradictionPacketIDs: []string{"p2", "p2", "p3"}},
			{PacketID: "p2", ContradictionPacketIDs: []string{"p1"}},
		}}
		assert.Equal(t, 3, contradictionCount(raw))
		assert.Equal(t, 0, contradictionCount(evidence.ManuscriptRawMaterialSet{}))
	})
}
