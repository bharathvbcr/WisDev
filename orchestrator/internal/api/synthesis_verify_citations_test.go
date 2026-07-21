package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyCitations_GroundAuthorYear(t *testing.T) {
	handler := NewSynthesisHandler(stubGenerateClient{}, nil)
	body := `{
		"mode":"ground",
		"text":"According to Smith (2023) the effect holds.",
		"sources":[{"paperId":"p1","title":"Test Paper","authors":[{"name":"Smith"}],"year":2023,"link":"http://test"}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/synthesis?action=verify-citations", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.HandleSynthesis(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	citations, ok := resp["citations"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, citations)
	first := citations[0].(map[string]any)
	assert.Equal(t, true, first["verified"])
	assert.Greater(t, first["confidence"].(float64), 0.8)
	stats := resp["stats"].(map[string]any)
	assert.Equal(t, float64(1), stats["verified"])
}

func TestVerifyCitations_GroundFlagsUnverified(t *testing.T) {
	handler := NewSynthesisHandler(stubGenerateClient{}, nil)
	body := `{
		"mode":"ground",
		"text":"According to Doe (2020) nothing.",
		"sources":[{"paperId":"p1","title":"Test Paper","authors":[{"name":"Smith"}],"year":2023}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/synthesis?action=verify-citations", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.HandleSynthesis(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Contains(t, resp["groundedText"].(string), "[⚠️ unverified]")
}

func TestVerifyCitations_SectionNumberedMarker(t *testing.T) {
	handler := NewSynthesisHandler(stubGenerateClient{}, nil)
	body := `{
		"mode":"section",
		"text":"Prior work shows gains [1].",
		"sources":[{"paperId":"paper-1","title":"Gains Paper","authors":[{"name":"Lee"}],"year":2022}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/synthesis?action=verify-citations", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.HandleSynthesis(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Citations []FETracedCitation `json:"citations"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Len(t, resp.Citations, 1)
	assert.True(t, resp.Citations[0].Verified)
	assert.Equal(t, "paper-1", resp.Citations[0].PaperID)
	assert.Equal(t, "grounding", resp.Citations[0].VerificationMethod)
	assert.GreaterOrEqual(t, resp.Citations[0].Confidence, 0.6)
}

func TestVerifyCitations_TraceWithKeywordChunks(t *testing.T) {
	handler := NewSynthesisHandler(stubGenerateClient{}, nil)
	body := `{
		"mode":"trace",
		"paperId":"p1",
		"claim":"atmospheric scattering causes blue sky",
		"chunks":[
			{"id":"c1","paperId":"p1","chunkIndex":0,"content":"Atmospheric scattering causes the sky to appear blue during the day."},
			{"id":"c2","paperId":"p1","chunkIndex":1,"content":"Unrelated photosynthesis discussion."}
		]
	}`
	req := httptest.NewRequest(http.MethodPost, "/synthesis?action=verify-citations", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.HandleSynthesis(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Chunks []FEChunkReference `json:"chunks"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.NotEmpty(t, resp.Chunks)
	assert.Equal(t, "c1", resp.Chunks[0].ChunkID)
	assert.Greater(t, resp.Chunks[0].RelevanceScore, 0.5)
}

func TestVerifyCitations_ClaimChunkKeywordFallback(t *testing.T) {
	handler := NewSynthesisHandler(stubGenerateClient{}, nil)
	body := `{
		"mode":"claim-chunk",
		"claim":"atmospheric scattering causes blue sky appearance",
		"chunkContent":"Atmospheric scattering causes the sky to appear blue during the day."
	}`
	req := httptest.NewRequest(http.MethodPost, "/synthesis?action=verify-citations", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.HandleSynthesis(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Greater(t, resp["confidence"].(float64), float64(30))
}

func TestVerifyCitations_Numbered(t *testing.T) {
	handler := NewSynthesisHandler(stubGenerateClient{}, nil)
	body := `{
		"mode":"numbered",
		"text":"Claim supported [1]. Missing [99].",
		"chunks":[{"number":1,"chunkId":"c1","content":"Claim supported by evidence","paperId":"p1","paperTitle":"T"}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/synthesis?action=verify-citations", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.HandleSynthesis(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Citations         []FEInlineCitation `json:"citations"`
		UngroundedNumbers []int              `json:"ungroundedNumbers"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Len(t, resp.Citations, 1)
	assert.Equal(t, 1, resp.Citations[0].CitationNumber)
	assert.Equal(t, "c1", resp.Citations[0].ChunkID)
	assert.Contains(t, resp.UngroundedNumbers, 99)
}

func TestVerifyCitations_InvalidMode(t *testing.T) {
	handler := NewSynthesisHandler(stubGenerateClient{}, nil)
	req := httptest.NewRequest(http.MethodPost, "/synthesis?action=verify-citations", bytes.NewBufferString(`{"mode":"nope"}`))
	rec := httptest.NewRecorder()
	handler.HandleSynthesis(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestVerifyCitations_TraceFull(t *testing.T) {
	handler := NewSynthesisHandler(stubGenerateClient{}, nil)
	body := `{
		"mode":"trace-full",
		"citationText":"[1]",
		"paperId":"paper-1",
		"claim":"gains observed",
		"sources":[{"paperId":"paper-1","title":"Gains","authors":[{"name":"Lee"}],"year":2022}],
		"chunks":[{"id":"c1","paperId":"paper-1","chunkIndex":0,"content":"Significant gains observed in trials."}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/synthesis?action=verify-citations", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.HandleSynthesis(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp FETracedCitation
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "paper-1", resp.PaperID)
	assert.Equal(t, "[1]", resp.CitationText)
	assert.NotEmpty(t, resp.CitationID)
}

func TestExtractCitationsFromText(t *testing.T) {
	cites := extractCitationsFromText(`Smith (2023) and (Jones et al., 2021) plus [2] and "A Long Enough Paper Title Here".`)
	assert.NotEmpty(t, cites)
	texts := make([]string, len(cites))
	for i, c := range cites {
		texts[i] = c.Text
	}
	assert.Contains(t, texts, "Smith (2023)")
	assert.Contains(t, texts, "[2]")
}
