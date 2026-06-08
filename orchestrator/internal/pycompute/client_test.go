package pycompute

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClientEmbedTextBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/ml/embed/batch", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"embeddings": [][]float64{
				{0.1, 0.2},
				{0.3, 0.4},
			},
		}))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL)
	vectors, err := client.EmbedTextBatch(context.Background(), []string{"alpha", "beta"})
	require.NoError(t, err)
	require.Len(t, vectors, 2)
	require.Equal(t, []float64{0.1, 0.2}, vectors[0])
	require.Equal(t, []float64{0.3, 0.4}, vectors[1])
}

func TestClientDoclingParsePDF(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/ml/docling/parse", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"paper": map[string]any{
				"fileName": "paper.pdf",
				"title":    "paper.pdf",
			},
			"fullText": "normalized full text",
			"sections": []map[string]any{
				{"label": "Introduction", "page": 1},
			},
			"structureMap": []map[string]any{
				{"label": "Introduction", "page": 1},
			},
			"doclingMeta": map[string]any{
				"version": "2.3.0",
			},
			"extractionInfo": map[string]any{
				"usedDocling": true,
			},
		}))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL)
	result, err := client.DoclingParsePDF(context.Background(), "paper.pdf", []byte("%PDF-1.4"))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "normalized full text", result.FullText)
	require.Len(t, result.Sections, 1)
	require.Len(t, result.StructureMap, 1)
	require.Equal(t, "2.3.0", result.DoclingMeta["version"])
	require.Equal(t, true, result.ExtractionInfo["usedDocling"])
}

func TestResolveBaseURLPrefersHTTPEnv(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", "http://canonical-sidecar:8090/")

	require.Equal(t, "http://canonical-sidecar:8090", resolveBaseURL())
}

func TestClientGenerateResearchIdeas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/ml/research/generate-ideas", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"ideas": []map[string]any{
				{"title": "Counterfactual benchmark"},
			},
			"thoughtSignature": "sig-1",
		}))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL)
	result, err := client.GenerateResearchIdeas(context.Background(), map[string]any{
		"query":            "causal inference",
		"papers":           []map[string]any{{"title": "Paper 1"}},
		"thoughtSignature": "sig-1",
	})
	require.NoError(t, err)
	require.Len(t, result["ideas"], 1)
	require.Equal(t, "sig-1", result["thoughtSignature"])
}

func TestClientBM25(t *testing.T) {
	t.Run("index", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/ml/bm25/index", r.URL.Path)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}))
		defer server.Close()

		client := NewClientWithBaseURL(server.URL)
		err := client.DelegateBM25Index(context.Background(), []string{"doc1"}, []string{"id1"})
		require.NoError(t, err)
	})

	t.Run("search", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/ml/bm25/search", r.URL.Path)
			json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{
					{"id": "id1", "score": 0.9},
				},
			})
		}))
		defer server.Close()

		client := NewClientWithBaseURL(server.URL)
		results, err := client.DelegateBM25Search(context.Background(), "query", 1)
		require.NoError(t, err)
		require.Len(t, results, 1)
		require.Equal(t, "id1", results[0]["id"])
	})
}

func TestClientErrors(t *testing.T) {
	t.Run("http error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}))
		defer server.Close()

		client := NewClientWithBaseURL(server.URL)
		_, err := client.EmbedTextBatch(context.Background(), []string{"x"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "python compute returned 500")
	})

	t.Run("invalid json", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("not json"))
		}))
		defer server.Close()

		client := NewClientWithBaseURL(server.URL)
		_, err := client.EmbedTextBatch(context.Background(), []string{"x"})
		require.Error(t, err)
	})
}

func TestClientAddsInternalServiceKeyHeader(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "python-secret")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "python-secret", r.Header.Get("X-Internal-Service-Key"))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"embeddings": [][]float64{{0.1, 0.2}},
		}))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL)
	_, err := client.EmbedTextBatch(context.Background(), []string{"alpha"})
	require.NoError(t, err)
}

func TestClientAddsLocalManifestInternalServiceKeyHeader(t *testing.T) {
	t.Setenv("ENDPOINTS_MANIFEST_ENV", "local")
	t.Setenv("INTERNAL_SERVICE_KEY", "")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "dev-internal-key", r.Header.Get("X-Internal-Service-Key"))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"embeddings": [][]float64{{0.1, 0.2}},
		}))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL)
	_, err := client.EmbedTextBatch(context.Background(), []string{"alpha"})
	require.NoError(t, err)
}
