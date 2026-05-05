package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
)

func TestSearchHelperHandlers(t *testing.T) {
	reg := search.NewProviderRegistry()
	h := NewSearchHandler(reg, reg, nil)

	t.Run("HandleRelatedArticles - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/related", nil)
		rec := httptest.NewRecorder()
		h.HandleRelatedArticles(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
		assert.Equal(t, http.MethodPost, resp.Error.Details["allowedMethod"])
	})

	t.Run("HandleRelatedArticles - Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/related", strings.NewReader(`{invalid`))
		rec := httptest.NewRecorder()
		h.HandleRelatedArticles(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleRelatedArticles - Missing Query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/related", strings.NewReader(`{"query":"","source_title":""}`))
		rec := httptest.NewRecorder()
		h.HandleRelatedArticles(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("HandleQueryField - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/field", nil)
		rec := httptest.NewRecorder()
		h.HandleQueryField(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleQueryField - Missing Query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/field", strings.NewReader(`{"query":"   "}`))
		rec := httptest.NewRecorder()
		h.HandleQueryField(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("HandleQueryCategories - Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/categories", strings.NewReader(`{invalid`))
		rec := httptest.NewRecorder()
		h.HandleQueryCategories(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleQueryCategories - Missing Query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/categories", strings.NewReader(`{"query":"   "}`))
		rec := httptest.NewRecorder()
		h.HandleQueryCategories(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("HandleQueryIntroduction - Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/introduction", strings.NewReader(`{invalid`))
		rec := httptest.NewRecorder()
		h.HandleQueryIntroduction(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleQueryIntroduction - Missing Query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/introduction", strings.NewReader(`{"query":"","papers":[]}`))
		rec := httptest.NewRecorder()
		h.HandleQueryIntroduction(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("HandleBatchSummaries - Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/summaries", strings.NewReader(`{invalid`))
		rec := httptest.NewRecorder()
		h.HandleBatchSummaries(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleBatchSummaries - Missing Papers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/summaries", strings.NewReader(`{"papers":[]}`))
		rec := httptest.NewRecorder()
		h.HandleBatchSummaries(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("HandleRelatedArticles - Success filters excluded links", func(t *testing.T) {
		originalFastParallelSearch := wisdev.FastParallelSearch
		t.Cleanup(func() {
			wisdev.FastParallelSearch = originalFastParallelSearch
		})

		var capturedQuery string
		wisdev.FastParallelSearch = func(_ context.Context, _ redis.UniversalClient, query string, limit int) ([]wisdev.Source, error) {
			capturedQuery = query
			assert.Equal(t, 8, limit)
			return []wisdev.Source{
				{Link: "", Title: "skip blank"},
				{Link: "#", Title: "skip hash"},
				{Link: "https://source.example/paper", Title: "skip source"},
				{Link: "https://existing.example/1", Title: "skip existing"},
				{Link: "https://result.example/1", Title: "Result One", Authors: []string{"A", "B"}, Summary: " summary one "},
				{Link: "https://result.example/2", Title: "Result Two", Authors: []string{"C"}, Summary: " summary two "},
				{Link: "https://result.example/3", Title: "Result Three", Authors: []string{"D"}, Summary: " summary three "},
				{Link: "https://result.example/4", Title: "Result Four", Authors: []string{"E"}, Summary: " summary four "},
				{Link: "https://result.example/5", Title: "Result Five", Authors: []string{"F"}, Summary: " summary five "},
				{Link: "https://result.example/6", Title: "Result Six", Authors: []string{"G"}, Summary: " summary six "},
			}, nil
		}

		req := httptest.NewRequest(http.MethodPost, "/related", strings.NewReader(`{"query":"deep learning","source_title":"neural nets","source_link":"https://source.example/paper","existing_links":["https://existing.example/1"]}`))
		rec := httptest.NewRecorder()

		h.HandleRelatedArticles(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "deep learning neural nets", capturedQuery)

		var resp map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		articles := resp["articles"].([]any)
		assert.Len(t, articles, 5)
		first := articles[0].(map[string]any)
		assert.Equal(t, "https://result.example/1", first["link"])
		assert.Equal(t, "Result One", first["title"])
		assert.Equal(t, "A, B", first["authors"])
		assert.Equal(t, "summary one", first["summary"])
	})

	t.Run("HandleRelatedArticles - Uses source title when query is blank", func(t *testing.T) {
		originalFastParallelSearch := wisdev.FastParallelSearch
		t.Cleanup(func() {
			wisdev.FastParallelSearch = originalFastParallelSearch
		})

		var capturedQuery string
		wisdev.FastParallelSearch = func(_ context.Context, _ redis.UniversalClient, query string, limit int) ([]wisdev.Source, error) {
			capturedQuery = query
			assert.Equal(t, 8, limit)
			return []wisdev.Source{{Link: "https://result.example/one", Title: "Only Result"}}, nil
		}

		req := httptest.NewRequest(http.MethodPost, "/related", strings.NewReader(`{"query":"","source_title":"quantum networks"}`))
		rec := httptest.NewRecorder()

		h.HandleRelatedArticles(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "quantum networks", capturedQuery)
	})

	t.Run("HandleQueryField - Success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/field", strings.NewReader(`{"query":"deep learning transformers"}`))
		rec := httptest.NewRecorder()

		h.HandleQueryField(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, "computerscience", resp["fieldId"])
		assert.Greater(t, resp["confidence"].(float64), 0.6)
	})

	t.Run("HandleQueryCategories - Success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/categories", strings.NewReader(`{"query":"RLHF reinforcement learning","keywords":"alignment reward models"}`))
		rec := httptest.NewRecorder()

		h.HandleQueryCategories(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		categories := resp["categories"].([]any)
		assert.NotEmpty(t, categories)

		first := categories[0].(map[string]any)
		assert.NotEmpty(t, first["name"])
		assert.Contains(t, strings.ToLower(first["description"].(string)), "rlhf reinforcement learning")
	})

	t.Run("HandleQueryIntroduction - Success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/introduction", strings.NewReader(`{
			"query":"graph rag",
			"papers":[
				{"title":"Graph RAG in Practice","summary":"A benchmark-style evaluation.","abstract":"This paper presents a benchmark for graph retrieval.","authors":["A"],"year":2024,"publication":"ACL","sourceApis":["semantic_scholar"]},
				{"title":"Graph Retrieval","summary":"A review of retrieval methods.","abstract":"This review covers graph retrieval.","authors":["B"],"year":2023,"publication":"EMNLP","sourceApis":["openalex"]}
			],
			"providersUsed":["semantic_scholar","openalex"]
		}`))
		rec := httptest.NewRecorder()

		h.HandleQueryIntroduction(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		markdown := resp["markdown"].(string)
		assert.Contains(t, markdown, "What this field studies")
		assert.Contains(t, markdown, "This field brief organizes 2 papers around Graph RAG")
		assert.Contains(t, markdown, "Major themes in the evidence base")
		assert.Contains(t, markdown, "Open gaps and contested claims")
		assert.Contains(t, markdown, "Compare methods and benchmarks")
		assert.Contains(t, markdown, "semantic_scholar")
		assert.Contains(t, markdown, "openalex")

		metaRaw, ok := resp["introductionMeta"].(map[string]any)
		assert.True(t, ok)
		assert.Equal(t, "graph rag", strings.ToLower(metaRaw["fieldLabel"].(string)))
		assert.Contains(t, metaRaw["overview"].(string), "This field brief organizes 2 papers around Graph RAG")
		assert.Contains(t, metaRaw["overview"].(string), "The strongest recurring signals are")
		assert.NotEmpty(t, metaRaw["coreThemes"])
		assert.NotEmpty(t, metaRaw["researchDirections"])
	})

	t.Run("HandleQueryIntroduction - RLHF primer", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/introduction", strings.NewReader(`{
			"query":"RLHF reinforcement learning",
			"papers":[
				{"title":"RLHF Survey","summary":"A review of alignment methods.","abstract":"This paper reviews reward modeling, policy optimization, and multi-turn evaluation for RLHF.","authors":["A"],"year":2024,"publication":"NeurIPS","sourceApis":["openalex"]},
				{"title":"Reward Model Benchmark","summary":"A benchmark-style evaluation.","abstract":"This paper compares preference datasets, reward-model evaluation, and reward hacking stress tests for RLHF systems.","authors":["B"],"year":2023,"publication":"ICLR","sourceApis":["crossref"]}
			],
			"providersUsed":["openalex","crossref"]
		}`))
		rec := httptest.NewRecorder()

		h.HandleQueryIntroduction(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		markdown := resp["markdown"].(string)
		assert.Contains(t, markdown, "RLHF (reinforcement learning from human feedback)")
		assert.Contains(t, markdown, "This field brief organizes 2 papers around RLHF Reinforcement Learning")
		assert.Contains(t, markdown, "Changes in preference data, reward-model calibration, and evaluation protocol")
		assert.Contains(t, markdown, "Compare reward models and preference data")
		assert.Contains(t, markdown, "Compare RLHF with adjacent alignment methods")
		assert.NotContains(t, markdown, "computerscience research")

		metaRaw, ok := resp["introductionMeta"].(map[string]any)
		assert.True(t, ok)
		assert.Contains(t, metaRaw["overview"].(string), "RLHF (reinforcement learning from human feedback)")
		assert.Contains(t, metaRaw["overview"].(string), "This field brief organizes 2 papers around RLHF Reinforcement Learning")
		assert.Contains(t, strings.Join(toStringSlice(metaRaw["coreThemes"]), " | "), "Preference data, reward modeling, and policy optimization")
		assert.Contains(t, strings.Join(toStringSlice(metaRaw["coreThemes"]), " | "), "Reward hacking, robustness, and multi-turn evaluation")
		assert.NotContains(t, metaRaw["overview"].(string), "computerscience research")
	})

	t.Run("HandleBatchSummaries - Success with defaulted title and paper id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/summaries", strings.NewReader(`{
			"max_findings": 2,
			"papers":[
				{"paper_id":"","title":"   ","abstract":"First sentence. Second sentence. Third sentence."},
				{"paper_id":"paper-2","title":"Second Paper","abstract":"Alpha. Beta."}
			]
		}`))
		rec := httptest.NewRecorder()

		h.HandleBatchSummaries(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		results := resp["results"].([]any)
		assert.Len(t, results, 2)

		first := results[0].(map[string]any)
		assert.Equal(t, "Untitled paper", first["title"])
		assert.Equal(t, "Untitled paper", first["paper_id"])
		assert.Equal(t, true, first["success"])

		second := results[1].(map[string]any)
		assert.Equal(t, "Second Paper", second["title"])
		assert.Equal(t, "paper-2", second["paper_id"])
		assert.Equal(t, true, second["success"])
	})
}

func toStringSlice(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, ok := item.(string)
		if ok {
			out = append(out, s)
		}
	}
	return out
}
