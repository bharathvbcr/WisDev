package api

import (
	"testing"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
	"github.com/stretchr/testify/assert"
)

func TestWisDevV2_MathHelpers(t *testing.T) {
	t.Run("resolveOperationMode", func(t *testing.T) {
		assert.Equal(t, "yolo", resolveOperationMode("YOLO"))
		assert.Equal(t, "guided", resolveOperationMode("something"))
		assert.Equal(t, "guided", resolveOperationMode(""))
	})
}

func TestWisDevV2_CommitteeHelpers(t *testing.T) {
	papers := []wisdev.Source{
		{ID: "p1", Title: "Title 1", Score: 0.9},
		{ID: "p2", Title: "Title 2", Score: 0.8},
	}

	t.Run("buildCommitteeAnswer", func(t *testing.T) {
		ans := buildCommitteeAnswer("q", papers)
		assert.Contains(t, ans, "Title 1")
		assert.Contains(t, ans, "Title 2")
		
		ansEmpty := buildCommitteeAnswer("q", nil)
		assert.Contains(t, ansEmpty, "No committee evidence")
		
		ansNoTitle := buildCommitteeAnswer("q", []wisdev.Source{{ID: "1"}})
		assert.Contains(t, ansNoTitle, "Committee review completed")
	})

	t.Run("buildCommitteeCitations", func(t *testing.T) {
		citations := buildCommitteeCitations(papers)
		assert.Len(t, citations, 2)
		assert.Equal(t, "p1", citations[0]["sourceId"])
		
		papersNoID := []wisdev.Source{{Title: "T1", DOI: "D1"}}
		c2 := buildCommitteeCitations(papersNoID)
		assert.Equal(t, "D1", c2[0]["sourceId"])
	})

	t.Run("buildCommitteePapers", func(t *testing.T) {
		mapped := buildCommitteePapers(papers)
		assert.Len(t, mapped, 2)
		assert.Equal(t, "p1", mapped[0]["id"])
	})

	t.Run("buildMultiAgentCommitteeResult", func(t *testing.T) {
		res := buildMultiAgentCommitteeResult("q", "cs", papers, 5, true)
		assert.True(t, res["success"].(bool))
		assert.Equal(t, "go_committee_v2", res["mode"])
		assert.NotEmpty(t, res["analyst"])
	})
}

func TestWisDevV2_GateHelpers(t *testing.T) {
	t.Run("extractCommitteeSignals", func(t *testing.T) {
		meta := map[string]any{
			"multiAgent": map[string]any{
				"critic": map[string]any{
					"citationCount": 5.0,
					"decision": "accept",
				},
				"supervisor": map[string]any{
					"sourceCount": 10.0,
				},
			},
		}
		cc, sc, dec := extractCommitteeSignals(meta)
		assert.Equal(t, 5, cc)
		assert.Equal(t, 10, sc)
		assert.Equal(t, "accept", dec)
		
		_, _, decEmpty := extractCommitteeSignals(nil)
		assert.Empty(t, decEmpty)
	})

	t.Run("buildEvidenceGatePayload", func(t *testing.T) {
		claims := []map[string]any{
			{"source": map[string]any{"id": "1"}},
			{"source": nil},
		}
		res := buildEvidenceGatePayload(claims, 0)
		assert.False(t, res["passed"].(bool))
		assert.True(t, res["provisional"].(bool))
		
		res2 := buildEvidenceGatePayload(nil, 0)
		assert.True(t, res2["passed"].(bool))
	})
}

func TestWisDevV2_DraftHelpers(t *testing.T) {
	t.Run("normalizeSectionID", func(t *testing.T) {
		assert.Equal(t, "intro_section", normalizeSectionID("Intro Section "))
	})

	t.Run("uniqueStrings", func(t *testing.T) {
		assert.Equal(t, []string{"a", "b"}, uniqueStrings([]string{" a", "b", "a "}))
	})

	t.Run("inferDraftSections", func(t *testing.T) {
		assert.Contains(t, inferDraftSections("Survey of AI", nil), "Landscape")
		assert.Contains(t, inferDraftSections("Benchmark of models", nil), "Comparative Findings")
		assert.Contains(t, inferDraftSections("Architecture of X", nil), "Operational Risks")
	})

	t.Run("buildDraftOutlinePayload", func(t *testing.T) {
		res := buildDraftOutlinePayload("d1", "Title", 1000, []string{"Custom"})
		assert.Equal(t, "d1", res["documentId"])
		items := res["items"].([]map[string]any)
		assert.NotEmpty(t, items)
	})

	t.Run("buildDraftSectionPayload", func(t *testing.T) {
		papers := []map[string]any{
			{"title": "T1", "summary": "S1", "score": 0.9},
		}
		res := buildDraftSectionPayload("d1", "s1", "Title", 200, papers)
		assert.Equal(t, "d1", res["documentId"])
		assert.Contains(t, res["content"].(string), "S1")
	})
}

func TestWisDevV2_MapHelpers(t *testing.T) {
	t.Run("mapAny", func(t *testing.T) {
		assert.Empty(t, mapAny(nil))
		assert.NotEmpty(t, mapAny(map[string]any{"a": 1}))
	})

	t.Run("mergeAnyMap", func(t *testing.T) {
		base := map[string]any{"a": 1, "b": map[string]any{"c": 2}}
		override := map[string]any{"b": map[string]any{"c": 3, "d": 4}}
		merged := mergeAnyMap(base, override)
		b := merged["b"].(map[string]any)
		assert.Equal(t, 3, b["c"])
		assert.Equal(t, 4, b["d"])
	})

	t.Run("sliceAnyMap", func(t *testing.T) {
		assert.Empty(t, sliceAnyMap(nil))
		in := []any{map[string]any{"a": 1}}
		assert.Len(t, sliceAnyMap(in), 1)
	})

	t.Run("sliceStrings", func(t *testing.T) {
		assert.Empty(t, sliceStrings(nil))
		in := []any{"a", "b"}
		assert.Equal(t, []string{"a", "b"}, sliceStrings(in))
	})
}
