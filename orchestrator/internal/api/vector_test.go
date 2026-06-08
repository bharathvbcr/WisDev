package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVectorHandlers(t *testing.T) {
	t.Run("cosineSimilarity", func(t *testing.T) {
		assert.InDelta(t, 1.0, cosineSimilarity([]float64{1, 0}, []float64{1, 0}), 1e-9)
		assert.InDelta(t, 0.0, cosineSimilarity([]float64{1, 0}, []float64{0, 1}), 1e-9)
		assert.Equal(t, 0.0, cosineSimilarity([]float64{0, 0}, []float64{1, 1}))
		assert.Equal(t, 0.0, cosineSimilarity([]float64{}, []float64{1}))
		assert.InDelta(t, 1.0, cosineSimilarity([]float64{1, 1}, []float64{1, 1, 1}), 1e-9)

		// Vector with zero norm
		assert.Equal(t, 0.0, cosineSimilarity([]float64{0, 0}, []float64{0, 0}))
	})

	t.Run("minMaxNormalize", func(t *testing.T) {
		scores := []PaperScore{{Score: 10}, {Score: 20}}
		minMaxNormalize(scores)
		assert.Equal(t, 0.0, scores[0].Score)
		assert.Equal(t, 1.0, scores[1].Score)

		scores2 := []PaperScore{{Score: 10}, {Score: 10}}
		minMaxNormalize(scores2)
		assert.Equal(t, 10.0, scores2[0].Score)

		scores3 := []PaperScore{{Score: 10}}
		minMaxNormalize(scores3)
		assert.Equal(t, 10.0, scores3[0].Score)
	})

	t.Run("HandleBatchSimilarity - Success", func(t *testing.T) {
		req := BatchSimilarityRequest{
			QueryEmbedding: []float64{1, 0},
			Papers: []PaperEmbedding{
				{ID: "p1", Embedding: []float64{1, 0}},
				{ID: "p2", Embedding: []float64{0, 1}},
				{ID: "p3", Embedding: []float64{}}, // skipped
			},
		}
		body, _ := json.Marshal(req)
		r := httptest.NewRequest(http.MethodPost, "/similarity", bytes.NewReader(body))
		w := httptest.NewRecorder()
		HandleBatchSimilarity(w, r)
		assert.Equal(t, http.StatusOK, w.Code)
		var resp BatchSimilarityResponse
		json.Unmarshal(w.Body.Bytes(), &resp)
		assert.Len(t, resp.Scores, 2)
	})

	t.Run("HandleBatchSimilarity - Errors", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/similarity", nil)
		w := httptest.NewRecorder()
		HandleBatchSimilarity(w, r)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
		var resp1 APIError
		assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp1))
		assert.Equal(t, ErrBadRequest, resp1.Error.Code)

		r2 := httptest.NewRequest(http.MethodPost, "/similarity", bytes.NewReader([]byte("{invalid")))
		w2 := httptest.NewRecorder()
		HandleBatchSimilarity(w2, r2)
		assert.Equal(t, http.StatusBadRequest, w2.Code)
		var resp2 APIError
		assert.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp2))
		assert.Equal(t, ErrBadRequest, resp2.Error.Code)

		r3 := httptest.NewRequest(http.MethodPost, "/similarity", bytes.NewReader([]byte(`{"query_embedding":[]}`)))
		w3 := httptest.NewRecorder()
		HandleBatchSimilarity(w3, r3)
		assert.Equal(t, http.StatusBadRequest, w3.Code)
		var resp3 APIError
		assert.NoError(t, json.Unmarshal(w3.Body.Bytes(), &resp3))
		assert.Equal(t, ErrInvalidParameters, resp3.Error.Code)
	})

	t.Run("HandleFuseResults - RRF Success", func(t *testing.T) {
		req := FuseRequest{
			Mode: "rrf",
			Sources: []FuseSource{
				{
					Name:   "s1",
					Weight: 0, // default to 1.0
					Papers: []FusePaper{
						{PaperID: "p1", Title: "T1"},
						{PaperID: "p2", Title: "T2", DOI: "10.1"},
						{PaperID: "p3", Title: ""}, // skipped
					},
				},
			},
		}
		body, _ := json.Marshal(req)
		r := httptest.NewRequest(http.MethodPost, "/fuse", bytes.NewReader(body))
		w := httptest.NewRecorder()
		HandleFuseResults(w, r)
		assert.Equal(t, http.StatusOK, w.Code)
		var resp FuseResponse
		json.Unmarshal(w.Body.Bytes(), &resp)
		assert.Len(t, resp.Papers, 2)
	})

	t.Run("HandleFuseResults - Scores Success", func(t *testing.T) {
		req := FuseRequest{
			Mode:  "scores",
			Alpha: 0.5,
			Sources: []FuseSource{
				{
					Name:   "s1",
					IsBM25: false,
					Papers: []FusePaper{
						{PaperID: "p1", Title: "T1", Score: 0.9},
						{PaperID: "p2", Title: "T2", Score: 0.7},
					},
				},
				{
					Name:   "s2",
					IsBM25: true,
					Papers: []FusePaper{
						{PaperID: "p1", Title: "T1", Score: 0.8},
					},
				},
				{
					Name:   "s3_empty",
					Papers: []FusePaper{}, // skipped in fuseScores
				},
			},
		}
		body, _ := json.Marshal(req)
		r := httptest.NewRequest(http.MethodPost, "/fuse", bytes.NewReader(body))
		w := httptest.NewRecorder()
		HandleFuseResults(w, r)
		assert.Equal(t, http.StatusOK, w.Code)
		var resp FuseResponse
		json.Unmarshal(w.Body.Bytes(), &resp)
		assert.NotEmpty(t, resp.Papers)
	})

	t.Run("HandleFuseResults - Default RRF and Overrides", func(t *testing.T) {
		req := FuseRequest{
			Mode:  "unknown",
			Alpha: 2.0, // should default to 0.6
			Limit: 1,
			Sources: []FuseSource{
				{
					Name: "s1",
					Papers: []FusePaper{
						{PaperID: "p1", Title: "T1"},
						{PaperID: "p2", Title: "T2"},
					},
				},
			},
		}
		body, _ := json.Marshal(req)
		r := httptest.NewRequest(http.MethodPost, "/fuse", bytes.NewReader(body))
		w := httptest.NewRecorder()
		HandleFuseResults(w, r)
		assert.Equal(t, http.StatusOK, w.Code)
		var resp FuseResponse
		json.Unmarshal(w.Body.Bytes(), &resp)
		assert.Len(t, resp.Papers, 1)
	})

	t.Run("HandleFuseResults - Errors", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/fuse", bytes.NewReader([]byte(`{"sources":[]}`)))
		w := httptest.NewRecorder()
		HandleFuseResults(w, r)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})
}
