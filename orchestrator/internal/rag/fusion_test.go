package rag

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

func TestRRF(t *testing.T) {
	is := assert.New(t)

	p1 := search.Paper{ID: "1", Title: "Paper 1"}
	p2 := search.Paper{ID: "2", Title: "Paper 2"}
	p3 := search.Paper{ID: "3", Title: "Paper 3"}

	t.Run("Fusion of two lists", func(t *testing.T) {
		res1 := []search.Paper{p1, p2}
		res2 := []search.Paper{p2, p3}

		fused := RRF([][]search.Paper{res1, res2}, 60)
		is.Len(fused, 3)
		// p2 should be first as it appears in both
		is.Equal("2", fused[0].ID)
		is.Greater(fused[0].Score, fused[1].Score)
	})

	t.Run("Empty input", func(t *testing.T) {
		is.Empty(RRF(nil, 60))
	})

	t.Run("Default k", func(t *testing.T) {
		res := [][]search.Paper{{p1}}
		fused := RRF(res, -1)
		is.NotEmpty(fused)
	})

	t.Run("Paper with only Title or DOI", func(t *testing.T) {
		p4 := search.Paper{DOI: "doi1", Title: "T1"}
		p5 := search.Paper{Title: "T2"}
		fused := RRF([][]search.Paper{{p4, p5}}, 60)
		is.Len(fused, 2)
	})
}

func TestRRFWithIntelligence(t *testing.T) {
	base := [][]search.Paper{{{ID: "1", Title: "Paper 1"}}, {{ID: "2", Title: "Paper 2"}}}

	t.Run("returns fused results when provider scores are empty", func(t *testing.T) {
		got := RRFWithIntelligence(base, 60, nil)
		assert.Len(t, got, 2)
	})

	t.Run("applies provider intelligence scores", func(t *testing.T) {
		got := RRFWithIntelligence(base, 60, map[string]float64{"semantic_scholar": 2})
		assert.Len(t, got, 2)
	})
}

func TestRRFWithClicks(t *testing.T) {
	base := [][]search.Paper{{{ID: "1", Title: "Paper 1"}}, {{ID: "2", Title: "Paper 2"}}}

	t.Run("returns fused results when clicks are empty", func(t *testing.T) {
		got := RRFWithClicks(base, 60, nil)
		assert.Len(t, got, 2)
	})

	t.Run("applies click history", func(t *testing.T) {
		got := RRFWithClicks(base, 60, map[string]int{"1": 3})
		assert.Len(t, got, 2)
	})
}
