package rag

import (
	"math"
	"strings"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
)

func TestContextPacketCrucialHelpers(t *testing.T) {
	t.Run("collectMemoryHintTerms trims dedupes and preserves first spelling", func(t *testing.T) {
		primer := &ResearchMemoryPrimer{
			RelatedTopics:      []string{" Sleep ", "AI", "sleep"},
			RelatedMethods:     []string{"hippocampal replay"},
			RecommendedQueries: []string{" hippocampal replay ", "targeted memory reactivation"},
		}

		assert.Equal(t, []string{
			"Sleep",
			"hippocampal replay",
			"targeted memory reactivation",
		}, collectMemoryHintTerms(primer))
		assert.Nil(t, collectMemoryHintTerms(nil))
	})

	t.Run("packetDedupKey trims stable identity fields", func(t *testing.T) {
		key := packetDedupKey(evidencePacket{
			PaperID:    " p1 ",
			SourceKind: " abstract ",
			Text:       " evidence text ",
		})
		assert.Equal(t, "p1|abstract|evidence text", key)
	})

	t.Run("lightweightTextEmbedding is normalized and ignores stop words", func(t *testing.T) {
		empty := lightweightTextEmbedding("the and of")
		assert.Len(t, empty, lightEmbeddingDims)
		assert.Equal(t, 0.0, vectorNorm(empty))

		embedding := lightweightTextEmbedding("hippocampal replay improves memory")
		assert.Len(t, embedding, lightEmbeddingDims)
		assert.InDelta(t, 1.0, vectorNorm(embedding), 0.000001)
		assert.Equal(t, embedding, lightweightTextEmbedding("hippocampal replay improves memory"))
	})

	t.Run("tokenizeForEmbedding filters short tokens and punctuation", func(t *testing.T) {
		assert.Equal(t, []string{"alpha", "beta2", "gamma"}, tokenizeForEmbedding("an alpha, beta2! THE gamma"))
	})

	t.Run("buildEvidencePackets covers fallback sources and memory boosts", func(t *testing.T) {
		papers := []search.Paper{
			{ID: "p1", Title: "Title One", Abstract: "Hippocampal replay improves memory consolidation in sleep studies."},
			{ID: "p2", Title: "Only title available"},
			{ID: "p3", StructureMap: []any{map[string]any{"section": "methods"}, map[string]any{"section": "results"}}},
		}
		primer := &ResearchMemoryPrimer{RelatedTopics: []string{"hippocampal replay"}}

		packets := buildEvidencePackets("memory consolidation", papers, primer, false)
		assert.Len(t, packets, 3)
		kinds := make([]string, 0, len(packets))
		for _, packet := range packets {
			kinds = append(kinds, packet.SourceKind)
		}
		assert.Contains(t, kinds, "abstract")
		assert.Contains(t, kinds, "title_only")
		assert.Contains(t, kinds, "structure_map")

		abstractScore := 0.0
		for _, packet := range packets {
			if packet.PaperID == "p1" {
				abstractScore = packet.Score
			}
		}
		assert.Greater(t, abstractScore, 0.0)
	})
}

func TestBuildRaptorOverviewCrucialBranches(t *testing.T) {
	assert.Empty(t, buildRaptorOverview(t.Context(), nil, nil))
	assert.Empty(t, buildRaptorOverview(t.Context(), []evidencePacket{
		{PaperID: "p1", Text: "one"},
		{PaperID: "p2", Text: "two"},
	}, NewRaptorService(nil)))

	longText := strings.Repeat("hippocampal replay supports memory consolidation. ", 8)
	overview := buildRaptorOverview(t.Context(), []evidencePacket{
		{PaperID: "p1", Section: "abstract", SourceKind: "abstract", Text: longText},
		{PaperID: "p2", Section: "abstract", SourceKind: "abstract", Text: longText},
		{PaperID: "p3", Section: "abstract", SourceKind: "abstract", Text: longText},
	}, NewRaptorService(nil))
	assert.NotEmpty(t, overview)
	assert.LessOrEqual(t, len([]rune(overview)), maxPacketChars)
}

func vectorNorm(values []float64) float64 {
	total := 0.0
	for _, value := range values {
		total += value * value
	}
	return math.Sqrt(total)
}
