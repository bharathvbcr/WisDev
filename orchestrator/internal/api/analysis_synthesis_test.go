package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
)

type stubGenerateClient struct {
	generate         func(context.Context, *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error)
	structuredOutput func(context.Context, *llmv1.StructuredRequest) (*llmv1.StructuredResponse, error)
}

func (s stubGenerateClient) Generate(ctx context.Context, req *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error) {
	if s.generate == nil {
		return &llmv1.GenerateResponse{Text: "ok"}, nil
	}
	return s.generate(ctx, req)
}

func (s stubGenerateClient) StructuredOutput(ctx context.Context, req *llmv1.StructuredRequest) (*llmv1.StructuredResponse, error) {
	if s.structuredOutput == nil {
		return &llmv1.StructuredResponse{JsonResult: `{"methodology":"ok","studyDesign":"default","keyVariables":[]}`}, nil
	}
	return s.structuredOutput(ctx, req)
}

func TestAnalysisHandlerErrors(t *testing.T) {
	handler := NewAnalysisHandler(stubGenerateClient{}, nil)

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/analysis?action=gaps", nil)
		rec := httptest.NewRecorder()

		handler.HandleAnalysis(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("invalid action", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/analysis?action=unknown", bytes.NewBufferString(`{}`))
		rec := httptest.NewRecorder()

		handler.HandleAnalysis(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("gaps requires papers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/analysis?action=gaps", bytes.NewBufferString(`{"papers":[{"title":"one"}]}`))
		rec := httptest.NewRecorder()

		handler.HandleAnalysis(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("gaps llm failure", func(t *testing.T) {
		failing := NewAnalysisHandler(stubGenerateClient{
			generate: func(context.Context, *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error) {
				return nil, errors.New("sidecar down")
			},
		}, nil)
		body := `{"topic":"biology","papers":[{"title":"a","abstract":"x","year":"2020"},{"title":"b","abstract":"y","year":"2021"},{"title":"c","abstract":"z","year":"2022"}]}`
		req := httptest.NewRequest(http.MethodPost, "/analysis?action=gaps", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()

		failing.HandleAnalysis(rec, req)

		assert.Equal(t, http.StatusBadGateway, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrDependencyFailed, resp.Error.Code)
	})
}

func TestSynthesisHandler(t *testing.T) {
	handler := NewSynthesisHandler(stubGenerateClient{}, nil)

	t.Run("literature review success", func(t *testing.T) {
		body := `{"topic":"AI","style":"accessible","papers":[{"title":"T1","authors":"A1","year":"2021","abstract":"Abs1","doi":"10.1000/example"}]}`
		req := httptest.NewRequest(http.MethodPost, "/synthesis?action=review", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		handler.HandleSynthesis(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, float64(0), resp["groundingRatio"])
		assert.Equal(t, float64(0), resp["citationCount"])
	})

	t.Run("summary levels", func(t *testing.T) {
		levels := []string{"tldr", "brief", "detailed", "default"}
		for _, level := range levels {
			body := `{"title":"T1","abstract":"Abs1","level":"` + level + `"}`
			req := httptest.NewRequest(http.MethodPost, "/synthesis?action=summary", bytes.NewBufferString(body))
			rec := httptest.NewRecorder()
			handler.HandleSynthesis(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code)
		}
	})

	t.Run("compare success", func(t *testing.T) {
		body := `{"papers":[{"title":"T1","authors":"A1","year":"2021","abstract":"Abs1"},{"title":"T2","authors":"A2","year":"2022","abstract":"Abs2"}],"aspects":["methodology"]}`
		req := httptest.NewRequest(http.MethodPost, "/synthesis?action=compare", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		handler.HandleSynthesis(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/synthesis?action=summary", nil)
		rec := httptest.NewRecorder()
		handler.HandleSynthesis(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("invalid action", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/synthesis?action=unknown", bytes.NewBufferString(`{}`))
		rec := httptest.NewRecorder()
		handler.HandleSynthesis(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("summary requires title", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/synthesis?action=summary", bytes.NewBufferString(`{"abstract":"test"}`))
		rec := httptest.NewRecorder()
		handler.HandleSynthesis(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("compare requires 2 papers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/synthesis?action=compare", bytes.NewBufferString(`{"papers":[{"title":"T1"}]}`))
		rec := httptest.NewRecorder()
		handler.HandleSynthesis(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("compare llm failure", func(t *testing.T) {
		failing := NewSynthesisHandler(stubGenerateClient{
			generate: func(context.Context, *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error) {
				return nil, errors.New("llm unavailable")
			},
		}, nil)
		body := `{"papers":[{"title":"a","abstract":"x","authors":"u","year":"2020"},{"title":"b","abstract":"y","authors":"v","year":"2021"}]}`
		req := httptest.NewRequest(http.MethodPost, "/synthesis?action=compare", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		failing.HandleSynthesis(rec, req)
		assert.Equal(t, http.StatusBadGateway, rec.Code)
	})

	t.Run("doi normalization trims doi urls", func(t *testing.T) {
		assert.Equal(t, "10.1000/example", normalizedDOI("https://doi.org/10.1000/example"))
		assert.Equal(t, "10.1000/example", normalizedDOI("DOI:10.1000/example"))
	})

	t.Run("review defaults to academic style", func(t *testing.T) {
		var captured *llmv1.GenerateRequest
		handler := NewSynthesisHandler(stubGenerateClient{
			generate: func(_ context.Context, req *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error) {
				captured = req
				return &llmv1.GenerateResponse{Text: " review text "}, nil
			},
		}, nil)
		body := `{"topic":"AI","papers":[{"title":"T1","authors":"A1","year":"2021","abstract":"Abs1","doi":"10.1000/example"}]}`
		req := httptest.NewRequest(http.MethodPost, "/synthesis?action=review", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		handler.HandleSynthesis(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		if assert.NotNil(t, captured) {
			assert.Contains(t, captured.Prompt, "formal academic style")
			assert.Equal(t, "heavy", captured.RequestClass)
			assert.Equal(t, "priority", captured.ServiceTier)
			assert.Equal(t, "standard", captured.RetryProfile)
			assert.Equal(t, int32(8192), captured.GetThinkingBudget())
			assert.Equal(t, llm.ResolveHeavyModel(), captured.Model)
		}
		var resp map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, "review text", resp["text"])
	})

	t.Run("summary budgets for each level", func(t *testing.T) {
		type call struct {
			level  string
			tokens int32
		}
		expected := map[string]int32{
			"tldr":     100,
			"brief":    300,
			"detailed": 1000,
			"":         1000,
			"unknown":  1000,
		}
		for level, budget := range expected {
			var got int32
			handler := NewSynthesisHandler(stubGenerateClient{
				generate: func(_ context.Context, req *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error) {
					got = req.MaxTokens
					assert.Equal(t, "standard", req.RequestClass)
					assert.Equal(t, "standard", req.ServiceTier)
					assert.Equal(t, "standard", req.RetryProfile)
					assert.Equal(t, int32(1024), req.GetThinkingBudget())
					assert.Equal(t, llm.ResolveStandardModel(), req.Model)
					return &llmv1.GenerateResponse{Text: "ok"}, nil
				},
			}, nil)
			body := `{"title":"T1","abstract":"A paper","level":"` + level + `"}`
			req := httptest.NewRequest(http.MethodPost, "/synthesis?action=summary", bytes.NewBufferString(body))
			rec := httptest.NewRecorder()
			handler.HandleSynthesis(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, budget, got)
		}
	})

	t.Run("compare default aspects branch", func(t *testing.T) {
		var reqPayload *llmv1.GenerateRequest
		handler := NewSynthesisHandler(stubGenerateClient{
			generate: func(_ context.Context, req *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error) {
				reqPayload = req
				return &llmv1.GenerateResponse{Text: "ok"}, nil
			},
		}, nil)
		body := `{"papers":[{"title":"T1","authors":"A1","year":"2021","abstract":"Abs1"},{"title":"T2","authors":"A2","year":"2022","abstract":"Abs2"}]}`
		req := httptest.NewRequest(http.MethodPost, "/synthesis?action=compare", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		handler.HandleSynthesis(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		if assert.NotNil(t, reqPayload) {
			assert.Contains(t, reqPayload.Prompt, "methodology, findings, limitations, and future directions")
			assert.Equal(t, "standard", reqPayload.RequestClass)
			assert.Equal(t, "standard", reqPayload.ServiceTier)
			assert.Equal(t, "standard", reqPayload.RetryProfile)
			assert.Equal(t, int32(1024), reqPayload.GetThinkingBudget())
			assert.Equal(t, llm.ResolveStandardModel(), reqPayload.Model)
		}
	})

	t.Run("summary rejects empty model output", func(t *testing.T) {
		handler := NewSynthesisHandler(stubGenerateClient{
			generate: func(_ context.Context, req *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error) {
				return &llmv1.GenerateResponse{Text: "   "}, nil
			},
		}, nil)
		req := httptest.NewRequest(http.MethodPost, "/synthesis?action=summary", bytes.NewBufferString(`{"title":"T1","abstract":"Abs1"}`))
		rec := httptest.NewRecorder()
		handler.HandleSynthesis(rec, req)
		assert.Equal(t, http.StatusBadGateway, rec.Code)
	})
}

func TestSynthesisHandler_DeadlineExceededFailsQuickly(t *testing.T) {
	previousTimeout := synthesisLLMRequestTimeout
	synthesisLLMRequestTimeout = 50 * time.Millisecond
	defer func() { synthesisLLMRequestTimeout = previousTimeout }()

	testCases := []struct {
		name string
		url  string
		body string
	}{
		{
			name: "review",
			url:  "/synthesis?action=review",
			body: `{"topic":"AI","papers":[{"title":"T1","authors":"A1","year":"2021","abstract":"Abs1"}]}`,
		},
		{
			name: "summary",
			url:  "/synthesis?action=summary",
			body: `{"title":"T1","abstract":"Abs1","level":"brief"}`,
		},
		{
			name: "compare",
			url:  "/synthesis?action=compare",
			body: `{"papers":[{"title":"T1","authors":"A1","year":"2021","abstract":"Abs1"},{"title":"T2","authors":"A2","year":"2022","abstract":"Abs2"}]}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewSynthesisHandler(stubGenerateClient{
				generate: func(ctx context.Context, req *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error) {
					if _, ok := ctx.Deadline(); !ok {
						t.Fatal("expected synthesis call to carry a deadline")
					}
					select {
					case <-ctx.Done():
						if ctx.Err() != context.DeadlineExceeded {
							t.Fatalf("expected deadline exceeded, got %v", ctx.Err())
						}
						return nil, ctx.Err()
					case <-time.After(1 * time.Second):
						t.Fatal("expected synthesis context cancellation")
						return nil, nil
					}
				},
			}, nil)

			req := httptest.NewRequest(http.MethodPost, tc.url, bytes.NewBufferString(tc.body))
			rec := httptest.NewRecorder()

			startedAt := time.Now()
			handler.HandleSynthesis(rec, req)

			assert.Equal(t, http.StatusBadGateway, rec.Code)
			assert.Less(t, time.Since(startedAt), time.Second)
		})
	}
}

type testRoundTripper struct {
	roundTrip func(*http.Request) (*http.Response, error)
}

func (f testRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f.roundTrip(req)
}

func TestSynthesisGroundingHelpers(t *testing.T) {
	t.Run("extract inline refs deduplicates and sorts", func(t *testing.T) {
		refs := extractInlineRefs("Refs: [10], [2], [2], [1], [10]")
		assert.Equal(t, []int{1, 2, 10}, refs)
		assert.Nil(t, extractInlineRefs("No citations here"))
	})

	t.Run("title and year helpers", func(t *testing.T) {
		assert.Equal(t, 1, minInt(5, 9, 12, 1))
		assert.Equal(t, "2021", yearString(2021))
		assert.Equal(t, "", yearString(0))
		assert.Equal(t, "", normalizeTitle("   "))
		assert.Equal(t, "deep neural networks", normalizeTitle("  Deep, Neural Networks!  "))
		assert.InDelta(t, 1.0, titleSimilarity("abc", "abc"), 0.0001)
		assert.InDelta(t, 0.0, titleSimilarity("", "abc"), 0.0001)
		assert.InDelta(t, 0.0, titleSimilarity("hello", "zzzzzz"), 0.0001)
		assert.GreaterOrEqual(t, titleSimilarity("abc", "adc"), 0.0)
	})

	t.Run("levenshtein distance helper", func(t *testing.T) {
		assert.Equal(t, 0, levenshteinDistance([]rune("test"), []rune("test")))
		assert.Equal(t, 4, levenshteinDistance([]rune(""), []rune("abcd")))
		assert.Equal(t, 4, levenshteinDistance([]rune("abcd"), []rune("")))
	})

	t.Run("grounded citation from paper handles paper fields", func(t *testing.T) {
		base := GroundedCitation{InlineRef: "[1]", Title: "base"}
		p := &search.Paper{
			Title:   "Updated Title",
			Authors: []string{"Alice", "Bob"},
			Year:    2020,
			DOI:     "10.1000/test",
			ID:      "s2:p1",
		}
		result := groundedCitationFromPaper(base, p, 0.91, true)
		assert.Equal(t, 0.91, result.ConfidenceScore)
		assert.True(t, result.Verified)
		assert.Equal(t, "Updated Title", result.Title)
		assert.Equal(t, "Alice, Bob", result.Authors)
		assert.Equal(t, "2020", result.Year)
		assert.Equal(t, "p1", result.S2PaperID)
	})

	t.Run("citation matching selects best candidate", func(t *testing.T) {
		matches := []search.Paper{
			{Title: "Deep Learning for NLP"},
			{Title: "A Deep Learning Survey"},
		}
		paper, score := bestCitationMatch("deep learning", matches)
		assert.NotNil(t, paper)
		assert.Equal(t, "Deep Learning for NLP", paper.Title)
		assert.Greater(t, score, 0.4)
	})

	t.Run("new citation grounder", func(t *testing.T) {
		provider := search.NewSemanticScholarProvider()
		grounder := NewCitationGrounder(provider)
		assert.NotNil(t, grounder)
		assert.Equal(t, provider, grounder.s2Provider)
	})
}

func TestGroundCitations(t *testing.T) {
	provider := search.NewSemanticScholarProvider()
	origClient := search.SharedHTTPClient
	defer func() {
		search.SharedHTTPClient = origClient
	}()

	search.SharedHTTPClient = &http.Client{
		Transport: testRoundTripper{roundTrip: func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.RequestURI(), "10.1000/verified") || strings.Contains(req.URL.RequestURI(), "10.1000%2Fverified") {
				resp := `{"paperId":"v1","title":"Verified Paper","abstract":"","url":"http://example.com","externalIds":{"DOI":"10.1000/verified"},"authors":[{"name":"Verified Author"}],"year":2024,"citationCount":3}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(resp)),
					Header:     make(http.Header),
				}, nil
			}

			if strings.HasSuffix(req.URL.Path, "/graph/v1/paper/search") {
				resp := `{"data":[{"paperId":"s2:search1","title":"Search Matched Paper","abstract":"","url":"","externalIds":{"DOI":"10.2000/search"},"authors":[{"name":"Search Author"}],"year":2019,"citationCount":11}]}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(resp)),
					Header:     make(http.Header),
				}, nil
			}

			return nil, errors.New("unexpected request: " + req.URL.String())
		}},
		Timeout: 2 * time.Second,
	}
	grounder := NewCitationGrounder(provider)

	t.Run("grounds verified by doi lookup", func(t *testing.T) {
		ratio, citations, err := grounder.GroundCitations(context.Background(), "Cite [1]", []SourcePaper{
			{Title: "Verified", DOI: "10.1000/verified"},
		})
		assert.NoError(t, err)
		assert.Equal(t, 1.0, ratio)
		assert.Len(t, citations, 1)
		assert.True(t, citations[0].Verified)
		assert.Equal(t, "Verified Paper", citations[0].Title)
	})

	t.Run("falls back to title search when doi lookup fails", func(t *testing.T) {
		search.SharedHTTPClient.Transport = testRoundTripper{roundTrip: func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.RequestURI(), "10.1000/fallback") || strings.Contains(req.URL.RequestURI(), "10.1000%2Ffallback") {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       io.NopCloser(strings.NewReader("{}")),
					Header:     make(http.Header),
				}, nil
			}
			if strings.HasSuffix(req.URL.Path, "/graph/v1/paper/search") {
				resp := `{"data":[{"paperId":"search2","title":"Deep Learning with Go","externalIds":{"DOI":"10.3000/fallback"},"authors":[{"name":"Search Author"}],"year":2022}]}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(resp)),
					Header:     make(http.Header),
				}, nil
			}
			return nil, errors.New("unexpected request: " + req.URL.String())
		}}
		ratio, citations, err := grounder.GroundCitations(context.Background(), "Cite [1]", []SourcePaper{
			{Title: "Deep Learning with Go", DOI: "10.1000/fallback"},
		})
		assert.NoError(t, err)
		assert.Greater(t, ratio, 0.9)
		assert.Len(t, citations, 1)
		assert.Equal(t, "Deep Learning with Go", citations[0].Title)
		assert.True(t, citations[0].Verified)
		assert.Equal(t, "search2", citations[0].S2PaperID)
		assert.Equal(t, "10.3000/fallback", citations[0].DOI)
	})

	t.Run("ground citations handles no refs", func(t *testing.T) {
		ratio, citations, err := grounder.GroundCitations(context.Background(), "No references here", []SourcePaper{
			{Title: "No citations"},
		})
		assert.NoError(t, err)
		assert.Equal(t, 0.0, ratio)
		assert.Len(t, citations, 0)
	})

	t.Run("ground single citation handles out of range and empty title", func(t *testing.T) {
		citation := grounder.groundSingleCitation(context.Background(), 10, []SourcePaper{{Title: "Out of range"}})
		assert.Equal(t, "[10]", citation.InlineRef)
		assert.False(t, citation.Verified)
		assert.Empty(t, citation.Title)

		citation = grounder.groundSingleCitation(context.Background(), 1, []SourcePaper{{Title: ""}})
		assert.Empty(t, citation.Title)
		assert.False(t, citation.Verified)
		assert.Equal(t, "[1]", citation.InlineRef)
	})

	t.Run("ground single citation handles missing title match", func(t *testing.T) {
		search.SharedHTTPClient.Transport = testRoundTripper{roundTrip: func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/graph/v1/paper/search") {
				resp := `{"data":[]}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(resp)),
					Header:     make(http.Header),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader(`{}`)),
				Header:     make(http.Header),
			}, nil
		}}
		citation := grounder.groundSingleCitation(context.Background(), 1, []SourcePaper{{Title: "No Match Title"}})
		assert.Equal(t, "[1]", citation.InlineRef)
		assert.False(t, citation.Verified)
		assert.Equal(t, "No Match Title", citation.Title)
	})
}
