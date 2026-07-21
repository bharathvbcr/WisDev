package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleResolveFullText_MethodAndValidation(t *testing.T) {
	h := NewPaperHandler(nil, "")

	t.Run("MethodNotAllowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/paper/full-text/resolve", nil)
		rec := httptest.NewRecorder()
		h.HandleResolveFullText(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("EmptyPapers", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"papers": []any{}})
		req := httptest.NewRequest(http.MethodPost, "/paper/full-text/resolve", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.HandleResolveFullText(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("InvalidJSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/paper/full-text/resolve", bytes.NewReader([]byte("{")))
		rec := httptest.NewRecorder()
		h.HandleResolveFullText(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestHandleResolveFullText_OASuccess(t *testing.T) {
	h := NewPaperHandler(nil, "")
	h.extractFullTextFromURL = func(ctx context.Context, pdfURL string) (string, error) {
		assert.Equal(t, "https://cdn.example.com/paper.pdf", pdfURL)
		return "Introduction\n\nFull paper body from S2 OA.", nil
	}

	origOA := fullTextLookupOpenAccess
	origCORE := fullTextLookupCORE
	origRet := fullTextCheckRetractions
	t.Cleanup(func() {
		fullTextLookupOpenAccess = origOA
		fullTextLookupCORE = origCORE
		fullTextCheckRetractions = origRet
	})
	fullTextLookupOpenAccess = func(ctx context.Context, doi string) (*search.OpenAccessInfo, error) {
		t.Fatal("Unpaywall should not be called when S2 OA succeeds")
		return nil, nil
	}
	fullTextLookupCORE = func(ctx context.Context, doi string) (string, error) {
		t.Fatal("CORE should not be called when S2 OA succeeds")
		return "", nil
	}
	fullTextCheckRetractions = func(ctx context.Context, dois []string) ([]search.RetractionInfo, error) {
		return []search.RetractionInfo{{
			DOI: dois[0], IsRetracted: false, Source: "openretractions", FetchedAt: time.Now().UTC().Format(time.RFC3339),
		}}, nil
	}

	body, _ := json.Marshal(map[string]any{
		"papers": []map[string]any{{
			"paperId":       "p-s2",
			"doi":           "10.1000/oa-success",
			"openAccessUrl": "https://cdn.example.com/paper.pdf",
			"abstract":      "should not be used",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/paper/full-text/resolve", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.HandleResolveFullText(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var envelope struct {
		Results []FullTextResolveResult `json:"results"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&envelope))
	require.Len(t, envelope.Results, 1)
	got := envelope.Results[0]
	assert.Equal(t, "p-s2", got.PaperID)
	assert.Equal(t, "s2", got.Source)
	assert.False(t, got.AbstractOnly)
	assert.Contains(t, got.FullText, "Full paper body")
	assert.Equal(t, "https://cdn.example.com/paper.pdf", got.PDFUrl)
	assert.NotNil(t, got.Retraction)
	assert.False(t, got.Retraction.IsRetracted)
	assert.NotEmpty(t, got.DetectedSections)
}

func TestHandleResolveFullText_ProviderFailureFallsThrough(t *testing.T) {
	h := NewPaperHandler(nil, "")
	var extractCalls atomic.Int32
	h.extractFullTextFromURL = func(ctx context.Context, pdfURL string) (string, error) {
		extractCalls.Add(1)
		if strings.Contains(pdfURL, "arxiv.org") {
			return "arXiv extracted text\n\nMethods\n\nResults", nil
		}
		return "", errors.New("provider pdf failed")
	}

	origOA := fullTextLookupOpenAccess
	origCORE := fullTextLookupCORE
	origRet := fullTextCheckRetractions
	t.Cleanup(func() {
		fullTextLookupOpenAccess = origOA
		fullTextLookupCORE = origCORE
		fullTextCheckRetractions = origRet
	})
	fullTextLookupOpenAccess = func(ctx context.Context, doi string) (*search.OpenAccessInfo, error) {
		return nil, errors.New("unpaywall: upstream error (503)")
	}
	fullTextLookupCORE = func(ctx context.Context, doi string) (string, error) {
		return "", errors.New("core unavailable")
	}
	fullTextCheckRetractions = func(ctx context.Context, dois []string) ([]search.RetractionInfo, error) {
		return nil, nil
	}

	body, _ := json.Marshal(map[string]any{
		"papers": []map[string]any{{
			"paperId": "p-arxiv",
			"doi":     "10.48550/arXiv.2310.12773",
			"abstract": "fallback abstract",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/paper/full-text/resolve", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.HandleResolveFullText(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var envelope struct {
		Results []FullTextResolveResult `json:"results"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&envelope))
	require.Len(t, envelope.Results, 1)
	got := envelope.Results[0]
	assert.Equal(t, "arxiv", got.Source)
	assert.Contains(t, got.FullText, "arXiv extracted text")
	assert.False(t, got.AbstractOnly)
	assert.GreaterOrEqual(t, extractCalls.Load(), int32(1))
}

func TestHandleResolveFullText_TimeoutFallsBackToAbstract(t *testing.T) {
	h := NewPaperHandler(nil, "")
	h.extractFullTextFromURL = func(ctx context.Context, pdfURL string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}

	origOA := fullTextLookupOpenAccess
	origCORE := fullTextLookupCORE
	origRet := fullTextCheckRetractions
	origTO := fullTextResolvePerPaperTO
	t.Cleanup(func() {
		fullTextLookupOpenAccess = origOA
		fullTextLookupCORE = origCORE
		fullTextCheckRetractions = origRet
		fullTextResolvePerPaperTO = origTO
	})
	fullTextResolvePerPaperTO = 30 * time.Millisecond
	fullTextLookupOpenAccess = func(ctx context.Context, doi string) (*search.OpenAccessInfo, error) {
		return nil, nil
	}
	fullTextLookupCORE = func(ctx context.Context, doi string) (string, error) {
		return "", nil
	}
	fullTextCheckRetractions = func(ctx context.Context, dois []string) ([]search.RetractionInfo, error) {
		return nil, nil
	}

	body, _ := json.Marshal(map[string]any{
		"papers": []map[string]any{{
			"paperId":       "p-timeout",
			"openAccessUrl": "https://cdn.example.com/slow.pdf",
			"abstract":      "abstract after timeout",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/paper/full-text/resolve", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.HandleResolveFullText(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var envelope struct {
		Results []FullTextResolveResult `json:"results"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&envelope))
	require.Len(t, envelope.Results, 1)
	got := envelope.Results[0]
	assert.True(t, got.AbstractOnly)
	assert.Equal(t, "abstract", got.Source)
	assert.Equal(t, "abstract after timeout", got.FullText)
}

func TestHandleResolveFullText_UnsafeURLRejection(t *testing.T) {
	h := NewPaperHandler(nil, "")
	var extractCalls atomic.Int32
	h.extractFullTextFromURL = func(ctx context.Context, pdfURL string) (string, error) {
		extractCalls.Add(1)
		return "should not run", nil
	}

	origOA := fullTextLookupOpenAccess
	origCORE := fullTextLookupCORE
	origRet := fullTextCheckRetractions
	t.Cleanup(func() {
		fullTextLookupOpenAccess = origOA
		fullTextLookupCORE = origCORE
		fullTextCheckRetractions = origRet
	})
	fullTextLookupOpenAccess = func(ctx context.Context, doi string) (*search.OpenAccessInfo, error) {
		return nil, nil
	}
	fullTextLookupCORE = func(ctx context.Context, doi string) (string, error) {
		return "", nil
	}
	fullTextCheckRetractions = func(ctx context.Context, dois []string) ([]search.RetractionInfo, error) {
		return nil, nil
	}

	body, _ := json.Marshal(map[string]any{
		"papers": []map[string]any{{
			"paperId":       "p-ssrf",
			"openAccessUrl": "http://127.0.0.1:8080/secret.pdf",
			"abstract":      "safe abstract fallback",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/paper/full-text/resolve", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.HandleResolveFullText(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var envelope struct {
		Results []FullTextResolveResult `json:"results"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&envelope))
	require.Len(t, envelope.Results, 1)
	got := envelope.Results[0]
	assert.Equal(t, int32(0), extractCalls.Load())
	assert.True(t, got.AbstractOnly)
	assert.Equal(t, "safe abstract fallback", got.FullText)

	_, err := validatePublicHTTPFetchURL("http://169.254.169.254/latest/meta-data/")
	assert.Error(t, err)
	_, err = validatePublicHTTPFetchURL("file:///etc/passwd")
	assert.Error(t, err)
}

func TestHandleResolveFullText_ExtractionFailure(t *testing.T) {
	h := NewPaperHandler(nil, "")
	h.extractFullTextFromURL = func(ctx context.Context, pdfURL string) (string, error) {
		return "", errors.New("sidecar extraction failed")
	}

	origOA := fullTextLookupOpenAccess
	origCORE := fullTextLookupCORE
	origRet := fullTextCheckRetractions
	t.Cleanup(func() {
		fullTextLookupOpenAccess = origOA
		fullTextLookupCORE = origCORE
		fullTextCheckRetractions = origRet
	})
	fullTextLookupOpenAccess = func(ctx context.Context, doi string) (*search.OpenAccessInfo, error) {
		return &search.OpenAccessInfo{
			DOI:  doi,
			IsOA: true,
			BestOALocation: &search.OpenAccessLocation{
				URLForPDF: "https://oa.example.com/paper.pdf",
			},
		}, nil
	}
	fullTextLookupCORE = func(ctx context.Context, doi string) (string, error) {
		return "", nil
	}
	fullTextCheckRetractions = func(ctx context.Context, dois []string) ([]search.RetractionInfo, error) {
		return nil, nil
	}

	body, _ := json.Marshal(map[string]any{
		"papers": []map[string]any{{
			"paperId":  "p-extract-fail",
			"doi":      "10.1000/extract-fail",
			"abstract": "abstract when extraction fails",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/paper/full-text/resolve", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.HandleResolveFullText(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var envelope struct {
		Results []FullTextResolveResult `json:"results"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&envelope))
	require.Len(t, envelope.Results, 1)
	got := envelope.Results[0]
	assert.True(t, got.AbstractOnly)
	assert.Equal(t, "abstract", got.Source)
	assert.Equal(t, "abstract when extraction fails", got.FullText)
}

func TestHandleResolveFullText_AbstractOnlyFallback(t *testing.T) {
	h := NewPaperHandler(nil, "")
	h.extractFullTextFromURL = func(ctx context.Context, pdfURL string) (string, error) {
		t.Fatal("no PDF extract expected for abstract-only paper")
		return "", nil
	}

	origOA := fullTextLookupOpenAccess
	origCORE := fullTextLookupCORE
	origRet := fullTextCheckRetractions
	t.Cleanup(func() {
		fullTextLookupOpenAccess = origOA
		fullTextLookupCORE = origCORE
		fullTextCheckRetractions = origRet
	})
	fullTextLookupOpenAccess = func(ctx context.Context, doi string) (*search.OpenAccessInfo, error) {
		return nil, nil
	}
	fullTextLookupCORE = func(ctx context.Context, doi string) (string, error) {
		return "", nil
	}
	fullTextCheckRetractions = func(ctx context.Context, dois []string) ([]search.RetractionInfo, error) {
		return []search.RetractionInfo{{
			DOI: dois[0], IsRetracted: true, RetractionReason: "fraud", Source: "openretractions",
			FetchedAt: time.Now().UTC().Format(time.RFC3339),
		}}, nil
	}

	body, _ := json.Marshal(map[string]any{
		"papers": []map[string]any{{
			"paperId":  "p-abs",
			"doi":      "10.1000/abs-only",
			"title":    "Abstract Only Paper",
			"abstract": "Only the abstract is available.",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/paper/full-text/resolve", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.HandleResolveFullText(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var envelope struct {
		Results []FullTextResolveResult `json:"results"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&envelope))
	require.Len(t, envelope.Results, 1)
	got := envelope.Results[0]
	assert.True(t, got.AbstractOnly)
	assert.Equal(t, "abstract", got.Source)
	assert.Equal(t, "Only the abstract is available.", got.FullText)
	require.NotNil(t, got.Retraction)
	assert.True(t, got.Retraction.IsRetracted)
}

func TestDetectFullTextSectionsAndPageBreaks(t *testing.T) {
	text := "Abstract\nIntro body\n\nIntroduction\nMore\n\f\nMethods\nDone"
	breaks := detectFullTextPageBreaks(text)
	assert.NotEmpty(t, breaks)
	sections := detectFullTextSections(text)
	require.NotEmpty(t, sections)
	assert.Equal(t, "abstract", sections[0].Name)
}

func TestResolvePaperArxivID(t *testing.T) {
	assert.Equal(t, "2310.12773", resolvePaperArxivID(FullTextPaperInput{DOI: "10.48550/arXiv.2310.12773"}))
	assert.Equal(t, "2310.12773", resolvePaperArxivID(FullTextPaperInput{Link: "https://arxiv.org/abs/2310.12773"}))
	assert.Equal(t, "2310.12773", resolvePaperArxivID(FullTextPaperInput{ArxivID: "2310.12773"}))
}
