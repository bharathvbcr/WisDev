package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestHandleProviderByDOI_Validation covers the no-network validation branches of the
// DOI-resolution route added for frontend-thinning Phase 2.
func TestHandleProviderByDOI_Validation(t *testing.T) {
	t.Run("missing doi -> 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/provider/by-doi", nil)
		rec := httptest.NewRecorder()
		handleProviderByDOI(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("unsupported method -> 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/provider/by-doi?doi=10.1/x", nil)
		rec := httptest.NewRecorder()
		handleProviderByDOI(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})
}
