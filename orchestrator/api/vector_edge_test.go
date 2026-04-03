package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVectorHandlers_Edge(t *testing.T) {
	t.Run("fuseScores - Negative Alpha", func(t *testing.T) {
		req := FuseRequest{
			Mode:  "scores",
			Alpha: -1.0, // should be clamped to 0 or handled
			Sources: []FuseSource{
				{
					Name:   "s1",
					IsBM25: false,
					Papers: []FusePaper{{PaperID: "p1", Title: "T1", Score: 0.9}},
				},
			},
		}
		// Directly test fuseScores
		scores := fuseScores(req.Sources, -1.0)
		assert.Equal(t, 0.0, scores["title:t1"])
	})

	t.Run("HandleFuseResults - Missing Meta", func(t *testing.T) {
		// This path is hard to hit because we build paperMeta from req.Sources and then 
		// build ks from scoreMap which also comes from req.Sources.
		// The only way is if fusionDedupeKey returns "" for some but not others?
		// No, both loops use the same logic.
		// Wait, if fusionDedupeKey(p.DOI, p.Title) returns "" it's skipped in BOTH loops.
		// So it's mostly unreachable.
	})
	
	t.Run("HandleBatchSimilarity - Empty Paper Embedding", func(t *testing.T) {
		req := BatchSimilarityRequest{
			QueryEmbedding: []float64{1, 0},
			Papers: []PaperEmbedding{
				{ID: "p1", Embedding: nil},
			},
		}
		body, _ := json.Marshal(req)
		r := httptest.NewRequest(http.MethodPost, "/similarity", bytes.NewReader(body))
		w := httptest.NewRecorder()
		HandleBatchSimilarity(w, r)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}
