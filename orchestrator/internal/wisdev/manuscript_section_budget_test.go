package wisdev

import (
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/evidence"

	"github.com/stretchr/testify/assert"
)

func TestSectionPacketLimitScalesWithMinCitations(t *testing.T) {
	assert.Equal(t, 18, sectionPacketLimit("introduction", 0))
	assert.Equal(t, 25, sectionPacketLimit("introduction", 25))
	assert.Equal(t, 80, sectionPacketLimit("introduction", 120))

	assert.Equal(t, 14, sectionPacketLimit("methods", 0))
	assert.Equal(t, 20, sectionPacketLimit("methods", 40))
	assert.Equal(t, 80, sectionPacketLimit("results", 200))
}

func TestSelectRelevantPacketsForceDiversifyWithoutEnv(t *testing.T) {
	t.Setenv("MANUSCRIPT_DIVERSIFY_SOURCES", "")
	mk := func(id, src string) evidence.EvidencePacket {
		return evidence.EvidencePacket{
			PacketID:         id,
			SectionRelevance: []string{"introduction"},
			EvidenceSpans:    []evidence.EvidenceSpan{{SourceCanonicalID: src}},
		}
	}
	packets := []evidence.EvidencePacket{
		mk("a1", "srcA"), mk("a2", "srcA"), mk("a3", "srcA"),
		mk("b1", "srcB"), mk("c1", "srcC"), mk("d1", "srcD"), mk("e1", "srcE"),
	}
	distinct := func(sel []evidence.EvidencePacket) int {
		seen := map[string]struct{}{}
		for _, p := range sel {
			seen[packetPrimarySource(p)] = struct{}{}
		}
		return len(seen)
	}

	def := selectRelevantPackets(packets, "introduction", 5, nil, false)
	forced := selectRelevantPackets(packets, "introduction", 5, nil, true)
	assert.Equal(t, 5, len(forced))
	assert.Equal(t, 5, distinct(forced))
	assert.Greater(t, distinct(forced), distinct(def))
}

func TestPlanSectionsBroadSectionsBurnAssignedWhenMinCitationsSet(t *testing.T) {
	t.Setenv("MANUSCRIPT_DIVERSIFY_SOURCES", "")
	mk := func(id, src string) evidence.EvidencePacket {
		return evidence.EvidencePacket{
			PacketID:         id,
			SectionRelevance: []string{"introduction", "discussion"},
			EvidenceSpans:    []evidence.EvidenceSpan{{SourceCanonicalID: src}},
		}
	}
	raw := evidence.ManuscriptRawMaterialSet{
		ClaimPackets: []evidence.EvidencePacket{
			mk("p1", "s1"), mk("p2", "s2"), mk("p3", "s3"), mk("p4", "s4"),
		},
	}
	pipeline := &ManuscriptPipeline{MinCitations: 4}
	blueprint := pipeline.planSections("job-budget", "query", raw)
	if len(blueprint.Sections) < 2 {
		t.Fatalf("expected multiple sections, got %d", len(blueprint.Sections))
	}
	union := map[string]struct{}{}
	for _, section := range blueprint.Sections {
		for _, canonicalID := range section.SourceCanonicalIDs {
			if canonicalID != "" {
				union[canonicalID] = struct{}{}
			}
		}
	}
	assert.GreaterOrEqual(t, len(union), 3, "MinCitations>0 should spread sources across broad sections")
}
