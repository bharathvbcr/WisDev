package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
)

func TestFullPaperHandler(t *testing.T) {
	reg := search.NewProviderRegistry()
	reg.Register(&MockProvider{
		name: "mock",
		papers: []search.Paper{
			{ID: "1", Title: "P1", DOI: "10.1/1", Source: "mock"},
		},
	})
	h := NewFullPaperHandler(reg)

	t.Run("HandleFullPaperRetrieval - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/full", nil)
		rec := httptest.NewRecorder()
		h.HandleFullPaperRetrieval(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleFullPaperRetrieval - Invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/full", strings.NewReader("{invalid"))
		rec := httptest.NewRecorder()
		h.HandleFullPaperRetrieval(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleFullPaperRetrieval - Success", func(t *testing.T) {
		body := FullPaperRetrievalRequest{
			JobID:       "j1",
			Query:       "q1",
			PlanQueries: []string{"q2"},
			Limit:       5,
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/full", bytes.NewReader(jsonBody))
		rec := httptest.NewRecorder()
		h.HandleFullPaperRetrieval(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp FullPaperRetrievalResponse
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Equal(t, "j1", resp.JobID)
		assert.Equal(t, "q1", resp.Query)
		assert.Len(t, resp.NormalizedQueries, 2)
		assert.NotEmpty(t, resp.DeduplicatedPapers)
	})

	t.Run("HandleFullPaperRetrieval - Empty Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/full", strings.NewReader(""))
		rec := httptest.NewRecorder()
		h.HandleFullPaperRetrieval(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleFullPaperRetrieval - Missing Query Error", func(t *testing.T) {
		body := FullPaperRetrievalRequest{
			JobID: "j1",
			Query: "", // Trigger error in runFullPaperRetrieval
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/full", bytes.NewReader(jsonBody))
		rec := httptest.NewRecorder()
		h.HandleFullPaperRetrieval(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})
}

func TestFullPaperHelpers(t *testing.T) {
	t.Run("boundedFullPaperLimit", func(t *testing.T) {
		assert.Equal(t, 10, boundedFullPaperLimit(0))
		assert.Equal(t, 10, boundedFullPaperLimit(-1))
		assert.Equal(t, 50, boundedFullPaperLimit(100))
		assert.Equal(t, 25, boundedFullPaperLimit(25))
	})

	t.Run("normalizeFullPaperQueries", func(t *testing.T) {
		q := "main"
		plan := []string{"sub1", "sub2"}
		normalized := normalizeFullPaperQueries(q, plan)
		assert.Equal(t, []string{"main", "sub1", "sub2"}, normalized)
	})
}
