package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleCitations_InvalidDirection(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/provider/citations?paperId=abc&direction=sideways", nil)
	rec := httptest.NewRecorder()
	h.HandleCitations(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleCitations_BackwardUpstreamUnavailable(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/provider/citations?paperId=abc&direction=backward", nil)
	rec := httptest.NewRecorder()
	h.HandleCitations(rec, req)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestParseCitationDirection(t *testing.T) {
	forward, ok := parseCitationDirection("")
	assert.True(t, ok)
	assert.True(t, forward)

	forward, ok = parseCitationDirection("references")
	assert.True(t, ok)
	assert.False(t, forward)

	_, ok = parseCitationDirection("diagonal")
	assert.False(t, ok)
}

func TestHandleProviderEnrichment_Validation(t *testing.T) {
	t.Run("open-access missing doi -> 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/provider/open-access", nil)
		rec := httptest.NewRecorder()
		handleProviderOpenAccess(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("open-access method -> 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/provider/open-access?doi=10.1/x", nil)
		rec := httptest.NewRecorder()
		handleProviderOpenAccess(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("retractions missing dois -> 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/provider/retractions", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		handleProviderRetractions(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("retractions method -> 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/provider/retractions", nil)
		rec := httptest.NewRecorder()
		handleProviderRetractions(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("retractions too many dois -> 400", func(t *testing.T) {
		dois := make([]string, 30)
		for i := range dois {
			dois[i] = fmtDOI(i)
		}
		body, _ := json.Marshal(map[string]any{"dois": dois})
		req := httptest.NewRequest(http.MethodPost, "/provider/retractions", strings.NewReader(string(body)))
		rec := httptest.NewRecorder()
		handleProviderRetractions(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("author metrics missing id -> 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/provider/bibliometrics/author", nil)
		rec := httptest.NewRecorder()
		handleProviderAuthorMetrics(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("source metrics missing id -> 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/provider/bibliometrics/source", nil)
		rec := httptest.NewRecorder()
		handleProviderSourceMetrics(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestRegisterProviderEnrichmentRoutes(t *testing.T) {
	mux := http.NewServeMux()
	RegisterProviderEnrichmentRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/provider/open-access", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func fmtDOI(i int) string {
	return "10.1/" + strings.Repeat("x", 1) + string(rune('a'+i%26)) + string(rune('0'+i%10))
}
