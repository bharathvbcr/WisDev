package wisdev

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveCitationsLivePrefersDedicatedGatewayEndpoint(t *testing.T) {
	paths := make([]string, 0, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		require.Equal(t, "/internal/citations/resolve", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"data": map[string]any{
				"resolved": []map[string]any{
					{
						"id":                       "p1",
						"doi":                      "10.1000/test-1",
						"title":                    "Paper 1",
						"engine":                   "rust-citation-resolve",
						"api_confirmed":            true,
						"verification_status":      "verified",
						"source_authority":         "crossref",
						"resolver_agreement_count": 2,
						"provenance_hash":          "hash-1",
					},
				},
				"promotionEligible": true,
				"blockingIssues":    []string{},
				"engine":            "rust-citation-resolve",
			},
		}))
	}))
	defer server.Close()

	t.Setenv("RUST_GATEWAY_INTERNAL_URL", server.URL)

	records, err := ResolveCitationsLive([]Source{{ID: "p1", Title: "Paper 1", DOI: "10.1000/test-1"}})
	require.NoError(t, err)
	require.Len(t, records, 1)

	assert.Equal(t, []string{"/internal/citations/resolve"}, paths)
	assert.Equal(t, "10.1000/test-1", AsOptionalString(records[0]["doi"]))
	assert.Equal(t, "crossref", AsOptionalString(records[0]["sourceAuthority"]))
	assert.Equal(t, true, records[0]["verified"])
}

func TestResolveCitationsLiveReturnsDedicatedEndpointErrors(t *testing.T) {
	paths := make([]string, 0, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		require.Equal(t, "/internal/citations/resolve", r.URL.Path)
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer server.Close()

	t.Setenv("RUST_GATEWAY_INTERNAL_URL", server.URL)

	records, err := ResolveCitationsLive([]Source{{ID: "p2", Title: "Paper 2", DOI: "10.1000/test-2"}})
	require.Error(t, err)
	require.Nil(t, records)

	assert.Equal(t, []string{"/internal/citations/resolve"}, paths)
	assert.Contains(t, err.Error(), "status 404")
}
