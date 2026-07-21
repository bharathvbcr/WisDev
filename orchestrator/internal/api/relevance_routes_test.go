package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
)

func TestRelevanceHandler_ScoreBatchSuccess(t *testing.T) {
	var calls int32
	handler := NewRelevanceHandler(stubGenerateClient{
		structuredOutput: func(_ context.Context, req *llmv1.StructuredRequest) (*llmv1.StructuredResponse, error) {
			atomic.AddInt32(&calls, 1)
			assert.Contains(t, req.GetPrompt(), "research relevance assessor")
			assert.Contains(t, req.GetPrompt(), "transformer scaling")
			return &llmv1.StructuredResponse{
				JsonResult: `{"scores":[{"index":1,"score":85,"reason":"direct match"},{"index":2,"score":30}]}`,
			}, nil
		},
	})

	body := `{"query":"transformer scaling","searchMode":"academic","papers":[{"title":"Scaling Laws","abstract":"We study scaling"},{"title":"Unrelated","abstract":"Botany"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/relevance?action=score-batch", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.Handle(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Relevance struct {
			Scores []struct {
				Index  int     `json:"index"`
				Score  float64 `json:"score"`
				Reason string  `json:"reason"`
			} `json:"scores"`
			AppliedThreshold int `json:"appliedThreshold"`
			FailedChunks     int `json:"failedChunks"`
		} `json:"relevance"`
	}
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Len(t, resp.Relevance.Scores, 2)
	assert.Equal(t, 1, resp.Relevance.Scores[0].Index)
	assert.Equal(t, 85.0, resp.Relevance.Scores[0].Score)
	assert.Equal(t, 55, resp.Relevance.AppliedThreshold) // academic mode
	assert.Equal(t, 0, resp.Relevance.FailedChunks)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls)) // 2 papers => 1 chunk
}

func TestRelevanceHandler_ScoreBatchChunksAndGlobalIndices(t *testing.T) {
	handler := NewRelevanceHandler(stubGenerateClient{
		structuredOutput: func(_ context.Context, req *llmv1.StructuredRequest) (*llmv1.StructuredResponse, error) {
			// Score every paper in the chunk as its local index * 10.
			count := strings.Count(req.GetPrompt(), "Title:")
			var b strings.Builder
			b.WriteString(`{"scores":[`)
			for i := 1; i <= count; i++ {
				if i > 1 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, `{"index":%d,"score":%d}`, i, i*10)
			}
			b.WriteString(`]}`)
			return &llmv1.StructuredResponse{JsonResult: b.String()}, nil
		},
	})

	papers := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		papers = append(papers, fmt.Sprintf(`{"title":"P%d","abstract":"a"}`, i))
	}
	body := fmt.Sprintf(`{"query":"q","papers":[%s]}`, strings.Join(papers, ","))
	req := httptest.NewRequest(http.MethodPost, "/api/relevance?action=score-batch", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.Handle(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Relevance struct {
			Scores []struct {
				Index int `json:"index"`
			} `json:"scores"`
		} `json:"relevance"`
	}
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	// 12 papers => chunks of 10+2; global indices must cover 1..12 uniquely.
	assert.Len(t, resp.Relevance.Scores, 12)
	seen := map[int]bool{}
	for _, s := range resp.Relevance.Scores {
		assert.False(t, seen[s.Index], "duplicate global index %d", s.Index)
		seen[s.Index] = true
		assert.GreaterOrEqual(t, s.Index, 1)
		assert.LessOrEqual(t, s.Index, 12)
	}
}

func TestRelevanceHandler_ScoreBatchAllChunksFail(t *testing.T) {
	handler := NewRelevanceHandler(stubGenerateClient{
		structuredOutput: func(_ context.Context, _ *llmv1.StructuredRequest) (*llmv1.StructuredResponse, error) {
			return nil, fmt.Errorf("upstream unavailable")
		},
	})

	body := `{"query":"q","papers":[{"title":"P","abstract":"a"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/relevance?action=score-batch", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.Handle(rec, req)

	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestRelevanceHandler_ScoreDeepSuccess(t *testing.T) {
	handler := NewRelevanceHandler(stubGenerateClient{
		structuredOutput: func(_ context.Context, req *llmv1.StructuredRequest) (*llmv1.StructuredResponse, error) {
			assert.Contains(t, req.GetPrompt(), "research relevance expert")
			assert.Contains(t, req.GetPrompt(), "Attention Is All You Need")
			return &llmv1.StructuredResponse{
				JsonResult: `{"score":92,"reason":"foundational","matchedConcepts":["attention","transformers"]}`,
			}, nil
		},
	})

	body := `{"query":"transformer architectures","paper":{"title":"Attention Is All You Need","abstract":"...","keywords":["attention"]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/relevance?action=score-deep", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.Handle(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Relevance struct {
			Score           float64  `json:"score"`
			Reason          string   `json:"reason"`
			MatchedConcepts []string `json:"matchedConcepts"`
		} `json:"relevance"`
	}
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, 92.0, resp.Relevance.Score)
	assert.Equal(t, []string{"attention", "transformers"}, resp.Relevance.MatchedConcepts)
}

func TestRelevanceHandler_FailurePaths(t *testing.T) {
	handler := NewRelevanceHandler(stubGenerateClient{})

	t.Run("invalid action", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/relevance?action=nope", bytes.NewBufferString(`{}`))
		rec := httptest.NewRecorder()
		handler.Handle(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/relevance?action=score-batch", nil)
		rec := httptest.NewRecorder()
		handler.Handle(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/relevance?action=score-batch", bytes.NewBufferString(`{"papers":[{"title":"x"}]}`))
		rec := httptest.NewRecorder()
		handler.Handle(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("too many papers", func(t *testing.T) {
		papers := make([]string, 0, relevanceMaxPapers+1)
		for i := 0; i <= relevanceMaxPapers; i++ {
			papers = append(papers, `{"title":"t","abstract":"a"}`)
		}
		body := fmt.Sprintf(`{"query":"q","papers":[%s]}`, strings.Join(papers, ","))
		req := httptest.NewRequest(http.MethodPost, "/api/relevance?action=score-batch", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		handler.Handle(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("deep missing title", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/relevance?action=score-deep", bytesReaderJSON(`{"query":"q","paper":{"abstract":"a"}}`))
		rec := httptest.NewRecorder()
		handler.Handle(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func bytesReaderJSON(s string) *bytes.Buffer {
	return bytes.NewBufferString(s)
}
