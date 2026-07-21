package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestHandleCitations_Validation covers the no-network validation branches of the citation-graph
// route added for the Semantic Scholar cutover (frontend-thinning Phase 2). The paperId/method
// checks run before any registry access, so a nil-registry handler is sufficient.
func TestHandleCitations_Validation(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil)

	t.Run("missing paperId -> 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/provider/citations", nil)
		rec := httptest.NewRecorder()
		h.HandleCitations(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("unsupported method -> 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/provider/citations?paperId=abc", nil)
		rec := httptest.NewRecorder()
		h.HandleCitations(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})
}
