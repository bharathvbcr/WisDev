package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/evidence"
)

func TestParallelPacketSourceMeta(t *testing.T) {
	titleByCanonicalID := map[string]string{
		"doi:10.1000/aaa": "Alpha Paper",
		"doi:10.1000/bbb": "Beta Paper",
		"doi:10.1000/zzz": "Zeta Paper",
	}

	t.Run("multi-span packet uses primary source from first span", func(t *testing.T) {
		packets := []evidence.EvidencePacket{
			{
				PacketID: "pkt-multi",
				EvidenceSpans: []evidence.EvidenceSpan{
					{SourceCanonicalID: "doi:10.1000/zzz"},
					{SourceCanonicalID: "doi:10.1000/aaa"},
				},
			},
		}

		claimPacketIDs, sourceCanonicalIDs, sourceTitles := parallelPacketSourceMeta(packets, titleByCanonicalID)

		require.Len(t, claimPacketIDs, 1)
		require.Len(t, sourceCanonicalIDs, 1)
		require.Len(t, sourceTitles, 1)
		assert.Equal(t, "pkt-multi", claimPacketIDs[0])
		assert.Equal(t, "doi:10.1000/zzz", sourceCanonicalIDs[0])
		assert.Equal(t, "Zeta Paper", sourceTitles[0])
	})

	t.Run("two packets sharing one source preserve parallel length and duplicate canonical IDs", func(t *testing.T) {
		packets := []evidence.EvidencePacket{
			{
				PacketID: "pkt-1",
				EvidenceSpans: []evidence.EvidenceSpan{
					{SourceCanonicalID: "doi:10.1000/bbb"},
				},
			},
			{
				PacketID: "pkt-2",
				EvidenceSpans: []evidence.EvidenceSpan{
					{SourceCanonicalID: "doi:10.1000/bbb"},
				},
			},
		}

		claimPacketIDs, sourceCanonicalIDs, sourceTitles := parallelPacketSourceMeta(packets, titleByCanonicalID)

		require.Len(t, claimPacketIDs, 2)
		require.Len(t, sourceCanonicalIDs, 2)
		require.Len(t, sourceTitles, 2)
		assert.Equal(t, []string{"pkt-1", "pkt-2"}, claimPacketIDs)
		assert.Equal(t, []string{"doi:10.1000/bbb", "doi:10.1000/bbb"}, sourceCanonicalIDs)
		assert.Equal(t, []string{"Beta Paper", "Beta Paper"}, sourceTitles)
	})

	t.Run("index k maps to packet primary source not alphabetically sorted unique set", func(t *testing.T) {
		packets := []evidence.EvidencePacket{
			{
				PacketID: "pkt-zeta",
				EvidenceSpans: []evidence.EvidenceSpan{
					{SourceCanonicalID: "doi:10.1000/zzz"},
				},
			},
			{
				PacketID: "pkt-alpha",
				EvidenceSpans: []evidence.EvidenceSpan{
					{SourceCanonicalID: "doi:10.1000/aaa"},
				},
			},
		}

		claimPacketIDs, sourceCanonicalIDs, sourceTitles := parallelPacketSourceMeta(packets, titleByCanonicalID)

		require.Len(t, claimPacketIDs, 2)
		assert.Equal(t, "doi:10.1000/zzz", sourceCanonicalIDs[0])
		assert.Equal(t, "Zeta Paper", sourceTitles[0])
		assert.Equal(t, "doi:10.1000/aaa", sourceCanonicalIDs[1])
		assert.Equal(t, "Alpha Paper", sourceTitles[1])

		sortedUnique := uniqueStrings([]string{"doi:10.1000/zzz", "doi:10.1000/aaa"})
		assert.NotEqual(t, sortedUnique[0], sourceCanonicalIDs[0], "positional [1] must not follow sorted-unique index 0")
	})
}
