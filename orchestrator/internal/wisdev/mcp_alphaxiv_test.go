package wisdev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAlphaXivClient_GetPaper(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/paper/2401.12345", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"arxiv_id": "2401.12345", "title": "Test Paper"}`))
	}))
	defer ts.Close()

	client := NewAlphaXivClient(ts.URL)
	record, err := client.GetPaper(context.Background(), "2401.12345")

	assert.NoError(t, err)
	assert.Equal(t, "Test Paper", record.Title)
	assert.Equal(t, "2401.12345", record.ArxivID)
}

func TestAlphaXivClient_Lookup(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/search", r.URL.Path)
		assert.Equal(t, "quantum", r.URL.Query().Get("q"))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"arxiv_id": "1"}, {"arxiv_id": "2"}]`))
	}))
	defer ts.Close()

	client := NewAlphaXivClient(ts.URL)
	results, err := client.Lookup(context.Background(), "quantum")

	assert.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestAlphaXivClient_InjectsAuthHeader(t *testing.T) {
	var capturedKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedKey = r.Header.Get("X-API-Key")
		json.NewEncoder(w).Encode(CitationRecord{ArxivID: "2401.00001", Title: "Test"})
	}))
	defer ts.Close()

	client := NewAlphaXivClientWithKey(ts.URL, "test-secret-key")
	_, err := client.GetPaper(context.Background(), "2401.00001")

	assert.NoError(t, err)
	assert.Equal(t, "test-secret-key", capturedKey)
}

func TestAlphaXivClient_FromEnv(t *testing.T) {
	t.Setenv("ALPHAXIV_BASE_URL", "https://example.com")
	t.Setenv("ALPHAXIV_API_KEY", "env-key")
	client := NewAlphaXivClientFromEnv()
	assert.Equal(t, "https://example.com", client.BaseURL)
	assert.Equal(t, "env-key", client.APIKey)
}
