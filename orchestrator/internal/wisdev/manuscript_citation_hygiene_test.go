package wisdev

import (
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/evidence"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeOutOfRangePositionalCitationsDropsInvalidMarkers(t *testing.T) {
	packets := []evidence.EvidencePacket{
		{PacketID: "p1"},
		{PacketID: "p2"},
		{PacketID: "p3"},
	}

	content, dropped := sanitizeOutOfRangePositionalCitations(
		"A claim [1]. Another cites [18] and [2, 18].",
		packets,
	)
	assert.Equal(t, []int{18}, dropped)
	assert.Equal(t, "A claim [1]. Another cites and [2].", content)
}

func TestSanitizeOutOfRangePositionalCitationsPreservesInRangeAndPacketIDs(t *testing.T) {
	packets := []evidence.EvidencePacket{
		{PacketID: "p1"},
		{PacketID: "p2"},
	}

	content, dropped := sanitizeOutOfRangePositionalCitations(
		"Grounded [1][2] and explicit [p1].",
		packets,
	)
	assert.Empty(t, dropped)
	assert.Equal(t, "Grounded [1][2] and explicit [p1].", content)
}

func TestSanitizeOutOfRangePositionalCitationsNoOpWhenValid(t *testing.T) {
	packets := []evidence.EvidencePacket{{PacketID: "p1"}, {PacketID: "p2"}}
	original := "Valid [1] and [2, 1]."
	content, dropped := sanitizeOutOfRangePositionalCitations(original, packets)
	assert.Empty(t, dropped)
	assert.Equal(t, original, content)
}

func TestApplyCitationMarkerHygieneRebuildsParagraphs(t *testing.T) {
	packets := []evidence.EvidencePacket{
		{PacketID: "p1", ClaimText: "first claim", VerifierStatus: "verified", EvidenceSpans: []evidence.EvidenceSpan{{SourceCanonicalID: "s1"}}},
		{PacketID: "p2", ClaimText: "second claim", VerifierStatus: "verified", EvidenceSpans: []evidence.EvidenceSpan{{SourceCanonicalID: "s2"}}},
	}
	section := SectionDraftArtifact{
		SectionID:      "introduction",
		Title:          "Introduction",
		Content:        "Grounded claim [1]. Cross-section drift [18].",
		ClaimPacketIDs: []string{"p1", "p2"},
	}
	applyCitationMarkerHygiene(&section, packets)
	assert.Equal(t, "Grounded claim [1]. Cross-section drift.", section.Content)
	require.Len(t, section.Paragraphs, 1)
	assert.Equal(t, []string{"p1"}, section.Paragraphs[0].ClaimPacketIDs)
}
